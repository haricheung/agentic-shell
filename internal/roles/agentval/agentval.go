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

Output — choose ONE:

Gap closed:
{"verdict":"matched","score":1.0,"unmet_criteria":[]}

Gap non-zero, retries remain:
{"verdict":"retry","score":0.5,"unmet_criteria":["..."],"what_was_wrong":"<specific observation>","what_to_do":"<concrete alternative action>"}

Failed or infrastructure error:
{"verdict":"failed","score":0.0,"unmet_criteria":["..."],"failure_reason":"..."}

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

type verdict struct {
	Verdict       string   `json:"verdict"` // "matched" | "retry" | "failed"
	Score         float64  `json:"score"`
	UnmetCriteria []string `json:"unmet_criteria"`
	WhatWasWrong  string   `json:"what_was_wrong,omitempty"`
	WhatToDo      string   `json:"what_to_do,omitempty"`
	FailureReason string   `json:"failure_reason,omitempty"`
}

// Run drives the fast loop for one sub-task.
// resultCh receives ExecutionResult messages from the Executor.
// correctionCh is for sending CorrectionSignals back to the Executor.
func (a *AgentValidator) Run(
	ctx context.Context,
	subTask types.SubTask,
	resultCh <-chan types.ExecutionResult,
	correctionCh chan<- types.CorrectionSignal,
) types.SubTaskOutcome {
	// Log full criteria once before the loop so they're always visible in the debug log.
	log.Printf("[R4a] subtask=%s seq=%d intent=%q criteria(%d):",
		subTask.SubTaskID, subTask.Sequence, subTask.Intent, len(subTask.SuccessCriteria))
	for i, c := range subTask.SuccessCriteria {
		log.Printf("[R4a]   [%d] %s", i+1, c)
	}

	var trajectory []types.GapTrajectoryPoint
	attempt := 0

	for {
		// Wait for execution result
		var result types.ExecutionResult
		select {
		case <-ctx.Done():
			reason := "context cancelled"
			return types.SubTaskOutcome{
				SubTaskID:    subTask.SubTaskID,
				ParentTaskID: subTask.ParentTaskID,
				Status:       "failed",
				FailureReason: &reason,
				GapTrajectory: trajectory,
			}
		case r, ok := <-resultCh:
			if !ok {
				reason := "result channel closed"
				return types.SubTaskOutcome{
					SubTaskID:    subTask.SubTaskID,
					ParentTaskID: subTask.ParentTaskID,
					Status:       "failed",
					FailureReason: &reason,
					GapTrajectory: trajectory,
				}
			}
			result = r
		}

		attempt++
		log.Printf("[R4a] scoring subtask=%s attempt=%d status=%s", subTask.SubTaskID, attempt, result.Status)

		v, err := a.score(ctx, subTask, result)
		if err != nil {
			log.Printf("[R4a] ERROR scoring: %v", err)
			reason := fmt.Sprintf("scoring error: %v", err)
			v = &verdict{Verdict: "failed", Score: 0, FailureReason: reason}
		}

		// Log the full verdict cross-referenced against the original criteria so the
		// reader never has to scroll up to correlate criteria with the outcome.
		log.Printf("[R4a] subtask=%s attempt=%d verdict=%s score=%.2f",
			subTask.SubTaskID, attempt, v.Verdict, v.Score)
		if v.Verdict == "matched" {
			// All criteria satisfied — list them explicitly.
			for i, c := range subTask.SuccessCriteria {
				log.Printf("[R4a]   [✓] [%d] %s", i+1, c)
			}
		} else {
			// Show original criteria, then the validator's unmet list, so the
			// reader can compare them even when the LLM paraphrases.
			log.Printf("[R4a]   original criteria:")
			for i, c := range subTask.SuccessCriteria {
				log.Printf("[R4a]     [%d] %s", i+1, c)
			}
			if len(v.UnmetCriteria) > 0 {
				log.Printf("[R4a]   unmet (validator's assessment):")
				for i, c := range v.UnmetCriteria {
					log.Printf("[R4a]     [%d] %s", i+1, c)
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
		}

		trajectory = append(trajectory, types.GapTrajectoryPoint{
			Attempt:      attempt,
			Score:        v.Score,
			UnmetCriteria: v.UnmetCriteria,
		})

		switch v.Verdict {
		case "matched":
			log.Printf("[R4a] subtask=%s MATCHED on attempt=%d", subTask.SubTaskID, attempt)
			outcome := types.SubTaskOutcome{
				SubTaskID:     subTask.SubTaskID,
				ParentTaskID:  subTask.ParentTaskID,
				Status:        "matched",
				Output:        result.Output,
				GapTrajectory: trajectory,
			}
			a.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleAgentVal,
				To:        types.RoleMetaVal,
				Type:      types.MsgSubTaskOutcome,
				Payload:   outcome,
			})
			return outcome

		case "retry":
			if attempt >= maxRetries {
				log.Printf("[R4a] subtask=%s max retries reached, reporting failed", subTask.SubTaskID)
				reason := fmt.Sprintf("max retries (%d) reached; last issue: %s", maxRetries, v.WhatWasWrong)
				outcome := types.SubTaskOutcome{
					SubTaskID:     subTask.SubTaskID,
					ParentTaskID:  subTask.ParentTaskID,
					Status:        "failed",
					Output:        result.Output,
					FailureReason: &reason,
					GapTrajectory: trajectory,
				}
				a.b.Publish(types.Message{
					ID:        uuid.New().String(),
					Timestamp: time.Now().UTC(),
					From:      types.RoleAgentVal,
					To:        types.RoleMetaVal,
					Type:      types.MsgSubTaskOutcome,
					Payload:   outcome,
				})
				return outcome
			}

			correction := types.CorrectionSignal{
				SubTaskID:     subTask.SubTaskID,
				AttemptNumber: attempt,
				WhatWasWrong:  v.WhatWasWrong,
				WhatToDo:      v.WhatToDo,
			}
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
				return types.SubTaskOutcome{
					SubTaskID:    subTask.SubTaskID,
					ParentTaskID: subTask.ParentTaskID,
					Status:       "failed",
					FailureReason: &reason,
					GapTrajectory: trajectory,
				}
			}

		default: // "failed"
			reason := v.FailureReason
			if reason == "" {
				reason = "validation failed"
			}
			log.Printf("[R4a] subtask=%s FAILED: %s", subTask.SubTaskID, reason)
			outcome := types.SubTaskOutcome{
				SubTaskID:     subTask.SubTaskID,
				ParentTaskID:  subTask.ParentTaskID,
				Status:        "failed",
				Output:        result.Output,
				FailureReason: &reason,
				GapTrajectory: trajectory,
			}
			a.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleAgentVal,
				To:        types.RoleMetaVal,
				Type:      types.MsgSubTaskOutcome,
				Payload:   outcome,
			})
			return outcome
		}
	}
}

func (a *AgentValidator) score(ctx context.Context, st types.SubTask, result types.ExecutionResult) (*verdict, error) {
	taskJSON, _ := json.MarshalIndent(st, "", "  ")
	resultJSON, _ := json.MarshalIndent(result, "", "  ")

	userPrompt := fmt.Sprintf("SubTask:\n%s\n\nExecutionResult:\n%s", taskJSON, resultJSON)

	raw, err := a.llm.Chat(ctx, systemPrompt, userPrompt)
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
