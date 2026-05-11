// Package gh shells out to the `gh` CLI for GitHub operations.
//
// We deliberately use `gh` rather than calling the GitHub API directly:
// it reuses the user's existing auth, handles enterprise hosts via
// `git remote get-url`, and gives us a single binary's worth of API
// behavior we don't have to reimplement.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client is a thin wrapper around the `gh` CLI.
type Client struct{}

// New returns a Client ready to use. The struct is empty today but
// reserved as a seam: e.g., per-invocation cwd, host override.
func New() *Client { return &Client{} }

// Issue is the subset of issue fields chomper needs for listing/filtering.
type Issue struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Labels []Label `json:"labels"`
	URL    string  `json:"url"`
}

// Label is a GitHub label as exposed by `gh issue list --json labels`.
type Label struct {
	Name string `json:"name"`
}

// RequireEnv verifies gh + git are installed, that we're inside a git
// repo, and that gh is authenticated for the current repo's host.
//
// Auth check is scoped to the repo's host (parsed from `git remote
// get-url origin`) rather than the unscoped `gh auth status`, because
// the unscoped check fails when an unrelated host (e.g., a stalled
// enterprise instance) times out — same lesson the bash prototype
// learned the hard way.
func (c *Client) RequireEnv(ctx context.Context) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("`gh` CLI not found on PATH. Install: https://cli.github.com/")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("`git` not found on PATH")
	}
	if err := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return fmt.Errorf("not inside a git repository")
	}

	host := c.CurrentHost(ctx)
	authCmd := exec.CommandContext(ctx, "gh", "auth", "status", "-h", host)
	if err := authCmd.Run(); err != nil {
		return fmt.Errorf("`gh` is not authenticated for %s. Run: gh auth login -h %s", host, host)
	}
	return nil
}

// CurrentHost returns the host of the current repo's `origin` remote
// (e.g., "github.com" or "github.enterprise.example.com"). Falls back
// to "github.com" if no remote or unparseable.
func (c *Client) CurrentHost(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "github.com"
	}
	return parseHost(strings.TrimSpace(string(out)))
}

// parseHost extracts the hostname from a git remote URL. Handles HTTPS
// (https://host/owner/repo) and SCP-style SSH (git@host:owner/repo).
func parseHost(url string) string {
	switch {
	case strings.HasPrefix(url, "git@"):
		rest := url[len("git@"):]
		if i := strings.Index(rest, ":"); i != -1 {
			return rest[:i]
		}
	case strings.HasPrefix(url, "https://"):
		rest := url[len("https://"):]
		if i := strings.Index(rest, "/"); i != -1 {
			return rest[:i]
		}
	case strings.HasPrefix(url, "http://"):
		rest := url[len("http://"):]
		if i := strings.Index(rest, "/"); i != -1 {
			return rest[:i]
		}
	}
	return "github.com"
}

// CurrentRepo returns the owner/name of the current GitHub repo.
func (c *Client) CurrentRepo(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "repo", "view",
		"--json", "nameWithOwner",
		"--jq", ".nameWithOwner",
	).Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// OpenIssues fetches all open issues with the minimal field set chomper
// needs to filter them.
func (c *Client) OpenIssues(ctx context.Context) ([]Issue, error) {
	cmd := exec.CommandContext(ctx, "gh", "issue", "list",
		"--state", "open",
		"--limit", "200",
		"--json", "number,title,labels,url",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh issue list: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	var issues []Issue
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("parse gh issue list output: %w", err)
	}
	return issues, nil
}

// FullIssue extends Issue with the body + comments needed for prompt
// rendering. Fetched per-issue via `gh issue view` (a more expensive
// call than `gh issue list`, so we only do it when we're actually
// going to work the issue).
type FullIssue struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	URL      string    `json:"url"`
	Comments []Comment `json:"comments"`
}

// Comment is one issue comment as returned by `gh issue view --json comments`.
type Comment struct {
	Author Author `json:"author"`
	Body   string `json:"body"`
}

// Author is the user who authored a comment.
type Author struct {
	Login string `json:"login"`
}

// IssueDetail fetches the full issue (body + comments) for prompt rendering.
func (c *Client) IssueDetail(ctx context.Context, number int) (*FullIssue, error) {
	cmd := exec.CommandContext(ctx, "gh", "issue", "view",
		strconv.Itoa(number),
		"--json", "number,title,body,comments,url",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh issue view %d: %w (%s)", number, err, strings.TrimSpace(stderr.String()))
	}
	var iss FullIssue
	if err := json.Unmarshal(stdout.Bytes(), &iss); err != nil {
		return nil, fmt.Errorf("parse gh issue view output: %w", err)
	}
	return &iss, nil
}

// WaitForPR polls for an open PR with the given head branch. Returns
// the PR number, or an error on timeout. Tolerates transient `gh`
// failures during polling — only the timeout produces a hard error.
func (c *Client) WaitForPR(ctx context.Context, branch string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	const interval = 5 * time.Second

	for {
		out, err := exec.CommandContext(ctx, "gh", "pr", "list",
			"--head", branch,
			"--state", "open",
			"--json", "number",
			"--jq", ".[0].number // empty",
		).Output()
		if err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil && n > 0 {
				return n, nil
			}
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("no PR opened on branch %s within %s", branch, timeout)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// WaitForChecks polls `gh pr checks` until all checks pass, any fail,
// or the timeout expires.
//
// The 60-second "grace period" handles the race where checks haven't
// registered yet on a freshly-opened PR: empty bucket list during
// the grace period means "keep waiting"; after the grace period it
// means "this repo has no checks configured, safe to merge". Same
// fix as the bash prototype.
func (c *Client) WaitForChecks(ctx context.Context, prNumber int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	graceUntil := time.Now().Add(60 * time.Second)
	const interval = 15 * time.Second

	for {
		buckets, _ := exec.CommandContext(ctx, "gh", "pr", "checks",
			strconv.Itoa(prNumber),
			"--json", "bucket",
			"--jq", "[.[].bucket]",
		).Output()
		verdict := classifyChecks(strings.TrimSpace(string(buckets)), time.Now().Before(graceUntil))
		switch verdict {
		case "pass":
			return nil
		case "fail":
			return fmt.Errorf("CI failed for PR #%d", prNumber)
		case "pending", "wait-for-registration":
			// keep polling
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("CI did not pass for PR #%d within %s", prNumber, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// classifyChecks interprets the bucket list from `gh pr checks`.
// Exported via package-private use so the logic is testable in isolation.
func classifyChecks(bucketsJSON string, inGracePeriod bool) string {
	var list []string
	if err := json.Unmarshal([]byte(bucketsJSON), &list); err != nil {
		return "pending" // tolerate transient gh failures
	}
	if len(list) == 0 {
		if inGracePeriod {
			return "wait-for-registration"
		}
		return "pass" // no checks configured for this repo
	}
	anyFail := false
	allPass := true
	for _, b := range list {
		if b == "fail" || b == "cancel" {
			anyFail = true
		}
		if b != "pass" && b != "skipping" {
			allPass = false
		}
	}
	switch {
	case anyFail:
		return "fail"
	case allPass:
		return "pass"
	default:
		return "pending"
	}
}

// MergePR triggers a merge via `gh pr merge`. We deliberately do NOT
// pass --delete-branch: that flag tries to delete the *local* branch
// too, which fails when the branch is checked out in our worktree
// and masks the successful merge with a non-zero exit. Chomper does
// branch cleanup itself after WaitForMerged confirms the merge.
func (c *Client) MergePR(ctx context.Context, prNumber int, strategy string) error {
	var flag string
	switch strategy {
	case "squash":
		flag = "--squash"
	case "merge":
		flag = "--merge"
	case "rebase":
		flag = "--rebase"
	default:
		return fmt.Errorf("unknown merge strategy: %s", strategy)
	}
	out, err := exec.CommandContext(ctx, "gh", "pr", "merge",
		strconv.Itoa(prNumber), flag, "--auto",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr merge %d: %w (%s)", prNumber, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WaitForMerged polls until the PR's state == "MERGED", or the timeout
// expires. `gh pr merge --auto` can return success after only enabling
// auto-merge when branch protection adds gates beyond CI; this poll
// guarantees the merge has actually landed before we tear down the
// worktree (so the next issue's worktree, based on a fresh fetch of
// trunk, sees this merge commit).
//
// Note: we use state == "MERGED" rather than a (nonexistent) `merged`
// field — `gh pr view --json` does not expose the GraphQL `merged`
// field directly. The bash prototype's first attempt at this poll
// silently always returned false; same bug here would manifest the
// same way. Don't ask for fields that --json doesn't list.
func (c *Client) WaitForMerged(ctx context.Context, prNumber int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	const interval = 5 * time.Second

	for {
		out, err := exec.CommandContext(ctx, "gh", "pr", "view",
			strconv.Itoa(prNumber),
			"--json", "state",
			"--jq", ".state",
		).Output()
		if err == nil {
			if strings.TrimSpace(string(out)) == "MERGED" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("PR #%d did not land within %s of gh pr merge", prNumber, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// Review is the subset of a GitHub PR review that chomper cares about.
type Review struct {
	ID          int64  `json:"id"`
	State       string `json:"state"`        // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED
	Body        string `json:"body"`
	SubmittedAt string `json:"submitted_at"` // RFC3339
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
}

// WaitForReview polls the PR for a review submitted after `since` by
// any of the configured reviewer logins. Returns the matching review
// (latest if multiple) or an error on timeout.
//
// `since` is an RFC3339 timestamp ("2026-05-10T15:00:00Z"). Reviews
// submitted before or at this time are ignored — important for fix
// iterations, where we don't want to re-match the review that
// triggered the previous iteration.
func (c *Client) WaitForReview(ctx context.Context, prNumber int,
	reviewers []string, since string, timeout time.Duration) (*Review, error) {
	if len(reviewers) == 0 {
		return nil, fmt.Errorf("no reviewers configured")
	}
	repo, err := c.CurrentRepo(ctx)
	if err != nil {
		return nil, fmt.Errorf("current repo: %w", err)
	}
	reviewerSet := make(map[string]bool, len(reviewers))
	for _, r := range reviewers {
		reviewerSet[r] = true
	}

	deadline := time.Now().Add(timeout)
	const interval = 20 * time.Second

	for {
		raw, err := exec.CommandContext(ctx, "gh", "api",
			fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, prNumber),
		).Output()
		if err == nil {
			var all []Review
			if jerr := json.Unmarshal(raw, &all); jerr == nil {
				var best *Review
				for i := range all {
					r := &all[i]
					if !reviewerSet[r.User.Login] {
						continue
					}
					if r.SubmittedAt <= since {
						continue
					}
					if best == nil || r.SubmittedAt > best.SubmittedAt {
						best = r
					}
				}
				if best != nil {
					return best, nil
				}
			}
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no review from %v within %s", reviewers, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// InlineReviewComments fetches the PR's inline-line review comments
// for inclusion in the judge's adjudication prompt. Best-effort:
// returns "[]" on any failure rather than erroring.
func (c *Client) InlineReviewComments(ctx context.Context, prNumber int) string {
	repo, err := c.CurrentRepo(ctx)
	if err != nil {
		return "[]"
	}
	out, err := exec.CommandContext(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/pulls/%d/comments", repo, prNumber),
		"--jq", "[.[] | {path, line, body, author: .user.login}]",
	).Output()
	if err != nil {
		return "[]"
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "[]"
	}
	return s
}

// Filter applies label (OR) + title (case-insensitive substring) filters
// and sorts results by issue number ascending.
//
// Empty `labels` means "no label filter" (every issue passes that gate).
// Empty `titleMatch` means "no title filter".
func Filter(issues []Issue, labels []string, titleMatch string) []Issue {
	titleLower := strings.ToLower(titleMatch)
	labelSet := make(map[string]bool, len(labels))
	for _, l := range labels {
		labelSet[l] = true
	}

	out := make([]Issue, 0, len(issues))
	for _, iss := range issues {
		if titleLower != "" && !strings.Contains(strings.ToLower(iss.Title), titleLower) {
			continue
		}
		if len(labelSet) > 0 {
			matched := false
			for _, l := range iss.Labels {
				if labelSet[l.Name] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, iss)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
