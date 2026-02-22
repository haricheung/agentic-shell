package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	ddgSearchURL = "https://html.duckduckgo.com/html/"
	ddgMax       = 5
)

// ddgClient bypasses any HTTP_PROXY / HTTPS_PROXY env vars that may be set by
// parent processes (e.g. Claude Code's internal proxy on port 26560).
// DDG must be reached directly; routing it through the CC proxy fails.
var ddgClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	},
}

// Search queries DuckDuckGo HTML search. No API key required.
//
// Expectations:
//   - Returns formatted results when DDG responds with results
//   - Returns a "(no results)" message when no result anchors are found
//   - Caps output at ddgMax results
//   - Decodes real destination URL from DDG redirect href when display URL is absent
func Search(ctx context.Context, query string) (string, error) {
	body := "q=" + url.QueryEscape(query) + "&kl=us-en"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ddgSearchURL, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("websearch: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; agsh/1.0)")

	resp, err := ddgClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("websearch: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("websearch: read response: %w", err)
	}

	pages := parseDDGHTML(string(raw))
	return formatSearchResult(query, pages), nil
}

// Compiled once at package init — safe for concurrent use.
var (
	reTitleHref = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]*)"[^>]*>(.*?)</a>`)
	reSnippet   = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)
	reDispURL   = regexp.MustCompile(`(?s)class="result__url"[^>]*>\s*(.*?)\s*</a>`)
)

func parseDDGHTML(html string) []searchPage {
	matches := reTitleHref.FindAllStringSubmatchIndex(html, -1)
	pages := make([]searchPage, 0, len(matches))
	for i, m := range matches {
		if i >= ddgMax {
			break
		}
		href := html[m[2]:m[3]]
		title := stripTags(html[m[4]:m[5]])

		// Chunk: from this result's title to the start of the next (or end of doc)
		end := len(html)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		chunk := html[m[0]:end]

		var snippet, dispURL string
		if sm := reSnippet.FindStringSubmatch(chunk); len(sm) > 1 {
			snippet = stripTags(sm[1])
		}
		if um := reDispURL.FindStringSubmatch(chunk); len(um) > 1 {
			dispURL = stripTags(um[1])
		}
		if dispURL == "" {
			dispURL = decodeUDDG(href)
		}

		pages = append(pages, searchPage{Name: title, URL: dispURL, Snippet: snippet})
	}
	return pages
}

// decodeUDDG extracts the real destination URL from a DDG redirect href
// of the form /l/?uddg=<url-encoded-url>&rut=...
func decodeUDDG(href string) string {
	if idx := strings.Index(href, "uddg="); idx >= 0 {
		encoded := href[idx+5:]
		if amp := strings.IndexByte(encoded, '&'); amp >= 0 {
			encoded = encoded[:amp]
		}
		if u, err := url.QueryUnescape(encoded); err == nil {
			return u
		}
	}
	return href
}

var reTag = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = reTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return strings.TrimSpace(s)
}

type searchPage struct {
	Name          string
	URL           string
	Snippet       string
	Summary       string
	DatePublished string
}

// formatSearchResult converts a list of search pages into a readable text block.
//
// Expectations:
//   - Returns "(no results)" message when pages slice is empty
//   - Includes title, snippet, and URL for each result
//   - Prefers summary over snippet when summary is non-empty
//   - Includes YYYY-MM-DD date prefix on URL line when datePublished is non-empty
//   - Omits date when datePublished is empty
//   - Separates results with a blank line
//   - Caps output at ddgMax results
func formatSearchResult(query string, pages []searchPage) string {
	if len(pages) == 0 {
		return fmt.Sprintf("No results found for: %q", query)
	}

	var sb strings.Builder
	for i, p := range pages {
		if i >= ddgMax {
			break
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		text := p.Snippet
		if p.Summary != "" {
			text = p.Summary
		}
		if text != "" {
			sb.WriteString(text)
			sb.WriteString("\n")
		}
		if len(p.DatePublished) >= 10 {
			sb.WriteString(p.DatePublished[:10])
			sb.WriteString(" ")
		}
		sb.WriteString(p.URL)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
