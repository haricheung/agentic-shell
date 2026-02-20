package metaval

import "testing"

// --- computeGapTrend ---

func TestComputeGapTrend_FirstRoundAlwaysStable(t *testing.T) {
	// Returns "stable" when replanCount == 0 regardless of failed counts
	if got := computeGapTrend(5, 0, 0); got != "stable" {
		t.Errorf("expected stable on first round, got %q", got)
	}
}

func TestComputeGapTrend_FewerFailuresIsImproving(t *testing.T) {
	// Returns "improving" when currentFailed < prevFailed
	if got := computeGapTrend(1, 3, 1); got != "improving" {
		t.Errorf("expected improving, got %q", got)
	}
}

func TestComputeGapTrend_MoreFailuresIsWorsening(t *testing.T) {
	// Returns "worsening" when currentFailed > prevFailed
	if got := computeGapTrend(4, 2, 1); got != "worsening" {
		t.Errorf("expected worsening, got %q", got)
	}
}

func TestComputeGapTrend_SameFailuresIsStable(t *testing.T) {
	// Returns "stable" when currentFailed == prevFailed (and replanCount > 0)
	if got := computeGapTrend(2, 2, 2); got != "stable" {
		t.Errorf("expected stable, got %q", got)
	}
}

func TestComputeGapTrend_ZeroFailedBothSidesIsStable(t *testing.T) {
	// Returns "stable" when both current and prev are zero
	if got := computeGapTrend(0, 0, 1); got != "stable" {
		t.Errorf("expected stable, got %q", got)
	}
}
