// Package tasklog provides per-task structured logging for the agentic pipeline.
//
// Each task gets one JSONL file in a configurable directory. Events capture every
// key stage: LLM calls (with full prompts), tool calls, criterion verdicts,
// corrections, and replans. The log is the raw substrate GGS needs to compute its
// loss function (token costs, failure evidence, gap trajectory).
//
// Design constraints:
//   - All TaskLog methods are nil-safe (no-op on nil receiver) so roles don't need
//     nil checks before every log call.
//   - Registry is the sole owner of JSONL persistence; roles never open files.
//   - Planner opens a log via Registry.Open; MetaVal closes it via Registry.Close.
//   - Executor and AgentVal receive a *TaskLog as a method parameter — not injected
//     into their constructors — so they stay stateless across subtasks.
package tasklog

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventKind labels a single structured event in the task log.
type EventKind string

const (
	KindTaskBegin        EventKind = "task_begin"
	KindTaskEnd          EventKind = "task_end"
	KindSubtaskBegin     EventKind = "subtask_begin"
	KindSubtaskEnd       EventKind = "subtask_end"
	KindLLMCall          EventKind = "llm_call"
	KindToolCall         EventKind = "tool_call"
	KindCriterionVerdict EventKind = "criterion_verdict"
	KindCorrection       EventKind = "correction"
	KindReplan           EventKind = "replan"
)

// Event is one JSONL line in the task log.
// Fields are omitempty so each event only serialises relevant data.
type Event struct {
	Kind      EventKind `json:"kind"`
	Timestamp string    `json:"ts"`

	// task_begin / task_end
	TaskID        string     `json:"task_id,omitempty"`
	Intent        string     `json:"intent,omitempty"`
	Status        string     `json:"status,omitempty"` // "accepted" | "abandoned"
	ElapsedMs     int64      `json:"elapsed_ms,omitempty"`
	TotalTokens   int        `json:"total_tokens,omitempty"`
	RoleStats     []RoleStat `json:"role_stats,omitempty"`     // task_end only
	ToolCallCount int        `json:"tool_call_count,omitempty"` // task_end only
	ToolElapsedMs int64      `json:"tool_elapsed_ms,omitempty"` // task_end only

	// subtask_begin / subtask_end / tool_call / criterion_verdict / correction
	SubtaskID string   `json:"subtask_id,omitempty"`
	Sequence  int      `json:"sequence,omitempty"`
	Criteria  []string `json:"criteria,omitempty"`
	Attempt   int      `json:"attempt,omitempty"`

	// llm_call
	Role             string `json:"role,omitempty"` // "planner" | "executor" | "agentval" | "metaval"
	SystemPrompt     string `json:"system_prompt,omitempty"`
	UserPrompt       string `json:"user_prompt,omitempty"`
	Response         string `json:"response,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	IterIndex        int    `json:"iter_index,omitempty"` // 1-indexed executor turn; omitted for single-call roles

	// tool_call
	Tool       string `json:"tool,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
	ToolError  string `json:"tool_error,omitempty"`

	// criterion_verdict
	Criterion string `json:"criterion,omitempty"`
	Met       *bool  `json:"met,omitempty"` // pointer: false must be serialised
	Evidence  string `json:"evidence,omitempty"`

	// correction
	WhatWasWrong string `json:"what_was_wrong,omitempty"`
	WhatToDo     string `json:"what_to_do,omitempty"`

	// replan
	GapSummary  string `json:"gap_summary,omitempty"`
	GapTrend    string `json:"gap_trend,omitempty"`
	ReplanRound int    `json:"replan_round,omitempty"`
}

// TaskStats aggregates all cost metrics for a completed task.
// Includes per-role LLM usage and tool execution totals.
//
// Expectations:
//   - Roles is sorted in canonical order (planner, executor, agentval, metaval)
//   - ToolCallCount equals the total number of ToolCall invocations
//   - ToolElapsedMs equals the sum of all elapsed times passed to ToolCall
type TaskStats struct {
	Roles         []RoleStat `json:"roles"`
	ToolCallCount int        `json:"tool_call_count"`
	ToolElapsedMs int64      `json:"tool_elapsed_ms"`
}

// RoleStat summarises LLM usage for one role across all calls in a task.
type RoleStat struct {
	Role             string `json:"role"`
	Calls            int    `json:"calls"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ElapsedMs        int64  `json:"elapsed_ms"`
}

// roleStat is the unexported per-role accumulator stored inside a TaskLog.
type roleStat struct {
	calls            int
	promptTokens     int
	completionTokens int
	elapsedMs        int64
}

// canonicalRoleOrder defines the display order for RoleStats().
var canonicalRoleOrder = []string{"planner", "executor", "agentval", "metaval"}

// TaskLog is a handle for writing structured events for one task.
//
// Expectations:
//   - All methods are nil-safe (no-op when called on nil *TaskLog)
//   - Concurrent writes are safe (mutex-protected)
//   - TotalTokens returns the running sum of prompt+completion tokens across all LLMCall events
type TaskLog struct {
	taskID           string
	started          time.Time
	mu               sync.Mutex
	f                *os.File
	promptTokens     int
	completionTokens int
	roleStats        map[string]*roleStat // role -> accumulator
	toolCallCount    int                  // total tool calls made this task
	toolElapsedMs    int64                // total tool wall-clock time ms
}

// Registry maps task IDs to open TaskLogs.
// It is the sole authority for creating and closing task log files.
//
// Expectations:
//   - Open creates the log directory if absent
//   - Open writes a task_begin event as the first JSONL line
//   - Open returns the existing log without re-opening when called twice for the same taskID
//   - Get returns nil for unknown task IDs
//   - Get returns the same pointer returned by Open for the same taskID
//   - Close writes task_end with status, elapsed_ms, total_tokens before flushing
//   - Close removes the taskID from the registry so subsequent Get returns nil
//   - Close no-ops gracefully when taskID is not registered
type Registry struct {
	dir   string
	mu    sync.Mutex
	logs  map[string]*TaskLog
	cache map[string]*TaskStats // taskID -> full stats snapshot saved on Close
}

// NewRegistry creates a Registry that writes one JSONL file per task under dir.
func NewRegistry(dir string) *Registry {
	return &Registry{
		dir:   dir,
		logs:  make(map[string]*TaskLog),
		cache: make(map[string]*TaskStats),
	}
}

// Open creates a new TaskLog for taskID, writes a task_begin event, and registers it.
// If a log for taskID is already open (e.g. a replan round), it returns the existing log.
func (r *Registry) Open(taskID, intent string) *TaskLog {
	r.mu.Lock()
	defer r.mu.Unlock()

	if tl, ok := r.logs[taskID]; ok {
		return tl // idempotent: already open (replan round)
	}

	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		slog.Error("[TASKLOG] could not create dir", "dir", r.dir, "error", err)
		return nil
	}
	path := filepath.Join(r.dir, taskID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Error("[TASKLOG] could not open log file", "path", path, "error", err)
		return nil
	}

	tl := &TaskLog{taskID: taskID, started: time.Now(), f: f, roleStats: make(map[string]*roleStat)}
	r.logs[taskID] = tl
	tl.write(Event{
		Kind:   KindTaskBegin,
		TaskID: taskID,
		Intent: intent,
	})
	return tl
}

// Get returns the TaskLog for taskID, or nil if not found.
// Nil is safe to pass to all TaskLog methods.
func (r *Registry) Get(taskID string) *TaskLog {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logs[taskID]
}

// Close writes a task_end event, flushes and closes the file, and removes the
// entry from the registry. Safe to call on a nil *Registry or unknown taskID.
func (r *Registry) Close(taskID, status string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	tl, ok := r.logs[taskID]
	if !ok {
		r.mu.Unlock()
		return
	}
	stats := tl.Stats() // snapshot before delete
	r.cache[taskID] = stats
	delete(r.logs, taskID)
	r.mu.Unlock()

	tl.mu.Lock()
	elapsed := time.Since(tl.started).Milliseconds()
	total := tl.promptTokens + tl.completionTokens
	tl.mu.Unlock()

	var roleStats []RoleStat
	var toolCallCount int
	var toolElapsedMs int64
	if stats != nil {
		roleStats = stats.Roles
		toolCallCount = stats.ToolCallCount
		toolElapsedMs = stats.ToolElapsedMs
	}
	tl.write(Event{
		Kind:          KindTaskEnd,
		TaskID:        taskID,
		Status:        status,
		ElapsedMs:     elapsed,
		TotalTokens:   total,
		RoleStats:     roleStats,
		ToolCallCount: toolCallCount,
		ToolElapsedMs: toolElapsedMs,
	})

	tl.mu.Lock()
	if tl.f != nil {
		_ = tl.f.Close()
		tl.f = nil
	}
	tl.mu.Unlock()
}

// SubtaskBegin writes a subtask_begin event.
func (tl *TaskLog) SubtaskBegin(subtaskID, intent string, seq int, criteria []string) {
	if tl == nil {
		return
	}
	tl.write(Event{
		Kind:      KindSubtaskBegin,
		SubtaskID: subtaskID,
		Intent:    intent,
		Sequence:  seq,
		Criteria:  criteria,
	})
}

// SubtaskEnd writes a subtask_end event.
func (tl *TaskLog) SubtaskEnd(subtaskID, status string) {
	if tl == nil {
		return
	}
	tl.write(Event{
		Kind:      KindSubtaskEnd,
		SubtaskID: subtaskID,
		Status:    status,
	})
}

// LLMCall writes an llm_call event with full prompts, response, token counts, and elapsed time.
// elapsedMs is the wall-clock ms for the LLM HTTP call (from llm.Usage.ElapsedMs).
// iterIndex is 1-indexed for executor multi-turn calls; pass 0 for single-call roles
// (it will be omitted from the JSON via omitempty).
func (tl *TaskLog) LLMCall(role, systemPrompt, userPrompt, response string, promptToks, completionToks int, elapsedMs int64, iterIndex int) {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.promptTokens += promptToks
	tl.completionTokens += completionToks
	rs := tl.roleStats[role]
	if rs == nil {
		rs = &roleStat{}
		tl.roleStats[role] = rs
	}
	rs.calls++
	rs.promptTokens += promptToks
	rs.completionTokens += completionToks
	rs.elapsedMs += elapsedMs
	tl.mu.Unlock()
	tl.write(Event{
		Kind:             KindLLMCall,
		Role:             role,
		SystemPrompt:     systemPrompt,
		UserPrompt:       userPrompt,
		Response:         response,
		PromptTokens:     promptToks,
		CompletionTokens: completionToks,
		IterIndex:        iterIndex,
	})
}

// ToolCall writes a tool_call event. toolError is empty on success.
// elapsedMs is the wall-clock milliseconds the tool execution took; pass 0 if unknown.
//
// Expectations:
//   - ToolCallCount increments by 1 per invocation
//   - ToolElapsedMs accumulates the sum of all elapsedMs values
//   - No-op on nil receiver
func (tl *TaskLog) ToolCall(subtaskID, tool, toolInput, toolOutput, toolError string, elapsedMs int64) {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.toolCallCount++
	tl.toolElapsedMs += elapsedMs
	tl.mu.Unlock()
	tl.write(Event{
		Kind:       KindToolCall,
		SubtaskID:  subtaskID,
		Tool:       tool,
		ToolInput:  toolInput,
		ToolOutput: toolOutput,
		ToolError:  toolError,
	})
}

// CriterionVerdict writes a criterion_verdict event for one success criterion.
func (tl *TaskLog) CriterionVerdict(subtaskID, criterion string, met bool, evidence string, attempt int) {
	if tl == nil {
		return
	}
	m := met
	tl.write(Event{
		Kind:      KindCriterionVerdict,
		SubtaskID: subtaskID,
		Criterion: criterion,
		Met:       &m,
		Evidence:  evidence,
		Attempt:   attempt,
	})
}

// Correction writes a correction event when R4a sends a CorrectionSignal.
func (tl *TaskLog) Correction(subtaskID, whatWasWrong, whatToDo string, attempt int) {
	if tl == nil {
		return
	}
	tl.write(Event{
		Kind:         KindCorrection,
		SubtaskID:    subtaskID,
		WhatWasWrong: whatWasWrong,
		WhatToDo:     whatToDo,
		Attempt:      attempt,
	})
}

// Replan writes a replan event when R4b triggers a replanning round.
// gapTrend is omitted in v0.7+ (GGS owns gradient computation).
func (tl *TaskLog) Replan(gapSummary string, replanRound int) {
	if tl == nil {
		return
	}
	tl.write(Event{
		Kind:        KindReplan,
		GapSummary:  gapSummary,
		ReplanRound: replanRound,
	})
}

// RoleStats returns a snapshot of per-role LLM usage sorted by canonical order.
// Roles that made no LLM calls are omitted.
//
// Expectations:
//   - Returns one entry per role that called LLMCall
//   - Calls count matches number of LLMCall invocations per role
//   - PromptTokens and CompletionTokens match the sum across calls for that role
//   - ElapsedMs matches the sum of all elapsedMs values for that role
func (tl *TaskLog) RoleStats() []RoleStat {
	if tl == nil {
		return nil
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()
	var out []RoleStat
	for _, role := range canonicalRoleOrder {
		rs, ok := tl.roleStats[role]
		if !ok {
			continue
		}
		out = append(out, RoleStat{
			Role:             role,
			Calls:            rs.calls,
			PromptTokens:     rs.promptTokens,
			CompletionTokens: rs.completionTokens,
			ElapsedMs:        rs.elapsedMs,
		})
	}
	return out
}

// Stats returns a snapshot of all cost metrics (LLM + tool) for the live task.
// Safe to call before Close() — metaval uses this to capture stats for memory entries.
//
// Expectations:
//   - Returns nil on nil receiver
//   - Includes accumulated role stats and tool call stats
func (tl *TaskLog) Stats() *TaskStats {
	if tl == nil {
		return nil
	}
	tl.mu.Lock()
	tc := tl.toolCallCount
	te := tl.toolElapsedMs
	tl.mu.Unlock()
	return &TaskStats{
		Roles:         tl.RoleStats(), // takes its own lock internally
		ToolCallCount: tc,
		ToolElapsedMs: te,
	}
}

// GetStats returns and removes the cached TaskStats for taskID.
// Returns nil if taskID is not in the cache (unknown or already consumed).
//
// Expectations:
//   - Returns nil for unknown taskID
//   - Deletes the cache entry on first call (subsequent calls return nil)
func (r *Registry) GetStats(taskID string) *TaskStats {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.cache[taskID]
	delete(r.cache, taskID)
	return s
}

// TotalTokens returns the total token count accumulated so far.
// Used by GGS to compute the resource cost component Ω of the loss function.
//
// Expectations:
//   - Returns 0 on nil receiver
//   - Returns sum of prompt and completion tokens from all LLMCall events
func (tl *TaskLog) TotalTokens() int {
	if tl == nil {
		return 0
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return tl.promptTokens + tl.completionTokens
}

// write appends one JSON line to the task log file. Adds timestamp, mutex-protected.
func (tl *TaskLog) write(e Event) {
	e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(e)
	if err != nil {
		slog.Error("[TASKLOG] marshal event", "error", err)
		return
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.f == nil {
		return
	}
	if _, err = fmt.Fprintf(tl.f, "%s\n", data); err != nil {
		slog.Error("[TASKLOG] write event", "error", err)
	}
}
