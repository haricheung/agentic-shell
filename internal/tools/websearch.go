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
	langSearchAPIURL = "https://api.langsearch.com/v1/web-search"
	langSearchMax    = 5
)

// Search queries the LangSearch web search API (langsearch.com — free, no credit card).
// Requires LANGSEARCH_API_KEY in the environment.
//
// Expectations:
//   - Returns an error when LANGSEARCH_API_KEY is not set
//   - Returns a formatted result string when the API responds with webPages
//   - Returns a "(no results)" message when webPages.value is empty
//   - Caps output at langSearchMax results
func Search(ctx context.Context, query string) (string, error) {
	apiKey := os.Getenv("LANGSEARCH_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("websearch: LANGSEARCH_API_KEY not set")
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	reqBody, err := json.Marshal(map[string]any{
		"query":     query,
		"freshness": "noLimit",
		"summary":   false,
		"count":     langSearchMax,
	})
	if err != nil {
		return "", fmt.Errorf("websearch: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, langSearchAPIURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("websearch: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("websearch: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("websearch: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("websearch: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result langSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("websearch: parse response: %w", err)
	}

	return formatSearchResult(query, result.Data.WebPages.Value), nil
}

type langSearchResponse struct {
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
//   - Includes title, snippet, and URL for each result
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
