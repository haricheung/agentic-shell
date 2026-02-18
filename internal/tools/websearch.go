package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ddgAPIURL = "https://api.duckduckgo.com/"

// Search queries the DuckDuckGo Instant Answer API (no API key required)
// and returns a text summary of the result.
func Search(ctx context.Context, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("no_html", "1")
	params.Set("skip_disambig", "1")

	reqURL := ddgAPIURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("websearch: create request: %w", err)
	}
	req.Header.Set("User-Agent", "agsh/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("websearch: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("websearch: read response: %w", err)
	}

	var result ddgResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("websearch: parse response: %w", err)
	}

	return formatDDGResult(query, &result), nil
}

type ddgResponse struct {
	AbstractText string      `json:"AbstractText"`
	AbstractURL  string      `json:"AbstractURL"`
	Answer       string      `json:"Answer"`
	AnswerType   string      `json:"AnswerType"`
	RelatedTopics []ddgTopic `json:"RelatedTopics"`
}

type ddgTopic struct {
	Text string `json:"Text"`
	FirstURL string `json:"FirstURL"`
}

func formatDDGResult(query string, r *ddgResponse) string {
	var parts []string

	if r.Answer != "" {
		parts = append(parts, "Answer: "+r.Answer)
	}
	if r.AbstractText != "" {
		parts = append(parts, "Summary: "+r.AbstractText)
		if r.AbstractURL != "" {
			parts = append(parts, "Source: "+r.AbstractURL)
		}
	}
	for i, t := range r.RelatedTopics {
		if i >= 3 {
			break
		}
		if t.Text != "" {
			parts = append(parts, "- "+t.Text)
		}
	}

	if len(parts) == 0 {
		return fmt.Sprintf("No instant answer found for: %q. Try a more specific query.", query)
	}

	return strings.Join(parts, "\n")
}
