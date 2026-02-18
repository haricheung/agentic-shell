package tools

import (
	"context"
	"os/exec"
	"strings"
)

// RunAppleScript executes an AppleScript via osascript and returns stdout.
// The script is passed via stdin so it can contain arbitrary quoting without
// shell escaping issues.
func RunAppleScript(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-")
	cmd.Stdin = strings.NewReader(script)

	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", &AppleScriptError{Stderr: string(ee.Stderr), Err: err}
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// AppleScriptError wraps an osascript failure with the error output.
type AppleScriptError struct {
	Stderr string
	Err    error
}

func (e *AppleScriptError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.Err.Error()
}
