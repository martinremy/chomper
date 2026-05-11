// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

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

// RunWorker invokes `codex exec` with the prompt on stdin. The trailing
// `-` tells codex to read instructions from stdin per its --help text.
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

// RunWorkerStream is not supported for codex today: the CLI doesn't
// expose a stream-json equivalent that lets us inject tool_result
// events mid-run. Users who need auto-answer should choose claude as
// the worker; chomper surfaces this as a config error rather than
// silently degrading.
func (c *Codex) RunWorkerStream(ctx context.Context, repoDir, prompt string) (*WorkerProcess, error) {
	return nil, fmt.Errorf("auto-answer is not supported with the codex harness in v0.5 (use harness: claude with auto_answer, or auto_answer: false with codex)")
}

// RunJudge invokes `codex exec` in a constrained mode (no approvals,
// empty sandbox) for one-shot judge calls. Codex has no
// --append-system-prompt flag the way claude does, so the system
// prompt is prepended to the user prompt with a clear separator.
func (c *Codex) RunJudge(ctx context.Context, systemPrompt, userPrompt, model string) (string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return "", fmt.Errorf("codex CLI not found on PATH")
	}
	args := []string{"exec"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "-a", "never", "-c", "sandbox_permissions=[]", "-")
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Stdin = strings.NewReader(systemPrompt + "\n\n---\n\n" + userPrompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codex judge: %w", err)
	}
	return string(out), nil
}
