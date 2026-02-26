package memory

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/haricheung/agentic-shell/internal/types"
)

// ---------------------------------------------------------------------------
// deriveAction tests
// ---------------------------------------------------------------------------

func TestDeriveAction_Ignore(t *testing.T) {
	// Returns "Ignore" when attention < 0.5
	if got := deriveAction(0.0, 0.9); got != "Ignore" {
		t.Errorf("expected Ignore, got %s", got)
	}
	if got := deriveAction(0.49, 1.0); got != "Ignore" {
		t.Errorf("expected Ignore at att=0.49, got %s", got)
	}
}

func TestDeriveAction_Exploit(t *testing.T) {
	// Returns "Exploit" when attention >= 0.5 and decision > 0.2
	if got := deriveAction(0.5, 0.21); got != "Exploit" {
		t.Errorf("expected Exploit, got %s", got)
	}
	if got := deriveAction(1.0, 1.0); got != "Exploit" {
		t.Errorf("expected Exploit at att=1.0, got %s", got)
	}
}

func TestDeriveAction_Avoid(t *testing.T) {
	// Returns "Avoid" when attention >= 0.5 and decision < -0.2
	if got := deriveAction(0.5, -0.21); got != "Avoid" {
		t.Errorf("expected Avoid, got %s", got)
	}
	if got := deriveAction(2.0, -1.0); got != "Avoid" {
		t.Errorf("expected Avoid at att=2.0 dec=-1.0, got %s", got)
	}
}

func TestDeriveAction_Caution(t *testing.T) {
	// Returns "Caution" when attention >= 0.5 and -0.2 <= decision <= 0.2
	if got := deriveAction(0.5, 0.0); got != "Caution" {
		t.Errorf("expected Caution at dec=0.0, got %s", got)
	}
	if got := deriveAction(1.0, 0.2); got != "Caution" {
		t.Errorf("expected Caution at dec=0.2, got %s", got)
	}
	if got := deriveAction(1.0, -0.2); got != "Caution" {
		t.Errorf("expected Caution at dec=-0.2, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// IntentSlug tests
// ---------------------------------------------------------------------------

func TestIntentSlug_Prefix(t *testing.T) {
	// Always starts with "intent:"
	result := IntentSlug("find all go files")
	if len(result) < 7 || result[:7] != "intent:" {
		t.Errorf("expected intent: prefix, got %q", result)
	}
}

func TestIntentSlug_ThreeWords(t *testing.T) {
	// Uses at most 3 words
	result := IntentSlug("find all go files in project")
	expected := "intent:find_all_go"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestIntentSlug_Lowercase(t *testing.T) {
	// Lowercases all characters
	result := IntentSlug("Find ALL Files")
	expected := "intent:find_all_files"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestIntentSlug_StripNonAlphanumeric(t *testing.T) {
	// Strips non-alphanumeric chars except underscore
	result := IntentSlug("list all .go files!")
	expected := "intent:list_all_go"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestIntentSlug_Empty(t *testing.T) {
	// Returns "intent:" for empty input
	result := IntentSlug("")
	if result != "intent:" {
		t.Errorf("expected %q, got %q", "intent:", result)
	}
}

func TestIntentSlug_FewerThanThreeWords(t *testing.T) {
	// Works correctly with fewer than 3 words
	result := IntentSlug("list files")
	expected := "intent:list_files"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// ---------------------------------------------------------------------------
// ParseToolCall tests
// ---------------------------------------------------------------------------

func TestParseToolCall_NoColon(t *testing.T) {
	// Returns ("", "") when string lacks ": "
	name, target := ParseToolCall("somestring")
	if name != "" || target != "" {
		t.Errorf("expected (\"\",\"\"), got (%q,%q)", name, target)
	}
}

func TestParseToolCall_ExtractsToolName(t *testing.T) {
	// Extracts tool name as the part before ": "
	name, _ := ParseToolCall(`shell: {"command":"ls"} → output`)
	if name != "shell" {
		t.Errorf("expected tool name 'shell', got %q", name)
	}
}

func TestParseToolCall_ExtractsQuery(t *testing.T) {
	// Returns "query" field from JSON input
	name, target := ParseToolCall(`search: {"query":"go test"} → results`)
	if name != "search" || target != "go test" {
		t.Errorf("expected (search, 'go test'), got (%q, %q)", name, target)
	}
}

func TestParseToolCall_ExtractsCommand(t *testing.T) {
	// Returns "command" field when "query" absent
	name, target := ParseToolCall(`shell: {"command":"ls -la"} → out`)
	if name != "shell" || target != "ls -la" {
		t.Errorf("expected (shell, 'ls -la'), got (%q, %q)", name, target)
	}
}

func TestParseToolCall_ExtractsPath(t *testing.T) {
	// Returns "path" field when "query" and "command" absent
	name, target := ParseToolCall(`read_file: {"path":"/tmp/foo.txt"} → content`)
	if name != "read_file" || target != "/tmp/foo.txt" {
		t.Errorf("expected (read_file, '/tmp/foo.txt'), got (%q, %q)", name, target)
	}
}

func TestParseToolCall_NoRecognizedField(t *testing.T) {
	// Returns ("toolname", "") when JSON has none of the recognized fields
	name, target := ParseToolCall(`tool: {"other":"value"}`)
	if name != "tool" || target != "" {
		t.Errorf("expected (tool,''), got (%q,%q)", name, target)
	}
}

func TestParseToolCall_MalformedJSON(t *testing.T) {
	// Returns ("toolname", "") when JSON is malformed
	name, target := ParseToolCall(`shell: not-json`)
	if name != "shell" || target != "" {
		t.Errorf("expected (shell,''), got (%q,%q)", name, target)
	}
}

func TestParseToolCall_StripOutputSnippet(t *testing.T) {
	// Handles " → output_snippet" suffix before parsing JSON
	name, target := ParseToolCall(`shell: {"command":"pwd"} → /home/user`)
	if name != "shell" || target != "pwd" {
		t.Errorf("expected (shell, 'pwd'), got (%q, %q)", name, target)
	}
}

// ---------------------------------------------------------------------------
// Integration tests using real LevelDB (temp directory)
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "megtest_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return New(nil, dir)
}

func TestWriteQueryMK_NewStoreReturnsIgnore(t *testing.T) {
	// QueryMK returns Ignore when no megrams match the tag pair
	s := newTestStore(t)
	defer s.db.Close()

	pots, err := s.QueryMK(context.Background(), "intent:test", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	if pots.Action != "Ignore" {
		t.Errorf("expected Ignore for empty store, got %q", pots.Action)
	}
	if pots.Attention != 0 || pots.Decision != 0 {
		t.Errorf("expected zero potentials, got att=%.3f dec=%.3f", pots.Attention, pots.Decision)
	}
}

func TestWriteQueryMK_Exploit(t *testing.T) {
	// QueryMK returns Exploit for freshly written positive-sigma megrams
	s := newTestStore(t)
	defer s.db.Close()

	// Write a positive-sigma megram with k=0 (timeless) so decay=1.0
	m := types.Megram{
		ID:        uuid.New().String(),
		Level:     "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space:     "intent:test_intent",
		Entity:    "env:local",
		State:     "accept",
		F:         0.9,
		Sigma:     1.0,
		K:         0.0,
	}
	s.persistMegram(m) // synchronous for testing

	pots, err := s.QueryMK(context.Background(), "intent:test_intent", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	if pots.Action != "Exploit" {
		t.Errorf("expected Exploit, got %q (att=%.3f dec=%.3f)", pots.Action, pots.Attention, pots.Decision)
	}
	if math.Abs(pots.Attention-0.9) > 1e-9 {
		t.Errorf("expected att=0.9, got %.6f", pots.Attention)
	}
	if math.Abs(pots.Decision-0.9) > 1e-9 {
		t.Errorf("expected dec=0.9, got %.6f", pots.Decision)
	}
}

func TestWriteQueryMK_Avoid(t *testing.T) {
	// QueryMK returns Avoid for a strong negative-sigma megram
	s := newTestStore(t)
	defer s.db.Close()

	m := types.Megram{
		ID:        uuid.New().String(),
		Level:     "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space:     "intent:avoid_me",
		Entity:    "env:local",
		State:     "abandon",
		F:         0.95,
		Sigma:     -1.0,
		K:         0.0,
	}
	s.persistMegram(m)

	pots, err := s.QueryMK(context.Background(), "intent:avoid_me", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	if pots.Action != "Avoid" {
		t.Errorf("expected Avoid, got %q", pots.Action)
	}
}

func TestQueryC_OnlyReturnsCLevel(t *testing.T) {
	// QueryC returns only C-level Megrams matching the tag pair
	s := newTestStore(t)
	defer s.db.Close()

	mM := types.Megram{
		ID: uuid.New().String(), Level: "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:find_files", Entity: "env:local",
		F: 0.8, Sigma: 1.0, K: 0.0, State: "accept", Content: "M-level",
	}
	mC := types.Megram{
		ID: uuid.New().String(), Level: "C",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:find_files", Entity: "env:local",
		F: 0.9, Sigma: 1.0, K: 0.0, State: "accept", Content: "C-level SOP",
	}
	s.persistMegram(mM)
	s.persistMegram(mC)

	sops, err := s.QueryC(context.Background(), "intent:find_files", "env:local")
	if err != nil {
		t.Fatalf("QueryC failed: %v", err)
	}
	if len(sops) != 1 {
		t.Fatalf("expected 1 C-level SOP, got %d", len(sops))
	}
	if sops[0].Content != "C-level SOP" {
		t.Errorf("unexpected SOP content: %q", sops[0].Content)
	}
}

func TestQueryC_UpdatesLastRecalledAt(t *testing.T) {
	// QueryC updates last_recalled_at for each returned entry
	s := newTestStore(t)
	defer s.db.Close()

	m := types.Megram{
		ID: uuid.New().String(), Level: "C",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:recall_test", Entity: "env:local",
		F: 0.9, Sigma: 1.0, K: 0.0, State: "accept",
	}
	s.persistMegram(m)

	// Before recall: no recall key
	if _, err := s.db.Get([]byte(prefixRecall+m.ID), nil); err == nil {
		t.Fatal("expected no recall key before QueryC")
	}

	_, err := s.QueryC(context.Background(), "intent:recall_test", "env:local")
	if err != nil {
		t.Fatalf("QueryC failed: %v", err)
	}
	// After recall: recall key should exist
	if _, err := s.db.Get([]byte(prefixRecall+m.ID), nil); err != nil {
		t.Errorf("expected recall key after QueryC, got error: %v", err)
	}
}

func TestGCPass_DeletesExpiredMegrams(t *testing.T) {
	// gcPass deletes M/K megrams whose decayed attention potential < 0.1
	s := newTestStore(t)
	defer s.db.Close()

	// Create a megram with k=50 and old timestamp → massive decay, att ≈ 0
	oldTime := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	m := types.Megram{
		ID:        uuid.New().String(),
		Level:     "M",
		CreatedAt: oldTime,
		Space:     "tool:shell",
		Entity:    "target:ls",
		State:     "refine",
		F:         0.1,
		Sigma:     0.5,
		K:         0.5, // fast decay; 30 days → att ≈ 0.1*exp(-15) ≈ 0
	}
	s.persistMegram(m)

	// Verify it was written
	if _, err := s.db.Get([]byte(prefixMegram+m.ID), nil); err != nil {
		t.Fatalf("megram should exist before GC: %v", err)
	}

	s.gcPass()

	// After GC, megram should be deleted
	if _, err := s.db.Get([]byte(prefixMegram+m.ID), nil); err == nil {
		t.Error("expected megram to be deleted by GC pass")
	}
}

func TestGCPass_PreservesActiveMegrams(t *testing.T) {
	// gcPass does not delete megrams with M_att >= 0.1
	s := newTestStore(t)
	defer s.db.Close()

	m := types.Megram{
		ID:        uuid.New().String(),
		Level:     "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space:     "tool:search",
		Entity:    "query:test",
		State:     "accept",
		F:         0.9,
		Sigma:     1.0,
		K:         0.0, // timeless
	}
	s.persistMegram(m)

	s.gcPass()

	if _, err := s.db.Get([]byte(prefixMegram+m.ID), nil); err != nil {
		t.Errorf("active megram should not be deleted by GC: %v", err)
	}
}

func TestWrite_FireAndForget(t *testing.T) {
	// Write() is non-blocking and enqueues megram for later persistence
	s := newTestStore(t)
	defer s.db.Close()

	m := types.Megram{
		Space: "intent:write_test", Entity: "env:local",
		State: "accept", F: 0.9, Sigma: 1.0, K: 0.0,
	}
	s.Write(m) // should not block

	// Verify ID was assigned
	if len(s.writeCh) == 0 {
		// Already drained (race), skip
		t.Skip("write queue already drained by runtime")
	}

	// Drain manually and verify persistence
	s.drainWriteQueue()
	// Check it's in DB — we don't know the ID since it was assigned in Write()
	// Just verify no error in QueryMK
	pots, err := s.QueryMK(context.Background(), "intent:write_test", "env:local")
	if err != nil {
		t.Fatalf("QueryMK after Write failed: %v", err)
	}
	if pots.Action != "Exploit" {
		t.Errorf("expected Exploit after Write+drain, got %q", pots.Action)
	}
}

func TestRecordNegativeFeedback_CancelsPositivePotential(t *testing.T) {
	// RecordNegativeFeedback appends a negative-σ Megram that cancels original positive potential
	s := newTestStore(t)
	defer s.db.Close()

	orig := types.Megram{
		ID:        uuid.New().String(),
		Level:     "C",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space:     "intent:good_sop",
		Entity:    "env:local",
		State:     "accept",
		F:         0.9,
		Sigma:     1.0,
		K:         0.0,
		Content:   "use tool X",
	}
	s.persistMegram(orig)

	// Before feedback: decision should be positive
	pots1, _ := s.QueryMK(context.Background(), "intent:good_sop", "env:local")
	if pots1.Decision <= 0 {
		t.Fatalf("expected positive decision before feedback, got %.3f", pots1.Decision)
	}

	s.RecordNegativeFeedback(context.Background(), orig.ID, "tool X caused failure")
	s.drainWriteQueue()

	// After feedback: decision should be cancelled (sum of +0.9 and -0.9 ≈ 0)
	pots2, _ := s.QueryMK(context.Background(), "intent:good_sop", "env:local")
	if math.Abs(pots2.Decision) > 0.01 {
		t.Errorf("expected near-zero decision after negative feedback, got %.3f", pots2.Decision)
	}
}

// ---------------------------------------------------------------------------
// QuantizationMatrix tests
// ---------------------------------------------------------------------------

func TestQuantizationMatrix_AllSevenStates(t *testing.T) {
	// Returns entries for all 7 GGS macro-states
	qm := QuantizationMatrix()
	for _, state := range []string{"abandon", "accept", "change_approach", "success", "break_symmetry", "change_path", "refine"} {
		if _, ok := qm[state]; !ok {
			t.Errorf("QuantizationMatrix missing state %q", state)
		}
	}
}

func TestQuantizationMatrix_AbandonValues(t *testing.T) {
	// abandon: f=0.95, sigma=-1.0, k=0.05 (PTSD trauma — highest stimulus, hard constraint)
	qm := QuantizationMatrix()
	ab := qm["abandon"]
	if math.Abs(ab.F-0.95) > 1e-9 {
		t.Errorf("abandon.F: expected 0.95, got %.3f", ab.F)
	}
	if math.Abs(ab.Sigma-(-1.0)) > 1e-9 {
		t.Errorf("abandon.Sigma: expected -1.0, got %.3f", ab.Sigma)
	}
	if math.Abs(ab.K-0.05) > 1e-9 {
		t.Errorf("abandon.K: expected 0.05, got %.3f", ab.K)
	}
}

func TestQuantizationMatrix_AcceptValues(t *testing.T) {
	// accept: f=0.90, sigma=+1.0, k=0.05 (flawless golden path)
	qm := QuantizationMatrix()
	ac := qm["accept"]
	if math.Abs(ac.F-0.90) > 1e-9 {
		t.Errorf("accept.F: expected 0.90, got %.3f", ac.F)
	}
	if math.Abs(ac.Sigma-1.0) > 1e-9 {
		t.Errorf("accept.Sigma: expected +1.0, got %.3f", ac.Sigma)
	}
	if math.Abs(ac.K-0.05) > 1e-9 {
		t.Errorf("accept.K: expected 0.05, got %.3f", ac.K)
	}
}

func TestQuantizationMatrix_RefineHasHighestK(t *testing.T) {
	// refine has k=0.5 (fastest decay — muscle memory, fast GC)
	qm := QuantizationMatrix()
	if math.Abs(qm["refine"].K-0.5) > 1e-9 {
		t.Errorf("refine.K: expected 0.5, got %.3f", qm["refine"].K)
	}
}

func TestQuantizationMatrix_ChangePathNeutralSigma(t *testing.T) {
	// change_path has sigma=0.0 — dead end; path avoided by GGS blocked_targets, not memory valence
	qm := QuantizationMatrix()
	if math.Abs(qm["change_path"].Sigma) > 1e-9 {
		t.Errorf("change_path.Sigma: expected 0.0, got %.3f", qm["change_path"].Sigma)
	}
}

// ---------------------------------------------------------------------------
// QueryMK time-decay and multi-entry tests
// ---------------------------------------------------------------------------

func TestQueryMK_DecayReducesAttentionOverTime(t *testing.T) {
	// Attention decreases for megrams with k>0 written in the past
	s := newTestStore(t)
	defer s.db.Close()

	// k=0.2, 10 days old → decay = exp(-2) ≈ 0.135; att ≈ 0.9 * 0.135 ≈ 0.122
	past := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	m := types.Megram{
		ID: uuid.New().String(), Level: "M",
		CreatedAt: past,
		Space: "intent:decay_test", Entity: "env:local",
		State: "accept", F: 0.9, Sigma: 1.0, K: 0.2,
	}
	s.persistMegram(m)

	pots, err := s.QueryMK(context.Background(), "intent:decay_test", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	expectedAtt := 0.9 * math.Exp(-0.2*10)
	if math.Abs(pots.Attention-expectedAtt) > 0.01 {
		t.Errorf("expected att≈%.4f after 10-day decay, got %.4f", expectedAtt, pots.Attention)
	}
	if pots.Attention >= 0.5 {
		t.Errorf("decayed attention should be below Ignore threshold (0.5), got %.4f", pots.Attention)
	}
}

func TestQueryMK_MultipleEntriesSumCorrectly(t *testing.T) {
	// Convolution sums contributions from multiple megrams with the same (space, entity)
	s := newTestStore(t)
	defer s.db.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	// pos: f=0.6, sigma=+1.0, k=0 → contribution: att=0.6, dec=+0.6
	// neg: f=0.4, sigma=-1.0, k=0 → contribution: att=0.4, dec=-0.4
	// sum: att=1.0, dec=0.2 → Caution (|dec|<=0.2)
	pos := types.Megram{ID: uuid.New().String(), Level: "M", CreatedAt: now,
		Space: "intent:multi_sum", Entity: "env:local", State: "accept", F: 0.6, Sigma: 1.0, K: 0.0}
	neg := types.Megram{ID: uuid.New().String(), Level: "K", CreatedAt: now,
		Space: "intent:multi_sum", Entity: "env:local", State: "change_approach", F: 0.4, Sigma: -1.0, K: 0.0}
	s.persistMegram(pos)
	s.persistMegram(neg)

	pots, err := s.QueryMK(context.Background(), "intent:multi_sum", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	if math.Abs(pots.Attention-1.0) > 1e-9 {
		t.Errorf("expected att=1.0, got %.6f", pots.Attention)
	}
	if math.Abs(pots.Decision-0.2) > 1e-9 {
		t.Errorf("expected dec=0.2, got %.6f", pots.Decision)
	}
	if pots.Action != "Caution" {
		t.Errorf("expected Caution (net dec=0.2), got %q", pots.Action)
	}
}

func TestQueryMK_RecallResetsDecayClock(t *testing.T) {
	// QueryMK uses last_recalled_at as decay origin when it is later than created_at
	s := newTestStore(t)
	defer s.db.Close()

	// Megram 7 days old, k=0.2 → without recall: att = 0.9*exp(-1.4) ≈ 0.222 → Ignore
	past := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	m := types.Megram{
		ID: uuid.New().String(), Level: "M",
		CreatedAt: past,
		Space: "intent:recall_clock", Entity: "env:local",
		State: "accept", F: 0.9, Sigma: 1.0, K: 0.2,
	}
	s.persistMegram(m)

	// Simulate a recent QueryC recall: write last_recalled_at = now
	_ = s.db.Put([]byte(prefixRecall+m.ID), []byte(time.Now().UTC().Format(time.RFC3339)), nil)

	pots, err := s.QueryMK(context.Background(), "intent:recall_clock", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	// With decay clock reset to now: att ≈ 0.9 → above Ignore threshold
	if pots.Attention < 0.85 {
		t.Errorf("recall should reset decay clock; expected att≈0.9, got %.4f", pots.Attention)
	}
	if pots.Action == "Ignore" {
		t.Errorf("recalled megram should not be Ignore, got %q (att=%.4f)", pots.Action, pots.Attention)
	}
}

// ---------------------------------------------------------------------------
// gcPass additional coverage
// ---------------------------------------------------------------------------

func TestGCPass_DeletesExpiredKLevel(t *testing.T) {
	// gcPass deletes K-level megrams whose decayed attention < Λ_gc=0.1
	s := newTestStore(t)
	defer s.db.Close()

	oldTime := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	m := types.Megram{
		ID: uuid.New().String(), Level: "K",
		CreatedAt: oldTime,
		Space: "tool:shell", Entity: "path:old",
		State: "change_path", F: 0.1, Sigma: 0.0, K: 0.5,
	}
	s.persistMegram(m)

	s.gcPass()

	if _, err := s.db.Get([]byte(prefixMegram+m.ID), nil); err == nil {
		t.Error("expected expired K-level megram to be deleted by GC")
	}
}

func TestGCPass_PreservesCLevel(t *testing.T) {
	// gcPass never deletes C-level megrams, even when attention would fall below Λ_gc
	s := newTestStore(t)
	defer s.db.Close()

	// k=0.05, 365 days old → att = 0.0001 * exp(-18.25) ≈ tiny; would be GC'd if not C
	oldTime := time.Now().UTC().Add(-365 * 24 * time.Hour).Format(time.RFC3339)
	m := types.Megram{
		ID: uuid.New().String(), Level: "C",
		CreatedAt: oldTime,
		Space: "intent:timeless_sop", Entity: "env:local",
		State: "success", F: 0.0001, Sigma: 1.0, K: 0.05,
	}
	s.persistMegram(m)

	s.gcPass()

	if _, err := s.db.Get([]byte(prefixMegram+m.ID), nil); err != nil {
		t.Errorf("C-level megram must be immune to GC (k=0 is not required): %v", err)
	}
}

// ---------------------------------------------------------------------------
// trustBankruptcyPass additional coverage
// ---------------------------------------------------------------------------

func TestTrustBankruptcyPass_SkipsMLevel(t *testing.T) {
	// trustBankruptcyPass does not modify M-level megrams
	s := newTestStore(t)
	defer s.db.Close()

	mID := uuid.New().String()
	m := types.Megram{
		ID: mID, Level: "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:skip_m", Entity: "env:local",
		State: "abandon", F: 0.95, Sigma: -1.0, K: 0.0,
	}
	s.persistMegram(m)

	s.trustBankruptcyPass()

	updated, err := s.fetchMegram(mID)
	if err != nil {
		t.Fatalf("megram should still exist: %v", err)
	}
	if updated.Level != "M" {
		t.Errorf("trust bankruptcy must not modify M-level megrams, got Level=%q", updated.Level)
	}
}

// ---------------------------------------------------------------------------
// RecordNegativeFeedback additional coverage
// ---------------------------------------------------------------------------

func TestRecordNegativeFeedback_NoopsOnUnknownID(t *testing.T) {
	// RecordNegativeFeedback silently no-ops when ruleID does not exist in DB
	s := newTestStore(t)
	defer s.db.Close()

	// Should not panic, error, or write anything
	s.RecordNegativeFeedback(context.Background(), "nonexistent-id-xyz", "bad content")
	s.drainWriteQueue()

	pots, err := s.QueryMK(context.Background(), "intent:nonexistent", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	if pots.Action != "Ignore" {
		t.Errorf("expected Ignore after no-op feedback, got %q", pots.Action)
	}
}

// ---------------------------------------------------------------------------
// Write auto-defaults tests
// ---------------------------------------------------------------------------

func TestWrite_AutoAssignsID(t *testing.T) {
	// Write auto-assigns a UUID when ID is empty
	s := newTestStore(t)
	defer s.db.Close()

	m := types.Megram{
		// ID intentionally omitted
		Space: "intent:autoid", Entity: "env:local",
		State: "accept", F: 0.8, Sigma: 1.0, K: 0.0,
	}
	s.Write(m)
	s.drainWriteQueue()

	pots, err := s.QueryMK(context.Background(), "intent:autoid", "env:local")
	if err != nil {
		t.Fatalf("QueryMK failed: %v", err)
	}
	if pots.Action == "Ignore" {
		t.Error("megram with auto-assigned ID should be queryable (got Ignore)")
	}
}

func TestWrite_AutoAssignsLevelM(t *testing.T) {
	// Write assigns Level="M" when Level field is empty
	s := newTestStore(t)
	defer s.db.Close()

	mID := uuid.New().String()
	m := types.Megram{
		ID: mID,
		// Level intentionally omitted
		Space: "intent:autolevel", Entity: "env:local",
		State: "accept", F: 0.8, Sigma: 1.0, K: 0.0,
	}
	s.Write(m)
	s.drainWriteQueue()

	stored, err := s.fetchMegram(mID)
	if err != nil {
		t.Fatalf("megram should be persisted: %v", err)
	}
	if stored.Level != "M" {
		t.Errorf("expected auto-assigned Level=M, got %q", stored.Level)
	}
}

// ---------------------------------------------------------------------------
// QueryC additional coverage
// ---------------------------------------------------------------------------

func TestQueryC_EmptyResultForNoMatch(t *testing.T) {
	// QueryC returns empty slice (not error) when no C-level entries exist for the tag pair
	s := newTestStore(t)
	defer s.db.Close()

	sops, err := s.QueryC(context.Background(), "intent:nothing_here", "env:local")
	if err != nil {
		t.Fatalf("QueryC should not error on empty result: %v", err)
	}
	if len(sops) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(sops))
	}
}

func TestQueryC_PreservesSigmaInSOPRecord(t *testing.T) {
	// QueryC copies Megram.Sigma into SOPRecord.Sigma for constraint vs. best-practice distinction
	s := newTestStore(t)
	defer s.db.Close()

	m := types.Megram{
		ID: uuid.New().String(), Level: "C",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:sigma_test", Entity: "env:local",
		State: "abandon", F: 0.95, Sigma: -1.0, K: 0.0,
		Content: "do not use this approach",
	}
	s.persistMegram(m)

	sops, err := s.QueryC(context.Background(), "intent:sigma_test", "env:local")
	if err != nil {
		t.Fatalf("QueryC failed: %v", err)
	}
	if len(sops) != 1 {
		t.Fatalf("expected 1 SOP, got %d", len(sops))
	}
	if sops[0].Sigma != -1.0 {
		t.Errorf("expected SOPRecord.Sigma=-1.0 (constraint), got %.2f", sops[0].Sigma)
	}
}

// ---------------------------------------------------------------------------
// LevelDB key safety tests
// ---------------------------------------------------------------------------

func TestSafeKeyPart_ReplacesPipes(t *testing.T) {
	// safeKeyPart replaces "|" with "_" to prevent LevelDB key ambiguity
	result := safeKeyPart("tool:some|pipe")
	if strings.Contains(result, "|") {
		t.Errorf("safeKeyPart should replace '|', got %q", result)
	}
	if result != "tool:some_pipe" {
		t.Errorf("expected 'tool:some_pipe', got %q", result)
	}
}

func TestIntentSlug_NeverContainsPipe(t *testing.T) {
	// IntentSlug output never contains "|" (safe for LevelDB key segments)
	inputs := []string{"foo|bar intent", "a|b|c", "|leading pipe", "trailing|"}
	for _, input := range inputs {
		result := IntentSlug(input)
		if strings.Contains(result, "|") {
			t.Errorf("IntentSlug(%q) = %q contains '|'", input, result)
		}
	}
}

func TestTrustBankruptcyPass_DemotesCLevel(t *testing.T) {
	// trustBankruptcyPass demotes C-level entries with live M_decision < 0.0 to K
	s := newTestStore(t)
	defer s.db.Close()

	// Two megrams same tags: one positive C-level, one stronger negative M-level
	cID := uuid.New().String()
	c := types.Megram{
		ID: cID, Level: "C",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:demote_test", Entity: "env:local",
		State: "accept", F: 0.5, Sigma: 1.0, K: 0.0,
	}
	neg := types.Megram{
		ID: uuid.New().String(), Level: "M",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Space: "intent:demote_test", Entity: "env:local",
		State: "abandon", F: 0.95, Sigma: -1.0, K: 0.0,
	}
	s.persistMegram(c)
	s.persistMegram(neg)

	// Live M_decision = 0.5*1.0 + 0.95*(-1.0) = -0.45 → should trigger demotion
	s.trustBankruptcyPass()

	// C should now be K
	m, err := s.fetchMegram(cID)
	if err != nil {
		t.Fatalf("megram should still exist after demotion: %v", err)
	}
	if m.Level != "K" {
		t.Errorf("expected Level=K after trust bankruptcy, got %q", m.Level)
	}
	if m.K != 0.05 {
		t.Errorf("expected k=0.05 after trust bankruptcy, got %.3f", m.K)
	}
}

// ---------------------------------------------------------------------------
// Summary tests
// ---------------------------------------------------------------------------

func TestSummary_EmptyStoreReturnsZeroCounts(t *testing.T) {
	// LevelCounts contains entries for all four levels with zero values when store is empty
	s := newTestStore(t)
	defer s.db.Close()
	got := s.Summary()
	for _, lvl := range []string{"M", "K", "C", "T"} {
		if got.LevelCounts[lvl] != 0 {
			t.Errorf("expected LevelCounts[%s]=0 on empty store, got %d", lvl, got.LevelCounts[lvl])
		}
	}
	if len(got.CLevel) != 0 {
		t.Errorf("expected empty CLevel on empty store, got %d entries", len(got.CLevel))
	}
}

func TestSummary_CountsMatchPersisted(t *testing.T) {
	// LevelCounts reflects Megrams actually persisted to LevelDB
	s := newTestStore(t)
	defer s.db.Close()

	s.persistMegram(types.Megram{ID: uuid.New().String(), Level: "M", Space: "a", Entity: "b", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	s.persistMegram(types.Megram{ID: uuid.New().String(), Level: "M", Space: "a", Entity: "b", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	s.persistMegram(types.Megram{ID: uuid.New().String(), Level: "K", Space: "c", Entity: "d", CreatedAt: time.Now().UTC().Format(time.RFC3339)})

	got := s.Summary()
	if got.LevelCounts["M"] != 2 {
		t.Errorf("expected M=2, got %d", got.LevelCounts["M"])
	}
	if got.LevelCounts["K"] != 1 {
		t.Errorf("expected K=1, got %d", got.LevelCounts["K"])
	}
	if got.LevelCounts["C"] != 0 {
		t.Errorf("expected C=0, got %d", got.LevelCounts["C"])
	}
}

func TestSummary_CLevelPopulatesSOPRecords(t *testing.T) {
	// CLevel contains one SOPRecord per C-level Megram with correct fields
	s := newTestStore(t)
	defer s.db.Close()

	s.persistMegram(types.Megram{
		ID: uuid.New().String(), Level: "C",
		Space: "intent:db_task", Entity: "env:local",
		Content: "Always backup first", Sigma: +1.0,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	got := s.Summary()
	if got.LevelCounts["C"] != 1 {
		t.Fatalf("expected C=1, got %d", got.LevelCounts["C"])
	}
	if len(got.CLevel) != 1 {
		t.Fatalf("expected 1 CLevel entry, got %d", len(got.CLevel))
	}
	rec := got.CLevel[0]
	if rec.Space != "intent:db_task" {
		t.Errorf("expected Space 'intent:db_task', got %q", rec.Space)
	}
	if rec.Entity != "env:local" {
		t.Errorf("expected Entity 'env:local', got %q", rec.Entity)
	}
	if rec.Content != "Always backup first" {
		t.Errorf("expected Content 'Always backup first', got %q", rec.Content)
	}
	if rec.Sigma != +1.0 {
		t.Errorf("expected Sigma=+1.0, got %.1f", rec.Sigma)
	}
}

func TestSummary_NonCLevelNotInCLevelSlice(t *testing.T) {
	// CLevel slice contains only C-level Megrams, not M or K
	s := newTestStore(t)
	defer s.db.Close()

	s.persistMegram(types.Megram{ID: uuid.New().String(), Level: "M", Space: "x", Entity: "y", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	s.persistMegram(types.Megram{ID: uuid.New().String(), Level: "K", Space: "x", Entity: "y", CreatedAt: time.Now().UTC().Format(time.RFC3339)})

	got := s.Summary()
	if len(got.CLevel) != 0 {
		t.Errorf("expected 0 CLevel entries when no C-level Megrams, got %d", len(got.CLevel))
	}
}
