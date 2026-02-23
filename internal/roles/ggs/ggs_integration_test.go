package ggs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/roles/ggs"
	"github.com/haricheung/agentic-shell/internal/types"
)

// --- helpers ---

func waitMsg(t *testing.T, ch <-chan types.Message, timeout time.Duration) types.Message {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatal("timeout waiting for bus message")
		return types.Message{}
	}
}

func toPlanDirective(t *testing.T, payload any) types.PlanDirective {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal PlanDirective: %v", err)
	}
	var pd types.PlanDirective
	if err := json.Unmarshal(b, &pd); err != nil {
		t.Fatalf("unmarshal PlanDirective: %v", err)
	}
	return pd
}

func toFinalResult(t *testing.T, payload any) types.FinalResult {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal FinalResult: %v", err)
	}
	var fr types.FinalResult
	if err := json.Unmarshal(b, &fr); err != nil {
		t.Fatalf("unmarshal FinalResult: %v", err)
	}
	return fr
}

func strPtr(s string) *string { return &s }

// publishReplan sends a ReplanRequest to the bus and waits for GGS to route it.
func publishReplan(b *bus.Bus, taskID string, outcomes []types.SubTaskOutcome, elapsedMs int64) {
	var failed []string
	for _, o := range outcomes {
		if o.Status == "failed" {
			failed = append(failed, o.SubTaskID)
		}
	}
	b.Publish(types.Message{
		ID:    "rr-" + taskID,
		From:  types.RoleMetaVal,
		To:    types.RoleGGS,
		Type:  types.MsgReplanRequest,
		Payload: types.ReplanRequest{
			TaskID:         taskID,
			GapSummary:     "test gap",
			FailedSubTasks: failed,
			Outcomes:       outcomes,
			ElapsedMs:      elapsedMs,
		},
	})
}

// failedOutcome returns a single failed SubTaskOutcome.
// reason="" → P=0.5 (neutral); reason="logic" → P>0.5 (logical failure).
func failedOutcome(taskID, subID, reason string) types.SubTaskOutcome {
	o := types.SubTaskOutcome{
		SubTaskID:    subID,
		ParentTaskID: taskID,
		Status:       "failed",
		GapTrajectory: []types.GapTrajectoryPoint{
			{Attempt: 1, Score: 0.0, UnmetCriteria: []string{"criterion not met"}},
		},
	}
	if reason != "" {
		o.FailureReason = strPtr(reason)
	}
	return o
}

// --- integration tests ---

func TestGGSIntegration_ChangePath(t *testing.T) {
	// First round, D > δ, P = 0.5 (no keywords → neutral) → plateau → change_path
	b := bus.New()
	g := ggs.New(b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directiveCh := b.Subscribe(types.MsgPlanDirective)
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond) // let GGS goroutine register its subscriptions

	publishReplan(b, "task-cp", []types.SubTaskOutcome{
		failedOutcome("task-cp", "st-1", ""), // no keyword → P=0.5
	}, 0)

	msg := waitMsg(t, directiveCh, 2*time.Second)
	pd := toPlanDirective(t, msg.Payload)
	if pd.Directive != "change_path" {
		t.Errorf("expected change_path, got %q (gradient=%s D=%.2f P=%.2f Ω=%.2f)",
			pd.Directive, pd.Gradient, pd.Loss.D, pd.Loss.P, pd.BudgetPressure)
	}
	if pd.Gradient != "plateau" {
		t.Errorf("expected plateau gradient, got %q", pd.Gradient)
	}
}

func TestGGSIntegration_BreakSymmetry(t *testing.T) {
	// First round, D > δ, P > 0.5 (logical keyword) → plateau → break_symmetry
	b := bus.New()
	g := ggs.New(b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directiveCh := b.Subscribe(types.MsgPlanDirective)
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	publishReplan(b, "task-bs", []types.SubTaskOutcome{
		failedOutcome("task-bs", "st-1", "logic error in approach"),
	}, 0)

	msg := waitMsg(t, directiveCh, 2*time.Second)
	pd := toPlanDirective(t, msg.Payload)
	if pd.Directive != "break_symmetry" {
		t.Errorf("expected break_symmetry, got %q (gradient=%s D=%.2f P=%.2f)",
			pd.Directive, pd.Gradient, pd.Loss.D, pd.Loss.P)
	}
}

func TestGGSIntegration_Refine_ImprovingGradient(t *testing.T) {
	// Round 1: all failed (D=1.0) → change_path; Round 2: half failed (D=0.5) → ∇L < 0 → refine
	b := bus.New()
	g := ggs.New(b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directiveCh := b.Subscribe(types.MsgPlanDirective)
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	// Round 1 — high D, no prior lPrev → gradL=0 → plateau → change_path
	publishReplan(b, "task-rf", []types.SubTaskOutcome{
		failedOutcome("task-rf", "st-1", ""),
	}, 0)
	msg1 := waitMsg(t, directiveCh, 2*time.Second)
	pd1 := toPlanDirective(t, msg1.Payload)
	if pd1.TaskID != "task-rf" {
		t.Fatalf("round 1: unexpected task_id %q", pd1.TaskID)
	}

	// Round 2 — D=0.5 (improvement from 1.0), L decreases → ∇L < 0 → improving → refine
	publishReplan(b, "task-rf", []types.SubTaskOutcome{
		{SubTaskID: "st-1", ParentTaskID: "task-rf", Status: "matched"},
		failedOutcome("task-rf", "st-2", ""),
	}, 0)
	msg2 := waitMsg(t, directiveCh, 2*time.Second)
	pd2 := toPlanDirective(t, msg2.Payload)
	if pd2.Directive != "refine" {
		t.Errorf("round 2: expected refine (improving), got %q (gradient=%s ∇L=%.3f)",
			pd2.Directive, pd2.Gradient, pd2.GradL)
	}
	if pd2.GradL >= 0 {
		t.Errorf("round 2: expected ∇L < 0 (improving), got ∇L=%.3f", pd2.GradL)
	}
}

func TestGGSIntegration_Abandon_EmitsFinalResult(t *testing.T) {
	// Ω ≥ 0.8 (large elapsedMs) → abandon → MsgFinalResult instead of MsgPlanDirective
	b := bus.New()
	g := ggs.New(b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directiveCh := b.Subscribe(types.MsgPlanDirective)
	finalCh := b.Subscribe(types.MsgFinalResult)
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	// elapsedMs=600000 (2× budget) → Ω = 0.6*(1/3) + 0.4*(600000/300000) = 0.2 + 0.8 = 1.0 ≥ 0.8
	publishReplan(b, "task-ab", []types.SubTaskOutcome{
		failedOutcome("task-ab", "st-1", ""),
	}, 600_000)

	// Expect FinalResult (not PlanDirective)
	select {
	case msg := <-finalCh:
		fr := toFinalResult(t, msg.Payload)
		if fr.TaskID != "task-ab" {
			t.Errorf("expected task_id 'task-ab', got %q", fr.TaskID)
		}
		if fr.Loss.D == 0 {
			t.Errorf("expected D > 0 on abandon path, got D=0")
		}
		if fr.Loss.Omega < 0.8 {
			t.Errorf("expected Ω ≥ 0.8 on abandon path, got Ω=%.3f", fr.Loss.Omega)
		}
	case <-directiveCh:
		t.Error("expected FinalResult (abandon) but received PlanDirective")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for FinalResult on abandon path")
	}
}

func TestGGSIntegration_AcceptPath_EmitsFinalResultWithDZero(t *testing.T) {
	// OutcomeSummary (all matched) → GGS accept path → MsgFinalResult with D=0
	b := bus.New()
	g := ggs.New(b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finalCh := b.Subscribe(types.MsgFinalResult)
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	b.Publish(types.Message{
		ID:   "os-1",
		From: types.RoleMetaVal,
		To:   types.RoleGGS,
		Type: types.MsgOutcomeSummary,
		Payload: types.OutcomeSummary{
			TaskID:       "task-acc",
			Summary:      "all done",
			MergedOutput: "result text",
			ElapsedMs:    30_000,
			Outcomes: []types.SubTaskOutcome{
				{SubTaskID: "st-1", ParentTaskID: "task-acc", Status: "matched"},
			},
		},
	})

	msg := waitMsg(t, finalCh, 2*time.Second)
	fr := toFinalResult(t, msg.Payload)
	if fr.Loss.D != 0.0 {
		t.Errorf("accept path: expected D=0, got D=%.3f", fr.Loss.D)
	}
	if fr.Summary != "all done" {
		t.Errorf("accept path: expected summary 'all done', got %q", fr.Summary)
	}
	if fr.Replans != 0 {
		t.Errorf("accept path (first try): expected Replans=0, got %d", fr.Replans)
	}
}
