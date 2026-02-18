package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/tools"
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R3 — Executor. Your mission is to execute exactly one assigned sub-task and return a concrete, verifiable result.

Available tools:
- shell: run a bash command. Input: {"tool":"shell","command":"..."}
- read_file: read a file. Input: {"tool":"read_file","path":"..."}
- write_file: write a file. Input: {"tool":"write_file","path":"...","content":"..."}
- search: web search via DuckDuckGo. Input: {"tool":"search","query":"..."}

Decision process:
1. Read the SubTask intent and success_criteria carefully.
2. Choose the minimal set of tool calls needed.
3. Execute them in sequence (respond with one tool call at a time).
4. When done, output the final ExecutionResult JSON.

Output rules:
- For a tool call: {"action":"tool","tool":"shell","command":"..."}
- For the final result: {"action":"result","subtask_id":"...","status":"completed|uncertain|failed","output":"...","uncertainty":null,"tool_calls":["..."]}
- No markdown, no prose, no code fences.`

const correctionPrompt = `You are R3 — Executor. A correction has been received. Apply it and re-execute.

Correction: %s
What to do: %s

Re-execute the sub-task with this correction in mind. Output a tool call or the final ExecutionResult JSON.`

// Executor is R3. It executes sub-tasks using available tools.
type Executor struct {
	llm *llm.Client
	b   *bus.Bus
}

// New creates an Executor.
func New(b *bus.Bus, llmClient *llm.Client) *Executor {
	return &Executor{llm: llmClient, b: b}
}

// Run starts the executor goroutine listening for SubTask messages.
// In the architecture, per-subtask executors are spawned by the planner.
// This Run method handles a single SubTask channel for a dedicated goroutine.
func (e *Executor) RunSubTask(ctx context.Context, subTask types.SubTask, correctionCh <-chan types.CorrectionSignal) {
	result, err := e.execute(ctx, subTask, nil)
	if err != nil {
		log.Printf("[R3] ERROR executing subtask %s: %v", subTask.SubTaskID, err)
		reason := err.Error()
		result = types.ExecutionResult{
			SubTaskID:   subTask.SubTaskID,
			Status:      "failed",
			Output:      reason,
			Uncertainty: &reason,
		}
	}

	e.b.Publish(types.Message{
		ID:        uuid.New().String(),
		Timestamp: time.Now().UTC(),
		From:      types.RoleExecutor,
		To:        types.RoleAgentVal,
		Type:      types.MsgExecutionResult,
		Payload:   result,
	})
	log.Printf("[R3] published ExecutionResult subtask_id=%s status=%s", result.SubTaskID, result.Status)

	// Listen for correction signals and re-execute
	for {
		select {
		case <-ctx.Done():
			return
		case correction, ok := <-correctionCh:
			if !ok {
				return
			}
			log.Printf("[R3] received CorrectionSignal attempt=%d for subtask=%s", correction.AttemptNumber, correction.SubTaskID)
			result, err = e.execute(ctx, subTask, &correction)
			if err != nil {
				log.Printf("[R3] ERROR re-executing subtask %s: %v", subTask.SubTaskID, err)
				reason := err.Error()
				result = types.ExecutionResult{
					SubTaskID:   subTask.SubTaskID,
					Status:      "failed",
					Output:      reason,
					Uncertainty: &reason,
				}
			}
			e.b.Publish(types.Message{
				ID:        uuid.New().String(),
				Timestamp: time.Now().UTC(),
				From:      types.RoleExecutor,
				To:        types.RoleAgentVal,
				Type:      types.MsgExecutionResult,
				Payload:   result,
			})
			log.Printf("[R3] published corrected ExecutionResult subtask_id=%s status=%s", result.SubTaskID, result.Status)
		}
	}
}

type toolCall struct {
	Action  string `json:"action"`
	Tool    string `json:"tool"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Query   string `json:"query,omitempty"`
}

type finalResult struct {
	Action      string   `json:"action"`
	SubTaskID   string   `json:"subtask_id"`
	Status      string   `json:"status"`
	Output      any      `json:"output"`
	Uncertainty *string  `json:"uncertainty"`
	ToolCalls   []string `json:"tool_calls"`
}

func (e *Executor) execute(ctx context.Context, st types.SubTask, correction *types.CorrectionSignal) (types.ExecutionResult, error) {
	var userPrompt string
	if correction != nil {
		userPrompt = fmt.Sprintf(correctionPrompt, correction.WhatWasWrong, correction.WhatToDo) +
			"\n\nOriginal SubTask:\n" + subTaskToJSON(st)
	} else {
		userPrompt = "Execute this SubTask:\n" + subTaskToJSON(st)
	}

	var toolCallHistory []string
	var toolResultsCtx strings.Builder

	const maxToolCalls = 10
	for i := 0; i < maxToolCalls; i++ {
		prompt := userPrompt
		if toolResultsCtx.Len() > 0 {
			prompt += "\n\nPrevious tool results:\n" + toolResultsCtx.String()
		}

		raw, err := e.llm.Chat(ctx, systemPrompt, prompt)
		if err != nil {
			return types.ExecutionResult{}, fmt.Errorf("llm: %w", err)
		}
		raw = llm.StripFences(raw)

		// Try to parse as final result first
		var fr finalResult
		if err := json.Unmarshal([]byte(raw), &fr); err == nil && fr.Action == "result" {
			return types.ExecutionResult{
				SubTaskID:   st.SubTaskID,
				Status:      fr.Status,
				Output:      fr.Output,
				Uncertainty: fr.Uncertainty,
				ToolCalls:   toolCallHistory,
			}, nil
		}

		// Parse as tool call
		var tc toolCall
		if err := json.Unmarshal([]byte(raw), &tc); err != nil {
			return types.ExecutionResult{}, fmt.Errorf("parse LLM output: %w (raw: %s)", err, raw)
		}

		toolCallHistory = append(toolCallHistory, tc.Tool+":"+firstN(tc.Command+tc.Path+tc.Query, 60))

		result, err := e.runTool(ctx, tc)
		if err != nil {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s ERROR: %v\n", tc.Tool, err))
		} else {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s result: %s\n", tc.Tool, firstN(result, 500)))
		}
	}

	return types.ExecutionResult{
		SubTaskID: st.SubTaskID,
		Status:    "uncertain",
		Output:    toolResultsCtx.String(),
		ToolCalls: toolCallHistory,
	}, nil
}

func (e *Executor) runTool(ctx context.Context, tc toolCall) (string, error) {
	switch tc.Tool {
	case "shell":
		stdout, stderr, err := tools.RunShell(ctx, tc.Command)
		if err != nil {
			return fmt.Sprintf("stdout: %s\nstderr: %s\nerror: %v", stdout, stderr, err), nil
		}
		return fmt.Sprintf("stdout: %s\nstderr: %s", stdout, stderr), nil
	case "read_file":
		return tools.ReadFile(tc.Path)
	case "write_file":
		return "ok", tools.WriteFile(tc.Path, tc.Content)
	case "search":
		return tools.Search(ctx, tc.Query)
	default:
		return "", fmt.Errorf("unknown tool: %s", tc.Tool)
	}
}

func subTaskToJSON(st types.SubTask) string {
	b, _ := json.MarshalIndent(st, "", "  ")
	return string(b)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
