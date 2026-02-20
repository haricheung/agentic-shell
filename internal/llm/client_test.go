package llm

import "testing"

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
