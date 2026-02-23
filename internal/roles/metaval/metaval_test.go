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
