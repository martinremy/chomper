package chomper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/martinremy/chomper/internal/gh"
	"github.com/martinremy/chomper/internal/prompt"
	"github.com/martinremy/chomper/internal/tui"
)

// FixCIResult is what FixCILoop returns to its caller (ProcessIssue).
type FixCIResult int

const (
	// FixCIProceed: CI is now green; continue the pipeline to reviews/merge.
	FixCIProceed FixCIResult = iota
	// FixCIAbort: do NOT merge; preserve worktree. The loop has already
	// emitted the appropriate warning.
	FixCIAbort
)

// fixCIIterDecision is the outcome of one iteration's CI re-poll.
// Kept package-private — the integration loop is the only caller.
type fixCIIterDecision int

const (
	fixCIContinue fixCIIterDecision = iota
	fixCIProceedDecision
	fixCIAbortDecision
)

// decideFixCIIter is the pure-logic decision after one fix iteration's
// CI poll. Pulled out from the integration loop so the state-table is
// unit-testable (mirrors how Decide pairs with ProcessIssue).
//
// Only ErrCIFailed counts as "keep trying" — ErrCITimeout means CI
// hasn't finished, which the fix loop can't help with, and unknown
// errors are aborted conservatively.
func decideFixCIIter(ciErr error, iter, maxIter int) fixCIIterDecision {
	if ciErr == nil {
		return fixCIProceedDecision
	}
	if !errors.Is(ciErr, gh.ErrCIFailed) {
		return fixCIAbortDecision
	}
	if iter >= maxIter {
		return fixCIAbortDecision
	}
	return fixCIContinue
}

// FixCILoop attempts to auto-fix failing CI by re-invoking the harness
// with failed-check context and re-polling CI, up to MaxIterations
// times. Entered only when WaitForChecks returns an ErrCIFailed and
// fix_ci.enabled is true.
//
// Shape mirrors ReviewLoop: each iteration is (fetch context, build
// prompt, run harness, re-poll CI), terminating on green, exhaustion,
// or unrecoverable error. Warnings are emitted by the loop; callers
// should not log around this call.
func FixCILoop(ctx context.Context, deps *Deps, prNumber int, detail *gh.FullIssue, worktreeDir string) FixCIResult {
	maxIter := deps.Cfg.FixCI.MaxIterations

	for iter := 1; iter <= maxIter; iter++ {
		fmt.Printf("CI-fix iteration %d/%d\n", iter, maxIter)

		var failed []gh.FailedCheck
		err := tui.With(fmt.Sprintf("fetching failed-check context for PR #%d", prNumber), func() error {
			var fErr error
			failed, fErr = deps.GH.FailedCheckContext(ctx, prNumber)
			return fErr
		})
		if err != nil {
			warn("could not fetch failed-check context on CI-fix iter %d: %s. Preserving worktree at %s.", iter, err, worktreeDir)
			return FixCIAbort
		}
		// Empty here means the CI poll said red but `gh pr checks` now
		// shows no failures — a race we can't act on. Abort with a hint.
		if len(failed) == 0 {
			warn("CI-fix iter %d: CI poll reported failure but no failing checks visible now; preserving worktree at %s for manual inspection", iter, worktreeDir)
			return FixCIAbort
		}

		fixPrompt := prompt.BuildCIFixPrompt(
			prompt.Issue{
				Number: detail.Number,
				Title:  detail.Title,
				Body:   detail.Body,
			},
			prNumber, iter, maxIter,
			convertFailedChecks(failed),
		)

		err = tui.With(fmt.Sprintf("%s addressing CI failure (iter %d)", deps.Harness.Name(), iter), func() error {
			_, hErr := deps.Harness.RunWorker(ctx, worktreeDir, fixPrompt)
			return hErr
		})
		if err != nil {
			warn("harness exited non-zero on CI-fix iteration %d; preserving worktree at %s: %s", iter, worktreeDir, err)
			return FixCIAbort
		}

		ciErr := tui.With(fmt.Sprintf("polling CI on PR #%d (after CI-fix %d)", prNumber, iter), func() error {
			return deps.GH.WaitForChecks(ctx, prNumber, time.Duration(deps.Cfg.CITimeoutMinutes)*time.Minute)
		})

		switch decideFixCIIter(ciErr, iter, maxIter) {
		case fixCIProceedDecision:
			fmt.Printf("CI passed on PR #%d after CI-fix iteration %d\n", prNumber, iter)
			return FixCIProceed
		case fixCIAbortDecision:
			// Two reasons to land here: cap reached, or non-fixable error
			// (timeout / unknown). The warning text branches via
			// ciFailureWarning, which already covers both.
			warn("CI-fix iteration %d: %s", iter, ciFailureWarning(ciErr, prNumber, deps.Cfg.CITimeoutMinutes, worktreeDir))
			return FixCIAbort
		case fixCIContinue:
			// Loop to next iteration.
		}
	}

	// Loop body's iter==maxIter branch returns FixCIAbort directly via
	// decideFixCIIter, so this is only reached when maxIter == 0 (which
	// Validate prevents when fix_ci.enabled is true). Defensive return.
	return FixCIAbort
}

// convertFailedChecks maps gh.FailedCheck -> prompt.FailedCheck to keep
// the prompt package free of any gh dependency. Same pattern as
// convertComments in process.go.
func convertFailedChecks(in []gh.FailedCheck) []prompt.FailedCheck {
	out := make([]prompt.FailedCheck, 0, len(in))
	for _, fc := range in {
		out = append(out, prompt.FailedCheck{Name: fc.Name, LogTail: fc.LogTail})
	}
	return out
}
