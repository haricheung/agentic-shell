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
	searchMaxResults = 5
	bingSearchURL    = "https://api.bing.microsoft.com/v7.0/search"
	bingAPIKeyEnv    = "BING_API_KEY"
)

var searchClient = &http.Client{Timeout: 15 * time.Second}

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
