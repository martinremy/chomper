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
