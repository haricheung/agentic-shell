package tools

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

const defaultShellTimeout = 30 * time.Second

// RunShell executes cmd in a bash shell with a default 30s timeout.
// Returns stdout, stderr, and any execution error.
func RunShell(ctx context.Context, cmd string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(ctx, defaultShellTimeout)
	defer cancel()

	c := exec.CommandContext(ctx, "bash", "-c", cmd)

	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf

	err = c.Run()
	return outBuf.String(), errBuf.String(), err
}
