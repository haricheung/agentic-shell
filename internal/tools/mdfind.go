package tools

import (
	"context"
	"fmt"
	"strings"
)

// RunMdfind searches for files using macOS Spotlight (mdfind -name).
// It matches any file whose name contains query (case-insensitive, Spotlight default).
// Results are newline-separated absolute paths.
//
// This is dramatically faster than find/glob for home-directory searches because
// Spotlight maintains a persistent filesystem index — typical response < 1 second
// vs several minutes for a full recursive walk of ~/.
func RunMdfind(ctx context.Context, query string) (string, error) {
	// -name restricts to filename match (no full-text content scan, faster).
	// Shell-quote the query to handle spaces and CJK characters safely.
	cmd := fmt.Sprintf("mdfind -name %s 2>/dev/null", shellQuote(query))
	stdout, _, err := RunShell(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("mdfind: %w", err)
	}
	result := strings.TrimSpace(stdout)
	if result == "" {
		return fmt.Sprintf("(no files found with name matching %q)", query), nil
	}
	return result, nil
}

// shellQuote wraps s in single quotes for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
