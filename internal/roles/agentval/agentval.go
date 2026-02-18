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

const systemPrompt = `You are R4a — Agent-Validator. Your mission is to close the gap between the Executor's output and the sub-task goal.

Skills:
- Score the ExecutionResult against each success criterion in the SubTask
- Compute a CorrectionSignal: specific, targeted feedback — not "try again" but exactly what was wrong and what to do differently
- Determine when the gap is closed (score == 1.0 or all criteria met)
- Determine when the gap cannot be closed (budget exhausted or status "failed")

Scoring rules:
- "matched" ONLY when the output POSITIVELY demonstrates it satisfies the criteria with actual evidence
- Be strict: a vague or self-referential output ("satisfies criteria") without concrete data is not evidence

Empty-result rule (IMPORTANT):
- If the task is to find/list items AND the tool_calls show that a real search was executed (e.g. shell find or glob), AND the stdout is empty or says "no files found", this IS a valid and complete result — output "matched". Absence of files is a legitimate answer.
- Only send "retry" for an empty result if there is NO evidence of a search being run at all (tool_calls is empty or the command was clearly wrong, e.g. wrong directory or wrong extension).

OS permission rule (IMPORTANT):
- If stderr contains "Operation not permitted", "Permission denied", or similar OS-level errors for specific directories (e.g. ~/Music/Music, ~/Library), this is an OS/privacy constraint — NOT the executor's fault.
- A search that covers all ACCESSIBLE directories and reports permission errors on protected ones is COMPLETE. Output "matched" if the accessible directories were searched.
- Do NOT send a retry asking the executor to access directories the OS has blocked.

Output rules — choose ONE:

If gap is closed, output:
{"verdict":"matched","score":1.0,"unmet_criteria":[]}

If gap is non-zero and retries remain, output:
{"verdict":"retry","score":0.0-1.0,"unmet_criteria":["..."],"what_was_wrong":"...","what_to_do":"..."}

If failed or budget exhausted, output:
{"verdict":"failed","score":0.0-1.0,"unmet_criteria":["..."],"failure_reason":"..."}

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
