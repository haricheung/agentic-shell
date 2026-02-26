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

// ── parseRedditResults ───────────────────────────────────────────────────────

func TestParseRedditResults_EmptyChildrenReturnsEmptySlice(t *testing.T) {
	// Returns empty slice when data.children is absent or empty
	data := []byte(`{"data":{"children":[]}}`)
	posts, err := parseRedditResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("expected 0 posts, got %d", len(posts))
	}
}

func TestParseRedditResults_MissingDataReturnsEmptySlice(t *testing.T) {
	// Returns empty slice when data.children is absent or empty
	data := []byte(`{}`)
	posts, err := parseRedditResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("expected 0 posts for missing data, got %d", len(posts))
	}
}

func TestParseRedditResults_MalformedJSONReturnsError(t *testing.T) {
	// Returns error on malformed JSON
	_, err := parseRedditResults([]byte(`{not valid`))
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestParseRedditResults_MapsFields(t *testing.T) {
	// Maps subreddit, title, score, url, permalink, selftext for each post
	data := []byte(`{"data":{"children":[
		{"data":{"subreddit":"golang","title":"Go is great","score":42,
		 "url":"https://go.dev","permalink":"/r/golang/comments/abc/","selftext":"Some text"}}
	]}}`)
	posts, err := parseRedditResults(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	p := posts[0]
	if p.Subreddit != "golang" {
		t.Errorf("expected Subreddit 'golang', got %q", p.Subreddit)
	}
	if p.Title != "Go is great" {
		t.Errorf("expected Title 'Go is great', got %q", p.Title)
	}
	if p.Score != 42 {
		t.Errorf("expected Score 42, got %d", p.Score)
	}
	if p.Permalink != "https://www.reddit.com/r/golang/comments/abc/" {
		t.Errorf("expected full permalink URL, got %q", p.Permalink)
	}
	if p.Body != "Some text" {
		t.Errorf("expected Body 'Some text', got %q", p.Body)
	}
}

// ── formatRedditResults ──────────────────────────────────────────────────────

func TestFormatRedditResults_EmptyPostsReturnsNoResults(t *testing.T) {
	// Returns "no results" message when posts slice is empty
	got := formatRedditResults("test", nil)
	if !strings.Contains(got, "No Reddit results") {
		t.Errorf("expected 'No Reddit results' message, got %q", got)
	}
}

func TestFormatRedditResults_IncludesSubredditTitleScorePermalink(t *testing.T) {
	// Includes subreddit, title, score, body preview, and permalink for each post
	posts := []redditPost{
		{Subreddit: "golang", Title: "Go tip", Score: 100,
			Permalink: "https://www.reddit.com/r/golang/comments/x/", Body: ""},
	}
	got := formatRedditResults("go", posts)
	if !strings.Contains(got, "r/golang") {
		t.Errorf("expected subreddit in output, got %q", got)
	}
	if !strings.Contains(got, "Go tip") {
		t.Errorf("expected title in output, got %q", got)
	}
	if !strings.Contains(got, "score: 100") {
		t.Errorf("expected score in output, got %q", got)
	}
	if !strings.Contains(got, "https://www.reddit.com/r/golang") {
		t.Errorf("expected permalink in output, got %q", got)
	}
}

func TestFormatRedditResults_OmitsBodyWhenEmpty(t *testing.T) {
	// Omits body line when body is empty (link posts)
	posts := []redditPost{
		{Subreddit: "go", Title: "Link post", Score: 1,
			Permalink: "https://www.reddit.com/r/go/comments/y/", Body: ""},
	}
	got := formatRedditResults("q", posts)
	// 3 lines: "r/go • score: 1", "Link post", permalink
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines for empty body, got %d: %q", len(lines), got)
	}
}

func TestFormatRedditResults_TruncatesBodyAt200Chars(t *testing.T) {
	// Truncates body preview to 200 characters
	longBody := strings.Repeat("x", 300)
	posts := []redditPost{
		{Subreddit: "sub", Title: "T", Score: 1,
			Permalink: "https://www.reddit.com/r/sub/comments/z/", Body: longBody},
	}
	got := formatRedditResults("q", posts)
	if strings.Contains(got, strings.Repeat("x", 201)) {
		t.Error("body was not truncated at 200 chars")
	}
	if !strings.Contains(got, "…") {
		t.Error("expected truncation ellipsis in output")
	}
}

func TestFormatRedditResults_CapsAtMaxResults(t *testing.T) {
	// Caps output at redditMaxResults posts
	posts := make([]redditPost, redditMaxResults+3)
	for i := range posts {
		posts[i] = redditPost{Subreddit: "sub", Title: "T", Score: 1,
			Permalink: "https://www.reddit.com/r/sub/comments/z/"}
	}
	got := formatRedditResults("q", posts)
	// "score:" appears exactly once per post header line
	count := strings.Count(got, "score:")
	if count != redditMaxResults {
		t.Errorf("expected %d posts, got %d", redditMaxResults, count)
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
