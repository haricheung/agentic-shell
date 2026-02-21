package tools

import (
	"strings"
	"testing"
)

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

func TestFormatSearchResult_PrefersSummaryOverSnippet(t *testing.T) {
	// Prefers summary over snippet when summary is non-empty
	pages := []searchPage{
		{Name: "Title", Snippet: "short snippet", Summary: "longer summary text", URL: "https://a.com"},
	}
	got := formatSearchResult("query", pages)
	if !strings.Contains(got, "longer summary text") {
		t.Errorf("expected summary in output, got %q", got)
	}
	if strings.Contains(got, "short snippet") {
		t.Errorf("expected snippet to be replaced by summary, got %q", got)
	}
}

func TestFormatSearchResult_OmitsDateWhenEmpty(t *testing.T) {
	// Omits date when datePublished is empty
	pages := []searchPage{
		{Name: "Title", Snippet: "text", URL: "https://a.com", DatePublished: ""},
	}
	got := formatSearchResult("query", pages)
	// URL line must not start with a digit (which would indicate a date prefix)
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "https://a.com") && len(line) > 0 && line[0] >= '0' && line[0] <= '9' {
			t.Errorf("unexpected date prefix on URL line: %q", line)
		}
	}
}

func TestFormatSearchResult_IncludesDateWhenPresent(t *testing.T) {
	// Includes YYYY-MM-DD prefix on URL line when datePublished is non-empty
	pages := []searchPage{
		{Name: "Title", Snippet: "text", URL: "https://a.com", DatePublished: "2024-07-22T00:00:00+08:00"},
	}
	got := formatSearchResult("query", pages)
	if !strings.Contains(got, "2024-07-22") {
		t.Errorf("expected date in output, got %q", got)
	}
}

func TestFormatSearchResult_CapsAtMaxResults(t *testing.T) {
	// Caps output at ddgMax results
	pages := make([]searchPage, ddgMax+3)
	for i := range pages {
		pages[i] = searchPage{Name: "Title", URL: "https://a.com"}
	}
	got := formatSearchResult("query", pages)
	count := strings.Count(got, "https://a.com")
	if count != ddgMax {
		t.Errorf("expected %d results, got %d", ddgMax, count)
	}
}

func TestFormatSearchResult_MultipleResultsSeparatedByBlankLine(t *testing.T) {
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
