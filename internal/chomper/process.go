// Package chomper implements the per-issue orchestrator.
//
// The first Go slice implements the direct-mode pipeline: fetch trunk,
// create worktree, run harness, poll for PR, poll CI, merge, confirm
// merge, clean up. Auto-answer (stream-json supervisor) and the
// review-wait loop land in subsequent slices.
package chomper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/martinremy/chomper/internal/config"
	"github.com/martinremy/chomper/internal/gh"
	"github.com/martinremy/chomper/internal/git"
	"github.com/martinremy/chomper/internal/harness"
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
	branch := fmt.Sprintf("fix/issue-%d", issue.Number)
	worktreeDir := filepath.Join("/tmp/chomper-worktrees", deps.Repo, fmt.Sprintf("issue-%d", issue.Number))

	fmt.Printf("--- Working issue #%d: %s ---\n", issue.Number, issue.Title)

	// Stale worktree: refuse and warn (per the design decision —
	// don't auto-clean state the user may be inspecting).
	if _, err := os.Stat(worktreeDir); err == nil {
		warn("worktree already exists at %s; skipping issue #%d. To recover: git worktree remove --force %s", worktreeDir, issue.Number, worktreeDir)
		return nil
	}

	// Branch already exists locally (probably from a prior aborted run).
	if git.BranchExists(ctx, branch) {
		warn("branch %s already exists; skipping issue #%d. To recover: git branch -D %s", branch, issue.Number, branch)
		return nil
	}

	// Refresh origin/trunk and create the worktree from it. The fetch
	// happens against the user's main checkout (no working-tree mutation
	// there — just a ref update), and the worktree is rooted at the
	// just-refreshed remote-tracking ref.
	if err := git.Fetch(ctx, "origin", deps.Cfg.TrunkBranch); err != nil {
		warn("failed to fetch origin/%s; skipping issue #%d: %s", deps.Cfg.TrunkBranch, issue.Number, err)
		return nil
	}
	if err := git.WorktreeAdd(ctx, worktreeDir, branch, "origin/"+deps.Cfg.TrunkBranch); err != nil {
		warn("failed to create worktree at %s; skipping issue #%d: %s", worktreeDir, issue.Number, err)
		return nil
	}

	// Fetch issue detail and render the prompt.
	detail, err := deps.GH.IssueDetail(ctx, issue.Number)
	if err != nil {
		warn("could not fetch detail for issue #%d; preserving worktree at %s: %s", issue.Number, worktreeDir, err)
		return nil
	}
	promptText := prompt.BuildIssuePrompt(prompt.Issue{
		Number:   detail.Number,
		Title:    detail.Title,
		Body:     detail.Body,
		Comments: convertComments(detail.Comments),
	})

	// Phase 1: harness work. Output is captured so the spinner can
	// own the terminal line uncontested; we print it after the
	// spinner stops.
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

	// Phase 2: poll for the PR to appear.
	var prNumber int
	err = tui.With(fmt.Sprintf("waiting for PR on branch %s", branch), func() error {
		var pErr error
		prNumber, pErr = deps.GH.WaitForPR(ctx, branch, 60*time.Second)
		return pErr
	})
	if err != nil {
		warn("no PR opened on branch %s; preserving worktree at %s for inspection", branch, worktreeDir)
		return nil
	}
	fmt.Printf("found PR #%d for issue #%d\n", prNumber, issue.Number)

	// Phase 3: poll CI to green.
	err = tui.With(fmt.Sprintf("polling CI on PR #%d", prNumber), func() error {
		return deps.GH.WaitForChecks(ctx, prNumber, time.Duration(deps.Cfg.CITimeoutMinutes)*time.Minute)
	})
	if err != nil {
		warn("CI did not pass for PR #%d within %dm; preserving worktree at %s for inspection", prNumber, deps.Cfg.CITimeoutMinutes, worktreeDir)
		return nil
	}
	fmt.Printf("CI passed on PR #%d\n", prNumber)

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
	fmt.Println()
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
