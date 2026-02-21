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
	bochaAPIURL     = "https://api.bochaai.com/v1/web-search"
	bochaMaxResults = 5
)

// Search queries the Bocha web search API (api.bochaai.com) and returns a
// formatted text summary. Requires BOCHA_API_KEY in the environment.
//
// Expectations:
//   - Returns an error when BOCHA_API_KEY is not set
//   - Returns a formatted result string when the API responds with webPages
//   - Returns a "(no results)" message when webPages.value is empty
//   - Caps output at bochaMaxResults results
func Search(ctx context.Context, query string) (string, error) {
	apiKey := os.Getenv("BOCHA_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("websearch: BOCHA_API_KEY not set")
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	reqBody, err := json.Marshal(map[string]any{
		"query":     query,
		"freshness": "noLimit",
		"summary":   false,
		"count":     bochaMaxResults,
	})
	if err != nil {
		return "", fmt.Errorf("websearch: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bochaAPIURL, bytes.NewReader(reqBody))
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

	var result bochaResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("websearch: parse response: %w", err)
	}

	return formatBochaResult(query, &result), nil
}

type bochaResponse struct {
	WebPages struct {
		Value []bochaWebPage `json:"value"`
	} `json:"webPages"`
}

type bochaWebPage struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	Summary       string `json:"summary"`
	SiteName      string `json:"siteName"`
	DatePublished string `json:"datePublished"`
}

// formatBochaResult converts a Bocha API response into a readable text block.
//
// Expectations:
//   - Returns "(no results)" message when pages slice is empty
//   - Includes title, snippet, and URL for each result
//   - Prefers summary over snippet when summary is non-empty
//   - Omits datePublished when empty
//   - Separates results with a blank line
func formatBochaResult(query string, r *bochaResponse) string {
	pages := r.WebPages.Value
	if len(pages) == 0 {
		return fmt.Sprintf("No results found for: %q", query)
	}

	var sb strings.Builder
	for i, p := range pages {
		if i >= bochaMaxResults {
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
		if p.DatePublished != "" {
			sb.WriteString(p.DatePublished[:10]) // YYYY-MM-DD only
			sb.WriteString(" ")
		}
		sb.WriteString(p.URL)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
