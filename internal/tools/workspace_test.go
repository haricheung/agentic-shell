package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── ExpandHome ───────────────────────────────────────────────────────────────

func TestExpandHome_ExpandsTildeSlash(t *testing.T) {
	// Expands "~/foo" to "<home>/foo"
	home, _ := os.UserHomeDir()
	got := ExpandHome("~/Documents/file.txt")
	want := filepath.Join(home, "Documents", "file.txt")
	if got != want {
		t.Errorf("ExpandHome(~/Documents/file.txt) = %q, want %q", got, want)
	}
}

func TestExpandHome_ExpandsBareTilde(t *testing.T) {
	// Expands bare "~" to "<home>"
	home, _ := os.UserHomeDir()
	got := ExpandHome("~")
	if got != home {
		t.Errorf("ExpandHome(~) = %q, want %q", got, home)
	}
}

func TestExpandHome_AbsolutePathUnchanged(t *testing.T) {
	// Returns "/absolute/path" unchanged (no "~")
	got := ExpandHome("/absolute/path")
	if got != "/absolute/path" {
		t.Errorf("ExpandHome(/absolute/path) = %q, want unchanged", got)
	}
}

// ── ResolveOutputPath ────────────────────────────────────────────────────────

func TestResolveOutputPath_BareFilenameRedirected(t *testing.T) {
	// Bare filename ("fetch_news.py") is redirected to the workspace directory
	resolved, redirected := ResolveOutputPath("fetch_news.py")
	if !redirected {
		t.Fatal("expected redirected=true for bare filename")
	}
	if !strings.HasPrefix(resolved, WorkspaceDir()) {
		t.Errorf("expected resolved path under workspace %q, got %q", WorkspaceDir(), resolved)
	}
}

func TestResolveOutputPath_DotSlashRedirected(t *testing.T) {
	// "./output.txt" (CWD-relative) is redirected to workspace
	resolved, redirected := ResolveOutputPath("./output.txt")
	if !redirected {
		t.Fatal("expected redirected=true for ./ prefix")
	}
	if !strings.HasPrefix(resolved, WorkspaceDir()) {
		t.Errorf("expected resolved path under workspace %q, got %q", WorkspaceDir(), resolved)
	}
}

func TestResolveOutputPath_DirComponentNotRedirected(t *testing.T) {
	// Paths with a real dir component are passed through unchanged
	path := "internal/roles/executor/executor.go"
	resolved, redirected := ResolveOutputPath(path)
	if redirected {
		t.Errorf("expected redirected=false for path with dir component, got resolved=%q", resolved)
	}
	if resolved != path {
		t.Errorf("expected path unchanged, got %q", resolved)
	}
}

func TestResolveOutputPath_AbsolutePathNotRedirected(t *testing.T) {
	// Absolute paths are passed through unchanged
	path := "/tmp/output.txt"
	resolved, redirected := ResolveOutputPath(path)
	if redirected {
		t.Errorf("expected redirected=false for absolute path, got resolved=%q", resolved)
	}
	if resolved != path {
		t.Errorf("expected path unchanged, got %q", resolved)
	}
}

func TestResolveOutputPath_ExpandedHomeDirNotRedirected(t *testing.T) {
	// An already-expanded home path ("~/Documents/...") is not redirected
	// (caller must ExpandHome first; after expansion it has a real dir component)
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "Documents", "report.md")
	resolved, redirected := ResolveOutputPath(path)
	if redirected {
		t.Errorf("expected redirected=false for expanded home path, got resolved=%q", resolved)
	}
	if resolved != path {
		t.Errorf("expected path unchanged, got %q", resolved)
	}
}
