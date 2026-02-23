package agentval

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/tasklog"
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R4a — Agent-Validator. Score the Executor's result and decide: matched, retry, or failed.

Scoring rules:
- Trust tool output (stdout, file paths, command results) as primary evidence. The executor's prose claim alone is not evidence.
- "matched" requires the output to POSITIVELY demonstrate each success criterion with concrete data.
- A vague claim ("task completed", "criteria satisfied") without supporting tool output → retry.

Special rules (apply in order, first match wins):

Infrastructure error rule:
- If output contains "context canceled", "context deadline exceeded", or any network/timeout error → verdict "failed" immediately. Do not retry infrastructure errors.

OS permission rule:
- If tool output contains "Operation not permitted" or "Permission denied" for specific directories (~/Music/Music, ~/Library) — this is an OS constraint, not executor error.
- If accessible directories were searched and permission errors are only on protected paths → "matched".

Empty-result rule:
- If task is to find/list items AND tool_calls show a real search ran AND result is empty → "matched". Absence is a valid answer.
- Send "retry" for empty results ONLY if tool_calls is empty or the search target was clearly wrong (wrong directory, wrong pattern).

For each failed criterion, set failure_class to "logical" (wrong approach, incorrect logic) or "environmental" (network error, timeout, file not found, permission denied).

Output — choose ONE. Always include criteria_results with one entry per success criterion.

Gap closed:
{"verdict":"matched","score":1.0,"criteria_results":[{"criterion":"<exact criterion text>","met":true,"evidence":"<one-line tool output snippet>"}],"unmet_criteria":[]}

Gap non-zero, retries remain:
{"verdict":"retry","score":0.5,"criteria_results":[{"criterion":"<exact criterion text>","met":false,"failure_class":"logical","evidence":"<why it failed>"}],"unmet_criteria":["..."],"what_was_wrong":"<specific observation>","what_to_do":"<concrete alternative action>"}

Failed or infrastructure error:
{"verdict":"failed","score":0.0,"criteria_results":[{"criterion":"<exact criterion text>","met":false,"failure_class":"environmental","evidence":"<why it failed>"}],"unmet_criteria":["..."],"failure_reason":"..."}

No markdown, no prose, no code fences.`

const maxRetries = 2

// AgentValidator is R4a. It drives the fast feedback loop for one sub-task.
type AgentValidator struct {
	llm *llm.Client
	b   *bus.Bus
}

// New creates an AgentValidator.
func New(b *bus.Bus, llmClient *llm.Client) *AgentValidator {
	return &AgentValidator{llm: llmClient, b: b}
}

type criterionResult struct {
	Criterion    string `json:"criterion"`
	Met          bool   `json:"met"`
	Evidence     string `json:"evidence,omitempty"`
	FailureClass string `json:"failure_class,omitempty"` // when Met=false: "logical" | "environmental"
}

type verdict struct {
	Verdict         string            `json:"verdict"` // "matched" | "retry" | "failed"
	Score           float64           `json:"score"`
	CriteriaResults []criterionResult `json:"criteria_results,omitempty"`
	UnmetCriteria   []string          `json:"unmet_criteria"`
	WhatWasWrong    string            `json:"what_was_wrong,omitempty"`
	WhatToDo        string            `json:"what_to_do,omitempty"`
	FailureReason   string            `json:"failure_reason,omitempty"`
}

// aggregateFailureClass returns "logical" | "environmental" | "mixed" | ""
// based on the failed criterionResult entries for one attempt.
//
// Expectations:
//   - Returns "" when no criterionResults or all are Met=true
//   - Returns "logical" when all failed criteria have failure_class=="logical"
//   - Returns "environmental" when all failed criteria have failure_class=="environmental"
//   - Returns "mixed" when both classes are present
func aggregateFailureClass(crs []criterionResult) string {
	logical, env := 0, 0
	for _, cr := range crs {
		if cr.Met {
			continue
		}
		switch cr.FailureClass {
		case "logical":
			logical++
		case "environmental":
			env++
		}
	}
	switch {
	case logical == 0 && env == 0:
		return ""
	case logical > 0 && env == 0:
		return "logical"
	case env > 0 && logical == 0:
		return "environmental"
	default:
		return "mixed"
	}
}

// toCriteriaVerdicts converts internal criterionResult slice to exported CriteriaVerdict slice.
//
// Expectations:
//   - Returns nil when input is nil or empty
//   - Verdict is "pass" when Met=true, "fail" when false
//   - FailureClass and Evidence are forwarded as-is
func toCriteriaVerdicts(crs []criterionResult) []types.CriteriaVerdict {
	if len(crs) == 0 {
		return nil
	}
	out := make([]types.CriteriaVerdict, len(crs))
	for i, cr := range crs {
		verdict := "pass"
		if !cr.Met {
			verdict = "fail"
		}
		out[i] = types.CriteriaVerdict{
			Criterion:    cr.Criterion,
			Verdict:      verdict,
			FailureClass: cr.FailureClass,
			Evidence:     cr.Evidence,
		}
	}
	return out
}

// outcome builds a SubTaskOutcome carrying the original criteria so R4b can check them.
// toolCalls are the tool calls from the final execution attempt, forwarded to R7 (GGS)
// so it can derive blocked_tools for break_symmetry/change_approach directives.
// criteriaVerdicts carries per-criterion verdicts from the final attempt (nil for infra errors).
func (a *AgentValidator) outcome(st types.SubTask, status string, output any, reason *string, traj []types.GapTrajectoryPoint, criteriaVerdicts []types.CriteriaVerdict, toolCalls []string) types.SubTaskOutcome {
	return types.SubTaskOutcome{
		SubTaskID:        st.SubTaskID,
		ParentTaskID:     st.ParentTaskID,
		Intent:           st.Intent,
		SuccessCriteria:  st.SuccessCriteria,
		Status:           status,
		Output:           output,
		FailureReason:    reason,
		GapTrajectory:    traj,
		CriteriaVerdicts: criteriaVerdicts,
		ToolCalls:        toolCalls,
	}
}

// publish sends a SubTaskOutcome to the bus.
func (a *AgentValidator) publish(o types.SubTaskOutcome) {
	a.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleAgentVal,
		To:        types.RoleMetaVal,
		Type:      types.MsgSubTaskOutcome,
		Payload:   o,
	})
}

// Run drives the fast loop for one sub-task.
// resultCh receives ExecutionResult messages from the Executor.
// correctionCh is for sending CorrectionSignals back to the Executor.
// tlog may be nil — all TaskLog methods are nil-safe.
func (a *AgentValidator) Run(
	ctx context.Context,
	subTask types.SubTask,
	resultCh <-chan types.ExecutionResult,
	correctionCh chan<- types.CorrectionSignal,
	tlog *tasklog.TaskLog,
) types.SubTaskOutcome {
	// Log all criteria on a single line (JSON) so they're always visible in one entry,
	// then repeat as numbered lines for easier reading.
	criteriaJSON, _ := json.Marshal(subTask.SuccessCriteria)
	log.Printf("[R4a] subtask=%s seq=%d intent=%q criteria(%d)=%s",
		subTask.SubTaskID, subTask.Sequence, subTask.Intent, len(subTask.SuccessCriteria), criteriaJSON)
	for i, c := range subTask.SuccessCriteria {
		log.Printf("[R4a]   [%d] %q", i+1, c)
	}

	var trajectory []types.GapTrajectoryPoint
	attempt := 0
	var lastToolCalls []string // tool calls from the most recent ExecutionResult, forwarded to GGS

	for {
		// Wait for execution result
		var result types.ExecutionResult
		select {
		case <-ctx.Done():
			reason := "context cancelled"
			o := a.outcome(subTask, "failed", nil, &reason, trajectory, nil, lastToolCalls)
			tlog.SubtaskEnd(subTask.SubTaskID, "failed")
			return o
		case r, ok := <-resultCh:
			if !ok {
				reason := "result channel closed"
				o := a.outcome(subTask, "failed", nil, &reason, trajectory, nil, lastToolCalls)
				tlog.SubtaskEnd(subTask.SubTaskID, "failed")
				return o
			}
			result = r
			lastToolCalls = result.ToolCalls
		}

		attempt++
		log.Printf("[R4a] scoring subtask=%s attempt=%d status=%s", subTask.SubTaskID, attempt, result.Status)

		v, err := a.score(ctx, subTask, result, tlog)
		if err != nil {
			log.Printf("[R4a] ERROR scoring: %v", err)
			reason := fmt.Sprintf("scoring error: %v", err)
			v = &verdict{Verdict: "failed", Score: 0, FailureReason: reason}
		}

		// Log the full verdict with per-criterion ✓/✗ so the gap between
		// criteria and outcome is explicit — no manual cross-referencing needed.
		log.Printf("[R4a] subtask=%s attempt=%d verdict=%s score=%.2f",
			subTask.SubTaskID, attempt, v.Verdict, v.Score)
		if len(v.CriteriaResults) > 0 {
			// Preferred path: LLM returned per-criterion breakdown.
			// Print summary JSON on one line first (never lost in scrollback),
			// then repeat as readable numbered lines.
			crJSON, _ := json.Marshal(v.CriteriaResults)
			log.Printf("[R4a]   criteria_results=%s", crJSON)
			for i, cr := range v.CriteriaResults {
				mark := "✓"
				if !cr.Met {
					mark = "✗"
				}
				if cr.Evidence != "" {
					log.Printf("[R4a]   [%s] [%d] %q — %s", mark, i+1, cr.Criterion, cr.Evidence)
				} else {
					log.Printf("[R4a]   [%s] [%d] %q", mark, i+1, cr.Criterion)
				}
			}
		} else {
			// Fallback: LLM omitted criteria_results; show original list untagged
			// and the unmet list so reader can compare.
			unmetJSON, _ := json.Marshal(v.UnmetCriteria)
			log.Printf("[R4a]   criteria_results=(none) unmet=%s", unmetJSON)
			for i, c := range subTask.SuccessCriteria {
				log.Printf("[R4a]   [?] [%d] %q", i+1, c)
			}
		}
		if v.WhatWasWrong != "" {
			log.Printf("[R4a]   wrong:  %s", v.WhatWasWrong)
		}
		if v.WhatToDo != "" {
			log.Printf("[R4a]   todo:   %s", v.WhatToDo)
		}
		if v.FailureReason != "" {
			log.Printf("[R4a]   reason: %s", v.FailureReason)
		}

		// Log per-criterion verdicts to the task log.
		for _, cr := range v.CriteriaResults {
			tlog.CriterionVerdict(subTask.SubTaskID, cr.Criterion, cr.Met, cr.Evidence, attempt)
		}

		trajectory = append(trajectory, types.GapTrajectoryPoint{
			Attempt:       attempt,
			Score:         v.Score,
			UnmetCriteria: v.UnmetCriteria,
			FailureClass:  aggregateFailureClass(v.CriteriaResults),
		})

		switch v.Verdict {
		case "matched":
			log.Printf("[R4a] subtask=%s MATCHED on attempt=%d", subTask.SubTaskID, attempt)
			o := a.outcome(subTask, "matched", result.Output, nil, trajectory, toCriteriaVerdicts(v.CriteriaResults), lastToolCalls)
			tlog.SubtaskEnd(subTask.SubTaskID, "matched")
			a.publish(o)
			return o

		case "retry":
			if attempt >= maxRetries {
				log.Printf("[R4a] subtask=%s max retries reached, reporting failed", subTask.SubTaskID)
				reason := fmt.Sprintf("max retries (%d) reached; last issue: %s", maxRetries, v.WhatWasWrong)
				o := a.outcome(subTask, "failed", result.Output, &reason, trajectory, toCriteriaVerdicts(v.CriteriaResults), lastToolCalls)
				tlog.SubtaskEnd(subTask.SubTaskID, "failed")
				a.publish(o)
				return o
			}

			// Extract the first failed criterion and its failure_class for the CorrectionSignal.
			failedCriterion, failureClass := "", ""
			for _, cr := range v.CriteriaResults {
				if !cr.Met {
					failedCriterion, failureClass = cr.Criterion, cr.FailureClass
					break
				}
			}
			if failedCriterion == "" && len(v.UnmetCriteria) > 0 {
				failedCriterion = v.UnmetCriteria[0]
			}

			correction := types.CorrectionSignal{
				SubTaskID:       subTask.SubTaskID,
				AttemptNumber:   attempt,
				FailedCriterion: failedCriterion,
				FailureClass:    failureClass,
				WhatWasWrong:    v.WhatWasWrong,
				WhatToDo:        v.WhatToDo,
			}
			tlog.Correction(subTask.SubTaskID, v.WhatWasWrong, v.WhatToDo, attempt)
			a.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleAgentVal,
				To:        types.RoleExecutor,
				Type:      types.MsgCorrectionSignal,
				Payload:   correction,
			})
			select {
			case correctionCh <- correction:
			case <-ctx.Done():
				reason := "context cancelled during correction"
				o := a.outcome(subTask, "failed", nil, &reason, trajectory, nil, lastToolCalls)
				tlog.SubtaskEnd(subTask.SubTaskID, "failed")
				return o
			}

		default: // "failed"
			reason := v.FailureReason
			if reason == "" {
				reason = "validation failed"
			}
			log.Printf("[R4a] subtask=%s FAILED: %s", subTask.SubTaskID, reason)
			o := a.outcome(subTask, "failed", result.Output, &reason, trajectory, toCriteriaVerdicts(v.CriteriaResults), lastToolCalls)
			tlog.SubtaskEnd(subTask.SubTaskID, "failed")
			a.publish(o)
			return o
		}
	}
}

func (a *AgentValidator) score(ctx context.Context, st types.SubTask, result types.ExecutionResult, tlog *tasklog.TaskLog) (*verdict, error) {
	taskJSON, _ := json.MarshalIndent(st, "", "  ")
	resultJSON, _ := json.MarshalIndent(result, "", "  ")

	today := time.Now().UTC().Format("2006-01-02")
	userPrompt := fmt.Sprintf("Today's date: %s\n\nSubTask:\n%s\n\nExecutionResult:\n%s", today, taskJSON, resultJSON)

	raw, usage, err := a.llm.Chat(ctx, systemPrompt, userPrompt)
	tlog.LLMCall("agentval", systemPrompt, userPrompt, raw, usage.PromptTokens, usage.CompletionTokens, 0)
	if err != nil {
		return nil, err
	}
	raw = llm.StripFences(raw)

	var v verdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("parse verdict: %w (raw: %s)", err, raw)
	}
	return &v, nil
}
