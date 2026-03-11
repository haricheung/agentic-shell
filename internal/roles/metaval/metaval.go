package metaval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/tasklog"
	"github.com/haricheung/agentic-shell/internal/types"
)

// Note: R4b no longer writes to R5. GGS is the sole writer to Shared Memory (R5).

const systemPrompt = `You are R4b — Meta-Validator. Merge SubTaskOutcome results and verify the combined output satisfies the task criteria.

You receive task_criteria (written by R2 to define what the COMBINED output must achieve) and all SubTaskOutcome results. Every subtask has already passed its own individual check.
Your job: verify that the MERGED output satisfies every task criterion.

Assessment rules:
- "accept" ONLY when the combined outputs POSITIVELY demonstrate every task criterion.
- "replan" if any task criterion is unmet. State exactly which criterion failed and why.
- Do NOT accept on vague grounds. Absence of failure is not the same as presence of evidence.

merged_output rules:
- Combine all subtask outputs into a single user-facing result string or object.
- Include concrete data (file paths, values, counts) — not process descriptions.
- Omit intermediate steps (file discovery, etc.) unless they are the answer.

JSON encoding rules (MANDATORY):
- Output ONLY raw JSON — no markdown, no prose, no code fences.
- Never write bare ASCII double-quote characters (") inside string values.
  Use Unicode curly quotes (\u201c \u201d) or rephrase without quoting instead.
- Every backslash inside a string value must be escaped as \\.

Output — choose ONE:

All criteria met:
{"verdict":"accept","summary":"<one sentence for the user>","merged_output":"<combined result>"}

Criteria unmet, replanning possible:
{"verdict":"replan","gap_summary":"<which criterion failed and why>","failed_subtasks":["<subtask_id>"],"recommendation":"replan"}`

// manifestTracker tracks incoming SubTaskOutcomes for a given dispatch manifest
type manifestTracker struct {
	spec          types.TaskSpec
	manifest      types.DispatchManifest
	outcomes      []types.SubTaskOutcome
	expectedCount int
}

// MetaValidator is R4b. It collects SubTaskOutcomes and merges results.
type MetaValidator struct {
	llm    *llm.Client
	b      *bus.Bus
	logReg *tasklog.Registry
	mu     sync.Mutex
	trackers   map[string]*manifestTracker // taskID -> tracker
	taskStart  map[string]time.Time        // taskID -> time first manifest was received
	replanCounts map[string]int            // replan round counter for maxReplans safety net
	// outputFn is called when a final result is ready for the user
	outputFn func(taskID, summary string, output any)
}

// New creates a MetaValidator.
func New(b *bus.Bus, llmClient *llm.Client, outputFn func(taskID, summary string, output any), logReg *tasklog.Registry) *MetaValidator {
	return &MetaValidator{
		llm:          llmClient,
		b:            b,
		logReg:       logReg,
		trackers:     make(map[string]*manifestTracker),
		taskStart:    make(map[string]time.Time),
		replanCounts: make(map[string]int),
		outputFn:     outputFn,
	}
}

// Run listens for DispatchManifest and SubTaskOutcome messages.
func (m *MetaValidator) Run(ctx context.Context) {
	manifestCh := m.b.Subscribe(types.MsgDispatchManifest)
	outcomeCh := m.b.Subscribe(types.MsgSubTaskOutcome)

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-manifestCh:
			if !ok {
				return
			}
			manifest, err := toDispatchManifest(msg.Payload)
			if err != nil {
				slog.Error("[R4b] bad DispatchManifest", "error", err)
				continue
			}

			var spec types.TaskSpec
			if manifest.TaskSpec != nil {
				spec = *manifest.TaskSpec
			}

			m.mu.Lock()
			m.trackers[manifest.TaskID] = &manifestTracker{
				spec:          spec,
				manifest:      manifest,
				expectedCount: len(manifest.SubTaskIDs),
			}
			// Record start time on first manifest for GGS Ω elapsed time computation.
			if _, seen := m.taskStart[manifest.TaskID]; !seen {
				m.taskStart[manifest.TaskID] = time.Now().UTC()
			}
			m.mu.Unlock()
			slog.Debug("[R4b] tracking task", "task", manifest.TaskID, "expecting", len(manifest.SubTaskIDs))

		case msg, ok := <-outcomeCh:
			if !ok {
				return
			}
			outcome, err := toSubTaskOutcome(msg.Payload)
			if err != nil {
				slog.Error("[R4b] bad SubTaskOutcome", "error", err)
				continue
			}

			m.mu.Lock()
			tracker, found := m.trackers[outcome.ParentTaskID]
			if !found {
				m.mu.Unlock()
				slog.Warn("[R4b] outcome for unknown task", "task", outcome.ParentTaskID)
				continue
			}
			tracker.outcomes = append(tracker.outcomes, outcome)
			complete := len(tracker.outcomes) >= tracker.expectedCount
			m.mu.Unlock()

			slog.Debug("[R4b] outcome received", "subtask", outcome.SubTaskID, "status", outcome.Status, "received", len(tracker.outcomes), "expected", tracker.expectedCount)

			if complete {
				go m.evaluate(ctx, tracker)
			}
		}
	}
}

func (m *MetaValidator) evaluate(ctx context.Context, tracker *manifestTracker) {
	taskID := tracker.manifest.TaskID

	// Collect failed sub-tasks
	var failedIDs []string
	totalCorrections := 0
	for _, o := range tracker.outcomes {
		if o.Status == "failed" {
			failedIDs = append(failedIDs, o.SubTaskID)
		}
		totalCorrections += len(o.GapTrajectory)
	}

	// Log the full criteria set R4b is evaluating against so it's auditable.
	slog.Info("[R4b] evaluating outcomes", "task", taskID, "total", len(tracker.outcomes), "failed", len(failedIDs))
	for _, o := range tracker.outcomes {
		criteriaJSON, _ := json.Marshal(o.SuccessCriteria)
		slog.Debug("[R4b] subtask outcome detail", "subtask", o.SubTaskID, "status", o.Status, "criteria", string(criteriaJSON))
	}

	// Hard gate (code-enforced, not LLM-dependent): any failed subtask forces
	// replan immediately. The LLM is only called when ALL subtasks matched so
	// it cannot override a failed status by reasoning about "overall goal".
	if len(failedIDs) > 0 {
		slog.Info("[R4b] hard-gate replan", "task", taskID, "failed_count", len(failedIDs), "failed_ids", failedIDs)
		// Build gap summary with actual failure reasons so GGS and memory get
		// actionable text, not just subtask IDs.
		var gapParts []string
		for _, o := range tracker.outcomes {
			if o.Status == "failed" {
				if o.FailureReason != nil && *o.FailureReason != "" {
					gapParts = append(gapParts, *o.FailureReason)
				} else {
					gapParts = append(gapParts, o.SubTaskID)
				}
			}
		}
		gapSummary := strings.Join(gapParts, "; ")
		if gapSummary == "" {
			gapSummary = fmt.Sprintf("%d subtask(s) failed: %v", len(failedIDs), failedIDs)
		}
		m.triggerReplan(ctx, tracker, failedIDs, totalCorrections, gapSummary)
		return
	}

	// All subtasks matched — call LLM to merge outputs and verify task_criteria.
	outcomesJSON, _ := json.MarshalIndent(tracker.outcomes, "", "  ")
	criteriaJSON, _ := json.MarshalIndent(tracker.manifest.TaskCriteria, "", "  ")
	userPrompt := fmt.Sprintf(
		"Task intent: %s\n\nTask criteria (written by R2 — ALL must be satisfied by the combined output):\n%s\n\nSubTaskOutcomes:\n%s\n\nMerge the subtask outputs and verify all task criteria are met.",
		tracker.spec.Intent, criteriaJSON, outcomesJSON)

	raw, usage, err := m.llm.Chat(ctx, systemPrompt, userPrompt)
	tl := m.logReg.Get(taskID)
	tl.LLMCall("metaval", systemPrompt, userPrompt, raw, usage.PromptTokens, usage.CompletionTokens, usage.ElapsedMs, 0)
	if err != nil {
		slog.Error("[R4b] LLM call failed", "error", err)
		return
	}
	raw = extractJSON(llm.StripFences(raw))

	var v struct {
		Verdict        string   `json:"verdict"`
		Summary        string   `json:"summary"`
		MergedOutput   any      `json:"merged_output"`
		GapSummary     string   `json:"gap_summary"`
		FailedSubtasks []string `json:"failed_subtasks"`
		Recommendation string   `json:"recommendation"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		slog.Error("[R4b] parse verdict failed", "error", err, "raw", raw)
		m.triggerReplan(ctx, tracker, nil, totalCorrections,
			"metaval verdict parse error: "+err.Error())
		return
	}

	switch v.Verdict {
	case "accept":
		slog.Info("[R4b] task ACCEPTED, forwarding to GGS", "task", taskID)
		// Snapshot cost metrics BEFORE Close() removes the log from the registry.
		tl := m.logReg.Get(taskID)
		_ = tl.Stats() // snapshot for future use; GGS handles memory writes
		m.logReg.Close(taskID, "accepted") // write task_end and flush before delivering result

		// Forward to GGS so it records the final loss (D=0) and delivers the result.
		// GGS is the sole decision-maker in the medium loop — it closes the loop even
		// on the happy path by computing L_t and updating L_prev before emitting FinalResult.
		// GGS also writes the "accept" Megram to R5 (GGS is the sole writer to Shared Memory).
		m.mu.Lock()
		start, hasStart := m.taskStart[taskID]
		outcomes := append([]types.SubTaskOutcome(nil), tracker.outcomes...)
		m.mu.Unlock()
		var elapsedMs int64
		if hasStart {
			elapsedMs = time.Since(start).Milliseconds()
		}
		m.b.Publish(types.Message{
			ID:        uuid.New().String(),
			Timestamp: time.Now().UTC(),
			From:      types.RoleMetaVal,
			To:        types.RoleGGS,
			Type:      types.MsgOutcomeSummary,
			Payload: types.OutcomeSummary{
				TaskID:       taskID,
				Intent:       tracker.spec.Intent,
				Summary:      v.Summary,
				MergedOutput: v.MergedOutput,
				ElapsedMs:    elapsedMs,
				Outcomes:     outcomes,
			},
		})

		// Clean up tracker and per-task timing
		m.mu.Lock()
		delete(m.trackers, taskID)
		delete(m.taskStart, taskID)
		delete(m.replanCounts, taskID)
		m.mu.Unlock()

	case "replan":
		m.triggerReplan(ctx, tracker, failedIDs, totalCorrections, v.GapSummary)
	}
}

// aggregateFailureClassFromOutcomes derives the dominant failure_class from
// SubTaskOutcome.CriteriaVerdicts across all failed outcomes.
//
// Expectations:
//   - Returns "" when no failed outcomes or no classified criteria
//   - Returns "logical" when logical count > environmental count
//   - Returns "environmental" when environmental count > logical count
//   - Returns "mixed" when tied and non-zero
//   - Ignores matched outcomes
func aggregateFailureClassFromOutcomes(outcomes []types.SubTaskOutcome) string {
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
	switch {
	case logical == 0 && env == 0:
		return ""
	case logical > env:
		return "logical"
	case env > logical:
		return "environmental"
	default:
		return "mixed"
	}
}

// triggerReplan handles the replan path for both the hard gate (code-enforced
// failed subtask check) and the LLM-driven replan verdict. It publishes a
// ReplanRequest to R7 (GGS), and resets the tracker. GGS owns gradient
// computation and terminal state (abandon/success) handling.
//
// Expectations:
//   - Sends Recommendation="abandon" to GGS when replanCount >= maxReplans
//   - Resets tracker.outcomes so the next round starts clean
//   - Increments replanCounts before checking the limit
//   - Sends ReplanRequest to R7 (GGS), not R2 (Planner)
//   - Includes full outcomes and elapsed_ms in ReplanRequest for GGS gradient computation
func (m *MetaValidator) triggerReplan(ctx context.Context, tracker *manifestTracker, failedIDs []string, totalCorrections int, gapSummary string) {
	const maxReplans = 3
	taskID := tracker.manifest.TaskID

	m.mu.Lock()
	m.replanCounts[taskID]++
	replanCount := m.replanCounts[taskID]
	start, hasStart := m.taskStart[taskID]
	outcomes := append([]types.SubTaskOutcome(nil), tracker.outcomes...) // snapshot before reset
	m.mu.Unlock()

	tl := m.logReg.Get(taskID)
	tl.Replan(gapSummary, replanCount)

	// Always route through GGS so it computes proper loss (D, P, Ω, L, ∇L)
	// and writes terminal Megrams. When replanCount >= maxReplans, signal
	// "abandon" via Recommendation — GGS honours this and force-abandons
	// after computing the full loss breakdown.
	recommendation := "replan"
	if replanCount >= maxReplans {
		recommendation = "abandon"
		slog.Info("[R4b] recommending abandon to GGS", "task", taskID, "replan_rounds", replanCount)
	}

	// Compute elapsed time for GGS Ω calculation.
	var elapsedMs int64
	if hasStart {
		elapsedMs = time.Since(start).Milliseconds()
	}

	rr := types.ReplanRequest{
		TaskID:          taskID,
		Intent:          tracker.spec.Intent,
		GapSummary:      gapSummary,
		FailedSubTasks:  failedIDs,
		CorrectionCount: totalCorrections,
		ElapsedMs:       elapsedMs,
		Outcomes:        outcomes,
		Recommendation:  recommendation,
	}
	slog.Info("[R4b] sending ReplanRequest to GGS", "task", taskID, "round", replanCount, "gap", gapSummary, "elapsed_ms", elapsedMs)
	m.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleMetaVal,
		To:        types.RoleGGS,
		Type:      types.MsgReplanRequest,
		Payload:   rr,
	})

	m.mu.Lock()
	if recommendation == "abandon" {
		// Terminal — GGS will handle FinalResult; clean up per-task state.
		delete(m.trackers, taskID)
		delete(m.replanCounts, taskID)
		delete(m.taskStart, taskID)
	} else {
		tracker.outcomes = nil
	}
	m.mu.Unlock()
}


// extractJSON finds the first complete JSON object in s by brace-matching.
// Returns the extracted substring or s unchanged if no complete object is found.
// Handles nested objects and strings with escaped characters correctly.
//
// Expectations:
//   - Returns s unchanged when s contains no '{'
//   - Returns the first complete {...} object, stripping leading/trailing prose
//   - Returns s unchanged when braces are unbalanced (no complete object)
//   - Handles nested braces correctly
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return s
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		b := s[i]
		if escaped {
			escaped = false
			continue
		}
		switch b {
		case '\\':
			if inStr {
				escaped = true
			}
		case '"':
			inStr = !inStr
		case '{':
			if !inStr {
				depth++
			}
		case '}':
			if !inStr {
				depth--
				if depth == 0 {
					return s[start : i+1]
				}
			}
		}
	}
	return s
}

func toDispatchManifest(payload any) (types.DispatchManifest, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.DispatchManifest{}, err
	}
	var d types.DispatchManifest
	return d, json.Unmarshal(b, &d)
}

func toSubTaskOutcome(payload any) (types.SubTaskOutcome, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return types.SubTaskOutcome{}, err
	}
	var o types.SubTaskOutcome
	return o, json.Unmarshal(b, &o)
}
