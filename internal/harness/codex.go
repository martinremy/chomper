package harness

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Codex adapts the OpenAI Codex CLI for chomper.
type Codex struct{}

// Name returns "codex".
func (c *Codex) Name() string { return "codex" }

// RunWorker invokes `codex exec` with the prompt on stdin (the trailing
// `-` tells codex to read instructions from stdin). `-a on-request`
// matches the bash prototype's autonomy posture.
func (c *Codex) RunWorker(ctx context.Context, repoDir, prompt string) (string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return "", fmt.Errorf("codex CLI not found on PATH")
	}
	cmd := exec.CommandContext(ctx, "codex", "exec",
		"-a", "on-request",
		"--search",
		"-",
	)
	cmd.Dir = repoDir
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
