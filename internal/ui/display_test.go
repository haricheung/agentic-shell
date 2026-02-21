package ui

import (
	"strings"
	"testing"

	"github.com/haricheung/agentic-shell/internal/types"
)

func makeMsg(t types.MessageType, payload any) types.Message {
	return types.Message{Type: t, Payload: payload}
}

// --- msgDetail: MsgSubTask ---

func TestMsgDetail_SubTask_WithCriteria(t *testing.T) {
	// MsgSubTask: returns "#N intent | first_criterion" when criteria present
	st := types.SubTask{
		Sequence:        1,
		Intent:          "locate the audio file",
		SuccessCriteria: []string{"output contains a valid file path"},
	}
	got := msgDetail(makeMsg(types.MsgSubTask, st))
	if !strings.Contains(got, "#1") {
		t.Errorf("expected sequence number, got %q", got)
	}
	if !strings.Contains(got, "|") {
		t.Errorf("expected | separator for criteria, got %q", got)
	}
	if !strings.Contains(got, "output contains") {
		t.Errorf("expected first criterion in detail, got %q", got)
	}
}

func TestMsgDetail_SubTask_MultipleCriteriaSuffix(t *testing.T) {
	// MsgSubTask: "+N" suffix when multiple criteria
	st := types.SubTask{
		Sequence:        2,
		Intent:          "extract audio",
		SuccessCriteria: []string{"output file exists", "duration matches", "format is mp3"},
	}
	got := msgDetail(makeMsg(types.MsgSubTask, st))
	if !strings.Contains(got, "(+2)") {
		t.Errorf("expected (+2) suffix for 3 criteria, got %q", got)
	}
}

func TestMsgDetail_SubTask_NoCriteria(t *testing.T) {
	// MsgSubTask: returns "#N intent" with no suffix when SuccessCriteria is empty
	st := types.SubTask{Sequence: 1, Intent: "list files"}
	got := msgDetail(makeMsg(types.MsgSubTask, st))
	if strings.Contains(got, "|") {
		t.Errorf("unexpected | separator when no criteria, got %q", got)
	}
	if strings.Contains(got, "(+") {
		t.Errorf("unexpected suffix when no criteria, got %q", got)
	}
}

// --- msgDetail: MsgSubTaskOutcome ---

func TestMsgDetail_SubTaskOutcome_FailedWithUnmetCriteria(t *testing.T) {
	// MsgSubTaskOutcome failed with trajectory: returns "failed | unmet: <first unmet criterion>"
	o := types.SubTaskOutcome{
		Status: "failed",
		GapTrajectory: []types.GapTrajectoryPoint{
			{Attempt: 1, UnmetCriteria: []string{"output contains a valid file path"}},
		},
	}
	got := msgDetail(makeMsg(types.MsgSubTaskOutcome, o))
	if !strings.HasPrefix(got, "failed | unmet:") {
		t.Errorf("expected 'failed | unmet:' prefix, got %q", got)
	}
	if !strings.Contains(got, "output contains") {
		t.Errorf("expected unmet criterion text, got %q", got)
	}
}

func TestMsgDetail_SubTaskOutcome_MatchedReturnsStatus(t *testing.T) {
	// MsgSubTaskOutcome matched: returns status string only
	o := types.SubTaskOutcome{Status: "matched"}
	got := msgDetail(makeMsg(types.MsgSubTaskOutcome, o))
	if got != "matched" {
		t.Errorf("expected 'matched', got %q", got)
	}
}

func TestMsgDetail_SubTaskOutcome_FailedNoTrajectory(t *testing.T) {
	// MsgSubTaskOutcome failed with no trajectory: returns status string only
	o := types.SubTaskOutcome{Status: "failed"}
	got := msgDetail(makeMsg(types.MsgSubTaskOutcome, o))
	if got != "failed" {
		t.Errorf("expected 'failed' with no unmet detail, got %q", got)
	}
}

// --- msgDetail: unknown type ---

func TestMsgDetail_UnknownType(t *testing.T) {
	// Returns "" for unknown or unparseable message types
	got := msgDetail(makeMsg("UnknownMessageType", nil))
	if got != "" {
		t.Errorf("expected empty string for unknown type, got %q", got)
	}
}

// --- runeWidth ---

func TestRuneWidth_ASCIIIsOneColumn(t *testing.T) {
	// ASCII runes (< 0x1100) always return 1
	for _, r := range "abcdefghijklmnopqrstuvwxyz0123456789 !@#" {
		if got := runeWidth(r); got != 1 {
			t.Errorf("runeWidth(%q) = %d, want 1", r, got)
		}
	}
}

func TestRuneWidth_CJKUnifiedIdeographsAreTwoColumns(t *testing.T) {
	// CJK Unified Ideographs (0x4E00–0x9FFF) return 2
	for _, r := range "重新执行命令文件" {
		if got := runeWidth(r); got != 2 {
			t.Errorf("runeWidth(%q U+%04X) = %d, want 2", r, r, got)
		}
	}
}

func TestRuneWidth_HangulSyllablesAreTwoColumns(t *testing.T) {
	// Hangul Syllables (0xAC00–0xD7A3) return 2
	for _, r := range "한글" {
		if got := runeWidth(r); got != 2 {
			t.Errorf("runeWidth(%q U+%04X) = %d, want 2", r, r, got)
		}
	}
}

func TestRuneWidth_FullWidthLatinAreTwoColumns(t *testing.T) {
	// Full-width Latin forms (0xFF01–0xFF60) return 2
	if got := runeWidth('Ａ'); got != 2 { // U+FF21 FULLWIDTH LATIN CAPITAL LETTER A
		t.Errorf("runeWidth(Ａ) = %d, want 2", got)
	}
}

// --- clipCols ---

func TestClipCols_UnchangedWhenWithinLimit(t *testing.T) {
	// Returns s unchanged when its column width is already ≤ cols
	s := "hello"
	if got := clipCols(s, 10); got != s {
		t.Errorf("clipCols(%q, 10) = %q, want unchanged", s, got)
	}
}

func TestClipCols_TruncatesAtRuneBoundaryForCJK(t *testing.T) {
	// All-CJK input is truncated at cols/2 runes
	// "重新执行命令" = 6 CJK runes = 12 cols; clip to 8 cols → 4 runes + "…"
	s := "重新执行命令"
	got := clipCols(s, 8)
	runes := []rune(got)
	// Must end with "…"
	if runes[len(runes)-1] != '…' {
		t.Errorf("clipCols CJK: expected trailing …, got %q", got)
	}
	// Visual width of result (excluding …) must be ≤ 8
	content := string(runes[:len(runes)-1])
	cols := 0
	for _, r := range content {
		cols += runeWidth(r)
	}
	if cols > 8 {
		t.Errorf("clipCols CJK: content is %d cols, want ≤ 8", cols)
	}
}

func TestClipCols_AppendsEllipsisOnlyWhenTrimmed(t *testing.T) {
	// Appends "…" only when truncation occurs; unchanged string has no suffix
	short := "ok"
	if got := clipCols(short, 10); strings.Contains(got, "…") {
		t.Errorf("clipCols: unexpected … in unchanged result %q", got)
	}
	long := strings.Repeat("a", 20)
	if got := clipCols(long, 10); !strings.HasSuffix(got, "…") {
		t.Errorf("clipCols: expected … suffix for truncated result, got %q", got)
	}
}

// --- dynamicStatus: CJK correction signal ---

func TestDynamicStatus_CorrectionSignal_CJKFitsWithinOneLine(t *testing.T) {
	// Spinner status with CJK WhatToDo must fit within ~54 visual columns
	// so that \r\033[K can overwrite it on an 80-col terminal without wrapping.
	allCJK := strings.Repeat("重", 30) // 30 runes × 2 cols = 60 cols if unclipped
	cs := types.CorrectionSignal{
		AttemptNumber: 1,
		WhatToDo:      allCJK,
	}
	got := dynamicStatus(makeMsg(types.MsgCorrectionSignal, cs))

	// Measure visual column width of the returned status.
	cols := 0
	for _, r := range got {
		cols += runeWidth(r)
	}
	// "⚙️  retry N — " prefix (~14 cols) + 38 col clip = 52 cols max.
	// Allow 60 cols as a generous upper bound (accounts for ANSI escapes not
	// being included in got; the actual terminal output adds the spinner frame).
	if cols > 60 {
		t.Errorf("dynamicStatus CJK: status is %d visual cols, want ≤ 60 (got %q)", cols, got)
	}
}
