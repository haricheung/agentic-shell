package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultLangSearchURL = "https://api.langsearch.com/v1/web-search"
	langSearchMax        = 5
)

// langSearchClient bypasses any HTTP_PROXY / HTTPS_PROXY env vars that may be
// set by parent processes (e.g. Claude Code's internal proxy on port 26560).
var langSearchClient = &http.Client{
	Timeout: 15 * time.Second,
}

// SearchAvailable reports whether the search tool is usable.
// Returns true only when LANGSEARCH_API_KEY is set.
//
// Expectations:
//   - Returns false when LANGSEARCH_API_KEY is unset or empty
//   - Returns true when LANGSEARCH_API_KEY is non-empty
func SearchAvailable() bool {
	return os.Getenv("LANGSEARCH_API_KEY") != ""
}

// Search queries the LangSearch web search API.
// Returns an error if LANGSEARCH_API_KEY is not set.
//
// Expectations:
//   - Returns error when LANGSEARCH_API_KEY is unset
//   - Returns formatted results when API responds with results
//   - Returns a "(no results)" message when value array is empty
//   - Caps output at langSearchMax results
//   - Includes datePublished prefix on URL line when non-empty
func Search(ctx context.Context, query string) (string, error) {
	apiKey := os.Getenv("LANGSEARCH_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("search: LANGSEARCH_API_KEY not set")
	}
	baseURL := os.Getenv("LANGSEARCH_BASE_URL")
	if baseURL == "" {
		baseURL = defaultLangSearchURL
	}

	reqBody, err := json.Marshal(map[string]any{
		"query":   query,
		"summary": true,
		"count":   langSearchMax,
	})
	if err != nil {
		return "", fmt.Errorf("search: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("search: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := langSearchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("search: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search: HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var result langSearchResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("search: parse response: %w", err)
	}

	pages := result.Data.WebPages.Value
	return formatSearchResult(query, pages), nil
}

type langSearchResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		WebPages struct {
			Value []searchPage `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

type searchPage struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	Summary       string `json:"summary"`
	DatePublished string `json:"datePublished"`
}

// formatSearchResult converts a list of search pages into a readable text block.
//
// Expectations:
//   - Returns "(no results)" message when pages slice is empty
//   - Includes title, snippet/summary, and URL for each result
//   - Prefers summary over snippet when summary is non-empty
//   - Includes YYYY-MM-DD date prefix on URL line when datePublished is non-empty
//   - Omits date when datePublished is empty
//   - Separates results with a blank line
//   - Caps output at langSearchMax results
func formatSearchResult(query string, pages []searchPage) string {
	if len(pages) == 0 {
		return fmt.Sprintf("No results found for: %q", query)
	}

	var sb strings.Builder
	for i, p := range pages {
		if i >= langSearchMax {
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
