package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const searchMaxResults = 5

var searchClient = &http.Client{Timeout: 15 * time.Second}

// Regex patterns for DuckDuckGo HTML results.
// Titles: <a rel="nofollow" class="result__a" href="URL">Title</a>
// Snippets: <a class="result__snippet" href="...">text</a>
var (
	titleRe   = regexp.MustCompile(`<a rel="nofollow" class="result__a" href="([^"]*)"[^>]*>([^<]*(?:<b>[^<]*</b>[^<]*)*)</a>`)
	snippetRe = regexp.MustCompile(`<a class="result__snippet"[^>]*>([^<]*(?:<[^>]+>[^<]*)*)</a>`)
)

// SearchAvailable reports whether the search tool is usable.
// DuckDuckGo HTML search requires no API key — always available.
//
// Expectations:
//   - Always returns true (no API key required)
func SearchAvailable() bool {
	return true
}

// Search queries DuckDuckGo's HTML search endpoint (no API key required).
//
// Expectations:
//   - Returns formatted results when DuckDuckGo responds with results
//   - Returns a "(no results)" message when no organic results are found
//   - Returns error when the HTTP request fails
func Search(ctx context.Context, query string) (string, error) {
	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://html.duckduckgo.com/html/",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("search: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	resp, err := searchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("search: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search: HTTP %d", resp.StatusCode)
	}

	pages := parseDDGResults(string(raw))
	return formatSearchResult(query, pages), nil
}

type searchPage struct {
	Name    string
	URL     string
	Snippet string
}

// parseDDGResults extracts organic search results from DuckDuckGo HTML.
//
// Expectations:
//   - Returns empty slice when body contains no result elements
//   - Skips ads (href containing "duckduckgo.com/y.js") and their paired snippets
//   - Extracts title text from result__a anchors, stripping inline HTML tags
//   - Extracts URL from result__a href attribute
//   - Extracts snippet text from result__snippet anchors, stripping inline HTML tags
//   - Unescapes HTML entities in title and snippet (e.g. &amp; → &)
func parseDDGResults(body string) []searchPage {
	titleMatches := titleRe.FindAllStringSubmatch(body, -1)
	snippetMatches := snippetRe.FindAllStringSubmatch(body, -1)

	var pages []searchPage
	si := 0 // snippet index
	for _, m := range titleMatches {
		href := m[1]
		if strings.Contains(href, "duckduckgo.com/y.js") {
			si++ // ads also have a paired snippet — skip it
			continue
		}
		title := stripHTMLTags(html.UnescapeString(m[2]))
		snippet := ""
		if si < len(snippetMatches) {
			snippet = stripHTMLTags(html.UnescapeString(snippetMatches[si][1]))
			si++
		}
		pages = append(pages, searchPage{Name: title, URL: href, Snippet: snippet})
	}
	return pages
}

// stripHTMLTags removes inline HTML tags (e.g. <b>, </b>) from a string.
//
// Expectations:
//   - Removes <b> and </b> tags, preserving inner text
//   - Removes arbitrary tags (e.g. <span>, <a>)
//   - Returns input unchanged when no tags are present
//   - Returns empty string for empty input
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatSearchResult converts a list of search pages into a readable text block.
//
// Expectations:
//   - Returns "(no results)" message when pages slice is empty
//   - Includes title, snippet, and URL for each result
//   - Omits snippet line when snippet is empty
//   - Separates results with a blank line
//   - Caps output at searchMaxResults results
func formatSearchResult(query string, pages []searchPage) string {
	if len(pages) == 0 {
		return fmt.Sprintf("No results found for: %q", query)
	}

	var sb strings.Builder
	for i, p := range pages {
		if i >= searchMaxResults {
			break
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		if p.Snippet != "" {
			sb.WriteString(p.Snippet)
			sb.WriteString("\n")
		}
		sb.WriteString(p.URL)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
