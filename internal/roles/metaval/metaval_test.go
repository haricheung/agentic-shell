package metaval

import (
	"testing"

	"github.com/haricheung/agentic-shell/internal/types"
)

// computeGapTrend was removed in v0.7 — gradient computation now lives in GGS (R7).
// The equivalent tests are in internal/roles/ggs/ggs_test.go (TestComputeGradient_*).

// ── aggregateFailureClassFromOutcomes ─────────────────────────────────────────

func TestAggregateFailureClassFromOutcomes_EmptyReturnsEmpty(t *testing.T) {
	// Returns "" when no failed outcomes or no classified criteria
	got := aggregateFailureClassFromOutcomes(nil)
	if got != "" {
		t.Errorf("expected empty string for nil outcomes, got %q", got)
	}
}

func TestAggregateFailureClassFromOutcomes_AllLogicalReturnsLogical(t *testing.T) {
	// Returns "logical" when logical count > environmental count
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "c1", Verdict: "fail", FailureClass: "logical"},
				{Criterion: "c2", Verdict: "fail", FailureClass: "logical"},
			},
		},
	}
	got := aggregateFailureClassFromOutcomes(outcomes)
	if got != "logical" {
		t.Errorf("expected logical, got %q", got)
	}
}

func TestAggregateFailureClassFromOutcomes_AllEnvironmentalReturnsEnvironmental(t *testing.T) {
	// Returns "environmental" when environmental count > logical count
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "c1", Verdict: "fail", FailureClass: "environmental"},
			},
		},
	}
	got := aggregateFailureClassFromOutcomes(outcomes)
	if got != "environmental" {
		t.Errorf("expected environmental, got %q", got)
	}
}

func TestAggregateFailureClassFromOutcomes_MixedReturnsMixed(t *testing.T) {
	// Returns "mixed" when tied and non-zero
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "c1", Verdict: "fail", FailureClass: "logical"},
				{Criterion: "c2", Verdict: "fail", FailureClass: "environmental"},
			},
		},
	}
	got := aggregateFailureClassFromOutcomes(outcomes)
	if got != "mixed" {
		t.Errorf("expected mixed, got %q", got)
	}
}

func TestAggregateFailureClassFromOutcomes_IgnoresMatchedOutcomes(t *testing.T) {
	// Ignores matched outcomes — only failed outcomes contribute
	outcomes := []types.SubTaskOutcome{
		{
			Status: "matched",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "c1", Verdict: "fail", FailureClass: "logical"},
			},
		},
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "c2", Verdict: "fail", FailureClass: "environmental"},
			},
		},
	}
	got := aggregateFailureClassFromOutcomes(outcomes)
	if got != "environmental" {
		t.Errorf("expected environmental (matched outcome ignored), got %q", got)
	}
}

func TestAggregateFailureClassFromOutcomes_NoClassifiedCriteriaReturnsEmpty(t *testing.T) {
	// Returns "" when failed outcomes exist but no criteria have a failure_class
	outcomes := []types.SubTaskOutcome{
		{
			Status: "failed",
			CriteriaVerdicts: []types.CriteriaVerdict{
				{Criterion: "c1", Verdict: "fail", FailureClass: ""},
			},
		},
	}
	got := aggregateFailureClassFromOutcomes(outcomes)
	if got != "" {
		t.Errorf("expected empty string when no criteria are classified, got %q", got)
	}
}

// ── safetyNetLoss ──────────────────────────────────────────────────────────────

func TestSafetyNetLoss_DEqualsOneWhenEmpty(t *testing.T) {
	// Returns D = 1.0 when outcomes slice is empty (failure is the invariant)
	loss := safetyNetLoss(nil)
	if loss.D != 1.0 {
		t.Errorf("expected D=1.0 for nil outcomes, got %v", loss.D)
	}
}

func TestSafetyNetLoss_DGreaterThanZeroWhenOneFailed(t *testing.T) {
	// Returns D > 0 when at least one outcome is failed
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
		{Status: "failed"},
	}
	loss := safetyNetLoss(outcomes)
	if loss.D <= 0 {
		t.Errorf("expected D > 0 with one failed outcome, got %v", loss.D)
	}
}

func TestSafetyNetLoss_DEqualsHalfWhenHalfFailed(t *testing.T) {
	// Returns D = 0.5 when exactly half the outcomes are failed
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
		{Status: "failed"},
	}
	loss := safetyNetLoss(outcomes)
	if loss.D != 0.5 {
		t.Errorf("expected D=0.5, got %v", loss.D)
	}
}

func TestSafetyNetLoss_DEqualsOneWhenAllFailed(t *testing.T) {
	// Returns D = 1.0 when all outcomes are failed
	outcomes := []types.SubTaskOutcome{
		{Status: "failed"},
		{Status: "failed"},
	}
	loss := safetyNetLoss(outcomes)
	if loss.D != 1.0 {
		t.Errorf("expected D=1.0, got %v", loss.D)
	}
}

func TestSafetyNetLoss_DEqualsOneFallbackWhenAllMatched(t *testing.T) {
	// Returns D = 1.0 (fallback) when all outcomes are matched but we still abandoned
	outcomes := []types.SubTaskOutcome{
		{Status: "matched"},
	}
	loss := safetyNetLoss(outcomes)
	if loss.D != 1.0 {
		t.Errorf("expected D=1.0 fallback when no failed outcomes, got %v", loss.D)
	}
}
