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
	tl.LLMCall("executor", "sys", "user", "resp", 100, 50, 500, 1)
	tl.ToolCall("s1", "shell", "ls", "file.go", "", 120)
	tl.CriterionVerdict("s1", "output contains path", true, "evidence", 1)
	tl.Correction("s1", "wrong", "try this", 1)
	tl.Replan("gap summary", 1)
	tl.GGSDecision(0.5, 0.5, 0.3, 0.6, -0.1, "refine", "rationale", 1)
	tl.PlanDirective("refine", []string{"shell"}, []string{"ls /"}, "environmental", "rationale")
	tl.MemoryQuery("intent_slug", "env:local", 3, "Exploit", 4.2, 2.1)
	tl.MemoryWrite("accept", "M", "intent_slug", "env:local")
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

	tl.LLMCall("planner", "sys", "user", "resp", 100, 50, 1000, 0)
	tl.LLMCall("executor", "sys", "user", "resp", 200, 80, 2000, 1)

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
	tl.LLMCall("planner", "sys", "user", "resp", 10, 5, 100, 0)
	tl.LLMCall("executor", "sys", "user", "resp", 20, 8, 200, 1)
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

// ── RoleStats + GetStats ─────────────────────────────────────────────────────

func TestRoleStats_OneEntryPerRole(t *testing.T) {
	// RoleStats returns one entry per role that called LLMCall
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.LLMCall("planner", "s", "u", "r", 10, 5, 100, 0)
	tl.LLMCall("executor", "s", "u", "r", 20, 8, 200, 1)
	stats := tl.RoleStats()
	r.Close("task1", "accepted")
	roles := make(map[string]bool)
	for _, rs := range stats {
		roles[rs.Role] = true
	}
	if !roles["planner"] {
		t.Error("expected planner entry in RoleStats")
	}
	if !roles["executor"] {
		t.Error("expected executor entry in RoleStats")
	}
	if len(stats) != 2 {
		t.Errorf("expected 2 entries, got %d", len(stats))
	}
}

func TestRoleStats_CallsCountMatchesInvocations(t *testing.T) {
	// Calls count matches number of LLMCall invocations per role
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.LLMCall("executor", "s", "u", "r", 10, 5, 100, 1)
	tl.LLMCall("executor", "s", "u", "r", 10, 5, 100, 2)
	tl.LLMCall("executor", "s", "u", "r", 10, 5, 100, 3)
	stats := tl.RoleStats()
	r.Close("task1", "accepted")
	for _, rs := range stats {
		if rs.Role == "executor" && rs.Calls != 3 {
			t.Errorf("executor Calls = %d, want 3", rs.Calls)
		}
	}
}

func TestRoleStats_PromptTokensMatchSum(t *testing.T) {
	// PromptTokens total matches sum across calls for that role
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.LLMCall("planner", "s", "u", "r", 100, 50, 500, 0)
	tl.LLMCall("planner", "s", "u", "r", 200, 80, 600, 0)
	stats := tl.RoleStats()
	r.Close("task1", "accepted")
	for _, rs := range stats {
		if rs.Role == "planner" {
			if rs.PromptTokens != 300 {
				t.Errorf("planner PromptTokens = %d, want 300", rs.PromptTokens)
			}
		}
	}
}

func TestRoleStats_ElapsedMsMatchesSum(t *testing.T) {
	// ElapsedMs total matches sum of all elapsedMs values for that role
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.LLMCall("agentval", "s", "u", "r", 10, 5, 300, 0)
	tl.LLMCall("agentval", "s", "u", "r", 10, 5, 700, 0)
	stats := tl.RoleStats()
	r.Close("task1", "accepted")
	for _, rs := range stats {
		if rs.Role == "agentval" {
			if rs.ElapsedMs != 1000 {
				t.Errorf("agentval ElapsedMs = %d, want 1000", rs.ElapsedMs)
			}
		}
	}
}

func TestGetStats_ReturnsNilForUnknownTaskID(t *testing.T) {
	// GetStats returns nil for unknown taskID
	dir := t.TempDir()
	r := NewRegistry(dir)
	got := r.GetStats("nonexistent")
	if got != nil {
		t.Errorf("expected nil for unknown taskID, got %v", got)
	}
}

func TestGetStats_DeletesCacheEntryOnFirstCall(t *testing.T) {
	// GetStats deletes the cache entry on first call (subsequent calls return nil)
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.LLMCall("planner", "s", "u", "r", 10, 5, 100, 0)
	r.Close("task1", "accepted")

	first := r.GetStats("task1")
	if first == nil {
		t.Fatal("expected non-nil *TaskStats on first call")
	}
	second := r.GetStats("task1")
	if second != nil {
		t.Error("expected nil on second call (cache entry should be deleted)")
	}
}

// ── Tool accumulation ─────────────────────────────────────────────────────────

func TestToolStats_AccumulatesCallCount(t *testing.T) {
	// ToolCallCount increments by 1 per ToolCall invocation
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.ToolCall("s1", "shell", "ls", "file.go", "", 100)
	tl.ToolCall("s1", "mdfind", "q", "result", "", 200)
	tl.ToolCall("s1", "glob", "*", "match", "", 50)
	stats := tl.Stats()
	r.Close("task1", "accepted")
	if stats.ToolCallCount != 3 {
		t.Errorf("ToolCallCount = %d, want 3", stats.ToolCallCount)
	}
}

func TestToolStats_AccumulatesElapsedMs(t *testing.T) {
	// ToolElapsedMs equals the sum of all elapsedMs values passed to ToolCall
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.ToolCall("s1", "shell", "ls", "file.go", "", 100)
	tl.ToolCall("s1", "mdfind", "q", "result", "", 250)
	stats := tl.Stats()
	r.Close("task1", "accepted")
	if stats.ToolElapsedMs != 350 {
		t.Errorf("ToolElapsedMs = %d, want 350", stats.ToolElapsedMs)
	}
}

func TestTaskEnd_IncludesToolStats(t *testing.T) {
	// task_end event includes tool_call_count and tool_elapsed_ms from ToolCall invocations
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.ToolCall("s1", "shell", "ls", "file.go", "", 400)
	tl.ToolCall("s1", "glob", "*.go", "x.go", "", 600)
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	last := events[len(events)-1]
	if last.Kind != KindTaskEnd {
		t.Fatalf("last event kind = %q, want task_end", last.Kind)
	}
	if last.ToolCallCount != 2 {
		t.Errorf("tool_call_count = %d, want 2", last.ToolCallCount)
	}
	if last.ToolElapsedMs != 1000 {
		t.Errorf("tool_elapsed_ms = %d, want 1000", last.ToolElapsedMs)
	}
}

// ── GGSDecision ───────────────────────────────────────────────────────────────

func TestGGSDecision_WritesEvent(t *testing.T) {
	// GGSDecision writes a ggs_decision event with all float and string fields
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.GGSDecision(0.4, 0.6, 0.2, 0.55, -0.05, "refine", "loss decreasing", 1)
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind != KindGGSDecision {
			continue
		}
		if e.D != 0.4 {
			t.Errorf("D = %v, want 0.4", e.D)
		}
		if e.P != 0.6 {
			t.Errorf("P = %v, want 0.6", e.P)
		}
		if e.Omega != 0.2 {
			t.Errorf("Omega = %v, want 0.2", e.Omega)
		}
		if e.L != 0.55 {
			t.Errorf("L = %v, want 0.55", e.L)
		}
		if e.GradL != -0.05 {
			t.Errorf("GradL = %v, want -0.05", e.GradL)
		}
		if e.Directive != "refine" {
			t.Errorf("Directive = %q, want %q", e.Directive, "refine")
		}
		if e.Rationale != "loss decreasing" {
			t.Errorf("Rationale = %q, want %q", e.Rationale, "loss decreasing")
		}
		if e.ReplanRound != 1 {
			t.Errorf("ReplanRound = %d, want 1", e.ReplanRound)
		}
		return
	}
	t.Fatal("no ggs_decision event found")
}

func TestGGSDecision_NilReceiverNoop(t *testing.T) {
	// GGSDecision is a no-op on nil receiver
	var tl *TaskLog
	tl.GGSDecision(0.5, 0.5, 0.3, 0.6, -0.1, "refine", "rationale", 1)
}

func TestGGSDecision_AcceptDirectiveEmptyRationale(t *testing.T) {
	// GGSDecision serialises directive="accept" with empty rationale (accept path)
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.GGSDecision(0.0, 0.5, 0.1, 0.2, -0.3, "accept", "", 0)
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind == KindGGSDecision {
			if e.Directive != "accept" {
				t.Errorf("Directive = %q, want %q", e.Directive, "accept")
			}
			if e.D != 0.0 {
				t.Errorf("D = %v, want 0.0", e.D)
			}
			return
		}
	}
	t.Fatal("no ggs_decision event found")
}

// ── PlanDirective ─────────────────────────────────────────────────────────────

func TestPlanDirective_WritesEvent(t *testing.T) {
	// PlanDirective writes a plan_directive event with blocked_tools and blocked_targets
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.PlanDirective("change_approach", []string{"shell", "glob"}, []string{"ls /tmp"}, "logical", "switch method")
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind != KindPlanDirective {
			continue
		}
		if e.Directive != "change_approach" {
			t.Errorf("Directive = %q, want %q", e.Directive, "change_approach")
		}
		if len(e.BlockedTools) != 2 || e.BlockedTools[0] != "shell" {
			t.Errorf("BlockedTools = %v, want [shell glob]", e.BlockedTools)
		}
		if len(e.BlockedTargets) != 1 || e.BlockedTargets[0] != "ls /tmp" {
			t.Errorf("BlockedTargets = %v, want [ls /tmp]", e.BlockedTargets)
		}
		if e.FailureClass != "logical" {
			t.Errorf("FailureClass = %q, want %q", e.FailureClass, "logical")
		}
		if e.Rationale != "switch method" {
			t.Errorf("Rationale = %q, want %q", e.Rationale, "switch method")
		}
		return
	}
	t.Fatal("no plan_directive event found")
}

func TestPlanDirective_NilReceiverNoop(t *testing.T) {
	// PlanDirective is a no-op on nil receiver
	var tl *TaskLog
	tl.PlanDirective("refine", nil, nil, "environmental", "")
}

func TestPlanDirective_NilBlockedSlices(t *testing.T) {
	// PlanDirective with nil blocked slices serialises without blocked_tools/blocked_targets
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.PlanDirective("refine", nil, nil, "environmental", "")
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind == KindPlanDirective {
			if e.BlockedTools != nil {
				t.Errorf("BlockedTools should be nil, got %v", e.BlockedTools)
			}
			if e.BlockedTargets != nil {
				t.Errorf("BlockedTargets should be nil, got %v", e.BlockedTargets)
			}
			return
		}
	}
	t.Fatal("no plan_directive event found")
}

// ── MemoryQuery ───────────────────────────────────────────────────────────────

func TestMemoryQuery_WritesEvent(t *testing.T) {
	// MemoryQuery writes a memory_query event with space, entity, sop_count, action, attention, decision
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.MemoryQuery("list_files", "env:local", 3, "Exploit", 4.5, 2.1)
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind != KindMemoryQuery {
			continue
		}
		if e.Space != "list_files" {
			t.Errorf("Space = %q, want %q", e.Space, "list_files")
		}
		if e.Entity != "env:local" {
			t.Errorf("Entity = %q, want %q", e.Entity, "env:local")
		}
		if e.SOPCount != 3 {
			t.Errorf("SOPCount = %d, want 3", e.SOPCount)
		}
		if e.Action != "Exploit" {
			t.Errorf("Action = %q, want %q", e.Action, "Exploit")
		}
		if e.Attention != 4.5 {
			t.Errorf("Attention = %v, want 4.5", e.Attention)
		}
		if e.Decision != 2.1 {
			t.Errorf("Decision = %v, want 2.1", e.Decision)
		}
		return
	}
	t.Fatal("no memory_query event found")
}

func TestMemoryQuery_NilReceiverNoop(t *testing.T) {
	// MemoryQuery is a no-op on nil receiver
	var tl *TaskLog
	tl.MemoryQuery("intent_slug", "env:local", 3, "Exploit", 4.2, 2.1)
}

func TestMemoryQuery_ZeroSOPsIgnoreAction(t *testing.T) {
	// MemoryQuery with sop_count=0 and action=Ignore is still serialised
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.MemoryQuery("new_task", "env:local", 0, "Ignore", 0.0, 0.0)
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind == KindMemoryQuery {
			if e.SOPCount != 0 {
				t.Errorf("SOPCount = %d, want 0", e.SOPCount)
			}
			if e.Action != "Ignore" {
				t.Errorf("Action = %q, want Ignore", e.Action)
			}
			return
		}
	}
	t.Fatal("no memory_query event found")
}

// ── MemoryWrite ───────────────────────────────────────────────────────────────

func TestMemoryWrite_WritesEvent(t *testing.T) {
	// MemoryWrite writes a memory_write event with state, level, space, entity
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.MemoryWrite("accept", "M", "list_files", "env:local")
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind != KindMemoryWrite {
			continue
		}
		if e.State != "accept" {
			t.Errorf("State = %q, want %q", e.State, "accept")
		}
		if e.Level != "M" {
			t.Errorf("Level = %q, want %q", e.Level, "M")
		}
		if e.Space != "list_files" {
			t.Errorf("Space = %q, want %q", e.Space, "list_files")
		}
		if e.Entity != "env:local" {
			t.Errorf("Entity = %q, want %q", e.Entity, "env:local")
		}
		return
	}
	t.Fatal("no memory_write event found")
}

func TestMemoryWrite_NilReceiverNoop(t *testing.T) {
	// MemoryWrite is a no-op on nil receiver
	var tl *TaskLog
	tl.MemoryWrite("accept", "M", "intent_slug", "env:local")
}

func TestMemoryWrite_ActionStateToolSpace(t *testing.T) {
	// MemoryWrite serialises tool-space and target-entity correctly for action states
	dir := t.TempDir()
	r := NewRegistry(filepath.Join(dir, "tasks"))
	tl := r.Open("task1", "intent")
	tl.MemoryWrite("change_approach", "M", "tool:shell", "target:ls /tmp")
	r.Close("task1", "accepted")

	events := readEvents(t, filepath.Join(dir, "tasks", "task1.jsonl"))
	for _, e := range events {
		if e.Kind == KindMemoryWrite {
			if e.Space != "tool:shell" {
				t.Errorf("Space = %q, want %q", e.Space, "tool:shell")
			}
			if e.Entity != "target:ls /tmp" {
				t.Errorf("Entity = %q, want %q", e.Entity, "target:ls /tmp")
			}
			if e.State != "change_approach" {
				t.Errorf("State = %q, want %q", e.State, "change_approach")
			}
			return
		}
	}
	t.Fatal("no memory_write event found")
}
