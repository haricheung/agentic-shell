package perceiver

import "testing"

// ── isConversational ─────────────────────────────────────────────────────────

func TestIsConversational_Greetings(t *testing.T) {
	// Returns true for common greetings
	for _, g := range []string{"hi", "hello", "hey", "你好", "Hi!", "Hello, world"} {
		if !isConversational(g) {
			t.Errorf("expected true for greeting %q", g)
		}
	}
}

func TestIsConversational_Identity(t *testing.T) {
	// Returns true for identity questions
	for _, q := range []string{"who are you", "what are you", "你是谁", "What's your name?"} {
		if !isConversational(q) {
			t.Errorf("expected true for identity question %q", q)
		}
	}
}

func TestIsConversational_ShortQuestions(t *testing.T) {
	// Returns true for short conversational questions
	for _, q := range []string{"what is go", "how are you", "why is the sky blue", "什么是AI"} {
		if !isConversational(q) {
			t.Errorf("expected true for short question %q", q)
		}
	}
}

func TestIsConversational_ActionVerbs(t *testing.T) {
	// Returns false for inputs with action verbs
	for _, q := range []string{"find my music files", "search for documents", "open safari", "play some music", "删除文件"} {
		if isConversational(q) {
			t.Errorf("expected false for action input %q", q)
		}
	}
}

func TestIsConversational_LongInput(t *testing.T) {
	// Returns false for inputs longer than 100 runes
	long := "this is a very long input that should not be considered conversational because it exceeds the one hundred rune limit for fast path detection"
	if isConversational(long) {
		t.Error("expected false for long input")
	}
}

func TestIsConversational_Empty(t *testing.T) {
	// Returns false for empty input
	if isConversational("") {
		t.Error("expected false for empty input")
	}
}

func TestIsConversational_ComplexTask(t *testing.T) {
	// Returns false for tasks needing tools
	for _, q := range []string{"list all go files", "check disk space", "download that video", "install homebrew"} {
		if isConversational(q) {
			t.Errorf("expected false for complex task %q", q)
		}
	}
}
