package readline

import (
	"bytes"
	"testing"
)

// newTestRuneBuffer creates a RuneBuffer with the given buf, idx and terminal width.
// interactive=false so no output is written during Refresh calls.
func newTestRuneBuffer(buf []rune, idx int, width int) *RuneBuffer {
	rb := &RuneBuffer{
		buf:         buf,
		idx:         idx,
		prompt:      []rune("> "), // 2 ASCII chars → promptLen == 2
		w:           new(bytes.Buffer),
		interactive: false,
		cfg:         &Config{},
		width:       width,
	}
	return rb
}

func TestGetBackspaceSequence_CursorAtEnd_EmptyResult(t *testing.T) {
	// Returns empty slice when r.idx == len(r.buf) (cursor already at end)
	buf := []rune("hello")
	rb := newTestRuneBuffer(buf, len(buf), 80)
	if got := rb.getBackspaceSequence(); len(got) != 0 {
		t.Errorf("expected empty, got %d bytes: %v", len(got), got)
	}
}

func TestGetBackspaceSequence_ASCII_OneBackspacePerRune(t *testing.T) {
	// Emits 1 backspace per ASCII rune between r.idx and end
	buf := []rune("hello") // 5 ASCII chars
	rb := newTestRuneBuffer(buf, 0, 80)
	got := rb.getBackspaceSequence()
	want := bytes.Repeat([]byte{'\b'}, 5)
	if !bytes.Equal(got, want) {
		t.Errorf("ASCII: got %d backspaces, want %d", len(got), len(want))
	}
}

func TestGetBackspaceSequence_CJK_TwoBackspacesPerRune(t *testing.T) {
	// Emits 2 backspaces per CJK rune between r.idx and end (double-width fix)
	buf := []rune("猜猜我") // 3 CJK chars, each visual width 2
	rb := newTestRuneBuffer(buf, 0, 80)
	got := rb.getBackspaceSequence()
	want := bytes.Repeat([]byte{'\b'}, 6) // 3 × 2 = 6 backspaces
	if !bytes.Equal(got, want) {
		t.Errorf("CJK: got %d backspaces, want %d (raw: %v)", len(got), len(want), got)
	}
}

func TestGetBackspaceSequence_SingleLine_NoSepEscape(t *testing.T) {
	// Does not emit the line-up escape sequence for single-line input
	buf := []rune("abc") // 3 ASCII chars, well within 80-column terminal
	rb := newTestRuneBuffer(buf, 0, 80)
	got := rb.getBackspaceSequence()
	if bytes.Contains(got, []byte("\033[A")) {
		t.Errorf("single-line input should not contain line-up escape, got: %v", got)
	}
}

// ── wideExtra (fullwidth punctuation width) ────────────────────────────────

func TestWidth_FullwidthCommaIsDoubleWide(t *testing.T) {
	// Returns 2 for fullwidth comma "，" (U+FF0C)
	if got := runes.Width('，'); got != 2 {
		t.Errorf("Width('，' U+FF0C) = %d, want 2", got)
	}
}

func TestWidth_FullwidthColonIsDoubleWide(t *testing.T) {
	// Returns 2 for fullwidth colon "：" (U+FF1A)
	if got := runes.Width('：'); got != 2 {
		t.Errorf("Width('：' U+FF1A) = %d, want 2", got)
	}
}

func TestWidth_IdeographicPeriodIsDoubleWide(t *testing.T) {
	// Returns 2 for CJK ideographic period "。" (U+3002)
	if got := runes.Width('。'); got != 2 {
		t.Errorf("Width('。' U+3002) = %d, want 2", got)
	}
}

func TestWidth_IdeographicCommaIsDoubleWide(t *testing.T) {
	// Returns 2 for CJK ideographic comma "、" (U+3001)
	if got := runes.Width('、'); got != 2 {
		t.Errorf("Width('、' U+3001) = %d, want 2", got)
	}
}

func TestWidth_ASCIIIsNotDoubleWide(t *testing.T) {
	// Returns 1 for ASCII 'a' (not double-wide)
	if got := runes.Width('a'); got != 1 {
		t.Errorf("Width('a') = %d, want 1", got)
	}
}
