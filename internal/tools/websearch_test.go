package tools

import (
	"strings"
	"testing"
)

// ── SearchAvailable ──────────────────────────────────────────────────────────

func TestSearchAvailable_AlwaysReturnsTrue(t *testing.T) {
	// Always returns true (no API key required for DDG)
	if !SearchAvailable() {
		t.Error("expected SearchAvailable()=true")
	}
}

// ── parseDDGResults ──────────────────────────────────────────────────────────

func TestParseDDGResults_EmptyBodyReturnsEmptySlice(t *testing.T) {
	// Returns empty slice when body contains no result elements
	pages := parseDDGResults("")
	if len(pages) != 0 {
		t.Errorf("expected 0 results for empty body, got %d", len(pages))
	}
}

func TestParseDDGResults_SkipsAdsAndPairedSnippets(t *testing.T) {
	// Skips ads (href containing "duckduckgo.com/y.js") and their paired snippets
	body := `
		<a rel="nofollow" class="result__a" href="https://duckduckgo.com/y.js?ad_domain=udemy.com&amp;stuff">Ad Title</a>
		<a class="result__snippet" href="https://duckduckgo.com/y.js?stuff">Ad snippet.</a>
		<a rel="nofollow" class="result__a" href="https://example.com/real">Real Title</a>
		<a class="result__snippet" href="https://example.com/real">Real snippet.</a>
	`
	pages := parseDDGResults(body)
	if len(pages) != 1 {
		t.Fatalf("expected 1 organic result (ad filtered), got %d", len(pages))
	}
	if pages[0].Name != "Real Title" {
		t.Errorf("expected 'Real Title', got %q", pages[0].Name)
	}
}

func TestParseDDGResults_ExtractsTitleStrippingHTMLTags(t *testing.T) {
	// Extracts title text from result__a anchors, stripping inline HTML tags
	body := `<a rel="nofollow" class="result__a" href="https://example.com">Learn <b>Golang</b> Fast</a>
		<a class="result__snippet" href="https://example.com">snippet</a>`
	pages := parseDDGResults(body)
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].Name != "Learn Golang Fast" {
		t.Errorf("expected 'Learn Golang Fast', got %q", pages[0].Name)
	}
}

func TestParseDDGResults_ExtractsURLFromHref(t *testing.T) {
	// Extracts URL from result__a href attribute
	body := `<a rel="nofollow" class="result__a" href="https://example.com/page">Title</a>
		<a class="result__snippet" href="https://example.com/page">snippet</a>`
	pages := parseDDGResults(body)
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].URL != "https://example.com/page" {
		t.Errorf("expected URL 'https://example.com/page', got %q", pages[0].URL)
	}
}

func TestParseDDGResults_ExtractsSnippetStrippingHTMLTags(t *testing.T) {
	// Extracts snippet text from result__snippet anchors, stripping inline HTML tags
	body := `<a rel="nofollow" class="result__a" href="https://example.com">Title</a>
		<a class="result__snippet" href="https://example.com">Some <b>bold</b> snippet text.</a>`
	pages := parseDDGResults(body)
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].Snippet != "Some bold snippet text." {
		t.Errorf("expected stripped snippet, got %q", pages[0].Snippet)
	}
}

func TestParseDDGResults_UnescapesHTMLEntities(t *testing.T) {
	// Unescapes HTML entities in title and snippet (e.g. &amp; → &)
	body := `<a rel="nofollow" class="result__a" href="https://example.com">Tom &amp; Jerry</a>
		<a class="result__snippet" href="https://example.com">Q&amp;A session</a>`
	pages := parseDDGResults(body)
	if len(pages) != 1 {
		t.Fatalf("expected 1 result, got %d", len(pages))
	}
	if pages[0].Name != "Tom & Jerry" {
		t.Errorf("expected unescaped title 'Tom & Jerry', got %q", pages[0].Name)
	}
	if pages[0].Snippet != "Q&A session" {
		t.Errorf("expected unescaped snippet 'Q&A session', got %q", pages[0].Snippet)
	}
}

// ── stripHTMLTags ────────────────────────────────────────────────────────────

func TestStripHTMLTags_RemovesBoldTags(t *testing.T) {
	// Removes <b> and </b> tags, preserving inner text
	got := stripHTMLTags("Learn <b>Golang</b> <b>web</b> scraping")
	if got != "Learn Golang web scraping" {
		t.Errorf("expected 'Learn Golang web scraping', got %q", got)
	}
}

func TestStripHTMLTags_RemovesArbitraryTags(t *testing.T) {
	// Removes arbitrary tags (e.g. <span>, <a>)
	got := stripHTMLTags(`<span class="hl">hello</span> <a href="x">world</a>`)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestStripHTMLTags_NoTagsPassthrough(t *testing.T) {
	// Returns input unchanged when no tags are present
	input := "plain text"
	if got := stripHTMLTags(input); got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestStripHTMLTags_EmptyInputReturnsEmpty(t *testing.T) {
	// Returns empty string for empty input
	if got := stripHTMLTags(""); got != "" {
		t.Errorf("expected empty string, got %q", got)
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

// ── formatSearchResult ───────────────────────────────────────────────────────

func TestFormatSearchResult_EmptyPagesReturnsNoResults(t *testing.T) {
	// Returns "no results" message when pages slice is empty
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
