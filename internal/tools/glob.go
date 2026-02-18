package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// GlobFiles walks root recursively and returns paths whose base name matches
// pattern (standard filepath.Match syntax: *.go, *.json, etc.).
// root supports ~ / ~/ prefix (expanded to the user's home directory).
// If root is empty, it defaults to ".".
// Inaccessible directories are silently skipped.
func GlobFiles(root, pattern string) ([]string, error) {
	if root == "" {
		root = "."
	}
	// Expand leading ~ so callers can pass "~", "~/Downloads", etc.
	if root == "~" || strings.HasPrefix(root, "~/") || strings.HasPrefix(root, "~\\") {
		home, err := os.UserHomeDir()
		if err == nil {
			root = home + root[1:]
		}
	}

	var matches []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if d.IsDir() {
			return nil
		}
		matched, _ := filepath.Match(pattern, d.Name())
		if matched {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}

// GlobJoin returns the matched paths as a newline-separated string,
// ready to be returned as a tool result.
func GlobJoin(paths []string) string {
	return strings.Join(paths, "\n")
}
