package prompt

import (
	"strings"
	"testing"
)

func TestBuildIssuePrompt_IncludesIssueNumberAndTitle(t *testing.T) {
	got := BuildIssuePrompt(Issue{Number: 42, Title: "Fix the bug", Body: "Some context"})
	if !strings.Contains(got, "#42") {
		t.Error("missing issue number")
	}
	if !strings.Contains(got, "Fix the bug") {
		t.Error("missing issue title")
	}
	if !strings.Contains(got, "Some context") {
		t.Error("missing issue body")
	}
}

// The "Closes #N" guidance is load-bearing — without it the PR
// description won't trigger GitHub's auto-close on merge. The bash
// version had a bug here at one point; the test exists so we don't
// regress.
func TestBuildIssuePrompt_HasClosesGuidance(t *testing.T) {
	got := BuildIssuePrompt(Issue{Number: 42, Title: "x", Body: "y"})
	if !strings.Contains(got, `"Closes #42"`) {
		t.Errorf("prompt should instruct agent to put 'Closes #42' in PR description")
	}
	if !strings.Contains(strings.ToLower(got), "pr description") {
		t.Error("guidance should mention PR description, not just commit")
	}
}

func TestBuildIssuePrompt_NoCommentsSectionWhenEmpty(t *testing.T) {
	got := BuildIssuePrompt(Issue{Number: 1, Title: "x", Body: "y"})
	if strings.Contains(got, "Issue comments:") {
		t.Error("should not include Issue comments header when there are none")
	}
}

func TestBuildIssuePrompt_IncludesCommentsWhenPresent(t *testing.T) {
	got := BuildIssuePrompt(Issue{
		Number: 1, Title: "x", Body: "y",
		Comments: []Comment{
			{Author: "alice", Body: "three retries should be enough"},
			{Author: "bob", Body: "use exponential backoff"},
		},
	})
	for _, want := range []string{"Issue comments:", "alice", "three retries", "bob", "exponential backoff"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}
}

func TestBuildReviewFixPrompt_ReferencesPRAndReviewer(t *testing.T) {
	got := BuildReviewFixPrompt(Issue{Number: 7, Title: "Title", Body: "Body"}, 19, "coderabbitai", 2)
	for _, want := range []string{"PR #19", "issue #7", `"Title"`, "coderabbitai", "iteration 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in review-fix prompt", want)
		}
	}
}

// Review-fix prompts must NOT instruct the agent to merge the PR or
// open a new one — that's the whole point of the iteration loop.
func TestBuildReviewFixPrompt_DoesNotInstructToMergeOrReopen(t *testing.T) {
	got := BuildReviewFixPrompt(Issue{Number: 7, Title: "Title", Body: ""}, 19, "bot", 1)
	if !strings.Contains(got, "Do NOT merge") {
		t.Error("must explicitly tell agent not to merge")
	}
	if !strings.Contains(got, "Do NOT open") {
		t.Error("must explicitly tell agent not to open a new PR")
	}
}
