package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Claude adapts the Claude Code CLI for chomper.
type Claude struct{}

// Name returns "claude".
func (c *Claude) Name() string { return "claude" }

// RunWorker invokes `claude --print --enable-auto-mode` with the prompt
// on stdin and the worktree as cwd. Output is captured (claude --print
// buffers anyway) so the caller can stop any active spinner before
// printing — avoiding the stderr/stdout collision the bash prototype hit.
func (c *Claude) RunWorker(ctx context.Context, repoDir, prompt string) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH")
	}
	cmd := exec.CommandContext(ctx, "claude", "--print", "--enable-auto-mode")
	cmd.Dir = repoDir
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
