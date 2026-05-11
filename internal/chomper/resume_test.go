// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package chomper

import (
	"testing"

	"github.com/martinremy/chomper/internal/gh"
)

// TestDecide pins the 7-row state table from issue #1. The matrix
// inputs are (PR state, worktree-present, branch-present) and the
// output is one of six Action values. Decide is pure, so this test
// runs without any I/O, fixtures, or mocks.
func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		in   ResumeFacts
		want Action
	}{
		// PR=Open: resume. Sub-action depends on whether the worktree is
		// available; the branch is irrelevant because rebuild covers both.
		{
			name: "open + worktree + branch -> reuse",
			in:   ResumeFacts{PRState: gh.PRStateOpen, PRNumber: 42, WorktreeExists: true, BranchExists: true},
			want: ActionResumeReuseWorktree,
		},
		{
			name: "open + no worktree + branch -> rebuild",
			in:   ResumeFacts{PRState: gh.PRStateOpen, PRNumber: 42, WorktreeExists: false, BranchExists: true},
			want: ActionResumeRebuildWorktree,
		},
		{
			name: "open + no worktree + no branch -> rebuild",
			in:   ResumeFacts{PRState: gh.PRStateOpen, PRNumber: 42, WorktreeExists: false, BranchExists: false},
			want: ActionResumeRebuildWorktree,
		},

		// PR=Closed (not merged): explicit rejection, skip regardless of
		// local state.
		{
			name: "closed + no local state -> skip",
			in:   ResumeFacts{PRState: gh.PRStateClosed, PRNumber: 42},
			want: ActionSkipPRClosed,
		},
		{
			name: "closed + worktree present -> skip",
			in:   ResumeFacts{PRState: gh.PRStateClosed, PRNumber: 42, WorktreeExists: true},
			want: ActionSkipPRClosed,
		},

		// PR=Merged but issue still open (Closes #N missing): skip and
		// let the user close the issue manually.
		{
			name: "merged + no local state -> skip",
			in:   ResumeFacts{PRState: gh.PRStateMerged, PRNumber: 42},
			want: ActionSkipPRMerged,
		},
		{
			name: "merged + worktree present -> skip",
			in:   ResumeFacts{PRState: gh.PRStateMerged, PRNumber: 42, WorktreeExists: true},
			want: ActionSkipPRMerged,
		},

		// PR=None: depends on local state. No local state = fresh flow.
		// Any local state = skip (recovery delegated to the user until
		// --force-restart lands).
		{
			name: "none + no local state -> fresh",
			in:   ResumeFacts{PRState: gh.PRStateNone},
			want: ActionFresh,
		},
		{
			name: "none + branch only -> skip stale",
			in:   ResumeFacts{PRState: gh.PRStateNone, BranchExists: true},
			want: ActionSkipStaleLocal,
		},
		{
			name: "none + worktree only -> skip stale",
			in:   ResumeFacts{PRState: gh.PRStateNone, WorktreeExists: true},
			want: ActionSkipStaleLocal,
		},
		{
			name: "none + both local -> skip stale",
			in:   ResumeFacts{PRState: gh.PRStateNone, WorktreeExists: true, BranchExists: true},
			want: ActionSkipStaleLocal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.in); got != tt.want {
				t.Errorf("Decide(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
