// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package chomper

import (
	"errors"
	"fmt"
	"testing"

	"github.com/martinremy/chomper/internal/gh"
)

// TestDecideFixCIIter pins the pure-logic decision tree at the end of
// one CI-fix iteration:
//
//   - CI green               -> ProceedDecision (return success to caller)
//   - CI red & iter < max    -> Continue (try another fix)
//   - CI red & iter == max   -> AbortDecision (cap reached)
//   - CI timeout             -> AbortDecision (not our problem; user re-runs)
//   - unknown error          -> AbortDecision (don't guess)
//
// Pure function over (error, int, int); no I/O, no mocks. Analogous
// in shape to TestDecide in resume_test.go.
func TestDecideFixCIIter(t *testing.T) {
	const maxIter = 3

	tests := []struct {
		name string
		err  error
		iter int
		want fixCIIterDecision
	}{
		{
			"CI green -> proceed",
			nil,
			1, fixCIProceedDecision,
		},
		{
			"CI red, iter 1 of 3 -> continue",
			fmt.Errorf("CI failed: %w", gh.ErrCIFailed),
			1, fixCIContinue,
		},
		{
			"CI red, iter 2 of 3 -> continue",
			fmt.Errorf("CI failed: %w", gh.ErrCIFailed),
			2, fixCIContinue,
		},
		{
			"CI red, iter 3 of 3 (cap) -> abort",
			fmt.Errorf("CI failed: %w", gh.ErrCIFailed),
			3, fixCIAbortDecision,
		},
		{
			"CI timeout -> abort (not a fixable state)",
			fmt.Errorf("CI timed out: %w", gh.ErrCITimeout),
			1, fixCIAbortDecision,
		},
		{
			"unknown error -> abort (conservative)",
			errors.New("something else"),
			1, fixCIAbortDecision,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideFixCIIter(tt.err, tt.iter, maxIter); got != tt.want {
				t.Errorf("decideFixCIIter(%v, iter=%d, max=%d) = %v, want %v",
					tt.err, tt.iter, maxIter, got, tt.want)
			}
		})
	}
}
