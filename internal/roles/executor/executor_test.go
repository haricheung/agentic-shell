package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── isIrreversibleShell ───────────────────────────────────────────────────────

func TestIsIrreversibleShell_ReturnsTrueForRm(t *testing.T) {
	// Returns true for "rm " commands (including rm -rf)
	ok, reason := isIrreversibleShell("rm -rf /tmp/foo")
	if !ok {
		t.Error("expected true for rm command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForSudoRm(t *testing.T) {
	// Returns true for "sudo rm" commands
	ok, reason := isIrreversibleShell("sudo rm -rf /tmp/foo")
	if !ok {
		t.Error("expected true for sudo rm command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForRmdir(t *testing.T) {
	// Returns true for "rmdir" commands
	ok, reason := isIrreversibleShell("rmdir /tmp/mydir")
	if !ok {
		t.Error("expected true for rmdir command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForTruncate(t *testing.T) {
	// Returns true for "truncate" commands
	ok, reason := isIrreversibleShell("truncate -s 0 myfile.log")
	if !ok {
		t.Error("expected true for truncate command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForShred(t *testing.T) {
	// Returns true for "shred" commands
	ok, reason := isIrreversibleShell("shred -u secrets.txt")
	if !ok {
		t.Error("expected true for shred command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForDdWithOf(t *testing.T) {
	// Returns true for "dd " with "of=" argument
	ok, reason := isIrreversibleShell("dd if=/dev/zero of=/dev/sda bs=4M")
	if !ok {
		t.Error("expected true for dd with of= command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForMkfs(t *testing.T) {
	// Returns true for "mkfs" commands
	ok, reason := isIrreversibleShell("mkfs.ext4 /dev/sdb1")
	if !ok {
		t.Error("expected true for mkfs command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForFindDelete(t *testing.T) {
	// Returns true for "find " commands containing " -delete"
	ok, reason := isIrreversibleShell(`find /tmp -maxdepth 1 -type f -name "*.log" -delete`)
	if !ok {
		t.Error("expected true for find -delete command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForFindExecRm(t *testing.T) {
	// Returns true for "find " commands containing "-exec rm"
	ok, reason := isIrreversibleShell(`find /tmp -name "*.log" -exec rm {} \;`)
	if !ok {
		t.Error("expected true for find -exec rm command")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsFalseForFindWithoutDelete(t *testing.T) {
	// Returns false for plain find without -delete (read-only)
	ok, _ := isIrreversibleShell(`find /tmp -type f -name "*.log"`)
	if ok {
		t.Error("expected false for find without -delete")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForForLoopWithRm(t *testing.T) {
	// Returns true for compound commands embedding rm inside a for-loop
	cmd := `for file in /tmp/a.log /tmp/b.log; do if [ -f "$file" ]; then rm "$file" && echo "done"; fi; done`
	ok, reason := isIrreversibleShell(cmd)
	if !ok {
		t.Error("expected true for for-loop with rm")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForAndAndRm(t *testing.T) {
	// Returns true for "ls /tmp && rm /tmp/foo" (rm after &&)
	ok, reason := isIrreversibleShell("ls /tmp && rm /tmp/foo")
	if !ok {
		t.Error("expected true for && rm")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsTrueForXargsRm(t *testing.T) {
	// Returns true for "find ... | xargs rm" (rm via xargs pipe)
	ok, reason := isIrreversibleShell(`find /tmp -name "*.log" | xargs rm -f`)
	if !ok {
		t.Error("expected true for xargs rm")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleShell_ReturnsFalseForReadOnlyPipeline(t *testing.T) {
	// Returns false for read-only pipeline (no destructive fragment)
	ok, _ := isIrreversibleShell("ls /tmp | grep foo | wc -l")
	if ok {
		t.Error("expected false for read-only pipeline")
	}
}

// ── splitShellFragments ───────────────────────────────────────────────────────

func TestSplitShellFragments_SingleCommandReturnsOneFragment(t *testing.T) {
	// Single command returns one fragment (itself, trimmed)
	got := splitShellFragments("ls -la /tmp")
	if len(got) != 1 || got[0] != "ls -la /tmp" {
		t.Errorf("expected [\"ls -la /tmp\"], got %v", got)
	}
}

func TestSplitShellFragments_SplitsOnAndAnd(t *testing.T) {
	// Splits on "&&" into two fragments
	got := splitShellFragments("ls /tmp && rm /tmp/foo")
	if len(got) != 2 {
		t.Fatalf("expected 2 fragments, got %d: %v", len(got), got)
	}
	if got[0] != "ls /tmp" || got[1] != "rm /tmp/foo" {
		t.Errorf("unexpected fragments: %v", got)
	}
}

func TestSplitShellFragments_SplitsOnSemicolon(t *testing.T) {
	// Splits on ";" into multiple fragments
	got := splitShellFragments("echo a; echo b; echo c")
	if len(got) != 3 {
		t.Fatalf("expected 3 fragments, got %d: %v", len(got), got)
	}
}

func TestSplitShellFragments_StripsLeadingThenKeyword(t *testing.T) {
	// Strips "then " from control-flow fragment → exposes the actual command
	got := splitShellFragments(`if true; then rm foo; fi`)
	found := false
	for _, f := range got {
		if f == "rm foo" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected fragment \"rm foo\" after stripping 'then', got %v", got)
	}
}

func TestSplitShellFragments_StripsLeadingDoKeyword(t *testing.T) {
	// Strips "do " from loop-body fragment → exposes the actual command
	got := splitShellFragments(`for f in a b; do rm "$f"; done`)
	found := false
	for _, f := range got {
		if strings.HasPrefix(f, "rm ") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected fragment starting with \"rm\" after stripping 'do', got %v", got)
	}
}

func TestSplitShellFragments_EmptyFragmentsDropped(t *testing.T) {
	// Returns only non-empty trimmed fragments
	got := splitShellFragments("echo a;;echo b")
	if len(got) != 2 {
		t.Errorf("expected 2 non-empty fragments, got %d: %v", len(got), got)
	}
}

func TestIsIrreversibleShell_ReturnsFalseForReadOnlyCommands(t *testing.T) {
	// Returns false for read-only commands
	readOnly := []string{
		"ls -la /tmp",
		"cat /etc/hosts",
		"grep -r foo /tmp",
		"find . -name '*.go'",
		"echo hello",
		"wc -l file.txt",
	}
	for _, cmd := range readOnly {
		ok, reason := isIrreversibleShell(cmd)
		if ok {
			t.Errorf("expected false for read-only command %q, got true (reason: %s)", cmd, reason)
		}
	}
}

// ── isIrreversibleWriteFile ───────────────────────────────────────────────────

func TestIsIrreversibleWriteFile_ReturnsTrueWhenFileExists(t *testing.T) {
	// Returns true when path exists and is a regular file
	tmp := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(tmp, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	ok, reason := isIrreversibleWriteFile(tmp)
	if !ok {
		t.Error("expected true for existing file")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIsIrreversibleWriteFile_ReturnsFalseWhenFileAbsent(t *testing.T) {
	// Returns false when path does not exist (creating a new file is safe)
	missing := filepath.Join(t.TempDir(), "nonexistent.txt")
	ok, _ := isIrreversibleWriteFile(missing)
	if ok {
		t.Error("expected false for non-existent path")
	}
}

func TestIsIrreversibleWriteFile_ReturnsFalseForDirectory(t *testing.T) {
	// Returns false when path is a directory
	dir := t.TempDir()
	ok, _ := isIrreversibleWriteFile(dir)
	if ok {
		t.Error("expected false for directory path")
	}
}

// ── headTail ─────────────────────────────────────────────────────────────────

func TestHeadTail_PassesThroughShortString(t *testing.T) {
	// Returns s unchanged for strings shorter than or equal to maxLen
	s := strings.Repeat("a", 100)
	got := headTail(s, 8000)
	if got != s {
		t.Errorf("expected passthrough for short string, got different result (len=%d)", len(got))
	}
}

func TestHeadTail_TruncatesMiddleOfLongString(t *testing.T) {
	// For a string longer than maxLen, keeps the head and tail and inserts a truncation marker
	head := strings.Repeat("H", 3000)
	middle := strings.Repeat("M", 5000)
	tail := strings.Repeat("T", 3000)
	s := head + middle + tail
	const maxLen = 8000
	got := headTail(s, maxLen)
	if len(got) >= len(s) {
		t.Errorf("expected truncation: got len=%d, original len=%d", len(got), len(s))
	}
	if !strings.HasPrefix(got, "H") {
		t.Error("expected result to start with head content")
	}
	if !strings.HasSuffix(got, "T") {
		t.Error("expected result to end with tail content")
	}
	if !strings.Contains(got, "[middle truncated]") {
		t.Error("expected truncation marker in result")
	}
}
