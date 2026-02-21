package tasklog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readEvents parses all JSONL lines from a file into a slice of Events.
func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readEvents: %v", err)
	}
	var events []Event
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("readEvents: unmarshal %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// --- Registry.Open ---

func TestRegistry_Open_WritesTaskBegin(t *testing.T) {
	// Open creates the log directory and writes a task_begin event as the first JSONL line
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "test intent")
	if tl == nil {
		t.Fatal("expected non-nil TaskLog")
	}
	// Close to flush the file
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Kind != KindTaskBegin {
		t.Errorf("first event kind = %q, want %q", events[0].Kind, KindTaskBegin)
	}
	if events[0].TaskID != "task1" {
		t.Errorf("task_id = %q, want %q", events[0].TaskID, "task1")
	}
	if events[0].Intent != "test intent" {
		t.Errorf("intent = %q, want %q", events[0].Intent, "test intent")
	}
}

func TestRegistry_Open_ReturnsExistingOnDuplicate(t *testing.T) {
	// Open returns the existing log without re-opening when called twice for the same taskID
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl1 := r.Open("task1", "intent A")
	tl2 := r.Open("task1", "intent B")
	if tl1 != tl2 {
		t.Errorf("expected same *TaskLog pointer on second Open, got different pointers")
	}
	r.Close("task1", "accepted")

	// Only one task_begin should be in the file
	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	beginCount := 0
	for _, e := range events {
		if e.Kind == KindTaskBegin {
			beginCount++
		}
	}
	if beginCount != 1 {
		t.Errorf("expected 1 task_begin, got %d", beginCount)
	}
}

// --- Registry.Get ---

func TestRegistry_Get_ReturnsNilForUnknown(t *testing.T) {
	// Get returns nil when taskID has no open log
	dir := t.TempDir()
	r := NewRegistry(dir)
	if got := r.Get("nonexistent"); got != nil {
		t.Errorf("expected nil for unknown taskID, got %v", got)
	}
}

func TestRegistry_Get_ReturnsSamePointer(t *testing.T) {
	// Get returns the same pointer as Open for the same taskID
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	if got := r.Get("task1"); got != tl {
		t.Errorf("Get returned different pointer than Open")
	}
	r.Close("task1", "accepted")
}

// --- Registry.Close ---

func TestRegistry_Close_WritesTaskEnd(t *testing.T) {
	// Close writes task_end with status, elapsed_ms, and removes taskID from registry
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	r.Open("task1", "intent")
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	last := events[len(events)-1]
	if last.Kind != KindTaskEnd {
		t.Errorf("last event kind = %q, want %q", last.Kind, KindTaskEnd)
	}
	if last.Status != "accepted" {
		t.Errorf("status = %q, want %q", last.Status, "accepted")
	}
	if last.ElapsedMs < 0 {
		t.Errorf("elapsed_ms = %d, want >= 0", last.ElapsedMs)
	}
	// After Close, Get should return nil
	if got := r.Get("task1"); got != nil {
		t.Errorf("expected nil after Close, got %v", got)
	}
}

func TestRegistry_Close_NoopsForUnknown(t *testing.T) {
	// Close no-ops gracefully when taskID is not registered
	dir := t.TempDir()
	r := NewRegistry(dir)
	// Should not panic or error
	r.Close("nonexistent", "accepted")
}

// --- nil TaskLog safety ---

func TestTaskLog_NilReceiverNoops(t *testing.T) {
	// All TaskLog methods are no-ops when called on nil *TaskLog
	var tl *TaskLog
	// None of these should panic:
	tl.SubtaskBegin("s1", "intent", 1, []string{"criterion"})
	tl.SubtaskEnd("s1", "matched")
	tl.LLMCall("executor", "sys", "user", "resp", 100, 50, 1)
	tl.ToolCall("s1", "shell", "ls", "file.go", "")
	tl.CriterionVerdict("s1", "output contains path", true, "evidence", 1)
	tl.Correction("s1", "wrong", "try this", 1)
	tl.Replan("gap summary", "worsening", 1)
}

// --- TotalTokens ---

func TestTaskLog_TotalTokens_ZeroOnNil(t *testing.T) {
	// TotalTokens returns 0 on nil receiver
	var tl *TaskLog
	if got := tl.TotalTokens(); got != 0 {
		t.Errorf("TotalTokens on nil = %d, want 0", got)
	}
}

func TestTaskLog_TotalTokens_AccumulatesAcrossLLMCalls(t *testing.T) {
	// TotalTokens returns sum of prompt and completion tokens from all LLMCall events
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")

	tl.LLMCall("planner", "sys", "user", "resp", 100, 50, 0)
	tl.LLMCall("executor", "sys", "user", "resp", 200, 80, 1)

	if got := tl.TotalTokens(); got != 430 {
		t.Errorf("TotalTokens = %d, want 430 (100+50+200+80)", got)
	}
	r.Close("task1", "accepted")
}

// --- task_end includes total_tokens ---

func TestRegistry_Close_WritesAccumulatedTokens(t *testing.T) {
	// task_end event includes total_tokens = sum of all LLM call tokens
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.LLMCall("planner", "sys", "user", "resp", 10, 5, 0)
	tl.LLMCall("executor", "sys", "user", "resp", 20, 8, 1)
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	last := events[len(events)-1]
	if last.Kind != KindTaskEnd {
		t.Fatalf("last event kind = %q, want task_end", last.Kind)
	}
	if last.TotalTokens != 43 {
		t.Errorf("total_tokens = %d, want 43 (10+5+20+8)", last.TotalTokens)
	}
}

// --- criterion_verdict: false must be serialised ---

func TestTaskLog_CriterionVerdict_FalseIsSerialised(t *testing.T) {
	// CriterionVerdict with met=false must include "met":false in JSON (pointer ensures this)
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.CriterionVerdict("s1", "output contains path", false, "no path found", 1)
	r.Close("task1", "abandoned")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	// Find the criterion_verdict event
	for _, e := range events {
		if e.Kind != KindCriterionVerdict {
			continue
		}
		if e.Met == nil {
			t.Fatal("Met field is nil (not serialised), want false")
		}
		if *e.Met != false {
			t.Errorf("Met = %v, want false", *e.Met)
		}
		return
	}
	t.Fatal("no criterion_verdict event found")
}
