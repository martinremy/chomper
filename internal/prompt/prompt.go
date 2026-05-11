// Package prompt renders the markdown prompts chomper feeds to the
// harness. Two flavors: the initial issue prompt, and (in a later
// slice) the review-fix prompt.
package prompt

import (
	"fmt"
	"strings"
)

// Issue is the minimal shape the prompt builder needs from a GitHub
// issue. Mapping from gh.FullIssue happens in the chomper package.
type Issue struct {
	Number   int
	Title    string
	Body     string
	Comments []Comment
}

// Comment is a single issue comment for rendering as context.
type Comment struct {
	Author string
	Body   string
}

// BuildIssuePrompt renders the per-issue worker prompt. The Closes-#N
// guidance lives in the instructions block so the harness puts the
// auto-close marker in the PR description (matches bash v0.1).
func BuildIssuePrompt(issue Issue) string {
	var b strings.Builder

	fmt.Fprintf(&b, `You are working autonomously on a GitHub repository.

Your task is to implement a fix or feature for the following GitHub issue:

Issue #%d: %s

%s

---
`, issue.Number, issue.Title, issue.Body)

	if len(issue.Comments) > 0 {
		b.WriteString("\nIssue comments:\n")
		for _, c := range issue.Comments {
			indented := strings.ReplaceAll(c.Body, "\n", "\n  ")
			fmt.Fprintf(&b, "- %s:\n  %s\n", c.Author, indented)
		}
	}

	fmt.Fprintf(&b, `
Instructions:
- Implement the fix or feature described in the issue.
- Write tests if applicable.
- Commit your changes with a descriptive commit message referencing the issue
  number (e.g. "fix: resolve null pointer in auth handler, closes #%d").
- Open a pull request against main with a clear title. The PR description
  MUST begin with "Closes #%d" on its own line at the top — GitHub
  uses that to auto-close the issue when the PR merges. Add any further
  context (summary of changes, test plan) below that line.
- Do not merge the PR yourself. Stop after the PR is open.
- Do not ask for confirmation. Work autonomously to completion.
`, issue.Number, issue.Number)

	return b.String()
}

// BuildReviewFixPrompt renders the review-fix prompt for a subsequent
// harness invocation when a reviewer (CodeRabbit, Greptile, etc.) has
// left feedback that the judge classified as needs_fix.
//
// Per design: the harness reads the review itself via `gh` rather than
// having chomper embed the review content in the prompt. The harness
// has more flexibility this way (it can navigate inline comments by
// id, fetch additional context).
func BuildReviewFixPrompt(issue Issue, prNumber int, reviewer string, iteration int) string {
	return fmt.Sprintf(`You are working autonomously on a GitHub repository.

You previously opened PR #%d to address issue #%d:
  "%s"

A reviewer (%s) has submitted feedback that requires your attention.
This is review-fix iteration %d.

Use the gh CLI to read the review and its inline comments yourself:

  gh pr view %d --json reviews,reviewThreads,comments,files,headRefName
  gh pr diff %d
  gh api repos/{owner}/{repo}/pulls/%d/comments
  gh api repos/{owner}/{repo}/pulls/%d/reviews

Then:
1. Read the latest review from %s and its inline comments carefully.
2. Make code changes that address each concrete feedback point.
3. Commit with a clear message (e.g. "fix: address review feedback").
4. Push to the existing branch. Do NOT open a new PR.

Constraints:
- Keep the fix focused on the reviewer's feedback. Do not expand scope.
- If a comment is genuinely unclear or you disagree, post a brief reply
  to that thread via:
    gh api repos/{owner}/{repo}/pulls/%d/comments/<id>/replies -F body='...'
  and still apply the change unless doing so would be incorrect.
- Do NOT merge the PR.
- Do NOT open additional PRs.
- Do NOT ask for confirmation. Work autonomously to completion.

Original issue body for context:
---
%s
---
`, prNumber, issue.Number, issue.Title, reviewer, iteration,
		prNumber, prNumber, prNumber, prNumber, reviewer, prNumber, issue.Body)
}
