package llm

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeBaseURL_StripsChatCompletionsSuffix(t *testing.T) {
	// Strips a trailing "/chat/completions" suffix
	got := normalizeBaseURL("https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions")
	want := "https://dashscope.aliyuncs.com/compatible-mode/v1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeBaseURL_StripTrailingSlash(t *testing.T) {
	// Strips a trailing slash without "/chat/completions"
	got := normalizeBaseURL("https://api.openai.com/v1/")
	want := "https://api.openai.com/v1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeBaseURL_StripSlashAndSuffix(t *testing.T) {
	// Strips trailing slash AND "/chat/completions" when both are present
	got := normalizeBaseURL("https://api.example.com/v1/chat/completions/")
	want := "https://api.example.com/v1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeBaseURL_NoSuffixUnchanged(t *testing.T) {
	// Returns the URL unchanged when neither suffix is present
	got := normalizeBaseURL("https://api.deepseek.com")
	want := "https://api.deepseek.com"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeBaseURL_EmptyInput(t *testing.T) {
	// Returns "" for empty input
	if got := normalizeBaseURL(""); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestNewTier_UsesTierSpecificVars(t *testing.T) {
	// Uses {prefix}_API_KEY / _BASE_URL / _MODEL when set and non-empty
	t.Setenv("BRAIN_API_KEY", "sk-brain-key")
	t.Setenv("BRAIN_BASE_URL", "https://api.deepseek.com")
	t.Setenv("BRAIN_MODEL", "deepseek-reasoner")
	t.Setenv("OPENAI_API_KEY", "sk-shared-key")
	t.Setenv("OPENAI_BASE_URL", "https://api.shared.com")
	t.Setenv("OPENAI_MODEL", "shared-model")
	c := NewTier("BRAIN")
	if c.apiKey != "sk-brain-key" {
		t.Errorf("apiKey: got %q, want sk-brain-key", c.apiKey)
	}
	if c.baseURL != "https://api.deepseek.com" {
		t.Errorf("baseURL: got %q, want https://api.deepseek.com", c.baseURL)
	}
	if c.model != "deepseek-reasoner" {
		t.Errorf("model: got %q, want deepseek-reasoner", c.model)
	}
}

func TestNewTier_FallsBackToSharedVars(t *testing.T) {
	// Falls back to OPENAI_* vars for any unset tier-specific var
	os.Unsetenv("TOOL_API_KEY")
	os.Unsetenv("TOOL_BASE_URL")
	os.Unsetenv("TOOL_MODEL")
	t.Setenv("OPENAI_API_KEY", "sk-shared-key")
	t.Setenv("OPENAI_BASE_URL", "https://api.shared.com/v1")
	t.Setenv("OPENAI_MODEL", "shared-model")
	c := NewTier("TOOL")
	if c.apiKey != "sk-shared-key" {
		t.Errorf("apiKey: got %q, want sk-shared-key", c.apiKey)
	}
	if c.model != "shared-model" {
		t.Errorf("model: got %q, want shared-model", c.model)
	}
}

func TestNewTier_SetsEnableThinkingWhenTrue(t *testing.T) {
	// Sets enableThinking when {prefix}_ENABLE_THINKING == "true"
	t.Setenv("BRAIN_ENABLE_THINKING", "true")
	c := NewTier("BRAIN")
	if !c.enableThinking {
		t.Error("expected enableThinking=true")
	}
}

func TestNewTier_EmptyPrefixReadsOnlySharedVars(t *testing.T) {
	// Empty prefix reads only OPENAI_* (identical to New())
	t.Setenv("OPENAI_API_KEY", "sk-shared-key")
	t.Setenv("OPENAI_MODEL", "shared-model")
	c := NewTier("")
	if c.apiKey != "sk-shared-key" {
		t.Errorf("apiKey: got %q, want sk-shared-key", c.apiKey)
	}
	if c.model != "shared-model" {
		t.Errorf("model: got %q, want shared-model", c.model)
	}
}

// --- StripThinkBlocks ---

func TestStripThinkBlocks_RemovesSingleBlock(t *testing.T) {
	// Removes a single <think>...</think> block
	got := StripThinkBlocks("<think>let me reason</think>\n{\"tool\": \"search\"}")
	want := "{\"tool\": \"search\"}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripThinkBlocks_RemovesMultipleBlocks(t *testing.T) {
	// Removes multiple <think>...</think> blocks
	got := StripThinkBlocks("<think>first</think>{\"a\":1}<think>second</think>{\"b\":2}")
	if strings.Contains(got, "<think>") || strings.Contains(got, "</think>") {
		t.Errorf("expected all think blocks removed, got %q", got)
	}
}

func TestStripThinkBlocks_UnclosedBlockStrippedToEnd(t *testing.T) {
	// Strips an unclosed <think> block from its start to end of string
	got := StripThinkBlocks("{\"tool\": \"search\"}<think>orphaned reasoning")
	want := "{\"tool\": \"search\"}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStripThinkBlocks_NoTagReturnedUnchanged(t *testing.T) {
	// Returns s unchanged when no <think> tag is present
	input := "{\"tool\": \"shell\", \"command\": \"ls\"}"
	got := StripThinkBlocks(input)
	if got != input {
		t.Errorf("expected unchanged, got %q", got)
	}
}

// ── Validate ─────────────────────────────────────────────────────────────────

func TestValidate_NilWhenAllFieldsPresent(t *testing.T) {
	// Returns nil when all three fields (baseURL, apiKey, model) are non-empty
	c := &Client{baseURL: "https://api.example.com", apiKey: "sk-key", model: "gpt-4o", label: "TEST"}
	if err := c.Validate(); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidate_ErrorListsBaseURL(t *testing.T) {
	// Returns error listing "base URL" when baseURL is empty
	c := &Client{baseURL: "", apiKey: "sk-key", model: "gpt-4o", label: "TEST"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "base URL") {
		t.Errorf("expected 'base URL' in error, got %q", err.Error())
	}
}

func TestValidate_ErrorListsAPIKey(t *testing.T) {
	// Returns error listing "API key" when apiKey is empty
	c := &Client{baseURL: "https://api.example.com", apiKey: "", model: "gpt-4o", label: "TEST"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("expected 'API key' in error, got %q", err.Error())
	}
}

func TestValidate_ErrorListsModel(t *testing.T) {
	// Returns error listing "model" when model is empty
	c := &Client{baseURL: "https://api.example.com", apiKey: "sk-key", model: "", label: "TEST"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Errorf("expected 'model' in error, got %q", err.Error())
	}
}

func TestValidate_ErrorListsAllMissingFieldsCommaSeparated(t *testing.T) {
	// Returns error listing all missing fields comma-separated when multiple are empty
	c := &Client{baseURL: "", apiKey: "", model: "", label: "TEST"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "base URL") || !strings.Contains(msg, "API key") || !strings.Contains(msg, "model") {
		t.Errorf("expected all three fields listed, got %q", msg)
	}
	if !strings.Contains(msg, ", ") {
		t.Errorf("expected comma-separated list, got %q", msg)
	}
}

func TestValidate_ErrorIncludesTierLabel(t *testing.T) {
	// Error message includes the tier label
	c := &Client{baseURL: "", apiKey: "sk-key", model: "gpt-4o", label: "BRAIN"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "BRAIN") {
		t.Errorf("expected tier label 'BRAIN' in error, got %q", err.Error())
	}
}
