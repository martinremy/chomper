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

// BuildCIFixPrompt is the v0.1 CI-failure repair prompt: it includes
// issue context (so the harness knows what the change is for), PR
// number, iteration counters (so the harness knows how many attempts
// are left), and the failed-check names with their log tails.
func TestBuildCIFixPrompt_IncludesIssuePRAndIterationContext(t *testing.T) {
	got := BuildCIFixPrompt(Issue{Number: 7, Title: "Add foo", Body: "Body"}, 19, 2, 3, []FailedCheck{
		{Name: "test", LogTail: "FAIL: TestAdd"},
	})
	for _, want := range []string{"PR #19", "issue #7", `"Add foo"`, "iteration 2 of 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in CI-fix prompt\n---\n%s", want, got)
		}
	}
}

// Each failed check's name and log tail must appear verbatim — the
// harness uses both to diagnose the failure.
func TestBuildCIFixPrompt_IncludesEachFailedCheck(t *testing.T) {
	got := BuildCIFixPrompt(Issue{Number: 1, Title: "x", Body: ""}, 5, 1, 3, []FailedCheck{
		{Name: "test", LogTail: "FAIL: TestFoo\n--- FAIL: TestFoo (0.00s)"},
		{Name: "lint", LogTail: "main.go:42:1: missing return"},
	})
	for _, want := range []string{
		"test", "FAIL: TestFoo",
		"lint", "main.go:42:1: missing return",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in CI-fix prompt\n---\n%s", want, got)
		}
	}
}

// CI-fix prompts must NOT instruct the agent to merge or open a new PR
// — same constraint as the review-fix loop. Push to the existing branch.
func TestBuildCIFixPrompt_DoesNotInstructToMergeOrReopen(t *testing.T) {
	got := BuildCIFixPrompt(Issue{Number: 1, Title: "x", Body: ""}, 5, 1, 3, []FailedCheck{
		{Name: "test", LogTail: "fail"},
	})
	if !strings.Contains(got, "Do NOT merge") {
		t.Error("must explicitly tell agent not to merge")
	}
	if !strings.Contains(got, "Do NOT open") {
		t.Error("must explicitly tell agent not to open a new PR")
	}
}

// Truncation marker should appear when a log tail is presented; the
// harness needs to know we've cut the log so it can fetch more via
// `gh run view` if the tail isn't sufficient.
func TestBuildCIFixPrompt_TellsAgentLogsAreTruncated(t *testing.T) {
	got := BuildCIFixPrompt(Issue{Number: 1, Title: "x", Body: ""}, 5, 1, 3, []FailedCheck{
		{Name: "test", LogTail: "some failure output"},
	})
	if !strings.Contains(strings.ToLower(got), "truncated") {
		t.Errorf("prompt should mention that logs are truncated tails\n---\n%s", got)
	}
	if !strings.Contains(got, "gh run view") {
		t.Errorf("prompt should point at gh run view for more context\n---\n%s", got)
	}
}
