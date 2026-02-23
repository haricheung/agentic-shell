package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceDir returns the agent's designated output directory.
// Reads $AGSH_WORKSPACE; defaults to ~/agsh_workspace.
// All generated files (scripts, reports, data) must land here instead of CWD.
func WorkspaceDir() string {
	if env := os.Getenv("AGSH_WORKSPACE"); env != "" {
		return ExpandHome(env)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "agsh_workspace")
}

// EnsureWorkspace creates the workspace directory if it does not exist.
// Called once at startup so write_file never fails on a missing directory.
func EnsureWorkspace() error {
	return os.MkdirAll(WorkspaceDir(), 0o755)
}

// ExpandHome replaces a leading "~/" or a bare "~" with the user's home directory.
// Returns path unchanged if it does not start with "~".
//
// Expectations:
//   - Expands "~/foo" to "<home>/foo"
//   - Expands bare "~" to "<home>"
//   - Returns path unchanged when it does not start with "~"
//   - Returns path unchanged for "/absolute/path"
func ExpandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// ResolveOutputPath redirects bare filenames and "./" relative paths to the
// workspace directory. Paths that contain a directory component (e.g.
// "internal/foo/bar.go", "/tmp/out.txt", "~/Documents/report.md") are returned
// unchanged — the model or user explicitly named a location.
//
// Call ExpandHome on the path before calling this function so that "~/" paths
// have a real directory component and are not accidentally redirected.
//
// Expectations:
//   - Bare filename ("fetch_news.py") → redirected to workspace
//   - "./" prefix ("./output.txt") → redirected to workspace
//   - Path with dir component ("internal/roles/foo.go") → not redirected
//   - Absolute path ("/tmp/out.txt") → not redirected
//   - Workspace-rooted path (already under WorkspaceDir()) → not redirected
func ResolveOutputPath(path string) (resolved string, redirected bool) {
	clean := filepath.Clean(path)
	if filepath.Dir(clean) == "." {
		return filepath.Join(WorkspaceDir(), clean), true
	}
	return path, false
}
