package ggs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/roles/memory"
	"github.com/haricheung/agentic-shell/internal/types"
)

// Loss hyperparameters (v0.8 defaults).
const (
	alpha         = 0.6     // weight on intent-result distance D
	beta          = 0.3     // weight on process implausibility P (before adaptive scaling)
	lambda        = 0.4     // weight on resource cost Ω
	w1            = 0.6     // Ω sub-weight for replan count
	w2            = 0.4     // Ω sub-weight for elapsed time
	epsilon       = 0.1     // |∇L| below this → plateau (no signal)
	delta         = 0.3     // D below this → success (convergence threshold)
	rho           = 0.5     // P above this → logical failure; below → environmental
	abandonOmega  = 0.8     // Ω above this → abandon regardless of other signals
	timeBudgetMs  = 300_000 // default time budget per task (5 min)
	maxReplansGGS = 3       // matches R4b's maxReplans; used in Ω computation
)

// GGS is R7 — Goal Gradient Solver. It sits between R4b (sensor) and R2 (actuator)
// in the medium loop. It receives ReplanRequest from R4b, computes D, P, Ω, L, ∇L,
// selects a macro-state from the v0.8 decision table, and either emits PlanDirective
// to R2 (action states) or FinalResult to the user (success/abandon paths).
// GGS is the sole writer to R5 (Shared Memory).
type GGS struct {
	b              *bus.Bus
	mem            types.MemoryService // R5 — sole writer; may be nil (memory disabled)
	outputFn       func(taskID, summary string, output any)
	mu             sync.Mutex
	lPrev          map[string]float64  // L_{t-1} per task_id
	replans        map[string]int      // replan round counter per task_id
	worseningCount map[string]int      // consecutive "worsening" gradient count per task_id
	triedTargets   map[string][]string // accumulated failed tool inputs per task_id (for environmental directives)
	prevDirective  map[string]string   // macro-state from the previous round per task_id
}

// New creates a GGS. mem may be nil to disable memory writes (e.g. in tests).
func New(b *bus.Bus, outputFn func(taskID, summary string, output any), mem types.MemoryService) *GGS {
	return &GGS{
		b:              b,
		mem:            mem,
		outputFn:       outputFn,
		lPrev:          make(map[string]float64),
		replans:        make(map[string]int),
		worseningCount: make(map[string]int),
		triedTargets:   make(map[string][]string),
		prevDirective:  make(map[string]string),
	}
}

// Run listens for ReplanRequest and OutcomeSummary messages from R4b.
// ReplanRequest → compute loss + gradient → emit PlanDirective (or abandon).
// OutcomeSummary → all subtasks matched → record final loss (D=0) → emit FinalResult.
// GGS is always in the medium loop; it is never idle even on the happy path.
func (g *GGS) Run(ctx context.Context) {
	replanCh := g.b.Subscribe(types.MsgReplanRequest)
	acceptCh := g.b.Subscribe(types.MsgOutcomeSummary)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-replanCh:
			if !ok {
				return
			}
			rr, err := toReplanRequest(msg.Payload)
			if err != nil {
				slog.Error("[R7] bad ReplanRequest", "error", err)
				continue
			}
			slog.Info("[R7] received ReplanRequest", "task", rr.TaskID, "gap", rr.GapSummary, "corrections", rr.CorrectionCount, "elapsed_ms", rr.ElapsedMs)
			go g.process(ctx, rr)
		case msg, ok := <-acceptCh:
			if !ok {
				return
			}
			os, err := toOutcomeSummary(msg.Payload)
			if err != nil {
				slog.Error("[R7] bad OutcomeSummary", "error", err)
				continue
			}
			slog.Info("[R7] received OutcomeSummary", "task", os.TaskID, "elapsed_ms", os.ElapsedMs)
			go g.processAccept(ctx, os)
		}
	}
}

func (g *GGS) process(ctx context.Context, rr types.ReplanRequest) {
	taskID := rr.TaskID

	g.mu.Lock()
	g.replans[taskID]++
	replanCount := g.replans[taskID]
	lPrev, hasPrev := g.lPrev[taskID]
	prevDir := g.prevDirective[taskID]
	g.mu.Unlock()

	prevDirective := "init"
	if prevDir != "" {
		prevDirective = prevDir
	}

	// Compute loss components.
	D := computeD(rr.Outcomes)
	P := computeP(rr.Outcomes)
	Omega := computeOmega(replanCount, rr.ElapsedMs)
	L := computeLoss(D, P, Omega)

	// Store L for next round's gradient.
	g.mu.Lock()
	g.lPrev[taskID] = L
	g.mu.Unlock()

	// Compute ∇L = L_t − L_{t-1}. First round: L_prev undefined → ∇L = 0.
	var gradL float64
	if hasPrev {
		gradL = L - lPrev
	}

	// computeGradient is used only for Law 2 worsening detection.
	gradient := computeGradient(gradL, D)

	// v0.8 diagnostic cascade: Ω → D → (|∇L|, P).
	directive := selectDirective(gradL, D, P, Omega)

	// Law 2 kill-switch: 2 consecutive worsening rounds → force abandon.
	// Does not override "success" — if D ≤ δ the result is good enough.
	g.mu.Lock()
	if gradient == "worsening" {
		g.worseningCount[taskID]++
	} else {
		g.worseningCount[taskID] = 0
	}
	consecutiveWorsening := g.worseningCount[taskID]
	g.mu.Unlock()

	const law2KillThreshold = 2
	if consecutiveWorsening >= law2KillThreshold && directive != "abandon" && directive != "success" {
		slog.Warn("[R7] LAW2 kill-switch: overriding to abandon", "task", taskID, "consecutive_worsening", consecutiveWorsening, "directive", directive)
		directive = "abandon"
	}

	slog.Info("[R7] GGS compute", "task", taskID, "round", replanCount, "D", D, "P", P, "Omega", Omega, "L", L, "gradL", gradL, "gradient", gradient, "directive", directive)

	// "success" macro-state: D ≤ δ, Ω < θ — close enough, deliver result without routing to R2.
	if directive == "success" {
		slog.Info("[R7] task SUCCESS", "task", taskID, "D", D, "delta", delta)
		summary := buildSuccessSummary(rr)
		output := mergeMatchedOutputs(rr.Outcomes)

		// Write terminal Megram to R5 (GGS is sole writer).
		g.writeTerminalMegram(rr.Intent, summary, "success")

		g.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleGGS,
			To:        types.RoleUser,
			Type:      types.MsgFinalResult,
			Payload: types.FinalResult{
				TaskID:        taskID,
				Summary:       summary,
				Output:        output,
				Loss:          types.LossBreakdown{D: D, P: P, Omega: Omega, L: L},
				GradL:         gradL,
				Replans:       replanCount,
				Directive:     "success",
				PrevDirective: prevDirective,
			},
		})
		if g.outputFn != nil {
			g.outputFn(taskID, summary, output)
		}

		g.mu.Lock()
		delete(g.lPrev, taskID)
		delete(g.replans, taskID)
		delete(g.worseningCount, taskID)
		delete(g.triedTargets, taskID)
		delete(g.prevDirective, taskID)
		g.mu.Unlock()
		return
	}

	// "abandon" macro-state: Ω ≥ θ or Law 2 kill-switch.
	if directive == "abandon" {
		slog.Info("[R7] task ABANDON", "task", taskID, "Omega", Omega, "threshold", abandonOmega)
		summary := buildAbandonSummary(rr)

		// Write terminal Megram to R5 (GGS is sole writer).
		g.writeTerminalMegram(rr.Intent, rr.GapSummary, "abandon")

		g.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleGGS,
			To:        types.RoleUser,
			Type:      types.MsgFinalResult,
			Payload: types.FinalResult{
				TaskID:        taskID,
				Summary:       summary,
				Loss:          types.LossBreakdown{D: D, P: P, Omega: Omega, L: L},
				GradL:         gradL,
				Replans:       replanCount,
				Directive:     "abandon",
				PrevDirective: prevDirective,
			},
		})
		if g.outputFn != nil {
			g.outputFn(taskID, summary, nil)
		}

		g.mu.Lock()
		delete(g.lPrev, taskID)
		delete(g.replans, taskID)
		delete(g.worseningCount, taskID)
		delete(g.triedTargets, taskID)
		delete(g.prevDirective, taskID)
		g.mu.Unlock()
		return
	}

	// Action states: refine | change_path | change_approach | break_symmetry.
	// Write one Megram per failed tool call to R5 (fire-and-forget).
	g.writeMegramsFromToolCalls(rr.Outcomes, directive)

	blockedTools := deriveBlockedTools(rr.Outcomes, directive)

	newTargets := deriveBlockedTargets(rr.Outcomes, directive)
	g.mu.Lock()
	if len(newTargets) > 0 {
		g.triedTargets[taskID] = appendDeduped(g.triedTargets[taskID], newTargets)
	}
	allBlockedTargets := g.triedTargets[taskID]
	g.prevDirective[taskID] = directive
	g.mu.Unlock()

	failedCriterion := primaryFailedCriterion(rr.Outcomes)
	failureClass := computeFailureClass(rr.Outcomes)
	rationale := buildRationale(directive, D, P, Omega, gradL, rr.GapSummary)

	slog.Info("[R7] emitting PlanDirective", "task", taskID, "directive", directive, "prev", prevDirective, "blocked_tools", blockedTools)

	g.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleGGS,
		To:        types.RolePlanner,
		Type:      types.MsgPlanDirective,
		Payload: types.PlanDirective{
			TaskID:          taskID,
			Loss:            types.LossBreakdown{D: D, P: P, Omega: Omega, L: L},
			PrevDirective:   prevDirective,
			Directive:       directive,
			BlockedTools:    blockedTools,
			BlockedTargets:  allBlockedTargets,
			FailedCriterion: failedCriterion,
			FailureClass:    failureClass,
			BudgetPressure:  Omega,
			GradL:           gradL,
			Rationale:       rationale,
		},
	})
}

// processAccept handles the happy-path case: all subtasks matched, R4b accepted.
// GGS records the final loss (D=0) and delivers FinalResult to the user.
// This keeps GGS in the medium loop even when no replanning is needed —
// a proper closed-loop controller computes the error signal on every cycle,
// including when the error is zero.
//
// Expectations:
//   - D is always 0.0 (all subtasks matched)
//   - Ω is computed from prior replan count + elapsed time (rewards fast, first-try solutions)
//   - gradient is always "stable" (D=0 ≤ δ)
//   - Emits MsgFinalResult to RoleUser with the merged output and summary
//   - FinalResult carries Loss (D=0), GradL, and Replans for trajectory checkpoint display
//   - Calls outputFn so the REPL can display the result
//   - Cleans up lPrev and replans state for the task
// processAccept handles the happy-path case: all subtasks matched, R4b accepted.
// GGS records the final loss (D=0) and delivers FinalResult to the user.
// This keeps GGS in the medium loop even when no replanning is needed —
// a proper closed-loop controller computes the error signal on every cycle,
// including when the error is zero.
//
// Expectations:
//   - D is always 0.0 (all subtasks matched)
//   - Ω is computed from prior replan count + elapsed time (rewards fast, first-try solutions)
//   - FinalResult.Directive is always "accept"
//   - FinalResult.PrevDirective is "init" on first-try accepts; prior directive after replanning
//   - Emits MsgFinalResult to RoleUser with the merged output and summary
//   - FinalResult carries Loss (D=0), GradL, and Replans for trajectory checkpoint display
//   - Calls outputFn so the REPL can display the result
//   - Cleans up all per-task state
func (g *GGS) processAccept(_ context.Context, os types.OutcomeSummary) {
	taskID := os.TaskID

	g.mu.Lock()
	lPrev, hasPrev := g.lPrev[taskID]
	replanCount := g.replans[taskID] // 0 for first-try accepts; >0 if GGS directed prior replans
	prevDir := g.prevDirective[taskID]
	g.mu.Unlock()

	prevDirective := "init"
	if prevDir != "" {
		prevDirective = prevDir
	}

	// D=0: all subtasks matched. P=0.5: no failures → neutral. Ω: elapsed time + prior replans.
	const D, P = 0.0, 0.5
	Omega := computeOmega(replanCount, os.ElapsedMs)
	L := computeLoss(D, P, Omega)

	var gradL float64
	if hasPrev {
		gradL = L - lPrev // ∇L across rounds (e.g. L after first replan → 0 on final accept)
	}

	slog.Info("[R7] task ACCEPT", "task", taskID, "Omega", Omega, "L", L, "gradL", gradL, "replans", replanCount, "prev", prevDirective)

	// Clean up per-task state (task is done).
	g.mu.Lock()
	delete(g.lPrev, taskID)
	delete(g.replans, taskID)
	delete(g.worseningCount, taskID)
	delete(g.triedTargets, taskID)
	delete(g.prevDirective, taskID)
	g.mu.Unlock()

	// Write terminal Megram to R5 (GGS is sole writer).
	g.writeTerminalMegram(os.Intent, os.Summary, "accept")

	// GGS is the sole emitter of FinalResult — consistent path for accept, success, and abandon.
	// Directive="accept"; Loss, GradL, Replans, PrevDirective for trajectory checkpoint display.
	g.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleGGS,
		To:        types.RoleUser,
		Type:      types.MsgFinalResult,
		Payload: types.FinalResult{
			TaskID:        taskID,
			Summary:       os.Summary,
			Output:        os.MergedOutput,
			Loss:          types.LossBreakdown{D: D, P: P, Omega: Omega, L: L},
			GradL:         gradL,
			Replans:       replanCount,
			Directive:     "accept",
			PrevDirective: prevDirective,
		},
	})
	if g.outputFn != nil {
		g.outputFn(taskID, os.Summary, os.MergedOutput)
	}
}

// computeD computes intent-result distance D ∈ [0, 1] at criterion granularity.
// When CriteriaVerdicts are present, D = failed_criteria / total_criteria.
// Falls back to subtask-level (1 synthetic criterion per outcome) when CriteriaVerdicts absent.
// Returns 1.0 when outcomes is empty (complete failure).
//
// Expectations:
//   - Returns 1.0 when outcomes is empty (no data = total failure)
//   - Returns 0.0 when all outcomes are matched (with or without CriteriaVerdicts)
//   - Uses CriteriaVerdicts when available: D = failed_criteria / total_criteria
//   - For outcomes without CriteriaVerdicts: counts each as 1 synthetic criterion
//   - Criterion-level result differs from subtask-level when subtasks have unequal criterion counts
func computeD(outcomes []types.SubTaskOutcome) float64 {
	if len(outcomes) == 0 {
		return 1.0
	}
	total, failed := 0, 0
	for _, o := range outcomes {
		if len(o.CriteriaVerdicts) > 0 {
			for _, cv := range o.CriteriaVerdicts {
				total++
				if cv.Verdict == "fail" {
					failed++
				}
			}
		} else {
			// Synthetic: 1 criterion per outcome (subtask-level fallback)
			total++
			if o.Status == "failed" {
				failed++
			}
		}
	}
	return float64(failed) / float64(total)
}

// computePKeyword computes process implausibility P ∈ [0, 1] via keyword heuristics.
// Used as fallback when no structured failure_class data is available.
//
// Expectations:
//   - Returns 0.5 when outcomes is empty (neutral default)
//   - Returns 0.5 when no outcomes have failure reasons (neutral)
//   - Returns value > 0.5 when failure reasons suggest logical errors
//   - Returns value < 0.5 when failure reasons suggest environmental errors
//   - Returns value in [0, 1]
func computePKeyword(outcomes []types.SubTaskOutcome) float64 {
	logicalKW := []string{"logic", "wrong approach", "incorrect", "invalid", "cannot", "not possible",
		"permission denied", "operation not permitted"}
	envKW := []string{"network", "timeout", "context deadline", "connection", "unavailable",
		"not found", "no such file", "temporary", "rate limit"}

	logical, environmental := 0, 0
	for _, o := range outcomes {
		if o.Status != "failed" {
			continue
		}
		reason := ""
		if o.FailureReason != nil {
			reason = strings.ToLower(*o.FailureReason)
		}
		for _, traj := range o.GapTrajectory {
			for _, uc := range traj.UnmetCriteria {
				reason += " " + strings.ToLower(uc)
			}
		}
		isLogical := false
		for _, kw := range logicalKW {
			if strings.Contains(reason, kw) {
				isLogical = true
				break
			}
		}
		isEnv := false
		for _, kw := range envKW {
			if strings.Contains(reason, kw) {
				isEnv = true
				break
			}
		}
		if isLogical && !isEnv {
			logical++
		} else if isEnv && !isLogical {
			environmental++
		}
		// Both or neither → neutral, contributes to neither
	}

	total := logical + environmental
	if total == 0 {
		return 0.5
	}
	return float64(logical) / float64(total)
}

// computeP computes process implausibility P ∈ [0, 1].
// Uses CriteriaVerdicts.FailureClass when available; falls back to keyword heuristics.
//
// Expectations:
//   - Returns 0.5 when outcomes is empty (neutral)
//   - Uses CriteriaVerdicts.FailureClass when at least one classified fail present
//   - Returns P = logical / (logical+environmental) from structured data
//   - Falls back to computePKeyword when no structured failure_class is classified
//   - Returns 0.5 (via fallback) when CriteriaVerdicts present but all FailureClass fields empty
func computeP(outcomes []types.SubTaskOutcome) float64 {
	logical, env := 0, 0
	for _, o := range outcomes {
		if o.Status != "failed" {
			continue
		}
		for _, cv := range o.CriteriaVerdicts {
			if cv.Verdict != "fail" {
				continue
			}
			switch cv.FailureClass {
			case "logical":
				logical++
			case "environmental":
				env++
			}
		}
	}
	if logical+env > 0 {
		return float64(logical) / float64(logical+env)
	}
	return computePKeyword(outcomes)
}

// computeOmega computes resource cost Ω ∈ [0, 1].
// Ω = w1*(replanCount/maxReplansGGS) + w2*(elapsedMs/timeBudgetMs), capped at 1.0.
//
// Expectations:
//   - Returns 0.0 when replanCount=0 and elapsedMs=0
//   - Returns w1 (0.6) when replanCount=maxReplansGGS and elapsedMs=0
//   - Returns w2 (0.4) when replanCount=0 and elapsedMs=timeBudgetMs
//   - Returns 1.0 when replanCount=maxReplansGGS and elapsedMs=timeBudgetMs
//   - Never exceeds 1.0
func computeOmega(replanCount int, elapsedMs int64) float64 {
	replanRatio := float64(replanCount) / float64(maxReplansGGS)
	timeRatio := float64(elapsedMs) / float64(timeBudgetMs)
	omega := w1*replanRatio + w2*timeRatio
	if omega > 1.0 {
		return 1.0
	}
	return omega
}

// computeLoss computes total loss L = α·D + β_eff·P + λ·Ω.
// β_eff = β·(1−Ω) — process plausibility weight decays as budget exhausts.
//
// Expectations:
//   - Returns α when D=1, P=0, Ω=0 (pure distance loss)
//   - Returns λ when D=0, P=0, Ω=1 (pure resource cost)
//   - Returns α+β when D=1, P=1, Ω=0 (λ·Ω term is zero at no budget pressure)
//   - β_eff is zero when Ω=1, so P has no effect when budget is exhausted
func computeLoss(D, P, Omega float64) float64 {
	betaEff := beta * (1.0 - Omega)
	return alpha*D + betaEff*P + lambda*Omega
}

// computeGradient converts ∇L and D into a gradient label.
// plateau: |∇L| < epsilon AND D > delta (local minimum).
// improving: ∇L < 0 (loss decreasing).
// worsening: ∇L > 0 (loss increasing).
// stable: |∇L| < epsilon AND D <= delta (converged or near-converged).
//
// Expectations:
//   - Returns "plateau" when |∇L| < epsilon and D > delta
//   - Returns "stable" when |∇L| < epsilon and D <= delta
//   - Returns "improving" when ∇L < -epsilon
//   - Returns "worsening" when ∇L > epsilon
func computeGradient(gradL, D float64) string {
	if math.Abs(gradL) < epsilon {
		if D > delta {
			return "plateau"
		}
		return "stable"
	}
	if gradL < 0 {
		return "improving"
	}
	return "worsening"
}

// selectDirective selects the macro-state using the v0.8 diagnostic cascade.
//
// Priority 1: Ω — hard constraint (can we continue?)
// Priority 2: D — target distance (are we close enough?)
// Priority 3: |∇L| and P together — action selection (what kind of change?)
//
// ∇L sign is a modulator within each macro-state (urgency), not a state selector.
//
// Expectations:
//   - Returns "abandon" when Omega >= abandonOmega regardless of other values
//   - Returns "success" when Omega < abandonOmega and D <= delta
//   - Returns "break_symmetry" when D > delta, |∇L| < epsilon, P > rho
//   - Returns "change_approach" when D > delta, |∇L| >= epsilon, P > rho
//   - Returns "change_path" when D > delta, |∇L| < epsilon, P <= rho
//   - Returns "refine" when D > delta, |∇L| >= epsilon, P <= rho
func selectDirective(gradL, D, P, Omega float64) string {
	// Priority 1: Ω — budget hard constraint.
	if Omega >= abandonOmega {
		return "abandon"
	}
	// Priority 2: D — convergence threshold.
	if D <= delta {
		return "success"
	}
	// Priority 3: (|∇L|, P) — action selection.
	hasSignal := math.Abs(gradL) >= epsilon
	highP := P > rho
	switch {
	case !hasSignal && highP:
		return "break_symmetry" // stuck + logical failure → novel approach
	case hasSignal && highP:
		return "change_approach" // has signal + logical failure → switch method
	case !hasSignal && !highP:
		return "change_path" // stuck + environmental failure → different target
	default: // hasSignal && !highP
		return "refine" // has signal + environmental failure → tighten parameters
	}
}

// deriveBlockedTools collects tool names from failed subtasks' ToolCalls.
// Only populated for break_symmetry or change_approach directives.
//
// Expectations:
//   - Returns nil for directives other than "break_symmetry" and "change_approach"
//   - Returns nil when no failed outcomes have ToolCalls
//   - Returns deduplicated tool name list (prefix before ":") for qualifying directives
func deriveBlockedTools(outcomes []types.SubTaskOutcome, directive string) []string {
	if directive != "break_symmetry" && directive != "change_approach" {
		return nil
	}
	seen := make(map[string]bool)
	var tools []string
	for _, o := range outcomes {
		if o.Status != "failed" {
			continue
		}
		for _, tc := range o.ToolCalls {
			// ToolCalls entries look like "shell: command..." or just "shell"
			name := tc
			if idx := strings.Index(tc, ":"); idx > 0 {
				name = strings.TrimSpace(tc[:idx])
			}
			if name != "" && !seen[name] {
				seen[name] = true
				tools = append(tools, name)
			}
		}
	}
	return tools
}

// deriveBlockedTargets extracts specific failed tool inputs from environmental-failure
// subtask tool calls. Only populated for change_path and refine directives.
//
// Tool call entries have the format: "tool: {json_input} → output_snippet".
// For each failed outcome, the JSON input is parsed and the following fields are
// extracted: "query" (search tool), "command" (shell tool), "path" (file tools).
// This gives R2 the concrete inputs that failed so it can avoid them in the next plan.
//
// Expectations:
//   - Returns nil for directives other than "change_path" and "refine"
//   - Returns nil when no failed outcomes have ToolCalls
//   - Extracts "query" field from search tool calls in failed outcomes
//   - Extracts "command" field from shell tool calls in failed outcomes
//   - Extracts "path" field from file tool calls in failed outcomes
//   - Returns deduplicated list of extracted input values
func deriveBlockedTargets(outcomes []types.SubTaskOutcome, directive string) []string {
	if directive != "change_path" && directive != "refine" {
		return nil
	}
	seen := make(map[string]bool)
	var targets []string
	for _, o := range outcomes {
		if o.Status != "failed" {
			continue
		}
		for _, tc := range o.ToolCalls {
			// Format: "tool: {json_input} → output_snippet"
			// Strip output snippet first.
			input := tc
			if idx := strings.Index(tc, " → "); idx > 0 {
				input = tc[:idx]
			}
			// Strip tool name prefix ("tool: ").
			if idx := strings.Index(input, ": "); idx > 0 {
				rawInput := input[idx+2:]
				// Parse the JSON input and extract known target fields.
				var m map[string]string
				if err := json.Unmarshal([]byte(rawInput), &m); err == nil {
					for _, key := range []string{"query", "command", "path"} {
						if val := strings.TrimSpace(m[key]); val != "" && !seen[val] {
							seen[val] = true
							targets = append(targets, val)
						}
					}
				}
			}
		}
	}
	return targets
}

// appendDeduped appends newItems to existing, skipping any already present.
func appendDeduped(existing, newItems []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, v := range existing {
		seen[v] = true
	}
	result := existing
	for _, v := range newItems {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// primaryFailedCriterion returns the first unmet criterion across all failed outcomes.
//
// Expectations:
//   - Returns "" when no outcomes are failed or GapTrajectory is empty
//   - Returns the first unmet criterion from the last trajectory point of the first failed outcome
func primaryFailedCriterion(outcomes []types.SubTaskOutcome) string {
	for _, o := range outcomes {
		if o.Status != "failed" {
			continue
		}
		if len(o.GapTrajectory) > 0 {
			last := o.GapTrajectory[len(o.GapTrajectory)-1]
			if len(last.UnmetCriteria) > 0 {
				return last.UnmetCriteria[0]
			}
		}
	}
	return ""
}

// computeFailureClass returns "logical", "environmental", or "mixed" based on P.
//
// Expectations:
//   - Returns "logical" when P > 0.5
//   - Returns "environmental" when P < 0.5
//   - Returns "mixed" when P == 0.5
func computeFailureClass(outcomes []types.SubTaskOutcome) string {
	P := computeP(outcomes)
	if P > 0.5 {
		return "logical"
	}
	if P < 0.5 {
		return "environmental"
	}
	return "mixed"
}

// buildAbandonSummary generates a structured summary from SubTaskOutcome data.
// No LLM call; R2 graceful failure (LLM-backed) is deferred to v0.8.
//
// Expectations:
//   - Lists completed subtask intents when any matched
//   - Lists failed subtask intents when any failed
//   - Includes gap_summary when non-empty
//   - Always ends with generic next-step suggestions
func buildAbandonSummary(rr types.ReplanRequest) string {
	var matched, failed []string
	for _, o := range rr.Outcomes {
		if o.Status == "matched" {
			matched = append(matched, o.Intent)
		} else {
			failed = append(failed, o.Intent)
		}
	}

	parts := []string{"❌ Task abandoned after budget exhausted."}
	if len(matched) > 0 {
		parts = append(parts, fmt.Sprintf("Completed: %s.", strings.Join(matched, "; ")))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("Failed: %s.", strings.Join(failed, "; ")))
	}
	if rr.GapSummary != "" {
		parts = append(parts, rr.GapSummary)
	}
	parts = append(parts, "Consider breaking the task into smaller steps or retrying with more specific instructions.")
	return strings.Join(parts, " ")
}

// buildSuccessSummary produces a user-facing summary for the "success" macro-state
// (D ≤ δ — close enough, delivering result without further replanning).
//
// Expectations:
//   - Starts with a success prefix indicating convergence threshold was met
//   - Lists how many subtasks matched and how many failed within threshold
func buildSuccessSummary(rr types.ReplanRequest) string {
	matched, failed := 0, 0
	for _, o := range rr.Outcomes {
		if o.Status == "matched" {
			matched++
		} else {
			failed++
		}
	}
	parts := []string{"✅ Task completed within convergence threshold."}
	if matched > 0 {
		parts = append(parts, fmt.Sprintf("%d subtask(s) completed.", matched))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d subtask(s) failed but gap is within acceptable threshold (D ≤ δ).", failed))
	}
	if rr.GapSummary != "" {
		parts = append(parts, rr.GapSummary)
	}
	return strings.Join(parts, " ")
}

// mergeMatchedOutputs collects outputs from matched subtask outcomes.
// Returns nil when no matched outputs exist; the single output when exactly one;
// a []any slice when multiple.
//
// Expectations:
//   - Returns nil when no matched outcomes have non-nil output
//   - Returns the single output directly (not wrapped in a slice) when exactly one
//   - Returns []any of all outputs when multiple matched outputs exist
func mergeMatchedOutputs(outcomes []types.SubTaskOutcome) any {
	var outputs []any
	for _, o := range outcomes {
		if o.Status == "matched" && o.Output != nil {
			outputs = append(outputs, o.Output)
		}
	}
	switch len(outputs) {
	case 0:
		return nil
	case 1:
		return outputs[0]
	default:
		return outputs
	}
}

// buildRationale produces a human-readable explanation of the directive.
func buildRationale(directive string, D, P, Omega, gradL float64, gapSummary string) string {
	switch directive {
	case "refine":
		if gradL < -epsilon {
			return fmt.Sprintf("Loss decreasing (∇L=%.3f), approach is sound (P=%.2f ≤ ρ). Tighten parameters. Gap: %s", gradL, P, gapSummary)
		}
		return fmt.Sprintf("Has signal (|∇L|=%.3f ≥ ε=%.1f), environmental issue (P=%.2f ≤ ρ). Adjust path/parameters. Gap: %s", math.Abs(gradL), epsilon, P, gapSummary)
	case "change_path":
		return fmt.Sprintf("Plateau (|∇L|=%.3f < ε=%.1f, D=%.2f > δ=%.1f), environmental origin (P=%.2f ≤ ρ). Same tool class, different target. Gap: %s",
			math.Abs(gradL), epsilon, D, delta, P, gapSummary)
	case "change_approach":
		return fmt.Sprintf("Has signal (|∇L|=%.3f ≥ ε=%.1f), logical failure (P=%.2f > ρ). Switch tool class entirely. Gap: %s", math.Abs(gradL), epsilon, P, gapSummary)
	case "break_symmetry":
		return fmt.Sprintf("Local minimum (|∇L|=%.3f < ε=%.1f, D=%.2f > δ=%.1f), logical failure (P=%.2f > ρ). Block all tried tools, demand novel approach. Gap: %s",
			math.Abs(gradL), epsilon, D, delta, P, gapSummary)
	case "abandon":
		return fmt.Sprintf("Budget exhausted (Ω=%.3f ≥ θ=%.1f). Continued replanning cost exceeds gap cost. Gap: %s", Omega, abandonOmega, gapSummary)
	default:
		return gapSummary
	}
}

// ---------------------------------------------------------------------------
// R5 Memory write helpers — GGS is the sole writer to Shared Memory (R5).
// ---------------------------------------------------------------------------

// writeTerminalMegram writes one Megram to R5 on terminal states (accept/success/abandon).
// Tags: space = intent slug derived from task intent; entity = "env:local".
// Also publishes MsgMegram to the bus for Auditor observability.
//
// Expectations:
//   - No-ops when mem is nil
//   - Uses IntentSlug to derive space tag from intent
//   - Sets f, sigma, k from quantization matrix for the given state
//   - Publishes MsgMegram to bus for Auditor observability
//   - Fires Write() async (non-blocking)
func (g *GGS) writeTerminalMegram(intent, content, state string) {
	if g.mem == nil {
		return
	}
	q, ok := memory.QuantizationMatrix()[state]
	if !ok {
		return
	}
	meg := types.Megram{
		ID:        uuid.New().String(),
		Level:     "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space:     memory.IntentSlug(intent),
		Entity:    "env:local",
		Content:   content,
		State:     state,
		F:         q.F,
		Sigma:     q.Sigma,
		K:         q.K,
	}
	g.mem.Write(meg)
	if g.b != nil {
		g.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleGGS,
			To:        types.RoleMemory,
			Type:      types.MsgMegram,
			Payload:   meg,
		})
	}
}

// writeMegramsFromToolCalls writes one Megram per unique (tool, target) pair found
// in failed subtask ToolCalls. Used for action states (refine, change_path, etc.)
// Tags: space = "tool:<name>"; entity = "target:<value>".
//
// Expectations:
//   - No-ops when mem is nil
//   - Only processes failed outcomes
//   - Deduplicates by (toolName, target) to avoid multiple Megrams for the same pair
//   - Sets f, sigma, k from quantization matrix for the given directive
//   - Skips tool calls where ParseToolCall returns an empty target
func (g *GGS) writeMegramsFromToolCalls(outcomes []types.SubTaskOutcome, directive string) {
	if g.mem == nil {
		return
	}
	q, ok := memory.QuantizationMatrix()[directive]
	if !ok {
		return
	}
	seen := make(map[string]bool)
	for _, o := range outcomes {
		if o.Status != "failed" {
			continue
		}
		for _, tc := range o.ToolCalls {
			toolName, target := memory.ParseToolCall(tc)
			if toolName == "" || target == "" {
				continue
			}
			key := toolName + "|" + target
			if seen[key] {
				continue
			}
			seen[key] = true

			content := o.Intent
			if o.FailureReason != nil && *o.FailureReason != "" {
				content += ": " + *o.FailureReason
			}
			meg := types.Megram{
				ID:        uuid.New().String(),
				Level:     "M",
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
				Space:     "tool:" + toolName,
				Entity:    "target:" + target,
				Content:   content,
				State:     directive,
				F:         q.F,
				Sigma:     q.Sigma,
				K:         q.K,
			}
			g.mem.Write(meg)
			if g.b != nil {
				g.b.Publish(types.Message{
					ID:        uuid.New().String(),
					Timestamp: time.Now().UTC(),
					From:      types.RoleGGS,
					To:        types.RoleMemory,
					Type:      types.MsgMegram,
					Payload:   meg,
				})
			}
		}
	}
}

func toReplanRequest(payload any) (types.ReplanRequest, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.ReplanRequest{}, err
	}
	var rr types.ReplanRequest
	return rr, json.Unmarshal(b, &rr)
}

func toOutcomeSummary(payload any) (types.OutcomeSummary, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.OutcomeSummary{}, err
	}
	var os types.OutcomeSummary
	return os, json.Unmarshal(b, &os)
}
