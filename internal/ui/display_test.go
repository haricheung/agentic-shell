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
