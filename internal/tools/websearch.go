package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	searchMaxResults  = 5
	bingSearchURL     = "https://api.bing.microsoft.com/v7.0/search"
	bingAPIKeyEnv     = "BING_API_KEY"
	redditSearchURL   = "https://www.reddit.com/search.json"
	redditMaxResults  = 5
)

var searchClient = &http.Client{
	Timeout: 15 * time.Second,
	// Reddit requires a real User-Agent or it returns 429.
	Transport: &userAgentTransport{ua: "artoo-agent/1.0 (agentic-shell)"},
}

type userAgentTransport struct {
	ua string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", t.ua)
	return http.DefaultTransport.RoundTrip(r)
}

// SearchAvailable reports whether the Bing search tool is usable.
//
// Expectations:
//   - Returns true when BING_API_KEY env var is non-empty
//   - Returns false when BING_API_KEY is not set or empty
func SearchAvailable() bool {
	return os.Getenv(bingAPIKeyEnv) != ""
}

// Search queries Bing Web Search API v7 and returns formatted results.
//
// Expectations:
//   - Returns error when BING_API_KEY is not set
//   - Returns formatted results when Bing responds with web results
//   - Returns a "no results" message when no web results are found
//   - Returns error when the HTTP request fails or returns non-200
func Search(ctx context.Context, query string) (string, error) {
	apiKey := os.Getenv(bingAPIKeyEnv)
	if apiKey == "" {
		return "", fmt.Errorf("search: %s not set", bingAPIKeyEnv)
	}

	reqURL := bingSearchURL + "?q=" + url.QueryEscape(query) + "&count=10"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("search: create request: %w", err)
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", apiKey)

	resp, err := searchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("search: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search: HTTP %d: %s", resp.StatusCode, string(body))
	}

	pages, err := parseBingResults(body)
	if err != nil {
		return "", fmt.Errorf("search: parse response: %w", err)
	}
	return formatSearchResult(query, pages), nil
}

type searchPage struct {
	Name    string
	URL     string
	Snippet string
}

// bingResponse is the top-level Bing Web Search API v7 response.
type bingResponse struct {
	WebPages struct {
		Value []struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"value"`
	} `json:"webPages"`
}

// parseBingResults extracts organic search results from a Bing API JSON response.
//
// Expectations:
//   - Returns empty slice when webPages.value is absent or empty
//   - Returns error on malformed JSON
//   - Maps name → Name, url → URL, snippet → Snippet for each result
func parseBingResults(data []byte) ([]searchPage, error) {
	var br bingResponse
	if err := json.Unmarshal(data, &br); err != nil {
		return nil, err
	}
	pages := make([]searchPage, 0, len(br.WebPages.Value))
	for _, v := range br.WebPages.Value {
		pages = append(pages, searchPage{Name: v.Name, URL: v.URL, Snippet: v.Snippet})
	}
	return pages, nil
}

// ---------------------------------------------------------------------------
// Reddit Search (public JSON API — no key required)
// ---------------------------------------------------------------------------

type redditPost struct {
	Subreddit string
	Title     string
	Score     int
	URL       string // external link or reddit thread URL
	Permalink string // always the reddit thread URL
	Body      string // selftext for text posts; empty for link posts
}

type redditResponse struct {
	Data struct {
		Children []struct {
			Data struct {
				Subreddit string  `json:"subreddit"`
				Title     string  `json:"title"`
				Score     int     `json:"score"`
				URL       string  `json:"url"`
				Permalink string  `json:"permalink"`
				Selftext  string  `json:"selftext"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

// RedditSearch queries Reddit's public search API and returns formatted posts.
//
// Expectations:
//   - Returns formatted posts when Reddit responds with results
//   - Returns a "no results" message when no posts are found
//   - Returns error when the HTTP request fails or returns non-200
//   - Caps output at redditMaxResults posts
func RedditSearch(ctx context.Context, query string) (string, error) {
	reqURL := redditSearchURL + "?q=" + url.QueryEscape(query) + "&limit=10&sort=relevance&type=link"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("reddit: create request: %w", err)
	}

	resp, err := searchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("reddit: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reddit: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reddit: HTTP %d", resp.StatusCode)
	}

	posts, err := parseRedditResults(body)
	if err != nil {
		return "", fmt.Errorf("reddit: parse response: %w", err)
	}
	return formatRedditResults(query, posts), nil
}

// parseRedditResults extracts posts from a Reddit search JSON response.
//
// Expectations:
//   - Returns empty slice when data.children is absent or empty
//   - Returns error on malformed JSON
//   - Maps subreddit, title, score, url, permalink, selftext for each post
func parseRedditResults(data []byte) ([]redditPost, error) {
	var rr redditResponse
	if err := json.Unmarshal(data, &rr); err != nil {
		return nil, err
	}
	posts := make([]redditPost, 0, len(rr.Data.Children))
	for _, c := range rr.Data.Children {
		d := c.Data
		posts = append(posts, redditPost{
			Subreddit: d.Subreddit,
			Title:     d.Title,
			Score:     d.Score,
			URL:       d.URL,
			Permalink: "https://www.reddit.com" + d.Permalink,
			Body:      d.Selftext,
		})
	}
	return posts, nil
}

// formatRedditResults converts a list of Reddit posts into a readable text block.
//
// Expectations:
//   - Returns "no results" message when posts slice is empty
//   - Includes subreddit, title, score, body preview, and permalink for each post
//   - Omits body line when body is empty (link posts)
//   - Separates posts with a blank line
//   - Caps output at redditMaxResults posts
//   - Truncates body preview to 200 characters
func formatRedditResults(query string, posts []redditPost) string {
	if len(posts) == 0 {
		return fmt.Sprintf("No Reddit results found for: %q", query)
	}
	var sb strings.Builder
	for i, p := range posts {
		if i >= redditMaxResults {
			break
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("r/%s • score: %d\n", p.Subreddit, p.Score))
		sb.WriteString(p.Title + "\n")
		if p.Body != "" {
			preview := p.Body
			if len(preview) > 200 {
				preview = preview[:200] + "…"
			}
			sb.WriteString(preview + "\n")
		}
		sb.WriteString(p.Permalink + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------------------
// Shared formatting helper
// ---------------------------------------------------------------------------

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
