package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
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
- glob: find files by pattern, recursively. Input: {"action":"tool","tool":"glob","pattern":"*.go","root":"."}
  PREFER over shell for ANY file discovery task — faster, always recursive, never fails on empty results.
  root MUST reflect where the files actually live:
    • "."  → current project directory (code, configs in the repo). Use for project-scoped searches.
    • "~"  → user's home directory. Use when searching for the user's personal files (documents, downloads, music, photos, etc.).
    • "~/Downloads", "~/Documents", etc. → specific user directories.
  NEVER use root:"." to search for user personal files — it will find nothing outside the project.
- read_file: read a file. Input: {"action":"tool","tool":"read_file","path":"..."}
- write_file: write a file. Input: {"action":"tool","tool":"write_file","path":"...","content":"..."}
- applescript: control macOS/Apple apps via AppleScript. Input: {"action":"tool","tool":"applescript","script":"tell application \"Mail\" to ..."}
  Use for: sending email, creating Calendar events, adding Reminders (syncs to iPhone/iPad/Watch via iCloud),
  sending iMessages, controlling Music, setting Focus modes, and any macOS app automation.
  Calendar events and Reminders created here automatically appear on the user's iPhone, iPad, and Apple Watch.
- shortcuts: run a named Apple Shortcut (synced via iCloud to all devices). Input: {"action":"tool","tool":"shortcuts","name":"My Shortcut","input":""}
  Use for: triggering user-defined iPhone/Watch automations (e.g. alarms via Clock app, Watch faces, HomeKit scenes).
- shell: run a bash command. Input: {"action":"tool","tool":"shell","command":"..."}
  Use for system operations, NOT file discovery and NOT Apple app control.
  For find commands: ALWAYS append 2>/dev/null so that "Operation not permitted" errors on macOS-protected directories do not cause exit status 1 or hide results from other directories.
  On macOS, never include ~/Music/Music or ~/Library in find paths — they are system-protected and always fail.
- search: web search. Input: {"action":"tool","tool":"search","query":"..."}

Decision process:
1. Read the SubTask intent and success_criteria carefully.
2. You are told the current working directory — use it to construct correct paths.
3. For file discovery: use glob. For Apple device actions: use applescript or shortcuts. For other system ops: use shell.
4. Execute tools in sequence (one tool call at a time, then wait for the result).
5. When you have enough evidence to satisfy all success_criteria, output the final ExecutionResult JSON.

Output rules:
- For a tool call: {"action":"tool","tool":"<name>","<param>":"<value>",...}
- For the final result: {"action":"result","subtask_id":"...","status":"completed|uncertain|failed","output":"...","uncertainty":null,"tool_calls":["..."]}
- Use status "completed" when a tool ran and the output clearly answers the task.
- Use status "uncertain" ONLY when the output is genuinely ambiguous and no further tool call would help.
- No markdown, no prose, no code fences.`

const correctionPrompt = `You are R3 — Executor. A correction has been received. Apply it and re-execute.

Correction: %s
What to do: %s
Previous tool calls attempted: %s

You MUST try a DIFFERENT approach from the previous attempts above. Output a tool call or the final ExecutionResult JSON.`

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
	var allToolCalls []string // accumulated across all attempts for correction context

	result, toolCalls, err := e.execute(ctx, subTask, nil, nil)
	allToolCalls = append(allToolCalls, toolCalls...)
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[R3] subtask=%s cancelled, skipping publish", subTask.SubTaskID)
			return
		}
		log.Printf("[R3] ERROR executing subtask %s: %v", subTask.SubTaskID, err)
		reason := err.Error()
		result = types.ExecutionResult{
			SubTaskID:   subTask.SubTaskID,
			Status:      "failed",
			Output:      reason,
			Uncertainty: &reason,
		}
	}
	if ctx.Err() != nil {
		log.Printf("[R3] subtask=%s context done after execute, skipping publish", subTask.SubTaskID)
		return
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
			result, toolCalls, err = e.execute(ctx, subTask, &correction, allToolCalls)
			allToolCalls = append(allToolCalls, toolCalls...)
			if err != nil {
				if ctx.Err() != nil {
					log.Printf("[R3] subtask=%s cancelled during correction, skipping publish", subTask.SubTaskID)
					return
				}
				log.Printf("[R3] ERROR re-executing subtask %s: %v", subTask.SubTaskID, err)
				reason := err.Error()
				result = types.ExecutionResult{
					SubTaskID:   subTask.SubTaskID,
					Status:      "failed",
					Output:      reason,
					Uncertainty: &reason,
				}
			}
			if ctx.Err() != nil {
				log.Printf("[R3] subtask=%s context done after correction, skipping publish", subTask.SubTaskID)
				return
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
	Pattern string `json:"pattern,omitempty"`
	Root    string `json:"root,omitempty"`
	Script  string `json:"script,omitempty"`  // applescript
	Name    string `json:"name,omitempty"`    // shortcuts
	Input   string `json:"input,omitempty"`   // shortcuts
}

type finalResult struct {
	Action      string   `json:"action"`
	SubTaskID   string   `json:"subtask_id"`
	Status      string   `json:"status"`
	Output      any      `json:"output"`
	Uncertainty *string  `json:"uncertainty"`
	ToolCalls   []string `json:"tool_calls"`
}

func (e *Executor) execute(ctx context.Context, st types.SubTask, correction *types.CorrectionSignal, priorToolCalls []string) (types.ExecutionResult, []string, error) {
	wd, _ := os.Getwd()

	var userPrompt string
	if correction != nil {
		prior := strings.Join(priorToolCalls, ", ")
		if prior == "" {
			prior = "none"
		}
		userPrompt = fmt.Sprintf(correctionPrompt, correction.WhatWasWrong, correction.WhatToDo, prior) +
			"\n\nOriginal SubTask:\n" + subTaskToJSON(st) +
			"\n\nCurrent working directory: " + wd
	} else {
		userPrompt = "Current working directory: " + wd + "\n\nExecute this SubTask:\n" + subTaskToJSON(st)
	}

	var toolCallHistory []string
	var toolResultsCtx strings.Builder

	const maxToolCalls = 10
	for i := 0; i < maxToolCalls; i++ {
		prompt := userPrompt
		if toolResultsCtx.Len() > 0 {
			prompt += "\n\nTool results so far:\n" + toolResultsCtx.String()
			prompt += "\nYou have the tool output above. Output the final ExecutionResult JSON now (status=completed). Only make another tool call if the output above is genuinely insufficient."
		}

		raw, err := e.llm.Chat(ctx, systemPrompt, prompt)
		if err != nil {
			return types.ExecutionResult{}, toolCallHistory, fmt.Errorf("llm: %w", err)
		}
		raw = llm.StripFences(raw)
		log.Printf("[R3] llm response (iter=%d): %s", i, firstN(raw, 200))

		// Try to parse as final result first
		var fr finalResult
		if err := json.Unmarshal([]byte(raw), &fr); err == nil && fr.Action == "result" {
			return types.ExecutionResult{
				SubTaskID:   st.SubTaskID,
				Status:      fr.Status,
				Output:      fr.Output,
				Uncertainty: fr.Uncertainty,
				ToolCalls:   toolCallHistory,
			}, toolCallHistory, nil
		}

		// Parse as tool call
		var tc toolCall
		if err := json.Unmarshal([]byte(raw), &tc); err != nil {
			return types.ExecutionResult{}, toolCallHistory, fmt.Errorf("parse LLM output: %w (raw: %s)", err, raw)
		}

		detail := tc.Command + tc.Path + tc.Query + tc.Pattern + tc.Name + firstN(tc.Script, 40)
		toolCallHistory = append(toolCallHistory, tc.Tool+":"+firstN(detail, 60))
		log.Printf("[R3] running tool=%s cmd=%s path=%s query=%s script=%s name=%s",
			tc.Tool, firstN(tc.Command, 80), tc.Path, tc.Query, firstN(tc.Script, 60), tc.Name)

		result, err := e.runTool(ctx, tc)
		if err != nil {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s ERROR: %v\n", tc.Tool, err))
			log.Printf("[R3] tool %s error: %v", tc.Tool, err)
		} else {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s result:\n%s\n", tc.Tool, firstN(result, 2000)))
			log.Printf("[R3] tool %s result: %s", tc.Tool, firstN(result, 200))
		}
	}

	return types.ExecutionResult{
		SubTaskID: st.SubTaskID,
		Status:    "uncertain",
		Output:    toolResultsCtx.String(),
		ToolCalls: toolCallHistory,
	}, toolCallHistory, nil
}

// maxdepthRe strips -maxdepth N from find commands so the LLM never accidentally
// limits searches to a single directory level in a project with subdirectories.
var maxdepthRe = regexp.MustCompile(`-maxdepth\s+\d+\s*`)

func normalizeFindCmd(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	if !strings.HasPrefix(trimmed, "find ") {
		return cmd
	}
	// Strip -maxdepth N
	cmd = maxdepthRe.ReplaceAllString(cmd, "")
	// Always suppress permission errors so they don't hide results or cause exit status 1.
	// This matters on macOS where ~/Music/Music, ~/Library etc. are TCC-protected.
	if !strings.Contains(cmd, "2>/dev/null") && !strings.Contains(cmd, "2> /dev/null") {
		cmd = strings.TrimRight(cmd, " \t") + " 2>/dev/null"
	}
	return cmd
}

func (e *Executor) runTool(ctx context.Context, tc toolCall) (string, error) {
	switch tc.Tool {
	case "shell":
		cmd := normalizeFindCmd(tc.Command)
		if cmd != tc.Command {
			log.Printf("[R3] normalized find cmd: %q -> %q", tc.Command, cmd)
		}
		stdout, stderr, err := tools.RunShell(ctx, cmd)
		if err != nil {
			return fmt.Sprintf("stdout: %s\nstderr: %s\nerror: %v", stdout, stderr, err), nil
		}
		return fmt.Sprintf("stdout: %s\nstderr: %s", stdout, stderr), nil
	case "glob":
		root := tc.Root
		if root == "" {
			root = "."
		}
		matches, err := tools.GlobFiles(root, tc.Pattern)
		if err != nil {
			return "", err
		}
		if len(matches) == 0 {
			return "(no files matched pattern " + tc.Pattern + " under " + root + ")", nil
		}
		return tools.GlobJoin(matches), nil
	case "applescript":
		result, err := tools.RunAppleScript(ctx, tc.Script)
		if err != nil {
			return fmt.Sprintf("applescript error: %v", err), nil
		}
		return result, nil
	case "shortcuts":
		result, err := tools.RunShortcut(ctx, tc.Name, tc.Input)
		if err != nil {
			return fmt.Sprintf("shortcuts error: %v", err), nil
		}
		return result, nil
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
