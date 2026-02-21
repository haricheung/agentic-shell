package readline

import (
	"io"
	"testing"
	"time"
)

// newTestConfig returns a Config wired to the given pipe reader, with all
// terminal functions stubbed out so the test runs without a real TTY.
func newTestConfig(pr *io.PipeReader) *Config {
	return &Config{
		Prompt:             "> ",
		Stdin:              pr,
		Stdout:             io.Discard,
		Stderr:             io.Discard,
		FuncIsTerminal:     func() bool { return false },
		FuncGetWidth:       func() int { return 80 },
		FuncMakeRaw:        func() error { return nil },
		FuncExitRaw:        func() error { return nil },
		FuncOnWidthChanged: func(func()) {},
	}
}

// Expectations:
//   - CharFwdDelete != CharDelete (distinct constant values)
//   - escapeExKey maps \033[3~ (attr="3", typ='~') to CharFwdDelete
//   - escapeExKey does NOT map \033[3~ to CharDelete
//   - Readline returns (empty string, nil) after fwd-delete on empty buffer + Enter
//   - Readline returns io.EOF after Ctrl+D (CharDelete) on empty buffer

func TestCharFwdDelete_DistinctFromCharDelete(t *testing.T) {
	// CharFwdDelete != CharDelete (distinct constant values)
	if CharFwdDelete == CharDelete {
		t.Errorf("CharFwdDelete (%d) must not equal CharDelete (%d)", CharFwdDelete, CharDelete)
	}
}

func TestEscapeExKey_ForwardDeleteMapsToCharFwdDelete(t *testing.T) {
	// escapeExKey maps \033[3~ (attr="3", typ='~') to CharFwdDelete
	key := &escapeKeyPair{attr: "3", typ: '~'}
	got := escapeExKey(key)
	if got != CharFwdDelete {
		t.Errorf("\\033[3~ should map to CharFwdDelete (%d), got %d", CharFwdDelete, got)
	}
}

func TestEscapeExKey_ForwardDeleteDoesNotMapToCharDelete(t *testing.T) {
	// escapeExKey does NOT map \033[3~ to CharDelete
	key := &escapeKeyPair{attr: "3", typ: '~'}
	got := escapeExKey(key)
	if got == CharDelete {
		t.Errorf("\\033[3~ must not map to CharDelete (%d) — would cause EOF on empty buffer", CharDelete)
	}
}

func TestReadline_FwdDeleteOnEmptyBuffer_DoesNotReturnEOF(t *testing.T) {
	// Readline returns (empty string, nil) after fwd-delete on empty buffer + Enter
	// This is the end-to-end regression test for the original bug.
	pr, pw := io.Pipe()
	rl, err := NewEx(newTestConfig(pr))
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := rl.Readline()
		ch <- result{line, err}
	}()

	// Forward-delete on empty buffer (should bell only), then Enter to submit.
	pw.Write([]byte("\033[3~\n"))

	select {
	case r := <-ch:
		if r.err == io.EOF {
			t.Error("forward-delete on empty buffer must NOT return io.EOF (regressed to pre-fix behaviour)")
		}
		if r.err != nil {
			t.Errorf("unexpected error: %v", r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Readline timed out — possible deadlock in ioloop")
	}
}

func TestReadline_CtrlD_OnEmptyBuffer_ReturnsEOF(t *testing.T) {
	// Readline returns io.EOF after Ctrl+D (CharDelete) on empty buffer.
	// Ensures we didn't accidentally break the intentional Ctrl+D = exit path.
	pr, pw := io.Pipe()
	rl, err := NewEx(newTestConfig(pr))
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := rl.Readline()
		ch <- result{line, err}
	}()

	// Ctrl+D = byte 0x04 = CharDelete → should trigger EOF on empty buffer.
	pw.Write([]byte{4})

	select {
	case r := <-ch:
		if r.err != io.EOF {
			t.Errorf("Ctrl+D on empty buffer should return io.EOF, got err=%v line=%q", r.err, r.line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Readline timed out")
	}
}
