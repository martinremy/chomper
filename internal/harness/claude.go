// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// RunWorkerStream launches `claude` in stream-json I/O mode. The
// initial prompt is sent as the first user event; stdin stays open
// for the Supervisor to inject tool_result events later.
//
// Important behavioral fix vs. v0.1-bash: in bash the worker adapter
// redirected stdin from a file (`< prompt.md`), which closed stdin
// after the prompt was read. judge.py's tool_result injection had
// no destination — the question-answering path was silently broken.
// In Go, we cmd.StdinPipe(), write the prompt, and keep the pipe open.
func (c *Claude) RunWorkerStream(ctx context.Context, repoDir, prompt string) (*WorkerProcess, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude CLI not found on PATH")
	}
	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--enable-auto-mode",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
	)
	cmd.Dir = repoDir
	cmd.Stderr = os.Stderr // pass harness errors through

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Send the initial prompt as a stream-json user event.
	initial := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
	}
	encoded, err := json.Marshal(initial)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("encode initial event: %w", err)
	}
	if _, err := stdin.Write(append(encoded, '\n')); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("write initial event: %w", err)
	}

	return &WorkerProcess{Cmd: cmd, Stdin: stdin, Stdout: stdout}, nil
}

// RunJudge invokes `claude` as a one-shot text judge with no tools,
// no session persistence, and an injected system prompt. This is the
// "ask Haiku a question and get a single answer" entry point.
//
// `--allowedTools ""` collapses the judge's action space to "emit
// text" — a deliberate capability-removal: without it, a judge could
// try to call AskUserQuestion, which would recurse into the very
// problem we're using the judge to solve.
func (c *Claude) RunJudge(ctx context.Context, systemPrompt, userPrompt, model string) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", fmt.Errorf("claude CLI not found on PATH")
	}
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	cmd := exec.CommandContext(ctx, "claude",
		"--print",
		"--model", model,
		"--output-format", "text",
		"--no-session-persistence",
		"--allowedTools", "",
		"--append-system-prompt", systemPrompt,
	)
	cmd.Stdin = strings.NewReader(userPrompt)
	out, err := cmd.Output()
	if err != nil {
		// Surface stderr if available (gh-cli style)
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("claude judge: %w (%s)", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("claude judge: %w", err)
	}
	return string(out), nil
}
