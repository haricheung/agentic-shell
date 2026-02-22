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
	"github.com/haricheung/agentic-shell/internal/tasklog"
	"github.com/haricheung/agentic-shell/internal/tools"
	"github.com/haricheung/agentic-shell/internal/types"
)

const systemPrompt = `You are R3 — Executor. Execute exactly one assigned sub-task and return a concrete, verifiable result.

Tool selection — use the FIRST tool that fits; do not skip down the list:
1. mdfind  — personal file search (Spotlight index, <1 s). Use for ANY file outside the project.
   Input: {"action":"tool","tool":"mdfind","query":"filename or phrase"}
2. glob    — project file search (filename pattern, recursive). Use ONLY for files inside the project.
   Input: {"action":"tool","tool":"glob","pattern":"*.json","root":"."}
   Pattern matches FILENAME ONLY — no "/" allowed. root:"." = project directory.
3. read_file  — read a file. Input: {"action":"tool","tool":"read_file","path":"..."}
4. write_file — write a file. Input: {"action":"tool","tool":"write_file","path":"...","content":"..."}
5. applescript — control macOS/Apple apps (Mail, Calendar, Reminders, Messages, Music, Focus).
   Input: {"action":"tool","tool":"applescript","script":"tell application \"Reminders\" to ..."}
   Calendar/Reminders sync to iPhone/iPad/Watch via iCloud automatically.
6. shortcuts — run a named Apple Shortcut (iCloud-synced, can trigger iPhone/Watch automations).
   Input: {"action":"tool","tool":"shortcuts","name":"My Shortcut","input":""}
7. shell — bash command for everything else (counting, aggregation, system info, file ops).
   Input: {"action":"tool","tool":"shell","command":"..."}
   NEVER use "find" to locate personal files — use mdfind (tool #1) instead.
   Never include ~/Music/Music or ~/Library in shell paths.
8. search — DuckDuckGo web search. Input: {"action":"tool","tool":"search","query":"..."}

Execution rules:
- Read intent, success_criteria, and context before acting. Context may contain prior-step outputs — use them directly.
- One tool call per turn; wait for the result before the next.
- When tool output satisfies ALL success_criteria, output the final result immediately.
- status "completed": tool ran and output clearly answers the task.
- status "uncertain": output is genuinely ambiguous AND no further tool would resolve it.
- status "failed": tool returned an error and retrying a different way is not possible.
- No markdown, no prose, no code fences.

Output format:
Tool call:    {"action":"tool","tool":"<name>","<param>":"<value>",...}
Final result: {"action":"result","subtask_id":"...","status":"completed|uncertain|failed","output":"<result text>","uncertainty":null,"tool_calls":["<tool: input → output summary>",...]}`

const correctionPrompt = `You are R3 — Executor. A correction has been received from R4a. Re-execute the subtask using a DIFFERENT approach.

What was wrong: %s
What to do instead: %s
Previous tool calls (do NOT repeat these): %s

Apply the same tool selection rules and output format as before:
- Tool call: {"action":"tool","tool":"<name>","<param>":"<value>",...}
- Final result: {"action":"result","subtask_id":"...","status":"completed|uncertain|failed","output":"...","uncertainty":null,"tool_calls":["..."]}`

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
// tlog may be nil — all TaskLog methods are nil-safe.
func (e *Executor) RunSubTask(ctx context.Context, subTask types.SubTask, correctionCh <-chan types.CorrectionSignal, tlog *tasklog.TaskLog) {
	tlog.SubtaskBegin(subTask.SubTaskID, subTask.Intent, subTask.Sequence, subTask.SuccessCriteria)

	var allToolCalls []string // accumulated across all attempts for correction context

	result, toolCalls, err := e.execute(ctx, subTask, nil, nil, tlog)
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
			result, toolCalls, err = e.execute(ctx, subTask, &correction, allToolCalls, tlog)
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

func (e *Executor) execute(ctx context.Context, st types.SubTask, correction *types.CorrectionSignal, priorToolCalls []string, tlog *tasklog.TaskLog) (types.ExecutionResult, []string, error) {
	wd, _ := os.Getwd()

	if correction == nil {
		log.Printf("[R3] execute subtask=%s seq=%d intent=%q", st.SubTaskID, st.Sequence, st.Intent)
	} else {
		log.Printf("[R3] re-execute subtask=%s seq=%d attempt=%d intent=%q",
			st.SubTaskID, st.Sequence, correction.AttemptNumber+1, st.Intent)
		log.Printf("[R3]   correction: wrong=%q todo=%q", correction.WhatWasWrong, correction.WhatToDo)
	}

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

		raw, usage, err := e.llm.Chat(ctx, systemPrompt, prompt)
		tlog.LLMCall("executor", systemPrompt, prompt, raw, usage.PromptTokens, usage.CompletionTokens, i+1)
		if err != nil {
			return types.ExecutionResult{}, toolCallHistory, fmt.Errorf("llm: %w", err)
		}
		raw = llm.StripFences(raw)
		log.Printf("[R3] llm response (iter=%d): %s", i, firstN(raw, 200))

		// Try to parse as final result first
		var fr finalResult
		if err := json.Unmarshal([]byte(raw), &fr); err == nil && fr.Action == "result" {
			outStr, _ := json.Marshal(fr.Output)
			log.Printf("[R3] result subtask=%s status=%s output=%s",
				st.SubTaskID, fr.Status, firstN(strings.TrimSpace(string(outStr)), 500))
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
		currentSig := tc.Tool + ":" + firstN(detail, 60)

		// Loop detection: identical consecutive call produces identical output — skip
		// execution and inject a hard stop so the LLM must either output a final result
		// or try a genuinely different tool/query.
		if len(toolCallHistory) > 0 && currentSig == toolCallHistory[len(toolCallHistory)-1] {
			log.Printf("[R3] loop detected: identical call blocked (tool=%s iter=%d)", tc.Tool, i+1)
			toolResultsCtx.WriteString(fmt.Sprintf(
				"\n⚠️ DUPLICATE CALL BLOCKED: [%s] was already called with identical parameters — repeated calls return identical results and waste budget. You MUST now either:\n1. Output the final result using what you already have (even if partial), OR\n2. Use a COMPLETELY DIFFERENT query, tool, or approach.\nDo NOT repeat this call.\n",
				tc.Tool))
			continue
		}

		toolCallHistory = append(toolCallHistory, currentSig)

		// Log tool invocation with the most relevant param per tool type.
		switch tc.Tool {
		case "shell":
			log.Printf("[R3] tool[%d] shell: %s", i+1, firstN(tc.Command, 120))
		case "mdfind":
			log.Printf("[R3] tool[%d] mdfind: query=%q", i+1, tc.Query)
		case "glob":
			log.Printf("[R3] tool[%d] glob: pattern=%q root=%q", i+1, tc.Pattern, tc.Root)
		case "read_file":
			log.Printf("[R3] tool[%d] read_file: %s", i+1, tc.Path)
		case "write_file":
			log.Printf("[R3] tool[%d] write_file: %s (%d bytes)", i+1, tc.Path, len(tc.Content))
		case "applescript":
			log.Printf("[R3] tool[%d] applescript: %s", i+1, firstN(tc.Script, 100))
		case "shortcuts":
			log.Printf("[R3] tool[%d] shortcuts: name=%q", i+1, tc.Name)
		case "search":
			log.Printf("[R3] tool[%d] search: query=%q", i+1, tc.Query)
		default:
			log.Printf("[R3] tool[%d] %s", i+1, tc.Tool)
		}

		tcInputJSON, _ := json.Marshal(tc)
		result, err := e.runTool(ctx, tc)
		if err != nil {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s ERROR: %v\n", tc.Tool, err))
			log.Printf("[R3] tool[%d] → ERROR: %v", i+1, err)
			// Append error evidence to tool_calls so R4a can verify
			toolCallHistory[len(toolCallHistory)-1] += " → ERROR: " + firstN(err.Error(), 80)
			tlog.ToolCall(st.SubTaskID, tc.Tool, string(tcInputJSON), "", err.Error())
		} else {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s result:\n%s\n", tc.Tool, headTail(result, 4000)))
			log.Printf("[R3] tool[%d] → %s", i+1, firstN(strings.TrimSpace(result), 500))
			// Append leading content to tool_calls so R4a sees concrete evidence.
			// firstN: nearly all tool outputs (search titles, file paths, shell results)
			// put the relevant content at the start. lastN was wrong for search results.
			toolCallHistory[len(toolCallHistory)-1] += " → " + firstN(strings.TrimSpace(result), 200)
			tlog.ToolCall(st.SubTaskID, tc.Tool, string(tcInputJSON), firstN(strings.TrimSpace(result), 500), "")
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

// findNameRe extracts the first -name pattern from a find command.
var findNameRe = regexp.MustCompile(`-name\s+["']?([^"'\s]+)["']?`)

// personalPathPrefixes are path prefixes that indicate a personal-file search.
// The model should use mdfind for these, not shell find.
// " ~" catches both "find ~" and "find ~/..." forms.
var personalPathPrefixes = []string{
	"/Users/", " ~/", " ~ ", "/home/", "/Volumes/",
}

// redirectPersonalFind detects `find <personal-path> ... -name <pattern>` commands
// and returns the equivalent mdfind query string. Returns ("", false) if the
// command is not a personal-file find.
func redirectPersonalFind(cmd string) (query string, ok bool) {
	trimmed := strings.TrimSpace(cmd)
	if !strings.HasPrefix(trimmed, "find ") {
		return "", false
	}
	isPersonal := false
	for _, pfx := range personalPathPrefixes {
		if strings.Contains(trimmed, pfx) {
			isPersonal = true
			break
		}
	}
	if !isPersonal {
		return "", false
	}
	m := findNameRe.FindStringSubmatch(trimmed)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

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
	case "mdfind":
		return tools.RunMdfind(ctx, tc.Query)
	case "shell":
		// Intercept personal-file find commands and redirect to mdfind.
		// The model occasionally ignores the prompt priority and emits
		// `find /Users/... -name <pattern>` which is extremely slow (~6 min).
		if query, ok := redirectPersonalFind(tc.Command); ok {
			log.Printf("[R3] redirecting personal find to mdfind: query=%q (original: %s)", query, firstN(tc.Command, 80))
			return tools.RunMdfind(ctx, query)
		}
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

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// headTail returns up to maxLen characters of s, preserving both the head and
// tail of the output. For long outputs like ffmpeg (banner + result at the end),
// this ensures the LLM sees both the command context and the actual result/error,
// rather than truncating before the result appears.
func headTail(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	head := maxLen / 3
	tail := maxLen - head
	return s[:head] + "\n...[middle truncated]...\n" + s[len(s)-tail:]
}
