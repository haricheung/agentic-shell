package tools

import (
	"context"
	"os/exec"
	"strings"
)

// RunShortcut runs a named Apple Shortcut using the macOS Shortcuts CLI.
// Shortcuts sync via iCloud so this can trigger automations on iPhone/iPad/Apple Watch.
// input is passed as stdin to the shortcut; pass "" if the shortcut needs no input.
func RunShortcut(ctx context.Context, name, input string) (string, error) {
	args := []string{"run", name}
	cmd := exec.CommandContext(ctx, "shortcuts", args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}

	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(ee.Stderr))
			if stderr != "" {
				return "", &ShortcutError{Name: name, Stderr: stderr, Err: err}
			}
		}
		return "", &ShortcutError{Name: name, Err: err}
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// ShortcutError wraps a Shortcuts CLI failure.
type ShortcutError struct {
	Name   string
	Stderr string
	Err    error
}

func (e *ShortcutError) Error() string {
	if e.Stderr != "" {
		return "shortcut '" + e.Name + "': " + e.Stderr
	}
	return "shortcut '" + e.Name + "': " + e.Err.Error()
}
