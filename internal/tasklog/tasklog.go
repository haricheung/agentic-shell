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
	"log"
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
	TaskID      string `json:"task_id,omitempty"`
	Intent      string `json:"intent,omitempty"`
	Status      string `json:"status,omitempty"` // "accepted" | "abandoned"
	ElapsedMs   int64  `json:"elapsed_ms,omitempty"`
	TotalTokens int    `json:"total_tokens,omitempty"`

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
	dir  string
	mu   sync.Mutex
	logs map[string]*TaskLog
}

// NewRegistry creates a Registry that writes one JSONL file per task under dir.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir, logs: make(map[string]*TaskLog)}
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
		log.Printf("[TASKLOG] could not create dir %s: %v", r.dir, err)
		return nil
	}
	path := filepath.Join(r.dir, taskID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[TASKLOG] could not open %s: %v", path, err)
		return nil
	}

	tl := &TaskLog{taskID: taskID, started: time.Now(), f: f}
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
	delete(r.logs, taskID)
	r.mu.Unlock()

	tl.mu.Lock()
	elapsed := time.Since(tl.started).Milliseconds()
	total := tl.promptTokens + tl.completionTokens
	tl.mu.Unlock()

	tl.write(Event{
		Kind:        KindTaskEnd,
		TaskID:      taskID,
		Status:      status,
		ElapsedMs:   elapsed,
		TotalTokens: total,
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

// LLMCall writes an llm_call event with full prompts, response, and token counts.
// iterIndex is 1-indexed for executor multi-turn calls; pass 0 for single-call roles
// (it will be omitted from the JSON via omitempty).
func (tl *TaskLog) LLMCall(role, systemPrompt, userPrompt, response string, promptToks, completionToks, iterIndex int) {
	if tl == nil {
		return
	}
	tl.mu.Lock()
	tl.promptTokens += promptToks
	tl.completionTokens += completionToks
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
func (tl *TaskLog) ToolCall(subtaskID, tool, toolInput, toolOutput, toolError string) {
	if tl == nil {
		return
	}
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
func (tl *TaskLog) Replan(gapSummary, gapTrend string, replanRound int) {
	if tl == nil {
		return
	}
	tl.write(Event{
		Kind:        KindReplan,
		GapSummary:  gapSummary,
		GapTrend:    gapTrend,
		ReplanRound: replanRound,
	})
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
		log.Printf("[TASKLOG] marshal error: %v", err)
		return
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.f == nil {
		return
	}
	if _, err = fmt.Fprintf(tl.f, "%s\n", data); err != nil {
		log.Printf("[TASKLOG] write error: %v", err)
	}
}
