package metaval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haricheung/agentic-shell/internal/bus"
	"github.com/haricheung/agentic-shell/internal/llm"
	"github.com/haricheung/agentic-shell/internal/tasklog"
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

// ── extractJSON ───────────────────────────────────────────────────────────────

func TestExtractJSON_PassesThroughPureJSON(t *testing.T) {
	// Returns s unchanged when s contains only a JSON object
	s := `{"verdict":"accept","summary":"ok"}`
	if got := extractJSON(s); got != s {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestExtractJSON_ExtractsFromLeadingProse(t *testing.T) {
	// Returns the first complete {...} object, stripping leading prose
	s := `Here is the result: {"verdict":"accept","summary":"ok"}`
	want := `{"verdict":"accept","summary":"ok"}`
	if got := extractJSON(s); got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractJSON_ExtractsFromTrailingProse(t *testing.T) {
	// Returns the first complete {...} object, stripping trailing prose
	s := `{"verdict":"accept","summary":"ok"} Let me know if you need more.`
	want := `{"verdict":"accept","summary":"ok"}`
	if got := extractJSON(s); got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractJSON_HandlesNestedBraces(t *testing.T) {
	// Handles nested braces correctly — stops at the matching outer '}'
	s := `{"a":{"b":1},"c":2}`
	if got := extractJSON(s); got != s {
		t.Errorf("expected unchanged for nested braces, got %q", got)
	}
}

func TestExtractJSON_ReturnsUnchangedWhenNoBrace(t *testing.T) {
	// Returns s unchanged when s contains no '{'
	s := `no json here`
	if got := extractJSON(s); got != s {
		t.Errorf("expected unchanged when no brace, got %q", got)
	}
}

// ── evaluate — replan on parse error ─────────────────────────────────────────

// mockLLMResponse builds a minimal OpenAI-compatible chat completion JSON
// whose content field is the provided body string.
func mockLLMResponse(body string) string {
	escaped, _ := json.Marshal(body)
	return `{"choices":[{"message":{"role":"assistant","content":` + string(escaped) + `}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
}

func TestEvaluate_TriggerReplanOnParseError(t *testing.T) {
	// When the LLM returns invalid JSON, evaluate() must call triggerReplan
	// and publish a MsgReplanRequest instead of returning silently.
	badJSON := `{"verdict":"accept","summary":"ok","merged_output":"text with "unescaped" quotes"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockLLMResponse(badJSON)))
	}))
	defer ts.Close()

	t.Setenv("OPENAI_BASE_URL", ts.URL)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_MODEL", "test-model")

	b := bus.New()
	replanCh := b.Subscribe(types.MsgReplanRequest)
	logReg := tasklog.NewRegistry("")
	logReg.Open("test-task", "test intent")

	mv := New(b, llm.New(), nil, logReg)
	mv.mu.Lock()
	mv.replanCounts["test-task"] = 0
	mv.taskStart["test-task"] = time.Now()
	mv.mu.Unlock()

	tracker := &manifestTracker{
		manifest: types.DispatchManifest{
			TaskID:       "test-task",
			SubTaskIDs:   []string{"s1"},
			TaskCriteria: []string{"output is correct"},
		},
		spec:          types.TaskSpec{Intent: "test intent"},
		outcomes:      []types.SubTaskOutcome{{SubTaskID: "s1", Status: "matched"}},
		expectedCount: 1,
	}

	mv.evaluate(context.Background(), tracker)

	select {
	case msg := <-replanCh:
		var rr types.ReplanRequest
		b2, _ := json.Marshal(msg.Payload)
		json.Unmarshal(b2, &rr)
		if !strings.Contains(rr.GapSummary, "parse error") {
			t.Errorf("expected 'parse error' in gap_summary, got %q", rr.GapSummary)
		}
	case <-time.After(3 * time.Second):
		t.Error("expected MsgReplanRequest but got none — evaluate() likely returned silently")
	}
}
