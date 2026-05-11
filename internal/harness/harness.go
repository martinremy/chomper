// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package harness abstracts the AI coding CLI chomper drives.
//
// First Go slice exposes direct-mode RunWorker only: invoke the CLI in
// --print mode with a prompt on stdin, capture the buffered output,
// return when it exits. Stream-json supervision + the judge calls will
// land in the next slice.
package harness

import (
	"context"
	"fmt"
)

// Harness is what every AI coding agent CLI exposes to chomper.
//
// Adding a new harness is a matter of (a) implementing this interface
// in a new file and (b) adding a case to New. The orchestrator only
// sees the interface.
type Harness interface {
	// Name returns the CLI's binary name on PATH ("claude", "codex").
	Name() string

	// RunWorker invokes the agent in direct (non-streaming) mode. The
	// agent is expected to: edit code in repoDir, commit, push, open a
	// PR. RunWorker returns the agent's captured stdout+stderr (printed
	// back by the caller after any spinner is stopped) and an error if
	// the subprocess exited non-zero or was cancelled.
	RunWorker(ctx context.Context, repoDir, prompt string) (output string, err error)

	// RunWorkerStream is the streaming-I/O counterpart of RunWorker for
	// use by the Supervisor. The returned process keeps stdin open
	// (for tool_result injection) and emits line-delimited stream-json
	// events on stdout. The initial prompt is sent as the first
	// stream-json user event before the function returns.
	//
	// Harnesses that don't support stream-json (codex today) return an
	// error; the supervisor surfaces this so chomper can refuse to
	// enable auto-answer for that harness.
	RunWorkerStream(ctx context.Context, repoDir, prompt string) (*WorkerProcess, error)

	// RunJudge invokes the harness as a one-shot text judge with no
	// tool access. The Supervisor uses it for silence classification
	// and question answering; the review loop uses it for review
	// adjudication. All three roles share this single entry point and
	// differ only in their system prompt.
	//
	// model may be empty — the adapter picks a sensible cheap default
	// (e.g., Haiku for claude).
	RunJudge(ctx context.Context, systemPrompt, userPrompt, model string) (string, error)
}

// New returns the harness implementation matching name.
func New(name string) (Harness, error) {
	switch name {
	case "claude":
		return &Claude{}, nil
	case "codex":
		return &Codex{}, nil
	default:
		return nil, fmt.Errorf("unknown harness: %q (use claude or codex)", name)
	}
}
