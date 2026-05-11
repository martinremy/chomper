// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package chomper

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/martinremy/chomper/internal/config"
	"github.com/martinremy/chomper/internal/gh"
)

// RunDoctor inspects the local environment and reports per-check status.
// Returns 0 on no hard failures, 1 if anything required is missing.
//
// Same intent as the bash v0.1 doctor: catch the "I cloned chomper but
// gh isn't authenticated / my config has a typo / claude is missing"
// class of problems before they bite mid-run.
func RunDoctor(ctx context.Context) int {
	errors, warns := 0, 0

	pass := func(msg string) { fmt.Printf("  [ok]   %s\n", msg) }
	fail := func(msg string) { fmt.Fprintf(os.Stderr, "  [FAIL] %s\n", msg); errors++ }
	miss := func(msg string) { fmt.Fprintf(os.Stderr, "  [warn] %s\n", msg); warns++ }
	info := func(msg string) { fmt.Printf("  [info] %s\n", msg) }

	exe, err := os.Executable()
	if err != nil {
		exe = "<unknown>"
	}
	fmt.Printf("chomper doctor (source: %s)\n\n", exe)

	fmt.Println("Core dependencies:")
	checkBinaryVersion(ctx, "git", "", pass, fail)
	checkBinaryVersion(ctx, "gh", " (install: https://cli.github.com/)", pass, fail)
	fmt.Println()

	fmt.Println("Harness CLIs (at least one required):")
	haveHarness := false
	for _, name := range []string{"claude", "codex"} {
		if _, err := exec.LookPath(name); err == nil {
			pass(name + " available")
			haveHarness = true
		} else {
			miss(name + " not installed")
		}
	}
	if !haveHarness {
		fail("no harness installed; chomper cannot drive an agent")
	}
	fmt.Println()

	fmt.Println("Repo context:")
	insideRepo := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree").Run() == nil
	if !insideRepo {
		miss("not currently inside a git repo (chomper needs to be run from one)")
	} else {
		client := gh.New()
		host := client.CurrentHost(ctx)
		if err := exec.CommandContext(ctx, "gh", "auth", "status", "-h", host).Run(); err == nil {
			pass("gh authenticated for " + host)
		} else {
			fail(fmt.Sprintf("gh not authenticated for %s (run: gh auth login -h %s)", host, host))
		}

		if repo, err := client.CurrentRepo(ctx); err == nil && repo != "" {
			pass("repo: " + repo)
		} else {
			miss("git repo here, but no GitHub remote visible to gh")
		}

		if _, err := os.Stat(".chomper.yaml"); err == nil {
			if _, err := config.Load(); err == nil {
				pass(".chomper.yaml parses cleanly")
			} else {
				fail(".chomper.yaml: " + err.Error())
			}
		} else {
			info(".chomper.yaml: not present (built-in defaults will be used)")
		}
	}
	fmt.Println()

	fmt.Println("Worktree base:")
	const wtRoot = "/tmp/chomper-worktrees"
	if err := os.MkdirAll(wtRoot, 0o755); err != nil {
		fail("cannot create " + wtRoot + ": " + err.Error())
	} else {
		// Probe-write a sentinel file; if we can create and delete it,
		// the dir is genuinely writable (not just permission-bit
		// readable from outside).
		probe := filepath.Join(wtRoot, ".chomper-doctor-probe")
		f, err := os.Create(probe)
		if err != nil {
			fail(wtRoot + " not writable: " + err.Error())
		} else {
			f.Close()
			_ = os.Remove(probe)
			pass(wtRoot + " writable")
		}
	}
	fmt.Println()

	switch {
	case errors == 0 && warns == 0:
		fmt.Println("All checks passed.")
		return 0
	case errors > 0:
		fmt.Fprintf(os.Stderr, "%d error(s), %d warning(s). Fix errors before running chomper.\n", errors, warns)
		return 1
	default:
		fmt.Printf("%d warning(s); chomper should still work.\n", warns)
		return 0
	}
}

// checkBinaryVersion verifies a CLI is on PATH and reports its version.
// installHint is appended to the failure message (e.g., a link).
func checkBinaryVersion(ctx context.Context, name, installHint string,
	pass, fail func(string)) {
	if _, err := exec.LookPath(name); err != nil {
		fail(name + " not found" + installHint)
		return
	}
	out, _ := exec.CommandContext(ctx, name, "--version").Output()
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if first == "" {
		first = "(version unknown)"
	}
	pass(name + " (" + first + ")")
}
