package llm

import (
	"testing"
	"os"
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
