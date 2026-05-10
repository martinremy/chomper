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

	// RunWorker invokes the agent on an issue prompt. The agent is
	// expected to: edit code in repoDir, commit, push, open a PR. It
	// returns the agent's captured stdout+stderr (printed back by the
	// caller after any spinner is stopped) and an error if the
	// subprocess exited non-zero or was cancelled.
	RunWorker(ctx context.Context, repoDir, prompt string) (output string, err error)
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
