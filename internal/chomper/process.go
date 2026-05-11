// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package chomper implements the per-issue orchestrator.
//
// The first Go slice implements the direct-mode pipeline: fetch trunk,
// create worktree, run harness, poll for PR, poll CI, merge, confirm
// merge, clean up. Auto-answer (stream-json supervisor) and the
// review-wait loop land in subsequent slices.
package chomper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/martinremy/chomper/internal/config"
	"github.com/martinremy/chomper/internal/gh"
	"github.com/martinremy/chomper/internal/git"
	"github.com/martinremy/chomper/internal/harness"
	"github.com/martinremy/chomper/internal/judge"
	"github.com/martinremy/chomper/internal/prompt"
	"github.com/martinremy/chomper/internal/tui"
)

// Deps bundles the runtime collaborators ProcessIssue needs.
type Deps struct {
	Cfg     *config.Config
	Repo    string // "owner/name"
	GH      *gh.Client
	Harness harness.Harness
}

// ProcessIssue walks one open issue through to a merged PR (or to a
// preserved worktree on any failure). Returns an error only on a
// programming error / unrecoverable infra failure; per-issue failures
// (no PR opened, CI red, merge refused) log a warning and return nil
// so the outer loop continues to the next issue.
func ProcessIssue(ctx context.Context, deps *Deps, issue gh.Issue) error {
	branch := BranchForIssue(issue.Number)
	worktreeDir := filepath.Join("/tmp/chomper-worktrees", deps.Repo, fmt.Sprintf("issue-%d", issue.Number))

	fmt.Printf("--- Working issue #%d: %s ---\n", issue.Number, issue.Title)

	// PR-link footer + inter-issue separator. Closes over prNumber so
	// every exit path that landed on a PR (success, all preserve-and-
	// warn aborts, skip-due-to-closed/merged) ends with a clickable URL.
	// Paths that never got a PR (no-PR-opened, stale-local skip) leave
	// prNumber = 0 and only the blank line fires — keeping issue
	// boundaries visually consistent regardless of outcome. Host is
	// fetched lazily so we don't pay the git-remote shell-out when
	// there's no PR to link to.
	var prNumber int
	defer func() {
		if prNumber > 0 {
			fmt.Printf("view PR: %s\n", gh.PRURL(deps.GH.CurrentHost(ctx), deps.Repo, prNumber))
		}
		fmt.Println()
	}()

	// Resume detection. Maps observed (PR state, worktree, branch) state
	// to an Action; Decide is pure logic in resume.go and is unit-tested
	// against the 7-row state table from issue #1.
	facts, err := GatherResumeFacts(ctx, deps.GH, branch, worktreeDir)
	if err != nil {
		warn("could not query resume state for issue #%d; skipping: %s", issue.Number, err)
		return nil
	}
	action := Decide(facts)

	switch action {
	case ActionFresh:
		// Fall through to fresh-flow setup below.

	case ActionResumeReuseWorktree:
		fmt.Printf("resume: reattaching to PR #%d (existing worktree at %s)\n", facts.PRNumber, worktreeDir)
		prNumber = facts.PRNumber

	case ActionResumeRebuildWorktree:
		fmt.Printf("resume: reattaching to PR #%d (rebuilding worktree from origin/%s)\n", facts.PRNumber, branch)
		if err := rebuildWorktreeFromOrigin(ctx, branch, worktreeDir); err != nil {
			warn("resume rebuild failed for issue #%d: %s", issue.Number, err)
			return nil
		}
		prNumber = facts.PRNumber

	case ActionSkipPRClosed:
		warn("PR #%d for issue #%d was closed without merging; skipping. Re-open the PR to resume, or delete the branch on origin to retry from scratch.", facts.PRNumber, issue.Number)
		prNumber = facts.PRNumber
		return nil

	case ActionSkipPRMerged:
		warn("PR #%d for issue #%d was already merged but the issue is still open (likely missing `Closes #%d` in the PR body); skipping. Close the issue manually if appropriate.", facts.PRNumber, issue.Number, issue.Number)
		prNumber = facts.PRNumber
		return nil

	case ActionSkipStaleLocal:
		// No PR on the branch, but local state from a prior aborted run
		// is in the way. Preserve today's exact recovery hints so users
		// with muscle memory see the same commands.
		if facts.WorktreeExists {
			warn("worktree already exists at %s; skipping issue #%d. To recover: git worktree remove --force %s", worktreeDir, issue.Number, worktreeDir)
		} else {
			warn("branch %s already exists; skipping issue #%d. To recover: git branch -D %s", branch, issue.Number, branch)
		}
		return nil
	}

	// Fetch issue detail. Needed by:
	//   - Fresh: rendering the harness prompt
	//   - Resume: feeding ReviewLoop's judge context if reviews are configured
	// Cheap either way (one gh call per issue) and keeps the path uniform.
	detail, err := deps.GH.IssueDetail(ctx, issue.Number)
	if err != nil {
		warn("could not fetch detail for issue #%d; preserving worktree at %s: %s", issue.Number, worktreeDir, err)
		return nil
	}

	// Fresh-only: create the worktree from trunk, run the harness, and
	// poll for the PR to open. Resume paths skip all of this — they
	// already know the PR number and the worktree is in place.
	if action == ActionFresh {
		if err := git.Fetch(ctx, "origin", deps.Cfg.TrunkBranch); err != nil {
			warn("failed to fetch origin/%s; skipping issue #%d: %s", deps.Cfg.TrunkBranch, issue.Number, err)
			return nil
		}
		if err := git.WorktreeAdd(ctx, worktreeDir, branch, "origin/"+deps.Cfg.TrunkBranch); err != nil {
			warn("failed to create worktree at %s; skipping issue #%d: %s", worktreeDir, issue.Number, err)
			return nil
		}

		promptText := prompt.BuildIssuePrompt(prompt.Issue{
			Number:   detail.Number,
			Title:    detail.Title,
			Body:     detail.Body,
			Comments: convertComments(detail.Comments),
		})

		// Phase 1: harness work. Two paths:
		//   - direct mode (auto_answer=false): capture output, print after spinner
		//   - auto-answer mode (auto_answer=true): route through Supervisor,
		//     which streams formatted events live and handles silence/questions
		//     via the judge. No spinner — supervisor owns its own UI.
		if deps.Cfg.AutoAnswer {
			sup := harness.NewSupervisor(harness.SupervisorConfig{
				Harness:          deps.Harness,
				JudgeModel:       deps.Cfg.AutoAnswerModel,
				SilenceThreshold: time.Duration(deps.Cfg.AutoAnswerSilenceS) * time.Second,
				MaxQuestions:     deps.Cfg.AutoAnswerMaxQuestions,
				IssueCtx: judge.IssueContext{
					Repo:   deps.Repo,
					Number: detail.Number,
					Title:  detail.Title,
					Body:   detail.Body,
				},
			})
			if err := sup.Run(ctx, worktreeDir, promptText); err != nil {
				warn("auto-answer supervisor failed for issue #%d; preserving worktree at %s: %s", issue.Number, worktreeDir, err)
				return nil
			}
		} else {
			var output string
			err = tui.With(fmt.Sprintf("%s coding on issue #%d", deps.Harness.Name(), issue.Number), func() error {
				var hErr error
				output, hErr = deps.Harness.RunWorker(ctx, worktreeDir, promptText)
				return hErr
			})
			if output != "" {
				fmt.Println(output)
			}
			if err != nil {
				warn("harness exited non-zero for issue #%d; preserving worktree at %s: %s", issue.Number, worktreeDir, err)
				return nil
			}
		}

		// Phase 2: poll for the PR to appear.
		err = tui.With(fmt.Sprintf("waiting for PR on branch %s", branch), func() error {
			var pErr error
			prNumber, pErr = deps.GH.WaitForPR(ctx, branch, 60*time.Second)
			return pErr
		})
		if err != nil {
			warn("no PR opened on branch %s; preserving worktree at %s for inspection", branch, worktreeDir)
			return nil
		}
	}

	// --- Resume boundary ---------------------------------------------------
	// Both fresh and resume paths converge here with prNumber set. Everything
	// below MUST be idempotent: resume re-enters the pipeline at this exact
	// line when reattaching to an existing open PR. Any new phase added below
	// must be safe to re-run after a prior interrupted run — queries against
	// PR/CI/review state are fine; uncommitted file mutations or
	// branch-only-local commits are not.

	fmt.Printf("found PR #%d for issue #%d\n", prNumber, issue.Number)

	// Phase 3: poll CI to green.
	err = tui.With(fmt.Sprintf("polling CI on PR #%d", prNumber), func() error {
		return deps.GH.WaitForChecks(ctx, prNumber, time.Duration(deps.Cfg.CITimeoutMinutes)*time.Minute)
	})
	if err != nil {
		// Branch on the typed error from gh.WaitForChecks. ErrCIFailed
		// is the only state the fix loop can act on; everything else
		// (timeout, unknown) preserves + warns and lets the user resume
		// or intervene manually.
		if errors.Is(err, gh.ErrCIFailed) && deps.Cfg.FixCI.Enabled {
			if FixCILoop(ctx, deps, prNumber, detail, worktreeDir) == FixCIAbort {
				return nil
			}
			// FixCIProceed: CI is now green; fall through to reviews+merge.
		} else {
			warn("%s", ciFailureWarning(err, prNumber, deps.Cfg.CITimeoutMinutes, worktreeDir))
			return nil
		}
	} else {
		fmt.Printf("CI passed on PR #%d\n", prNumber)
	}

	// Phase 3.5: wait for reviews (if any reviewers configured).
	// ReviewLoop owns the iteration cap + judge adjudication + fix
	// loop; it returns Proceed (merge) or Abort (preserve + skip).
	if len(deps.Cfg.WaitForReviews.Reviewers) > 0 {
		if ReviewLoop(ctx, deps, prNumber, detail, worktreeDir) == ReviewAbort {
			return nil
		}
	}

	// Phase 4: merge.
	err = tui.With(fmt.Sprintf("merging PR #%d", prNumber), func() error {
		return deps.GH.MergePR(ctx, prNumber, deps.Cfg.MergeStrategy)
	})
	if err != nil {
		warn("merge command failed for PR #%d; preserving worktree at %s for inspection: %s", prNumber, worktreeDir, err)
		return nil
	}

	// Phase 5: confirm the merge actually landed (vs. just being queued
	// by auto-merge under branch protection).
	err = tui.With(fmt.Sprintf("confirming merge of PR #%d", prNumber), func() error {
		return deps.GH.WaitForMerged(ctx, prNumber, 60*time.Second)
	})
	if err != nil {
		warn("PR #%d did not land within 60s — auto-merge may be queued by branch protection. Preserving worktree at %s for inspection.", prNumber, worktreeDir)
		return nil
	}

	// Success: tear down worktree, delete local + remote branches.
	if err := git.WorktreeRemove(ctx, worktreeDir); err != nil {
		warn("failed to remove worktree at %s; clean up manually: rm -rf %s && git worktree prune", worktreeDir, worktreeDir)
	}
	_ = git.DeleteBranch(ctx, branch)
	git.DeleteRemoteBranch(ctx, branch)

	fmt.Println()
	fmt.Printf("✓ Done with issue #%d (merged via PR #%d)\n", issue.Number, prNumber)
	return nil
}

func convertComments(in []gh.Comment) []prompt.Comment {
	out := make([]prompt.Comment, 0, len(in))
	for _, c := range in {
		out = append(out, prompt.Comment{
			Author: c.Author.Login,
			Body:   c.Body,
		})
	}
	return out
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

// ciFailureWarning composes the warning text emitted when a CI poll
// returns a terminal error. Branches on the typed errors from the gh
// package so users can tell whether re-running chomper will help
// (ErrCITimeout: yes — resume picks the poll back up; ErrCIFailed: not
// until the failing checks are fixed). Unknown error types fall through
// to a generic preserve-and-inspect message.
func ciFailureWarning(err error, prNumber, timeoutMinutes int, worktreeDir string) string {
	switch {
	case errors.Is(err, gh.ErrCIFailed):
		return fmt.Sprintf(
			"CI is failing for PR #%d; preserving worktree at %s for inspection. "+
				"Fix the failing checks and re-run chomper to continue.",
			prNumber, worktreeDir,
		)
	case errors.Is(err, gh.ErrCITimeout):
		return fmt.Sprintf(
			"CI did not finish for PR #%d within %dm; preserving worktree at %s. "+
				"Re-run chomper to continue polling, or raise ci_timeout_minutes if your CI runs are routinely longer.",
			prNumber, timeoutMinutes, worktreeDir,
		)
	default:
		return fmt.Sprintf(
			"CI poll for PR #%d ended with an unexpected error: %s; preserving worktree at %s for inspection.",
			prNumber, err, worktreeDir,
		)
	}
}
