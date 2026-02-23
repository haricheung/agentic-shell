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

func TestComputeD_CriterionLevelHigherThanSubtaskLevel(t *testing.T) {
	// st1: 3 criteria all fail; st2: 1 criterion passes
	// CriteriaVerdicts: D=3/4=0.75; subtask fallback would give D=1/2=0.5
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "a", Verdict: "fail"},
				{Criterion: "b", Verdict: "fail"},
				{Criterion: "c", Verdict: "fail"},
			},
		},
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "d", Verdict: "pass"},
			},
		},
	}
	got := computeD(outcomes)
	if math.Abs(got-0.75) > 1e-9 {
		t.Errorf("expected criterion-level D=0.75, got %f", got)
	}
}

func TestComputeD_FallsBackToSubtaskLevelWhenNoCriteriaVerdicts(t *testing.T) {
	// outcomes with no CriteriaVerdicts → same result as old computeD (subtask-level)
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
		{Status: "failed"},
	}
	got := computeD(outcomes)
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("expected subtask-level D=0.5, got %f", got)
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

func TestComputeP_AllLogicalCriteriaReturnsOne(t *testing.T) {
	// CriteriaVerdicts with all FailureClass="logical" → P=1.0
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "a", Verdict: "fail", FailureClass: "logical"},
				{Criterion: "b", Verdict: "fail", FailureClass: "logical"},
			},
		},
	}
	got := computeP(outcomes)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("expected P=1.0 for all logical failures, got %f", got)
	}
}

func TestComputeP_AllEnvironmentalCriteriaReturnsZero(t *testing.T) {
	// CriteriaVerdicts with all FailureClass="environmental" → P=0.0
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "a", Verdict: "fail", FailureClass: "environmental"},
			},
		},
	}
	got := computeP(outcomes)
	if math.Abs(got-0.0) > 1e-9 {
		t.Errorf("expected P=0.0 for all environmental failures, got %f", got)
	}
}

func TestComputeP_FallsBackToKeywordWhenNoStructuredClass(t *testing.T) {
	// CriteriaVerdicts present but FailureClass="" on all → falls back to keyword → neutral (0.5)
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "a", Verdict: "fail", FailureClass: ""},
			},
		},
	}
	got := computeP(outcomes)
	// No keywords in empty failure reasons → neutral 0.5
	if math.Abs(got-0.5) > 1e-9 {
		t.Errorf("expected P=0.5 fallback when no structured class, got %f", got)
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

// ── deriveBlockedTargets ─────────────────────────────────────────────────────

func TestDeriveBlockedTargets_NilForBreakSymmetryDirective(t *testing.T) {
	// Returns nil for directives other than "change_path" and "refine"
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: []string{`search: {"query":"reuters trump"} → results`}},
	}
	got := deriveBlockedTargets(outcomes, "break_symmetry")
	if got != nil {
		t.Errorf("expected nil for break_symmetry, got %v", got)
	}
}

func TestDeriveBlockedTargets_NilForNonFailedOutcomes(t *testing.T) {
	// Returns nil when no outcomes are failed
	outcomes := []types.SubTaskOutcome{
		{Status: "matched", ToolCalls: []string{`search: {"query":"reuters trump"} → results`}},
	}
	got := deriveBlockedTargets(outcomes, "change_path")
	if got != nil {
		t.Errorf("expected nil when outcome is matched, got %v", got)
	}
}

func TestDeriveBlockedTargets_ExtractsQueryFromSearchToolCall(t *testing.T) {
	// Extracts "query" field from search tool calls in failed outcomes
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: []string{`search: {"query":"trump china visit reuters.com"} → 451 error`}},
	}
	got := deriveBlockedTargets(outcomes, "change_path")
	if len(got) != 1 || got[0] != "trump china visit reuters.com" {
		t.Errorf("expected [trump china visit reuters.com], got %v", got)
	}
}

func TestDeriveBlockedTargets_ExtractsCommandFromShellToolCall(t *testing.T) {
	// Extracts "command" field from shell tool calls in failed outcomes
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: []string{`shell: {"command":"curl https://api.reuters.com/data"} → connection refused`}},
	}
	got := deriveBlockedTargets(outcomes, "refine")
	if len(got) != 1 || got[0] != "curl https://api.reuters.com/data" {
		t.Errorf("expected [curl https://api.reuters.com/data], got %v", got)
	}
}

func TestDeriveBlockedTargets_DeduplicatesAcrossMultipleCalls(t *testing.T) {
	// Returns deduplicated list when the same query appears in multiple failed tool calls
	outcomes := []types.SubTaskOutcome{
		{Status: "failed", ToolCalls: []string{
			`search: {"query":"trump china reuters"} → 451`,
			`search: {"query":"trump china reuters"} → 451`,
			`search: {"query":"trump china bbc"} → blocked`,
		}},
	}
	got := deriveBlockedTargets(outcomes, "change_path")
	if len(got) != 2 {
		t.Errorf("expected 2 unique targets, got %d: %v", len(got), got)
	}
}

// ── appendDeduped ─────────────────────────────────────────────────────────────

func TestAppendDeduped_AddsNewItemsOnly(t *testing.T) {
	// appendDeduped only adds items not already in existing
	existing := []string{"reuters.com", "bbc.com"}
	newItems := []string{"bbc.com", "cnn.com"}
	got := appendDeduped(existing, newItems)
	if len(got) != 3 {
		t.Errorf("expected 3 items (reuters.com, bbc.com, cnn.com), got %d: %v", len(got), got)
	}
	if got[2] != "cnn.com" {
		t.Errorf("expected last item to be cnn.com, got %s", got[2])
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

// ── Law 2 kill-switch ─────────────────────────────────────────────────────────

// worseningOutcomes returns a ReplanRequest whose outcomes produce a worsening
// gradient when L_prev is primed to a low value (e.g. 0.05).
// All outcomes failed → D=1.0 → L = α·1 + β_eff·P + λ·Ω > 0.05.
func worseningReplanRequest(taskID string) types.ReplanRequest {
	failReason := "logical error"
	return types.ReplanRequest{
		TaskID:     taskID,
		GapSummary: "all subtasks failed",
		ElapsedMs:  1000,
		Outcomes: []types.SubTaskOutcome{
			{
				Status:        "failed",
				FailureReason: &failReason,
				CriteriaVerdicts: []types.CriteriaVerdict{
					{Criterion: "c1", Verdict: "fail", FailureClass: "logical"},
				},
			},
		},
	}
}

func TestLaw2KillSwitch_AbandonAfterTwoConsecutiveWorsening(t *testing.T) {
	// directive overridden to "abandon" after 2 consecutive worsening gradients
	b := bus.New()
	tap := b.NewTap()
	gs := New(b, nil)

	taskID := "law2-task"
	// Prime lPrev to a very low value so first replan produces worsening gradient.
	gs.mu.Lock()
	gs.lPrev[taskID] = 0.01
	gs.mu.Unlock()

	rr := worseningReplanRequest(taskID)

	// Round 1: worsening but only count=1 — should emit PlanDirective (not abandon yet).
	gs.process(context.Background(), rr)
	timeout := time.After(500 * time.Millisecond)
	gotDirective := false
loop1:
	for {
		select {
		case msg := <-tap:
			if msg.Type == types.MsgPlanDirective {
				gotDirective = true
				break loop1
			}
			if msg.Type == types.MsgFinalResult {
				// abandoned on round 1 — only possible if Ω already ≥ 0.8
				// (small elapsed, only 1 replan: Ω = 0.6*(1/3) + 0.4*(1000/300000) ≈ 0.2)
				// This should not happen, but let the second call decide.
				gotDirective = false
				break loop1
			}
		case <-timeout:
			t.Fatal("timed out waiting for round-1 message")
		}
	}

	// Round 2: second consecutive worsening — kill-switch must fire → FinalResult (abandon).
	// Re-prime lPrev to a low value again so gradient stays worsening.
	gs.mu.Lock()
	gs.lPrev[taskID] = 0.01
	gs.mu.Unlock()

	_ = gotDirective // first round result doesn't affect kill-switch logic

	gs.process(context.Background(), rr)
	timeout2 := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-tap:
			if msg.Type == types.MsgFinalResult {
				return // kill-switch fired correctly
			}
		case <-timeout2:
			t.Fatal("timed out: Law 2 kill-switch did not fire after 2 consecutive worsening gradients")
		}
	}
}

func TestLaw2KillSwitch_NotFiredAfterOnlyOneWorsening(t *testing.T) {
	// Only 1 worsening does not trigger the kill-switch — PlanDirective emitted instead
	b := bus.New()
	tap := b.NewTap()
	gs := New(b, nil)

	taskID := "law2-one"
	gs.mu.Lock()
	gs.lPrev[taskID] = 0.01
	gs.mu.Unlock()

	gs.process(context.Background(), worseningReplanRequest(taskID))

	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-tap:
			if msg.Type == types.MsgPlanDirective {
				return // correct: directive emitted, not abandon
			}
			if msg.Type == types.MsgFinalResult {
				// Only acceptable if Ω ≥ 0.8 (budget exhausted), not kill-switch
				gs.mu.Lock()
				count := gs.worseningCount[taskID]
				gs.mu.Unlock()
				if count >= 2 {
					t.Error("kill-switch fired after only 1 worsening round")
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for round-1 message")
		}
	}
}

func TestLaw2KillSwitch_ResetWhenGradientImproves(t *testing.T) {
	// worseningCount resets to 0 when gradient is not worsening
	// Sequence: worsening(1) → improving(0) → worsening(1) → no kill-switch on 3rd call
	b := bus.New()
	gs := New(b, nil)

	taskID := "law2-reset"

	// Round 1: worsening → count=1
	gs.mu.Lock()
	gs.lPrev[taskID] = 0.01
	gs.mu.Unlock()
	gs.process(context.Background(), worseningReplanRequest(taskID))

	// Manually inspect: count should be 1 after worsening
	time.Sleep(50 * time.Millisecond) // let goroutine complete
	gs.mu.Lock()
	c1 := gs.worseningCount[taskID]
	gs.mu.Unlock()
	if c1 != 1 {
		t.Errorf("expected worseningCount=1 after first worsening, got %d", c1)
	}

	// Round 2: improving gradient (set lPrev high so ∇L < 0)
	gs.mu.Lock()
	gs.lPrev[taskID] = 10.0 // L will be << 10, so ∇L < 0 → improving
	gs.mu.Unlock()
	gs.process(context.Background(), worseningReplanRequest(taskID))
	time.Sleep(50 * time.Millisecond)

	gs.mu.Lock()
	c2 := gs.worseningCount[taskID]
	gs.mu.Unlock()
	if c2 != 0 {
		t.Errorf("expected worseningCount reset to 0 after improving, got %d", c2)
	}
}
