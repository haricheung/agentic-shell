package ggs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/types"
)

// Loss hyperparameters (v0.7 defaults).
const (
	alpha         = 0.6   // weight on intent-result distance D
	beta          = 0.3   // weight on process implausibility P (before adaptive scaling)
	lambda        = 0.4   // weight on resource cost Ω
	w1            = 0.6   // Ω sub-weight for replan count
	w2            = 0.4   // Ω sub-weight for elapsed time
	epsilon       = 0.1   // plateau detection threshold for |∇L|
	delta         = 0.3   // minimum D to trigger break_symmetry / change directives
	abandonOmega  = 0.8   // Ω threshold above which directive becomes abandon regardless of gradient
	timeBudgetMs  = 300_000 // default time budget per task (5 min)
	maxReplansGGS = 3     // matches R4b's maxReplans; used in Ω computation
)

// GGS is R7 — Goal Gradient Solver. It sits between R4b (sensor) and R2 (actuator)
// in the medium loop. It receives ReplanRequest from R4b, computes D, P, Ω, L, ∇L,
// selects a directive from the decision table, and emits PlanDirective to R2.
//
// When Ω ≥ abandonOmega, GGS handles abandonment directly (publishes FinalResult
// and calls outputFn) and does NOT forward a PlanDirective to R2.
type GGS struct {
	b        *bus.Bus
	outputFn func(taskID, summary string, output any)
	mu       sync.Mutex
	lPrev    map[string]float64 // L_{t-1} per task_id
	replans  map[string]int     // replan round counter per task_id
}

// New creates a GGS.
func New(b *bus.Bus, outputFn func(taskID, summary string, output any)) *GGS {
	return &GGS{
		b:        b,
		outputFn: outputFn,
		lPrev:    make(map[string]float64),
		replans:  make(map[string]int),
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
				log.Printf("[R7] ERROR: bad ReplanRequest: %v", err)
				continue
			}
			log.Printf("[R7] received ReplanRequest task=%s gap=%q corrections=%d elapsed=%dms",
				rr.TaskID, rr.GapSummary, rr.CorrectionCount, rr.ElapsedMs)
			go g.process(ctx, rr)
		case msg, ok := <-acceptCh:
			if !ok {
				return
			}
			os, err := toOutcomeSummary(msg.Payload)
			if err != nil {
				log.Printf("[R7] ERROR: bad OutcomeSummary: %v", err)
				continue
			}
			log.Printf("[R7] received OutcomeSummary task=%s elapsed=%dms — D=0 accept path",
				os.TaskID, os.ElapsedMs)
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
	g.mu.Unlock()

	// Compute loss components.
	D := computeD(rr.Outcomes)
	P := computeP(rr.Outcomes)
	Omega := computeOmega(replanCount, rr.ElapsedMs)
	L := computeLoss(D, P, Omega)

	// Store L for next round's gradient.
	g.mu.Lock()
	g.lPrev[taskID] = L
	g.mu.Unlock()

	// Compute gradient (∇L = L_t − L_{t-1}).
	// Q1: first round — L_prev undefined → ∇L = 0, treat as stable/plateau if D high.
	var gradL float64
	if hasPrev {
		gradL = L - lPrev
	}
	// hasPrev == false → gradL = 0 (first round)

	gradient := computeGradient(gradL, D)
	directive := selectDirective(gradL, D, P, Omega, gradient)
	blockedTools := deriveBlockedTools(rr.Outcomes, directive)
	failedCriterion := primaryFailedCriterion(rr.Outcomes)
	failureClass := computeFailureClass(rr.Outcomes)
	rationale := buildRationale(directive, gradient, D, P, Omega, gradL, rr.GapSummary)

	log.Printf("[R7] task=%s round=%d D=%.3f P=%.3f Ω=%.3f L=%.3f ∇L=%.3f gradient=%s directive=%s",
		taskID, replanCount, D, P, Omega, L, gradL, gradient, directive)

	// Budget exceeded — GGS handles abandonment directly without routing to R2.
	if directive == "abandon" {
		log.Printf("[R7] task=%s ABANDON (Ω=%.3f ≥ %.1f)", taskID, Omega, abandonOmega)
		summary := buildAbandonSummary(rr)

		// Publish FinalResult on bus for auditor + REPL subscription.
		// Loss/GradL/Replans populated for trajectory checkpoint display.
		g.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleGGS,
			To:        types.RoleUser,
			Type:      types.MsgFinalResult,
			Payload: types.FinalResult{
				TaskID:  taskID,
				Summary: summary,
				Loss:    types.LossBreakdown{D: D, P: P, Omega: Omega, L: L},
				GradL:   gradL,
				Replans: replanCount,
			},
		})
		if g.outputFn != nil {
			g.outputFn(taskID, summary, nil)
		}

		// Clean up per-task state.
		g.mu.Lock()
		delete(g.lPrev, taskID)
		delete(g.replans, taskID)
		g.mu.Unlock()
		return
	}

	pd := types.PlanDirective{
		TaskID: taskID,
		Loss: types.LossBreakdown{
			D:     D,
			P:     P,
			Omega: Omega,
			L:     L,
		},
		Gradient:        gradient,
		Directive:       directive,
		BlockedTools:    blockedTools,
		FailedCriterion: failedCriterion,
		FailureClass:    failureClass,
		BudgetPressure:  Omega,
		GradL:           gradL,
		Rationale:       rationale,
	}

	log.Printf("[R7] task=%s emitting PlanDirective directive=%s blocked_tools=%v",
		taskID, directive, blockedTools)

	g.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleGGS,
		To:        types.RolePlanner,
		Type:      types.MsgPlanDirective,
		Payload:   pd,
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
func (g *GGS) processAccept(_ context.Context, os types.OutcomeSummary) {
	taskID := os.TaskID

	g.mu.Lock()
	lPrev, hasPrev := g.lPrev[taskID]
	replanCount := g.replans[taskID] // 0 for first-try accepts; >0 if GGS directed prior replans
	g.mu.Unlock()

	// D=0: all subtasks matched. P=0.5: no failures → neutral. Ω: elapsed time + prior replans.
	const D, P = 0.0, 0.5
	Omega := computeOmega(replanCount, os.ElapsedMs)
	L := computeLoss(D, P, Omega)

	var gradL float64
	if hasPrev {
		gradL = L - lPrev // ∇L across rounds (e.g. L after first replan → 0 on final accept)
	}
	gradient := computeGradient(gradL, D) // always "stable": D=0 ≤ δ=0.3

	log.Printf("[R7] task=%s ACCEPT D=0.000 P=0.500 Ω=%.3f L=%.3f ∇L=%.3f gradient=%s replans=%d",
		taskID, Omega, L, gradL, gradient, replanCount)

	// Clean up per-task state (task is done).
	g.mu.Lock()
	delete(g.lPrev, taskID)
	delete(g.replans, taskID)
	g.mu.Unlock()

	// GGS is the sole emitter of FinalResult — consistent path for both accept and abandon.
	// Loss, GradL, Replans are populated so the UI can display the full trajectory checkpoint.
	g.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleGGS,
		To:        types.RoleUser,
		Type:      types.MsgFinalResult,
		Payload: types.FinalResult{
			TaskID:  taskID,
			Summary: os.Summary,
			Output:  os.MergedOutput,
			Loss:    types.LossBreakdown{D: D, P: P, Omega: Omega, L: L},
			GradL:   gradL,
			Replans: replanCount,
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

// selectDirective chooses the directive from the decision table.
//
// Decision table (Ω ≥ abandonOmega always wins):
//   ∇L < 0 (improving), Ω < abandonOmega → refine
//   plateau, D > δ, P > 0.5 (logical), Ω < abandonOmega → break_symmetry
//   plateau, D > δ, P ≤ 0.5 (environmental), Ω < abandonOmega → change_path
//   ∇L > 0 (worsening), D > δ, P > 0.5 (logical), Ω < abandonOmega → change_approach
//   ∇L > 0 (worsening), D > δ, P ≤ 0.5 (environmental), Ω < abandonOmega → refine
//   any, Ω ≥ abandonOmega → abandon
//
// Expectations:
//   - Returns "abandon" when Omega >= abandonOmega regardless of other values
//   - Returns "refine" when gradient is "improving"
//   - Returns "break_symmetry" when gradient is "plateau" and P > 0.5
//   - Returns "change_path" when gradient is "plateau" and P <= 0.5
//   - Returns "change_approach" when gradient is "worsening" and P > 0.5
//   - Returns "refine" when gradient is "worsening" and P <= 0.5
//   - Returns "refine" for "stable" gradient (converged, minor tightening)
func selectDirective(gradL, D, P, Omega float64, gradient string) string {
	if Omega >= abandonOmega {
		return "abandon"
	}
	highP := P > 0.5
	switch gradient {
	case "improving":
		return "refine"
	case "plateau":
		if highP {
			return "break_symmetry"
		}
		return "change_path"
	case "worsening":
		if highP {
			return "change_approach"
		}
		return "refine" // "refine with path hint" — environment is the issue
	default: // "stable" — D ≤ δ, near-converged
		return "refine"
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

// buildRationale produces a human-readable explanation of the directive.
func buildRationale(directive, gradient string, D, P, Omega, gradL float64, gapSummary string) string {
	switch directive {
	case "refine":
		if gradient == "improving" {
			return fmt.Sprintf("Loss decreasing (∇L=%.3f) — on the right track. Tighten parameters. Gap: %s", gradL, gapSummary)
		}
		return fmt.Sprintf("Environmental issue suspected (P=%.2f ≤ 0.5). Same tool sequence, adjust path/parameters. Gap: %s", P, gapSummary)
	case "change_path":
		return fmt.Sprintf("Plateau detected (|∇L|=%.3f < ε=%.1f, D=%.2f > δ=%.1f). Environmental origin (P=%.2f). Same approach, different target/parameters. Gap: %s",
			math.Abs(gradL), epsilon, D, delta, P, gapSummary)
	case "change_approach":
		return fmt.Sprintf("Loss worsening (∇L=%.3f) with logical failures (P=%.2f > 0.5). Escalate: use explicitly different tool class. Gap: %s", gradL, P, gapSummary)
	case "break_symmetry":
		return fmt.Sprintf("Local minimum (|∇L|=%.3f < ε=%.1f, D=%.2f > δ=%.1f, P=%.2f). Block all tried tools; demand novel approach. Gap: %s",
			math.Abs(gradL), epsilon, D, delta, P, gapSummary)
	case "abandon":
		return fmt.Sprintf("Budget exhausted (Ω=%.3f ≥ %.1f). Continued replanning cost exceeds gap cost. Gap: %s", Omega, abandonOmega, gapSummary)
	default:
		return gapSummary
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
