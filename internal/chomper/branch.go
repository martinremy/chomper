// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package chomper

import "fmt"

// BranchForIssue returns the local branch name chomper uses for a given
// issue number. The convention is load-bearing: producer (ProcessIssue)
// and consumer (resume detection via `gh pr list --head <branch>`) must
// agree on the exact string. Centralized here so the contract has a
// single definition.
func BranchForIssue(issueNumber int) string {
	return fmt.Sprintf("fix/issue-%d", issueNumber)
}
