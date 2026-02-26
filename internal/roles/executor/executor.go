package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

const systemPromptBase = `You are R3 — Executor. Execute exactly one assigned sub-task and return a concrete, verifiable result.

Tool selection — use the FIRST tool that fits; do not skip down the list:
1. mdfind  — personal file search (Spotlight index, <1 s). Use for ANY file outside the project.
   Input: {"action":"tool","tool":"mdfind","query":"filename or phrase"}
2. glob    — project file search (filename pattern, recursive). Use ONLY for files inside the project.
   Input: {"action":"tool","tool":"glob","pattern":"*.json","root":"."}
   Pattern matches FILENAME ONLY — no "/" allowed. root:"." = project directory.
3. read_file  — read a file. Input: {"action":"tool","tool":"read_file","path":"..."}
4. write_file — write a file. Output files (scripts, reports, generated content) MUST use ~/artoo_workspace/ as the base. Example: {"action":"tool","tool":"write_file","path":"~/artoo_workspace/report.md","content":"..."}
   Project source files may use their normal relative paths (e.g. "internal/foo/bar.go").
5. applescript — control macOS/Apple apps (Mail, Calendar, Reminders, Messages, Music, Focus).
   Input: {"action":"tool","tool":"applescript","script":"tell application \"Reminders\" to ..."}
   Calendar/Reminders sync to iPhone/iPad/Watch via iCloud automatically.
6. shortcuts — run a named Apple Shortcut (iCloud-synced, can trigger iPhone/Watch automations).
   Input: {"action":"tool","tool":"shortcuts","name":"My Shortcut","input":""}
7. shell — bash command for everything else (counting, aggregation, system info, file ops).
   Input: {"action":"tool","tool":"shell","command":"..."}
   NEVER use "find" to locate personal files — use mdfind (tool #1) instead.
   Never include ~/Music/Music or ~/Library in shell paths.`

// searchToolEntry is always included — DuckDuckGo requires no API key.
// Google Custom Search is used automatically when GOOGLE_API_KEY + GOOGLE_CSE_ID are set.
const searchToolEntry = `8. search — web search (DuckDuckGo by default; Google when GOOGLE_API_KEY + GOOGLE_CSE_ID are set). Input: {"action":"tool","tool":"search","query":"..."}`

const systemPromptExec = `
Execution rules:
- Read intent, success_criteria, and context before acting. Context may contain prior-step outputs — use them directly.
- One tool call per response; wait for the REAL tool result before proceeding.
- NEVER generate fake tool output or pretend a tool ran — only output a tool call JSON OR a final result JSON, never both in the same response.
- When tool output satisfies ALL success_criteria, output the final result immediately.
- status "completed": tool ran and output clearly answers the task.
- status "uncertain": output is genuinely ambiguous AND no further tool would resolve it.
- status "failed": tool returned an error and retrying a different way is not possible.
- No markdown, no prose, no code fences. Output ONLY raw JSON — no label before it.

Output format — raw JSON only, nothing before or after:

To call a tool:
{"action":"tool","tool":"<name>","<param>":"<value>",...}

To report the final result:
{"action":"result","subtask_id":"...","status":"completed|uncertain|failed","output":"<result text>","uncertainty":null,"tool_calls":["<tool: input → output summary>",...]}`

func buildSystemPrompt() string {
	var b strings.Builder
	b.WriteString(systemPromptBase)
	if tools.SearchAvailable() {
		b.WriteString("\n" + searchToolEntry)
	}
	b.WriteString(systemPromptExec)
	return b.String()
}

const correctionPrompt = `You are R3 — Executor. A correction has been received from R4a. Re-execute the subtask using a DIFFERENT approach.

What was wrong: %s
What to do instead: %s
Previous tool calls (do NOT repeat these): %s

Apply the same tool selection rules and output format as before.
Output ONLY raw JSON — no label, no prose, no markdown:

To call a tool:
{"action":"tool","tool":"<name>","<param>":"<value>",...}

To report the final result:
{"action":"result","subtask_id":"...","status":"completed|uncertain|failed","output":"...","uncertainty":null,"tool_calls":["..."]}`

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
			slog.Debug("[R3] subtask cancelled, skipping publish", "subtask", subTask.SubTaskID)
			return
		}
		slog.Error("[R3] execute subtask error", "subtask", subTask.SubTaskID, "error", err)
		reason := err.Error()
		result = types.ExecutionResult{
			SubTaskID:   subTask.SubTaskID,
			Status:      "failed",
			Output:      reason,
			Uncertainty: &reason,
		}
	}
	if ctx.Err() != nil {
		slog.Debug("[R3] context done after execute, skipping publish", "subtask", subTask.SubTaskID)
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
	slog.Debug("[R3] published ExecutionResult", "subtask", result.SubTaskID, "status", result.Status)

	// Listen for correction signals and re-execute
	for {
		select {
		case <-ctx.Done():
			return
		case correction, ok := <-correctionCh:
			if !ok {
				return
			}
			slog.Debug("[R3] received CorrectionSignal", "attempt", correction.AttemptNumber, "subtask", correction.SubTaskID)
			result, toolCalls, err = e.execute(ctx, subTask, &correction, allToolCalls, tlog)
			allToolCalls = append(allToolCalls, toolCalls...)
			if err != nil {
				if ctx.Err() != nil {
					slog.Debug("[R3] subtask cancelled during correction, skipping publish", "subtask", subTask.SubTaskID)
					return
				}
				slog.Error("[R3] re-execute subtask error", "subtask", subTask.SubTaskID, "error", err)
				reason := err.Error()
				result = types.ExecutionResult{
					SubTaskID:   subTask.SubTaskID,
					Status:      "failed",
					Output:      reason,
					Uncertainty: &reason,
				}
			}
			if ctx.Err() != nil {
				slog.Debug("[R3] context done after correction, skipping publish", "subtask", subTask.SubTaskID)
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
			slog.Debug("[R3] published corrected ExecutionResult", "subtask", result.SubTaskID, "status", result.Status)
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
		slog.Debug("[R3] execute subtask", "subtask", st.SubTaskID, "seq", st.Sequence, "intent", st.Intent)
	} else {
		slog.Debug("[R3] re-execute subtask", "subtask", st.SubTaskID, "seq", st.Sequence, "attempt", correction.AttemptNumber+1, "intent", st.Intent,
			"wrong", correction.WhatWasWrong, "todo", correction.WhatToDo)
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
			prompt += "\n\nTool results so far:\n" + headTail(toolResultsCtx.String(), 8000)
			prompt += "\nYou have the tool output above. Output the final ExecutionResult JSON now (status=completed). Only make another tool call if the output above is genuinely insufficient."
		}

		sysPrompt := buildSystemPrompt()
		raw, usage, err := e.llm.Chat(ctx, sysPrompt, prompt)
		tlog.LLMCall("executor", sysPrompt, prompt, raw, usage.PromptTokens, usage.CompletionTokens, usage.ElapsedMs, i+1)
		if err != nil {
			return types.ExecutionResult{}, toolCallHistory, fmt.Errorf("llm: %w", err)
		}
		raw = llm.StripFences(raw)
		slog.Debug("[R3] llm response", "iter", i, "preview", firstN(raw, 200))

		// Try to parse as final result first.
		// Use json.Decoder (not json.Unmarshal) so that a response containing multiple
		// concatenated JSON objects — e.g. the tool-call JSON followed immediately by a
		// hallucinated result JSON — still parses correctly: Decode reads the first
		// complete value and stops, ignoring any trailing content (issue #85).
		var fr finalResult
		if err := json.NewDecoder(strings.NewReader(raw)).Decode(&fr); err == nil && fr.Action == "result" {
			outStr, _ := json.Marshal(fr.Output)
			slog.Debug("[R3] final result", "subtask", st.SubTaskID, "status", fr.Status, "output", firstN(strings.TrimSpace(string(outStr)), 500))
			return types.ExecutionResult{
				SubTaskID:   st.SubTaskID,
				Status:      fr.Status,
				Output:      fr.Output,
				Uncertainty: fr.Uncertainty,
				ToolCalls:   toolCallHistory,
			}, toolCallHistory, nil
		}

		// Parse as tool call — same decoder approach for consistency.
		var tc toolCall
		if err := json.NewDecoder(strings.NewReader(raw)).Decode(&tc); err != nil {
			// Log the raw response to debug.log for diagnostics, but do NOT embed it in
			// the returned error — it could contain hallucinated tool output that R4a would
			// evaluate as evidence when scoring criteria (issue #83).
			slog.Warn("[R3] parse error", "iter", i+1, "raw", raw)
			return types.ExecutionResult{}, toolCallHistory, fmt.Errorf("parse LLM output: %w", err)
		}

		detail := tc.Command + tc.Path + tc.Query + tc.Pattern + tc.Name + firstN(tc.Script, 40)
		currentSig := tc.Tool + ":" + firstN(detail, 60)

		// Loop detection: identical consecutive call produces identical output — skip
		// execution and inject a hard stop so the LLM must either output a final result
		// or try a genuinely different tool/query.
		if len(toolCallHistory) > 0 && currentSig == toolCallHistory[len(toolCallHistory)-1] {
			slog.Warn("[R3] loop detected: identical call blocked", "tool", tc.Tool, "iter", i+1)
			toolResultsCtx.WriteString(fmt.Sprintf(
				"\n⚠️ DUPLICATE CALL BLOCKED: [%s] was already called with identical parameters — repeated calls return identical results and waste budget. You MUST now either:\n1. Output the final result using what you already have (even if partial), OR\n2. Use a COMPLETELY DIFFERENT query, tool, or approach.\nDo NOT repeat this call.\n",
				tc.Tool))
			continue
		}

		toolCallHistory = append(toolCallHistory, currentSig)

		// Log tool invocation with the most relevant param per tool type.
		switch tc.Tool {
		case "shell":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "shell", "cmd", firstN(tc.Command, 120))
		case "mdfind":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "mdfind", "query", tc.Query)
		case "glob":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "glob", "pattern", tc.Pattern, "root", tc.Root)
		case "read_file":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "read_file", "path", tc.Path)
		case "write_file":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "write_file", "path", tc.Path, "bytes", len(tc.Content))
		case "applescript":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "applescript", "script", firstN(tc.Script, 100))
		case "shortcuts":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "shortcuts", "name", tc.Name)
		case "search":
			slog.Info("[R3] tool call", "iter", i+1, "tool", "search", "query", tc.Query)
		default:
			slog.Info("[R3] tool call", "iter", i+1, "tool", tc.Tool)
		}

		tcInputJSON, _ := json.Marshal(tc)
		toolStart := time.Now()
		result, err := e.runTool(ctx, tc)
		toolElapsedMs := time.Since(toolStart).Milliseconds()
		if err != nil {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s ERROR: %v\n", tc.Tool, err))
			slog.Warn("[R3] tool error", "iter", i+1, "tool", tc.Tool, "error", err)
			// Append error evidence to tool_calls so R4a can verify
			toolCallHistory[len(toolCallHistory)-1] += " → ERROR: " + firstN(err.Error(), 80)
			tlog.ToolCall(st.SubTaskID, tc.Tool, string(tcInputJSON), "", err.Error(), toolElapsedMs)
		} else {
			toolResultsCtx.WriteString(fmt.Sprintf("Tool %s result:\n%s\n", tc.Tool, headTail(result, 4000)))
			slog.Debug("[R3] tool result", "iter", i+1, "tool", tc.Tool, "output", firstN(strings.TrimSpace(result), 500))
			// Append leading content to tool_calls so R4a sees concrete evidence.
			// firstN: nearly all tool outputs (search titles, file paths, shell results)
			// put the relevant content at the start. lastN was wrong for search results.
			toolCallHistory[len(toolCallHistory)-1] += " → " + firstN(strings.TrimSpace(result), 200)
			tlog.ToolCall(st.SubTaskID, tc.Tool, string(tcInputJSON), firstN(strings.TrimSpace(result), 500), "", toolElapsedMs)
		}
	}

	return types.ExecutionResult{
		SubTaskID: st.SubTaskID,
		Status:    "uncertain",
		Output:    toolResultsCtx.String(),
		ToolCalls: toolCallHistory,
	}, toolCallHistory, nil
}

// splitShellFragments splits a compound shell command into individual statement
// fragments by tokenizing on common separators (&&, ||, ;, |, newlines) and
// stripping leading shell control-flow keywords (then, do, else).
// This is intentionally simple — it does not parse quoting or subshells —
// but covers the patterns models use to embed destructive commands inside
// loops, conditionals, and pipelines.
//
// Expectations:
//   - Single command returns one fragment (itself, trimmed)
//   - Splits on "&&", "||", ";", "|", and "\n"
//   - Strips leading keywords "then ", "do ", "else ", "{ ", "( "
//   - Returns only non-empty trimmed fragments
func splitShellFragments(cmd string) []string {
	for _, sep := range []string{"&&", "||", ";", "|", "\n"} {
		cmd = strings.ReplaceAll(cmd, sep, "\x00")
	}
	leadingKeywords := []string{"then ", "do ", "else ", "{ ", "( "}
	var out []string
	for _, part := range strings.Split(cmd, "\x00") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, kw := range leadingKeywords {
			if strings.HasPrefix(part, kw) {
				part = strings.TrimSpace(part[len(kw):])
				break
			}
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// isIrreversibleFragment checks a single normalized shell fragment (no compound
// operators, no control-flow keywords) for destructive operations.
func isIrreversibleFragment(fragment string) (bool, string) {
	check := strings.TrimSpace(fragment)
	if strings.HasPrefix(check, "sudo ") {
		check = strings.TrimSpace(check[5:])
	}
	type pattern struct{ prefix, reason string }
	patterns := []pattern{
		{"rm ", "rm deletes files permanently"},
		{"rmdir", "rmdir removes directories permanently"},
		{"truncate ", "truncate destroys file contents"},
		{"shred ", "shred irrecoverably destroys files"},
		{"mkfs", "mkfs formats a filesystem, destroying all data"},
		{"fdisk ", "fdisk modifies disk partition table"},
		{"xargs rm", "xargs rm removes files permanently"},
		{"xargs /bin/rm", "xargs /bin/rm removes files permanently"},
	}
	for _, p := range patterns {
		if strings.HasPrefix(check, p.prefix) {
			return true, p.reason
		}
	}
	if strings.HasPrefix(check, "dd ") && strings.Contains(check, "of=") {
		return true, "dd with of= overwrites a device or file"
	}
	if strings.HasPrefix(check, "find ") {
		if strings.Contains(check, " -delete") {
			return true, "find -delete removes matching files permanently"
		}
		if strings.Contains(check, "-exec rm") || strings.Contains(check, "-exec /bin/rm") {
			return true, "find -exec rm removes matching files permanently"
		}
	}
	return false, ""
}

// isIrreversibleShell reports whether a shell command (including compound
// commands with &&, ||, ;, |, for-loops, and if-statements) is irreversible.
// It splits the command into fragments and checks each one independently.
//
// Expectations:
//   - Returns true for "rm " commands (including rm -rf, sudo rm)
//   - Returns true for "rmdir" commands
//   - Returns true for "truncate" commands
//   - Returns true for "shred" commands
//   - Returns true for "dd " with "of=" argument (device/file overwrite)
//   - Returns true for "mkfs" commands
//   - Returns true for "fdisk " commands
//   - Returns true for "find " commands containing " -delete"
//   - Returns true for "find " commands containing "-exec rm"
//   - Returns true for "xargs rm" (pipe to rm)
//   - Returns true for compound commands embedding rm (for-loop, if-then, &&)
//   - Returns false for read-only commands (ls, cat, grep, plain find, etc.)
func isIrreversibleShell(cmd string) (bool, string) {
	for _, fragment := range splitShellFragments(cmd) {
		if ok, reason := isIrreversibleFragment(fragment); ok {
			return true, reason
		}
	}
	return false, ""
}

// isIrreversibleWriteFile reports whether writing to path would overwrite an existing file.
//
// Expectations:
//   - Returns true when path exists and is a regular file
//   - Returns false when path does not exist (creating a new file is safe)
//   - Returns false when path is a directory (write_file will fail anyway)
func isIrreversibleWriteFile(path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil {
		return false, ""
	}
	if info.IsDir() {
		return false, ""
	}
	return true, fmt.Sprintf("write_file would overwrite existing file: %s", path)
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
		if irreversible, reason := isIrreversibleShell(tc.Command); irreversible {
			return fmt.Sprintf("[LAW1] %s — command blocked: %q. Re-issue the task with explicit permission to proceed.", reason, tc.Command), nil
		}
		// Intercept personal-file find commands and redirect to mdfind.
		// The model occasionally ignores the prompt priority and emits
		// `find /Users/... -name <pattern>` which is extremely slow (~6 min).
		if query, ok := redirectPersonalFind(tc.Command); ok {
			slog.Debug("[R3] redirecting personal find to mdfind", "query", query, "original", firstN(tc.Command, 80))
			return tools.RunMdfind(ctx, query)
		}
		cmd := normalizeFindCmd(tc.Command)
		if cmd != tc.Command {
			slog.Debug("[R3] normalized find cmd", "from", tc.Command, "to", cmd)
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
		// Expand "~/" before path analysis so workspace-rooted paths are not misclassified.
		writePath := tools.ExpandHome(tc.Path)
		// Redirect bare filenames and "./" paths to the workspace so generated files
		// (scripts, reports, data) never land in the project root or CWD.
		if resolved, redirected := tools.ResolveOutputPath(writePath); redirected {
			slog.Debug("[R3] write_file redirected to workspace", "from", tc.Path, "to", resolved)
			writePath = resolved
		}
		if irreversible, reason := isIrreversibleWriteFile(writePath); irreversible {
			return fmt.Sprintf("[LAW1] %s — write blocked. Re-issue the task with explicit permission to overwrite.", reason), nil
		}
		return "ok", tools.WriteFile(writePath, tc.Content)
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
