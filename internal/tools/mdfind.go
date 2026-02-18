package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// RunMdfind searches for files using macOS Spotlight (mdfind -name).
// It matches any file whose name contains query (case-insensitive, Spotlight default).
// Results are newline-separated absolute paths.
//
// This is dramatically faster than find/glob for home-directory searches because
// Spotlight maintains a persistent filesystem index — typical response < 1 second
// vs several minutes for a full recursive walk of ~/.
//
// Spotlight quirk: mdfind -name sometimes fails to match when the query includes
// a file extension (especially for CJK filenames). If no results, we retry with
// the extension stripped and post-filter by extension.
func RunMdfind(ctx context.Context, query string) (string, error) {
	result, err := runMdfindQuery(ctx, query)
	if err != nil {
		return "", err
	}
	if result != "" {
		return result, nil
	}

	// Retry without extension if query has one (Spotlight quirk with CJK + extension)
	ext := filepath.Ext(query)
	if ext != "" {
		stem := strings.TrimSuffix(query, ext)
		result2, err2 := runMdfindQuery(ctx, stem)
		if err2 == nil && result2 != "" {
			// Post-filter: keep only lines ending with the original extension
			var filtered []string
			for _, line := range strings.Split(result2, "\n") {
				if strings.HasSuffix(line, ext) {
					filtered = append(filtered, line)
				}
			}
			if len(filtered) > 0 {
				return strings.Join(filtered, "\n"), nil
			}
		}
	}

	return fmt.Sprintf("(no files found with name matching %q)", query), nil
}

func runMdfindQuery(ctx context.Context, query string) (string, error) {
	cmd := fmt.Sprintf("mdfind -name %s 2>/dev/null", shellQuote(query))
	stdout, _, err := RunShell(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("mdfind: %w", err)
	}
	return strings.TrimSpace(stdout), nil
}

// shellQuote wraps s in single quotes for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
