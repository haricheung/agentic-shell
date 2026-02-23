package agentval

import (
	"testing"

	"github.com/haricheung/agentic-shell/internal/types"
)

// ── classifyEnvironmental ────────────────────────────────────────────────────

func TestClassifyEnvironmental_PermissionDeniedInEvidence(t *testing.T) {
	// Returns true when evidence contains "permission denied"
	if !classifyEnvironmental("permission denied: /etc/shadow", nil) {
		t.Error("expected true for evidence containing 'permission denied'")
	}
}

func TestClassifyEnvironmental_LAW1InEvidence(t *testing.T) {
	// Returns true when evidence contains "[LAW1]"
	if !classifyEnvironmental("[LAW1] rm deletes files permanently — command blocked", nil) {
		t.Error("expected true for evidence containing '[LAW1]'")
	}
}

func TestClassifyEnvironmental_NoSuchFileInEvidence(t *testing.T) {
	// Returns true when evidence contains "no such file"
	if !classifyEnvironmental("open /tmp/missing: no such file or directory", nil) {
		t.Error("expected true for evidence containing 'no such file'")
	}
}

func TestClassifyEnvironmental_ConnectionRefusedInToolCall(t *testing.T) {
	// Returns true when a tool call output contains "connection refused"
	toolCalls := []string{"shell:curl http://internal → connection refused"}
	if !classifyEnvironmental("", toolCalls) {
		t.Error("expected true when tool call contains 'connection refused'")
	}
}

func TestClassifyEnvironmental_FalseForPureLogicFailure(t *testing.T) {
	// Returns false for a pure logic failure with no error keywords
	if classifyEnvironmental("the algorithm returned unexpected results: got 42, want 0", nil) {
		t.Error("expected false for pure logic failure with no environmental keywords")
	}
}

// ── aggregateFailureClass ────────────────────────────────────────────────────

func TestAggregateFailureClass_EmptyReturnsEmpty(t *testing.T) {
	// Returns "" when no criterionResults or all are Met=true
	got := aggregateFailureClass(nil)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestAggregateFailureClass_AllMetReturnsEmpty(t *testing.T) {
	// Returns "" when all criterionResults are Met=true
	crs := []criterionResult{
		{Criterion: "a", Met: true, FailureClass: "logical"},
	}
	got := aggregateFailureClass(crs)
	if got != "" {
		t.Errorf("expected empty when all met, got %q", got)
	}
}

func TestAggregateFailureClass_AllLogicalReturnsLogical(t *testing.T) {
	// Returns "logical" when all failed criteria have failure_class=="logical"
	crs := []criterionResult{
		{Criterion: "a", Met: false, FailureClass: "logical"},
		{Criterion: "b", Met: false, FailureClass: "logical"},
	}
	got := aggregateFailureClass(crs)
	if got != "logical" {
		t.Errorf("expected logical, got %q", got)
	}
}

func TestAggregateFailureClass_AllEnvironmentalReturnsEnvironmental(t *testing.T) {
	// Returns "environmental" when all failed criteria have failure_class=="environmental"
	crs := []criterionResult{
		{Criterion: "a", Met: false, FailureClass: "environmental"},
	}
	got := aggregateFailureClass(crs)
	if got != "environmental" {
		t.Errorf("expected environmental, got %q", got)
	}
}

func TestAggregateFailureClass_MixedReturnsMixed(t *testing.T) {
	// Returns "mixed" when both classes are present
	crs := []criterionResult{
		{Criterion: "a", Met: false, FailureClass: "logical"},
		{Criterion: "b", Met: false, FailureClass: "environmental"},
	}
	got := aggregateFailureClass(crs)
	if got != "mixed" {
		t.Errorf("expected mixed, got %q", got)
	}
}

// ── toCriteriaVerdicts ───────────────────────────────────────────────────────

func TestToCriteriaVerdicts_NilInputReturnsNil(t *testing.T) {
	// Returns nil when input is nil or empty
	got := toCriteriaVerdicts(nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestToCriteriaVerdicts_EmptyInputReturnsNil(t *testing.T) {
	// Returns nil when input is empty slice
	got := toCriteriaVerdicts([]criterionResult{})
	if got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

func TestToCriteriaVerdicts_VerdictIsPassWhenMetTrue(t *testing.T) {
	// Verdict is "pass" when Met=true
	crs := []criterionResult{{Criterion: "c1", Met: true, Evidence: "ok"}}
	got := toCriteriaVerdicts(crs)
	if len(got) != 1 || got[0].Verdict != "pass" {
		t.Errorf("expected verdict=pass, got %v", got)
	}
}

func TestToCriteriaVerdicts_VerdictIsFailWhenMetFalse(t *testing.T) {
	// Verdict is "fail" when Met=false
	crs := []criterionResult{{Criterion: "c1", Met: false, FailureClass: "logical", Evidence: "bad"}}
	got := toCriteriaVerdicts(crs)
	if len(got) != 1 || got[0].Verdict != "fail" {
		t.Errorf("expected verdict=fail, got %v", got)
	}
}

func TestToCriteriaVerdicts_FailureClassAndEvidenceForwarded(t *testing.T) {
	// FailureClass and Evidence are forwarded as-is
	crs := []criterionResult{
		{Criterion: "c1", Met: false, FailureClass: "environmental", Evidence: "timeout"},
	}
	got := toCriteriaVerdicts(crs)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	cv := got[0]
	if cv.FailureClass != "environmental" {
		t.Errorf("expected FailureClass=environmental, got %q", cv.FailureClass)
	}
	if cv.Evidence != "timeout" {
		t.Errorf("expected Evidence=timeout, got %q", cv.Evidence)
	}
	if cv.Criterion != "c1" {
		t.Errorf("expected Criterion=c1, got %q", cv.Criterion)
	}
}

func TestToCriteriaVerdicts_PreservesOrderAndCount(t *testing.T) {
	// Output length matches input length; order preserved
	crs := []criterionResult{
		{Criterion: "a", Met: true},
		{Criterion: "b", Met: false, FailureClass: "logical"},
		{Criterion: "c", Met: true},
	}
	got := toCriteriaVerdicts(crs)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	expected := []types.CriteriaVerdict{
		{Criterion: "a", Verdict: "pass"},
		{Criterion: "b", Verdict: "fail", FailureClass: "logical"},
		{Criterion: "c", Verdict: "pass"},
	}
	for i, e := range expected {
		if got[i].Criterion != e.Criterion || got[i].Verdict != e.Verdict || got[i].FailureClass != e.FailureClass {
			t.Errorf("[%d] expected %+v, got %+v", i, e, got[i])
		}
	}
}
