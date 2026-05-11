// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harness

import (
	"io"
	"os/exec"
)

// WorkerProcess wraps a running harness subprocess in streaming mode.
// The caller owns the process and is responsible for:
//   - Reading from Stdout (line-delimited stream-json events)
//   - Optionally writing to Stdin (for tool_result injection)
//   - Calling Cmd.Wait() when done
//   - Calling Cmd.Process.Kill() to abort early
//
// The supervisor (internal/harness.Supervisor) is the only intended consumer.
type WorkerProcess struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}
