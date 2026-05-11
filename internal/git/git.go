// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package git wraps the local `git` CLI for chomper's worktree-based
// per-issue isolation.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fetch refreshes the remote-tracking ref for `remote/branch` without
// touching the user's working tree. Idempotent and side-effect-free
// at the working-tree level.
func Fetch(ctx context.Context, remote, branch string) error {
	out, err := exec.CommandContext(ctx, "git", "fetch", "--quiet", remote, branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch %s %s: %w (%s)", remote, branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WorktreeAdd creates a new worktree at path with a fresh branch off
// the given base ref (e.g., "origin/main"). The parent directory is
// created if missing.
func WorktreeAdd(ctx context.Context, path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create worktree parent dir: %w", err)
	}
	out, err := exec.CommandContext(ctx, "git", "worktree", "add",
		"-b", branch, path, base,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WorktreeRemove tears down a worktree. --force is used because after
// a successful merge any leftover working-tree state in the worktree
// is no longer load-bearing.
func WorktreeRemove(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BranchExists returns true if the local branch ref is present.
func BranchExists(ctx context.Context, branch string) bool {
	return exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// DeleteBranch removes a local branch. -D forces removal regardless of
// merged state; callers should only invoke this after the upstream PR
// has been confirmed merged.
func DeleteBranch(ctx context.Context, branch string) error {
	out, err := exec.CommandContext(ctx, "git", "branch", "-D", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -D %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteRemoteBranch removes the remote branch on origin. Best-effort:
// tolerates "branch doesn't exist" (e.g., when the repo has
// "Automatically delete head branches" enabled and the merge already
// triggered the deletion).
func DeleteRemoteBranch(ctx context.Context, branch string) {
	_ = exec.CommandContext(ctx, "git", "push", "origin", "--delete", branch).Run()
}
