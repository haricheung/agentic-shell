package tools

import (
	"strings"
	"testing"
)

func makeBochaResponse(pages []bochaWebPage) *bochaResponse {
	r := &bochaResponse{}
	r.WebPages.Value = pages
	return r
}

func TestFormatBochaResult_EmptyPagesReturnsNoResults(t *testing.T) {
	// Returns "(no results)" message when pages slice is empty
	got := formatBochaResult("test query", makeBochaResponse(nil))
	if !strings.Contains(got, "No results") {
		t.Errorf("expected 'No results' message, got %q", got)
	}
}

func TestFormatBochaResult_IncludesTitleSnippetURL(t *testing.T) {
	// Includes title, snippet, and URL for each result
	pages := []bochaWebPage{
		{Name: "Example Title", Snippet: "An example snippet.", URL: "https://example.com"},
	}
	got := formatBochaResult("query", makeBochaResponse(pages))
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

func TestFormatBochaResult_PrefersSummaryOverSnippet(t *testing.T) {
	// Prefers summary over snippet when summary is non-empty
	pages := []bochaWebPage{
		{Name: "Title", Snippet: "short snippet", Summary: "longer summary text", URL: "https://a.com"},
	}
	got := formatBochaResult("query", makeBochaResponse(pages))
	if !strings.Contains(got, "longer summary text") {
		t.Errorf("expected summary in output, got %q", got)
	}
	if strings.Contains(got, "short snippet") {
		t.Errorf("expected snippet to be replaced by summary, got %q", got)
	}
}

func TestFormatBochaResult_OmitsDatePublishedWhenEmpty(t *testing.T) {
	// Omits datePublished when empty
	pages := []bochaWebPage{
		{Name: "Title", Snippet: "text", URL: "https://a.com", DatePublished: ""},
	}
	got := formatBochaResult("query", makeBochaResponse(pages))
	// Should not contain any date-like prefix before the URL
	lines := strings.Split(got, "\n")
	for _, line := range lines {
		if strings.Contains(line, "https://a.com") && strings.Contains(line, "2") {
			// A date would look like "2024-01-01 https://..."
			// If the URL line starts with a digit it means a date was prepended
			if line[0] >= '0' && line[0] <= '9' {
				t.Errorf("unexpected date prefix on URL line: %q", line)
			}
		}
	}
}

func TestFormatBochaResult_IncludesDateWhenPresent(t *testing.T) {
	// Includes YYYY-MM-DD prefix on URL line when datePublished is non-empty
	pages := []bochaWebPage{
		{Name: "Title", Snippet: "text", URL: "https://a.com", DatePublished: "2024-07-22T00:00:00+08:00"},
	}
	got := formatBochaResult("query", makeBochaResponse(pages))
	if !strings.Contains(got, "2024-07-22") {
		t.Errorf("expected date in output, got %q", got)
	}
}

func TestFormatBochaResult_CapsAtMaxResults(t *testing.T) {
	// Caps output at bochaMaxResults results
	pages := make([]bochaWebPage, bochaMaxResults+3)
	for i := range pages {
		pages[i] = bochaWebPage{Name: "Title", URL: "https://a.com"}
	}
	got := formatBochaResult("query", makeBochaResponse(pages))
	// Count URL occurrences — each result has one URL line
	count := strings.Count(got, "https://a.com")
	if count != bochaMaxResults {
		t.Errorf("expected %d results, got %d", bochaMaxResults, count)
	}
}

func TestFormatBochaResult_MultipleResultsSeparatedByBlankLine(t *testing.T) {
	// Separates results with a blank line
	pages := []bochaWebPage{
		{Name: "First", Snippet: "s1", URL: "https://a.com"},
		{Name: "Second", Snippet: "s2", URL: "https://b.com"},
	}
	got := formatBochaResult("query", makeBochaResponse(pages))
	if !strings.Contains(got, "\n\n") {
		t.Errorf("expected blank line between results, got %q", got)
	}
}
