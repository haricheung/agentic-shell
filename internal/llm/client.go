package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client is an OpenAI-compatible LLM client.
type Client struct {
	baseURL        string
	apiKey         string
	model          string
	label          string // tier name used in debug log lines (e.g. "R1", "BRAIN", "TOOL")
	enableThinking bool   // sends "enable_thinking":true in the request body (Kimi thinking mode)
	httpClient     *http.Client
}

// normalizeBaseURL strips trailing slashes and the "/chat/completions" suffix
// from a raw OPENAI_BASE_URL value so the path is never doubled when the
// client appends "/chat/completions" itself.
//
// Expectations:
//   - Strips a trailing "/chat/completions" suffix
//   - Strips a trailing slash without "/chat/completions"
//   - Strips trailing slash AND "/chat/completions" when both are present
//   - Returns the URL unchanged when neither suffix is present
//   - Returns "" for empty input
func normalizeBaseURL(raw string) string {
	s := strings.TrimRight(raw, "/")
	return strings.TrimSuffix(s, "/chat/completions")
}

// New creates a Client from the shared environment variables:
//
//	OPENAI_API_KEY, OPENAI_BASE_URL, OPENAI_MODEL
func New() *Client {
	return NewTier("")
}

// NewTier creates a Client for a named tier (e.g. "BRAIN", "TOOL").
// For each config key it first tries {prefix}_{KEY}; if unset it falls back
// to the shared OPENAI_{KEY}. An empty prefix reads only the shared vars,
// making it equivalent to New().
//
// Example — prefix "BRAIN" resolves credentials as:
//
//	BRAIN_API_KEY        → OPENAI_API_KEY
//	BRAIN_BASE_URL       → OPENAI_BASE_URL
//	BRAIN_MODEL          → OPENAI_MODEL
//	BRAIN_ENABLE_THINKING (no fallback; defaults false)
//
// Expectations:
//   - Uses {prefix}_API_KEY / _BASE_URL / _MODEL when set and non-empty
//   - Falls back to OPENAI_* vars for any unset tier-specific var
//   - Sets enableThinking when {prefix}_ENABLE_THINKING == "true"
//   - Empty prefix reads only OPENAI_* (identical to New())
func NewTier(prefix string) *Client {
	get := func(suffix, fallback string) string {
		if prefix != "" {
			if v := os.Getenv(prefix + "_" + suffix); v != "" {
				return v
			}
		}
		return os.Getenv(fallback)
	}
	enableThinking := prefix != "" && os.Getenv(prefix+"_ENABLE_THINKING") == "true"
	label := prefix
	if label == "" {
		label = "LLM"
	}
	return &Client{
		baseURL:        normalizeBaseURL(get("BASE_URL", "OPENAI_BASE_URL")),
		apiKey:         get("API_KEY", "OPENAI_API_KEY"),
		model:          get("MODEL", "OPENAI_MODEL"),
		label:          label,
		enableThinking: enableThinking,
		httpClient:     &http.Client{Timeout: 120 * time.Second},
	}
}

type chatRequest struct {
	Model          string    `json:"model"`
	Messages       []chatMsg `json:"messages"`
	EnableThinking bool      `json:"enable_thinking,omitempty"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage reports token consumption for one LLM call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Chat sends a system + user prompt and returns the assistant's text response and token usage.
func (c *Client) Chat(ctx context.Context, system, user string) (string, Usage, error) {
	log.Printf("[%s] ── SYSTEM PROMPT ──────────────────────────────\n%s\n── END SYSTEM ──────────────────────────────────", c.label, system)
	log.Printf("[%s] ── USER PROMPT ─────────────────────────────────\n%s\n── END USER ────────────────────────────────────", c.label, user)

	payload := chatRequest{
		Model: c.model,
		Messages: []chatMsg{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		EnableThinking: c.enableThinking,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", Usage{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, fmt.Errorf("llm: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("llm: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", Usage{}, fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", Usage{}, fmt.Errorf("llm: unmarshal response: %w", err)
	}

	if chatResp.Error != nil {
		return "", Usage{}, fmt.Errorf("llm: API error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("llm: no choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	log.Printf("[%s] ── RESPONSE (tokens: prompt=%d completion=%d) ──\n%s\n── END RESPONSE ────────────────────────────────",
		c.label, chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, content)
	return content, chatResp.Usage, nil
}

// StripThinkBlocks removes all <think>...</think> blocks from s.
// Reasoning models (e.g. deepseek-r1) emit these before or between JSON
// objects. The blocks are not part of structured output and must be stripped
// before JSON parsing.
//
// Expectations:
//   - Removes a single <think>...</think> block
//   - Removes multiple <think>...</think> blocks
//   - Strips an unclosed <think> block from its start to end of string
//   - Returns s unchanged when no <think> tag is present
func StripThinkBlocks(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "</think>")
		if end == -1 {
			// Unclosed block — strip from opening tag to end of string.
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// StripFences removes markdown code fences (```json ... ```) from LLM output,
// and also strips <think>...</think> reasoning blocks emitted by reasoning models.
func StripFences(s string) string {
	s = StripThinkBlocks(strings.TrimSpace(s))
	if strings.HasPrefix(s, "```") {
		// Remove opening fence line
		idx := strings.Index(s, "\n")
		if idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence
		if i := strings.LastIndex(s, "```"); i != -1 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}
