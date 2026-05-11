package chomper

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/martinremy/chomper/internal/gh"
)

// TestCIFailureWarning pins the message a user sees when WaitForChecks
// returns a terminal error. The text branches on the typed error so the
// user can tell whether re-running chomper will help (timeout: yes;
// failure: only after fixing the failing checks).
//
// Pure function from (error, params) -> string; no I/O, no mocks.
func TestCIFailureWarning(t *testing.T) {
	const (
		prNumber       = 21
		timeoutMinutes = 30
		worktreeDir    = "/tmp/chomper-worktrees/owner/repo/issue-20"
	)

	tests := []struct {
		name           string
		err            error
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "ErrCIFailed -> failing message, no within-Nm phrasing",
			err:  fmt.Errorf("CI failed for PR #%d: %w", prNumber, gh.ErrCIFailed),
			wantContains: []string{
				"failing",
				fmt.Sprintf("PR #%d", prNumber),
				worktreeDir,
				"Fix",
				"re-run chomper",
			},
			// "within 30m" implies waiting longer might help; it never will
			// for a red CI. Make sure it's absent.
			wantNotContain: []string{"within 30m"},
		},
		{
			name: "ErrCITimeout -> timeout message points at ci_timeout_minutes knob",
			err:  fmt.Errorf("CI did not pass for PR #%d within 30m: %w", prNumber, gh.ErrCITimeout),
			wantContains: []string{
				fmt.Sprintf("PR #%d", prNumber),
				"within 30m",
				worktreeDir,
				"Re-run chomper",
				"ci_timeout_minutes",
			},
			wantNotContain: []string{"failing"},
		},
		{
			name: "unknown error -> fallback mentions error and preserves worktree",
			err:  errors.New("network blew up"),
			wantContains: []string{
				fmt.Sprintf("PR #%d", prNumber),
				"network blew up",
				worktreeDir,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ciFailureWarning(tt.err, prNumber, timeoutMinutes, worktreeDir)
			for _, sub := range tt.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("ciFailureWarning() missing %q\ngot: %s", sub, got)
				}
			}
			for _, sub := range tt.wantNotContain {
				if strings.Contains(got, sub) {
					t.Errorf("ciFailureWarning() unexpectedly contains %q\ngot: %s", sub, got)
				}
			}
		})
	}
}
