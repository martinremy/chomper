// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package chomper

import (
	"context"
	"fmt"
	"os"

	"github.com/martinremy/chomper/internal/gh"
	"github.com/martinremy/chomper/internal/git"
)

// ResumeFacts captures the state observable at the top of ProcessIssue.
// All four fields come from simple I/O queries (gh + filesystem + git);
// the Decide function below maps them to an Action with no I/O of its
// own, which keeps the dense branching logic unit-testable.
type ResumeFacts struct {
	PRState        gh.PRState
	PRNumber       int // 0 when PRState == PRStateNone
	WorktreeExists bool
	BranchExists   bool
}

// Action is the dispatch outcome from the resume resolver. ProcessIssue
// switches on this value at the top of the per-issue pipeline.
type Action int

const (
	// ActionFresh runs the full pipeline from scratch (today's default flow).
	ActionFresh Action = iota
	// ActionResumeReuseWorktree skips harness work, reuses the existing
	// worktree, and jumps directly to CI polling on the known PR.
	ActionResumeReuseWorktree
	// ActionResumeRebuildWorktree skips harness work, rebuilds the worktree
	// from origin/<branch>, and jumps to CI polling on the known PR.
	ActionResumeRebuildWorktree
	// ActionSkipPRClosed skips the issue because the PR was closed
	// without merging (treated as an explicit rejection of the work).
	ActionSkipPRClosed
	// ActionSkipPRMerged skips the issue because the PR was merged but
	// the issue stayed open (almost always: `Closes #N` was missing
	// from the PR body).
	ActionSkipPRMerged
	// ActionSkipStaleLocal skips the issue because no PR exists for it
	// but local state (worktree or branch) from a prior aborted run
	// is in the way. The user is expected to clean up manually, or a
	// future --force-restart flag will reclaim these.
	ActionSkipStaleLocal
)

// Decide is a pure function from observed state to action. It captures
// the 7-row state table from issue #1.
//
// Dimensions:
//   - PR state: 4 values (None | Open | Closed | Merged)
//   - Worktree present: 2 values
//   - Branch present: 2 values
//
// Of the 16 combinations, most collapse:
//   - Open: worktree presence picks reuse vs rebuild (branch is irrelevant
//     because rebuild handles both branch-present and branch-absent).
//   - Closed/Merged: local state is irrelevant; the action is always Skip.
//   - None: only the OR of (worktree, branch) matters — any local state
//     blocks fresh flow today (would be handled by --force-restart later).
func Decide(f ResumeFacts) Action {
	switch f.PRState {
	case gh.PRStateOpen:
		if f.WorktreeExists {
			return ActionResumeReuseWorktree
		}
		return ActionResumeRebuildWorktree
	case gh.PRStateClosed:
		return ActionSkipPRClosed
	case gh.PRStateMerged:
		return ActionSkipPRMerged
	case gh.PRStateNone:
		if f.WorktreeExists || f.BranchExists {
			return ActionSkipStaleLocal
		}
		return ActionFresh
	}
	// Unknown PR state — be conservative and start fresh rather than
	// invoke a resume path with ambiguous remote semantics.
	return ActionFresh
}

// GatherResumeFacts performs the three I/O queries needed to populate
// a ResumeFacts. Only the gh query can fail meaningfully; the other
// two are boolean predicates that fold absence into a `false` value.
func GatherResumeFacts(ctx context.Context, ghClient *gh.Client, branch, worktreeDir string) (ResumeFacts, error) {
	status, err := ghClient.PRListByHead(ctx, branch)
	if err != nil {
		return ResumeFacts{}, fmt.Errorf("query PR for branch %s: %w", branch, err)
	}
	_, statErr := os.Stat(worktreeDir)
	return ResumeFacts{
		PRState:        status.State,
		PRNumber:       status.Number,
		WorktreeExists: statErr == nil,
		BranchExists:   git.BranchExists(ctx, branch),
	}, nil
}

// rebuildWorktreeFromOrigin creates a worktree at worktreeDir from
// origin/<branch>, dropping any stale local branch first. Used by
// ActionResumeRebuildWorktree.
//
// Resume trusts origin as the source of truth for in-flight PRs: any
// uncommitted or unpushed work in a stale local branch is discarded.
// The precondition for entering this path is "PR is open," which means
// the committed half is already on origin; anything not on origin
// would have been lost to the interruption anyway.
func rebuildWorktreeFromOrigin(ctx context.Context, branch, worktreeDir string) error {
	if err := git.Fetch(ctx, "origin", branch); err != nil {
		return fmt.Errorf("fetch origin/%s: %w", branch, err)
	}
	if git.BranchExists(ctx, branch) {
		if err := git.DeleteBranch(ctx, branch); err != nil {
			return fmt.Errorf("delete stale local branch %s: %w", branch, err)
		}
	}
	return git.WorktreeAdd(ctx, worktreeDir, branch, "origin/"+branch)
}
