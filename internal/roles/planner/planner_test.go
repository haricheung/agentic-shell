package planner

import (
	"strings"
	"testing"

	"github.com/haricheung/agentic-shell/internal/types"
)

// --- calibrateMKCT ---

func TestCalibrateMKCT_EmptyReturnsEmpty(t *testing.T) {
	// Returns "" when sops is empty and pots.Action is "Ignore"
	got := calibrateMKCT(nil, types.Potentials{Action: "Ignore"})
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestCalibrateMKCT_ExploitIncludesShouldPrefer(t *testing.T) {
	// Includes "SHOULD PREFER" block when pots.Action is Exploit
	got := calibrateMKCT(nil, types.Potentials{Action: "Exploit"})
	if !strings.Contains(got, "SHOULD PREFER") {
		t.Errorf("expected SHOULD PREFER for Exploit action, got %q", got)
	}
}

func TestCalibrateMKCT_AvoidIncludesMustNot(t *testing.T) {
	// Includes "MUST NOT" block when pots.Action is Avoid
	got := calibrateMKCT(nil, types.Potentials{Action: "Avoid"})
	if !strings.Contains(got, "MUST NOT") {
		t.Errorf("expected MUST NOT for Avoid action, got %q", got)
	}
}

func TestCalibrateMKCT_CautionIncludesCaution(t *testing.T) {
	// Includes "CAUTION" block when pots.Action is Caution
	got := calibrateMKCT(nil, types.Potentials{Action: "Caution"})
	if !strings.Contains(got, "CAUTION") {
		t.Errorf("expected CAUTION for Caution action, got %q", got)
	}
}

func TestCalibrateMKCT_PositiveSigmaUnderShouldPrefer(t *testing.T) {
	// Positive-σ SOPs appear under "SHOULD PREFER (proven best practices)"
	sops := []types.SOPRecord{{ID: "1", Content: "use mdfind for file search", Sigma: 1.0}}
	got := calibrateMKCT(sops, types.Potentials{Action: "Ignore"})
	if !strings.Contains(got, "SHOULD PREFER") {
		t.Errorf("expected SHOULD PREFER section for positive-sigma SOP, got %q", got)
	}
	if !strings.Contains(got, "use mdfind for file search") {
		t.Errorf("expected SOP content in output, got %q", got)
	}
}

func TestCalibrateMKCT_NegativeSigmaUnderMustNot(t *testing.T) {
	// Non-positive-σ SOPs appear under "MUST NOT (proven constraints)"
	sops := []types.SOPRecord{{ID: "2", Content: "never use shell find on /", Sigma: -1.0}}
	got := calibrateMKCT(sops, types.Potentials{Action: "Ignore"})
	if !strings.Contains(got, "MUST NOT") {
		t.Errorf("expected MUST NOT section for negative-sigma SOP, got %q", got)
	}
	if !strings.Contains(got, "never use shell find") {
		t.Errorf("expected SOP content in MUST NOT block, got %q", got)
	}
}

// --- calibrate ---

func TestCalibrate_EmptyEntries(t *testing.T) {
	// Returns "" when entries is empty
	if got := calibrate(nil, "find a file"); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestCalibrate_SortsNewestFirst(t *testing.T) {
	// Sorts entries newest-first before applying cap (most recent lessons take priority)
	entries := []types.MemoryEntry{
		{Type: "procedural", Timestamp: "2026-01-01T00:00:00Z", Tags: []string{"file"}, Content: "old find approach"},
		{Type: "procedural", Timestamp: "2026-02-01T00:00:00Z", Tags: []string{"file"}, Content: "new find approach"},
	}
	got := calibrate(entries, "find file")
	newIdx := strings.Index(got, "new")
	oldIdx := strings.Index(got, "old")
	if newIdx == -1 || oldIdx == -1 {
		t.Fatalf("expected both entries in output, got %q", got)
	}
	if newIdx > oldIdx {
		t.Errorf("expected newer entry to appear before older entry in output")
	}
}

func TestCalibrate_CapsAtMax(t *testing.T) {
	// Caps to maxMemoryEntries; entries beyond the cap are silently dropped
	entries := make([]types.MemoryEntry, maxMemoryEntries+5)
	for i := range entries {
		entries[i] = types.MemoryEntry{
			Type:      "procedural",
			Timestamp: "2026-01-01T00:00:00Z",
			Tags:      []string{"file"},
			Content:   "approach",
		}
	}
	got := calibrate(entries, "find file")
	// Each entry contributes one "  - " line; count them
	count := strings.Count(got, "\n  - ")
	if count > maxMemoryEntries {
		t.Errorf("expected at most %d entries in output, got %d", maxMemoryEntries, count)
	}
}

func TestCalibrate_FiltersZeroOverlap(t *testing.T) {
	// Drops entries with zero keyword overlap against intent
	entries := []types.MemoryEntry{
		{Type: "procedural", Timestamp: "2026-01-01T00:00:00Z", Tags: []string{"music"}, Content: "music approach"},
	}
	got := calibrate(entries, "send email to boss")
	if got != "" {
		t.Errorf("expected empty string for zero-overlap entry, got %q", got)
	}
}

func TestCalibrate_EmptyWhenAllFiltered(t *testing.T) {
	// Returns "" when all entries are filtered by keyword
	entries := []types.MemoryEntry{
		{Type: "procedural", Timestamp: "2026-01-01T00:00:00Z", Tags: []string{"xyz"}, Content: "xyz"},
	}
	if got := calibrate(entries, "abc def"); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestCalibrate_ProceduralUnderMustNot(t *testing.T) {
	// Procedural entries appear under "MUST NOT" heading
	entries := []types.MemoryEntry{
		{Type: "procedural", Timestamp: "2026-01-01T00:00:00Z", Tags: []string{"file"}, Content: "used shell find"},
	}
	got := calibrate(entries, "find the file")
	if !strings.Contains(got, "MUST NOT") {
		t.Errorf("expected MUST NOT heading for procedural entry, got %q", got)
	}
	if strings.Contains(got, "SHOULD PREFER") {
		t.Errorf("unexpected SHOULD PREFER for procedural-only entries")
	}
}

func TestCalibrate_EpisodicUnderShouldPrefer(t *testing.T) {
	// Episodic entries appear under "SHOULD PREFER" heading
	entries := []types.MemoryEntry{
		{Type: "episodic", Timestamp: "2026-01-01T00:00:00Z", Tags: []string{"file"}, Content: "used mdfind"},
	}
	got := calibrate(entries, "find the file")
	if !strings.Contains(got, "SHOULD PREFER") {
		t.Errorf("expected SHOULD PREFER heading for episodic entry, got %q", got)
	}
	if strings.Contains(got, "MUST NOT") {
		t.Errorf("unexpected MUST NOT for episodic-only entries")
	}
}

// --- entrySummary ---

func TestEntrySummary_TruncatesLongContent(t *testing.T) {
	// Truncates content JSON at 400 chars, appending "…" when trimmed
	long := strings.Repeat("x", 500)
	e := types.MemoryEntry{Content: long}
	got := entrySummary(e)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected trailing ellipsis for long content, got %q", got)
	}
	if len([]rune(got)) > 405 { // 400 content + "…" + small overhead
		t.Errorf("summary too long: %d runes", len([]rune(got)))
	}
}

func TestEntrySummary_PrependsTags(t *testing.T) {
	// Prepends "[tags: t1, t2] " when tags are present
	e := types.MemoryEntry{Tags: []string{"file", "search"}, Content: "used mdfind"}
	got := entrySummary(e)
	if !strings.HasPrefix(got, "[tags: file, search]") {
		t.Errorf("expected tag prefix, got %q", got)
	}
}

func TestEntrySummary_NoTagsNoPrefx(t *testing.T) {
	// Returns raw content JSON with no prefix when tags are empty
	e := types.MemoryEntry{Content: "used mdfind"}
	got := entrySummary(e)
	if strings.HasPrefix(got, "[tags:") {
		t.Errorf("unexpected tag prefix for entry with no tags, got %q", got)
	}
}

// --- memTokenize ---

func TestMemTokenize_DropsShortWords(t *testing.T) {
	// Returns only words with len >= 3 (short noise words are discarded)
	got := memTokenize("a is to find the file")
	for _, w := range got {
		if len(w) < 3 {
			t.Errorf("expected only words len>=3, got %q", w)
		}
	}
}

func TestMemTokenize_Lowercases(t *testing.T) {
	// All returned words are lowercase
	got := memTokenize("Find The FILE")
	for _, w := range got {
		if w != strings.ToLower(w) {
			t.Errorf("expected lowercase word, got %q", w)
		}
	}
}

func TestMemTokenize_EmptyInput(t *testing.T) {
	// Returns nil for empty or whitespace-only input
	if got := memTokenize(""); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
	if got := memTokenize("   "); got != nil {
		t.Errorf("expected nil for whitespace input, got %v", got)
	}
}
