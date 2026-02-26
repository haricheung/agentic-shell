package tools

import (
	"strings"
	"testing"
)

// ── SearchAvailable ──────────────────────────────────────────────────────────

func TestSearchAvailable_ReturnsTrueWhenKeySet(t *testing.T) {
	// Returns true when BING_API_KEY env var is non-empty
	t.Setenv(bingAPIKeyEnv, "test-key")
	if !SearchAvailable() {
		t.Error("expected SearchAvailable()=true when BING_API_KEY is set")
	}
}

func TestSearchAvailable_ReturnsFalseWhenKeyMissing(t *testing.T) {
	// Returns false when BING_API_KEY is not set or empty
	t.Setenv(bingAPIKeyEnv, "")
	if SearchAvailable() {
		t.Error("expected SearchAvailable()=false when BING_API_KEY is empty")
	}
}

// ── parseBingResults ─────────────────────────────────────────────────────────

func TestParseBingResults_EmptyValueReturnsEmptySlice(t *testing.T) {
	// Returns empty slice when webPages.value is absent or empty
	data := []byte(`{"webPages":{"value":[]}}`)
	pages, err := parseBingResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("expected 0 results, got %d", len(pages))
	}
}

func TestParseBingResults_MissingWebPagesReturnsEmptySlice(t *testing.T) {
	// Returns empty slice when webPages.value is absent or empty
	data := []byte(`{}`)
	pages, err := parseBingResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("expected 0 results for missing webPages, got %d", len(pages))
	}
}

func TestParseBingResults_MalformedJSONReturnsError(t *testing.T) {
	// Returns error on malformed JSON
	_, err := parseBingResults([]byte(`{not valid json`))
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestParseBingResults_MapsNameURLSnippet(t *testing.T) {
	// Maps name → Name, url → URL, snippet → Snippet for each result
	data := []byte(`{"webPages":{"value":[
		{"name":"Go Language","url":"https://go.dev","snippet":"An open source language."}
	]}}`)
	pages, err := parseBingResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].Name != "Go Language" {
		t.Errorf("expected Name 'Go Language', got %q", pages[0].Name)
	}
	if pages[0].URL != "https://go.dev" {
		t.Errorf("expected URL 'https://go.dev', got %q", pages[0].URL)
	}
	if pages[0].Snippet != "An open source language." {
		t.Errorf("expected Snippet 'An open source language.', got %q", pages[0].Snippet)
	}
}

func TestParseBingResults_MultipleResultsPreservesOrder(t *testing.T) {
	// Maps name → Name, url → URL, snippet → Snippet for each result
	data := []byte(`{"webPages":{"value":[
		{"name":"First","url":"https://a.com","snippet":""},
		{"name":"Second","url":"https://b.com","snippet":""},
		{"name":"Third","url":"https://c.com","snippet":""}
	]}}`)
	pages, err := parseBingResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("expected 3 results, got %d", len(pages))
	}
	if pages[0].Name != "First" || pages[1].Name != "Second" || pages[2].Name != "Third" {
		t.Errorf("results out of order: %v", pages)
	}
}

// ── formatSearchResult ───────────────────────────────────────────────────────

func TestFormatSearchResult_EmptyPagesReturnsNoResults(t *testing.T) {
	// Returns "(no results)" message when pages slice is empty
	got := formatSearchResult("test query", nil)
	if !strings.Contains(got, "No results") {
		t.Errorf("expected 'No results' message, got %q", got)
	}
}

func TestFormatSearchResult_IncludesTitleSnippetURL(t *testing.T) {
	// Includes title, snippet, and URL for each result
	pages := []searchPage{
		{Name: "Example Title", Snippet: "An example snippet.", URL: "https://example.com"},
	}
	got := formatSearchResult("query", pages)
	if !strings.Contains(got, "Example Title") {
		t.Errorf("expected title in output, got %q", got)
	}
	if !strings.Contains(got, "An example snippet.") {
		t.Errorf("expected snippet in output, got %q", got)
	}
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("expected URL in output, got %q", got)
	}
}

func TestFormatSearchResult_OmitsSnippetLineWhenEmpty(t *testing.T) {
	// Omits snippet line when snippet is empty
	pages := []searchPage{
		{Name: "Title", Snippet: "", URL: "https://example.com"},
	}
	got := formatSearchResult("query", pages)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (title + URL), got %d: %q", len(lines), got)
	}
}

func TestFormatSearchResult_SeparatesResultsWithBlankLine(t *testing.T) {
	// Separates results with a blank line
	pages := []searchPage{
		{Name: "First", Snippet: "s1", URL: "https://a.com"},
		{Name: "Second", Snippet: "s2", URL: "https://b.com"},
	}
	got := formatSearchResult("query", pages)
	if !strings.Contains(got, "\n\n") {
		t.Errorf("expected blank line between results, got %q", got)
	}
}

func TestFormatSearchResult_CapsAtMaxResults(t *testing.T) {
	// Caps output at searchMaxResults results
	pages := make([]searchPage, searchMaxResults+3)
	for i := range pages {
		pages[i] = searchPage{Name: "Title", URL: "https://a.com"}
	}
	got := formatSearchResult("query", pages)
	count := strings.Count(got, "https://a.com")
	if count != searchMaxResults {
		t.Errorf("expected %d results, got %d", searchMaxResults, count)
	}
}
