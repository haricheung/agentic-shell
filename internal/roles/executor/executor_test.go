package executor

import (
	"os"
	"path/filepath"
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
