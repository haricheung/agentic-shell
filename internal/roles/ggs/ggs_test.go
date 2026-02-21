package ggs

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/types"
)

// ── computeD ────────────────────────────────────────────────────────────────

func TestComputeD_EmptyOutcomesReturnsOne(t *testing.T) {
	// Returns 1.0 when outcomes is empty (no data = total failure)
	if got := computeD(nil); got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestComputeD_AllMatchedReturnsZero(t *testing.T) {
	// Returns 0.0 when all outcomes are "matched"
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
		{Status: "matched"},
	}
	if got := computeD(outcomes); got != 0.0 {
		t.Errorf("expected 0.0, got %f", got)
	}
}

func TestComputeD_AllFailedReturnsOne(t *testing.T) {
	// Returns 1.0 when all outcomes are "failed"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed"},
		{Status: "failed"},
	}
	if got := computeD(outcomes); got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestComputeD_HalfFailedReturnsHalf(t *testing.T) {
	// Returns 0.5 when half the outcomes are "failed"
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
		{Status: "failed"},
	}
	if got := computeD(outcomes); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("expected 0.5, got %f", got)
	}
}

// ── computeP ────────────────────────────────────────────────────────────────

func TestComputeP_EmptyOutcomesReturnsNeutral(t *testing.T) {
	// Returns 0.5 when outcomes is empty (neutral default)
	if got := computeP(nil); got != 0.5 {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestComputeP_NoFailureReasonsReturnsNeutral(t *testing.T) {
	// Returns 0.5 when no outcomes have failure reasons (neutral)
	outcomes := []types.SubTaskOutcome{
		{Status: "failed"}, // no FailureReason, no GapTrajectory
	}
	if got := computeP(outcomes); got != 0.5 {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestComputeP_LogicalKeywordPushesAboveHalf(t *testing.T) {
	// Returns value > 0.5 when failure reasons suggest logical errors
	reason := "wrong approach used"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", FailureReason: &reason},
	}
	if got := computeP(outcomes); got <= 0.5 {
		t.Errorf("expected P > 0.5 for logical failure, got %f", got)
	}
}

func TestComputeP_EnvironmentalKeywordPushesBelowHalf(t *testing.T) {
	// Returns value < 0.5 when failure reasons suggest environmental errors
	reason := "context deadline exceeded"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", FailureReason: &reason},
	}
	if got := computeP(outcomes); got >= 0.5 {
		t.Errorf("expected P < 0.5 for environmental failure, got %f", got)
	}
}

func TestComputeP_InRangeZeroToOne(t *testing.T) {
	// Returns value in [0, 1]
	reason := "network timeout"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", FailureReason: &reason},
	}
	got := computeP(outcomes)
	if got < 0.0 || got > 1.0 {
		t.Errorf("P out of range [0,1]: %f", got)
	}
}

// ── computeOmega ─────────────────────────────────────────────────────────────

func TestComputeOmega_BothZeroReturnsZero(t *testing.T) {
	// Returns 0.0 when replanCount=0 and elapsedMs=0
	if got := computeOmega(0, 0); got != 0.0 {
		t.Errorf("expected 0.0, got %f", got)
	}
}

func TestComputeOmega_MaxReplansNoTimeReturnsW1(t *testing.T) {
	// Returns w1 (0.6) when replanCount=maxReplansGGS and elapsedMs=0
	got := computeOmega(maxReplansGGS, 0)
	if math.Abs(got-w1) > 1e-9 {
		t.Errorf("expected w1=%.1f, got %f", w1, got)
	}
}

func TestComputeOmega_ZeroReplansFullBudgetReturnsW2(t *testing.T) {
	// Returns w2 (0.4) when replanCount=0 and elapsedMs=timeBudgetMs
	got := computeOmega(0, timeBudgetMs)
	if math.Abs(got-w2) > 1e-9 {
		t.Errorf("expected w2=%.1f, got %f", w2, got)
	}
}

func TestComputeOmega_MaxBothReturnsCappedAtOne(t *testing.T) {
	// Returns 1.0 when replanCount=maxReplansGGS and elapsedMs=timeBudgetMs
	got := computeOmega(maxReplansGGS, timeBudgetMs)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestComputeOmega_NeverExceedsOne(t *testing.T) {
	// Never exceeds 1.0
	got := computeOmega(maxReplansGGS*10, timeBudgetMs*10)
	if got > 1.0 {
		t.Errorf("Omega exceeded 1.0: %f", got)
	}
}

// ── computeLoss ──────────────────────────────────────────────────────────────

func TestComputeLoss_PureDistanceLoss(t *testing.T) {
	// Returns α when D=1, P=0, Ω=0 (pure distance loss)
	got := computeLoss(1.0, 0.0, 0.0)
	if math.Abs(got-alpha) > 1e-9 {
		t.Errorf("expected α=%.1f, got %f", alpha, got)
	}
}

func TestComputeLoss_PureResourceCost(t *testing.T) {
	// Returns λ when D=0, P=0, Ω=1 (pure resource cost; β_eff=0 when Ω=1)
	got := computeLoss(0.0, 0.0, 1.0)
	if math.Abs(got-lambda) > 1e-9 {
		t.Errorf("expected λ=%.1f, got %f", lambda, got)
	}
}

func TestComputeLoss_MaxDistancePlusBetaAtZeroOmega(t *testing.T) {
	// Returns α+β when D=1, P=1, Ω=0 (λ·Ω = 0 at zero budget pressure)
	expected := alpha + beta
	got := computeLoss(1.0, 1.0, 0.0)
	if math.Abs(got-expected) > 1e-9 {
		t.Errorf("expected α+β=%.2f, got %f", expected, got)
	}
}

func TestComputeLoss_BetaEffZeroWhenOmegaOne(t *testing.T) {
	// β_eff is zero when Ω=1, so P has no effect when budget is exhausted
	got1 := computeLoss(0.5, 0.0, 1.0)
	got2 := computeLoss(0.5, 1.0, 1.0)
	if math.Abs(got1-got2) > 1e-9 {
		t.Errorf("P should have no effect when Ω=1: loss(P=0)=%f loss(P=1)=%f", got1, got2)
	}
}

// ── computeGradient ──────────────────────────────────────────────────────────

func TestComputeGradient_PlateauWhenSmallGradAndHighD(t *testing.T) {
	// Returns "plateau" when |∇L| < epsilon and D > delta
	got := computeGradient(0.0, delta+0.1)
	if got != "plateau" {
		t.Errorf("expected plateau, got %q", got)
	}
}

func TestComputeGradient_StableWhenSmallGradAndLowD(t *testing.T) {
	// Returns "stable" when |∇L| < epsilon and D <= delta
	got := computeGradient(0.0, delta-0.1)
	if got != "stable" {
		t.Errorf("expected stable, got %q", got)
	}
}

func TestComputeGradient_ImprovingWhenNegativeGrad(t *testing.T) {
	// Returns "improving" when ∇L < -epsilon
	got := computeGradient(-(epsilon + 0.05), 0.5)
	if got != "improving" {
		t.Errorf("expected improving, got %q", got)
	}
}

func TestComputeGradient_WorseningWhenPositiveGrad(t *testing.T) {
	// Returns "worsening" when ∇L > epsilon
	got := computeGradient(epsilon+0.05, 0.5)
	if got != "worsening" {
		t.Errorf("expected worsening, got %q", got)
	}
}

// ── selectDirective ───────────────────────────────────────────────────────────

func TestSelectDirective_AbandonWhenOmegaHigh(t *testing.T) {
	// Returns "abandon" when Omega >= abandonOmega regardless of other values
	got := selectDirective(0.0, 0.5, 0.5, abandonOmega, "stable")
	if got != "abandon" {
		t.Errorf("expected abandon, got %q", got)
	}
}

func TestSelectDirective_RefineWhenImproving(t *testing.T) {
	// Returns "refine" when gradient is "improving"
	got := selectDirective(-0.2, 0.5, 0.3, 0.2, "improving")
	if got != "refine" {
		t.Errorf("expected refine, got %q", got)
	}
}

func TestSelectDirective_BreakSymmetryWhenPlateauHighP(t *testing.T) {
	// Returns "break_symmetry" when gradient is "plateau" and P > 0.5
	got := selectDirective(0.0, 0.5, 0.8, 0.2, "plateau")
	if got != "break_symmetry" {
		t.Errorf("expected break_symmetry, got %q", got)
	}
}

func TestSelectDirective_ChangePathWhenPlateauLowP(t *testing.T) {
	// Returns "change_path" when gradient is "plateau" and P <= 0.5
	got := selectDirective(0.0, 0.5, 0.2, 0.2, "plateau")
	if got != "change_path" {
		t.Errorf("expected change_path, got %q", got)
	}
}

func TestSelectDirective_ChangeApproachWhenWorseningHighP(t *testing.T) {
	// Returns "change_approach" when gradient is "worsening" and P > 0.5
	got := selectDirective(0.2, 0.5, 0.8, 0.2, "worsening")
	if got != "change_approach" {
		t.Errorf("expected change_approach, got %q", got)
	}
}

func TestSelectDirective_RefineWhenWorseningLowP(t *testing.T) {
	// Returns "refine" when gradient is "worsening" and P <= 0.5
	got := selectDirective(0.2, 0.5, 0.2, 0.2, "worsening")
	if got != "refine" {
		t.Errorf("expected refine, got %q", got)
	}
}

func TestSelectDirective_RefineWhenStable(t *testing.T) {
	// Returns "refine" for "stable" gradient (converged, minor tightening)
	got := selectDirective(0.0, 0.1, 0.5, 0.2, "stable")
	if got != "refine" {
		t.Errorf("expected refine for stable, got %q", got)
	}
}

// ── deriveBlockedTools ───────────────────────────────────────────────────────

func TestDeriveBlockedTools_NilForRefineDirective(t *testing.T) {
	// Returns nil for directives other than "break_symmetry" and "change_approach"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: []string{"shell: ls"}},
	}
	got := deriveBlockedTools(outcomes, "refine")
	if got != nil {
		t.Errorf("expected nil for refine, got %v", got)
	}
}

func TestDeriveBlockedTools_NilWhenNoFailedToolCalls(t *testing.T) {
	// Returns nil when no failed outcomes have ToolCalls
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: nil},
	}
	got := deriveBlockedTools(outcomes, "break_symmetry")
	if got != nil {
		t.Errorf("expected nil with no ToolCalls, got %v", got)
	}
}

func TestDeriveBlockedTools_ExtractsToolNamesForBreakSymmetry(t *testing.T) {
	// Returns deduplicated tool name list for break_symmetry
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: []string{"shell: ls -la", "shell: find / -name foo"}},
		{Status: "failed", ToolCalls: []string{"glob: *.go"}},
	}
	got := deriveBlockedTools(outcomes, "break_symmetry")
	if len(got) != 2 {
		t.Errorf("expected 2 unique tools (shell, glob), got %v", got)
	}
}

// ── primaryFailedCriterion ───────────────────────────────────────────────────

func TestPrimaryFailedCriterion_EmptyWhenNoFailures(t *testing.T) {
	// Returns "" when no outcomes are failed or GapTrajectory is empty
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
	}
	got := primaryFailedCriterion(outcomes)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestPrimaryFailedCriterion_ReturnsFirstUnmetCriterion(t *testing.T) {
	// Returns the first unmet criterion from the last trajectory point of the first failed outcome
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			GapTrajectory: []types.GapTrajectoryPoint{
				{Attempt: 1, UnmetCriteria: []string{"output contains file path"}},
			},
		},
	}
	got := primaryFailedCriterion(outcomes)
	if got != "output contains file path" {
		t.Errorf("expected criterion text, got %q", got)
	}
}

// ── computeFailureClass ──────────────────────────────────────────────────────

func TestComputeFailureClass_LogicalWhenPAboveHalf(t *testing.T) {
	// Returns "logical" when P > 0.5
	reason := "wrong approach used"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", FailureReason: &reason},
	}
	got := computeFailureClass(outcomes)
	if got != "logical" {
		t.Errorf("expected logical, got %q", got)
	}
}

func TestComputeFailureClass_EnvironmentalWhenPBelowHalf(t *testing.T) {
	// Returns "environmental" when P < 0.5
	reason := "network timeout"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", FailureReason: &reason},
	}
	got := computeFailureClass(outcomes)
	if got != "environmental" {
		t.Errorf("expected environmental, got %q", got)
	}
}

func TestComputeFailureClass_MixedWhenPEqualsHalf(t *testing.T) {
	// Returns "mixed" when P == 0.5
	got := computeFailureClass(nil) // empty → P = 0.5
	if got != "mixed" {
		t.Errorf("expected mixed, got %q", got)
	}
}

// ── processAccept ─────────────────────────────────────────────────────────────

func TestProcessAccept_EmitsFinalResultWithCorrectPayload(t *testing.T) {
	// Emits MsgFinalResult to RoleUser with summary and output from OutcomeSummary
	b := bus.New()
	tap := b.NewTap()
	var gotSummary string
	var gotOutput any
	gs := New(b, func(_ string, summary string, output any) {
		gotSummary = summary
		gotOutput = output
	})
	os := types.OutcomeSummary{
		TaskID:       "t1",
		Summary:      "all done",
		MergedOutput: "result data",
		ElapsedMs:    5000,
	}
	gs.processAccept(context.Background(), os)

	// Drain tap for FinalResult
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-tap:
			if msg.Type == types.MsgFinalResult {
				if msg.From != types.RoleGGS || msg.To != types.RoleUser {
					t.Errorf("expected From=R7 To=User, got From=%s To=%s", msg.From, msg.To)
				}
				if gotSummary != "all done" {
					t.Errorf("outputFn summary: expected %q got %q", "all done", gotSummary)
				}
				if gotOutput != "result data" {
					t.Errorf("outputFn output: expected %q got %v", "result data", gotOutput)
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for MsgFinalResult")
		}
	}
}

func TestProcessAccept_CleansUpPerTaskState(t *testing.T) {
	// Removes lPrev and replans entries after accept
	b := bus.New()
	gs := New(b, nil)
	gs.mu.Lock()
	gs.lPrev["t2"] = 0.5
	gs.replans["t2"] = 1
	gs.mu.Unlock()

	gs.processAccept(context.Background(), types.OutcomeSummary{TaskID: "t2"})

	gs.mu.Lock()
	defer gs.mu.Unlock()
	if _, ok := gs.lPrev["t2"]; ok {
		t.Error("expected lPrev[t2] to be deleted after accept")
	}
	if _, ok := gs.replans["t2"]; ok {
		t.Error("expected replans[t2] to be deleted after accept")
	}
}

func TestProcessAccept_DIsAlwaysZero(t *testing.T) {
	// D=0 because all subtasks matched — computeD on matched outcomes returns 0
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
		{Status: "matched"},
	}
	if got := computeD(outcomes); got != 0.0 {
		t.Errorf("expected D=0 for all-matched outcomes, got %f", got)
	}
}

func TestProcessAccept_OmegaUsesElapsedTimeAndPriorReplans(t *testing.T) {
	// Ω is non-zero even on first-try accept when significant time has elapsed
	omega := computeOmega(0, timeBudgetMs/2) // 0 replans, half budget elapsed
	if math.Abs(omega-w2*0.5) > 1e-9 {
		t.Errorf("expected Ω=w2*0.5=%.3f, got %.3f", w2*0.5, omega)
	}
}
