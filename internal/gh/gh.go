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
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors returned by WaitForChecks. Wrapped with %w in the actual
// returned errors so callers can branch via errors.Is — chomper decides
// whether to tell the user "fix the failing tests" (ErrCIFailed) or
// "re-run, CI just hasn't finished" (ErrCITimeout) based on which one
// is in the chain. Same chain, different action.
var (
	// ErrCIFailed signals that at least one check reported a terminal
	// failure (bucket "fail" or "cancel"). Returned within seconds of
	// the failing check appearing; re-polling alone won't help.
	ErrCIFailed = errors.New("CI failed")
	// ErrCITimeout signals that the WaitForChecks deadline expired with
	// checks still pending. Re-running chomper resumes the poll and may
	// succeed; raising ci_timeout_minutes is the other knob.
	ErrCITimeout = errors.New("CI timed out")
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

// PRState classifies the state of a pull request as returned by
// PRListByHead. Used by the resume resolver in internal/chomper.
type PRState int

const (
	PRStateNone   PRState = iota // no PR exists on the queried branch
	PRStateOpen                  // PR exists and is open
	PRStateClosed                // PR exists, was closed without merging
	PRStateMerged                // PR exists and was merged
)

// String renders the enum as a lowercase label for logs/errors.
func (s PRState) String() string {
	switch s {
	case PRStateNone:
		return "none"
	case PRStateOpen:
		return "open"
	case PRStateClosed:
		return "closed"
	case PRStateMerged:
		return "merged"
	}
	return "unknown"
}

// PRStatus is the result of PRListByHead: the most-recent PR (if any)
// for a given head branch, with its state.
type PRStatus struct {
	State  PRState
	Number int // 0 when State == PRStateNone
}

// PRListByHead queries `gh pr list --head <branch> --state all` and
// returns the most-recent PR's state and number. If no PR exists on the
// branch, returns PRStatus{State: PRStateNone, Number: 0}.
//
// `--state all` is load-bearing: the default `gh pr list` state is
// `open`, so omitting this flag would silently misclassify closed and
// merged PRs as None — and resume would incorrectly start fresh on a
// rejected or already-landed issue.
//
// Multiple PRs on the same head branch are uncommon in practice (would
// require deleting and recreating the branch between PRs). We take
// the first result, which `gh` sorts most-recent first.
func (c *Client) PRListByHead(ctx context.Context, branch string) (PRStatus, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch,
		"--state", "all",
		"--json", "number,state",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return PRStatus{}, fmt.Errorf("gh pr list --head %s: %w (%s)", branch, err, strings.TrimSpace(stderr.String()))
	}
	var rows []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		return PRStatus{}, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(rows) == 0 {
		return PRStatus{State: PRStateNone}, nil
	}
	return PRStatus{State: parsePRState(rows[0].State), Number: rows[0].Number}, nil
}

// parsePRState maps the string state returned by `gh pr list --json
// state` (uppercase: OPEN / CLOSED / MERGED) to our enum. Unknown
// values fall back to PRStateNone, which makes the caller treat them
// as "no resume" — safer than guessing.
func parsePRState(s string) PRState {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "OPEN":
		return PRStateOpen
	case "MERGED":
		return PRStateMerged
	case "CLOSED":
		return PRStateClosed
	default:
		return PRStateNone
	}
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
			return fmt.Errorf("CI failed for PR #%d: %w", prNumber, ErrCIFailed)
		case "pending", "wait-for-registration":
			// keep polling
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("CI did not pass for PR #%d within %s: %w", prNumber, timeout, ErrCITimeout)
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

// Review represents reviewer activity on a PR. It unifies two
// GitHub surfaces:
//   - formal PR reviews (`gh api .../pulls/N/reviews`)
//   - PR-level issue comments (`gh api .../issues/N/comments`)
//
// Some review bots (notably CodeRabbit on the Free plan) only post via
// the latter — their "review" is a summary comment, never a formal
// review object. Chomper has to look at both surfaces to detect
// activity from configured reviewers.
//
// The Kind field disambiguates: "review" or "comment". State is the
// PR-review state (APPROVED / CHANGES_REQUESTED / COMMENTED / DISMISSED)
// for Kind=review, or the synthetic "COMMENTED" for Kind=comment.
type Review struct {
	Kind        string // "review" or "comment"
	ID          int64
	State       string
	Body        string
	SubmittedAt string // RFC3339
	User        struct {
		Login string
	}
}

// SeenReviews tracks IDs of reviews/comments chomper has already
// processed, so subsequent poll iterations skip them. The two ID
// spaces (reviews vs issue comments) are separate, so we track them
// in two maps.
type SeenReviews struct {
	ReviewIDs  map[int64]bool
	CommentIDs map[int64]bool
}

// NewSeenReviews returns an empty seen-set ready for the review loop.
func NewSeenReviews() *SeenReviews {
	return &SeenReviews{
		ReviewIDs:  make(map[int64]bool),
		CommentIDs: make(map[int64]bool),
	}
}

// Mark records that we've processed this Review.
func (s *SeenReviews) Mark(r *Review) {
	if r.Kind == "review" {
		s.ReviewIDs[r.ID] = true
	} else {
		s.CommentIDs[r.ID] = true
	}
}

func (s *SeenReviews) has(kind string, id int64) bool {
	if kind == "review" {
		return s.ReviewIDs[id]
	}
	return s.CommentIDs[id]
}

// reviewerMatches reports whether actualLogin matches any of the
// configured reviewer logins. Tolerant of the [bot] suffix: GitHub
// returns "coderabbitai[bot]" from the issue-comments API and
// "coderabbitai" from `gh pr view --json comments`. We strip the
// suffix from both sides before comparing so user configs are
// portable across endpoints.
func reviewerMatches(configured []string, actualLogin string) bool {
	actualNorm := strings.TrimSuffix(actualLogin, "[bot]")
	for _, c := range configured {
		if strings.TrimSuffix(c, "[bot]") == actualNorm {
			return true
		}
	}
	return false
}

// WaitForReview polls the PR for new activity from any configured
// reviewer. Polls BOTH /reviews and /issues/N/comments, returning the
// most recent unseen item (by SubmittedAt) from any matching login.
// Returns an error on timeout.
//
// `seen` tracks which review/comment IDs have already been processed
// — iter 1 starts with an empty seen-set, so existing activity (e.g.,
// a CodeRabbit summary that posted before chomper entered this loop)
// is correctly picked up. Subsequent iterations skip already-seen IDs.
func (c *Client) WaitForReview(ctx context.Context, prNumber int,
	reviewers []string, seen *SeenReviews, timeout time.Duration) (*Review, error) {
	if len(reviewers) == 0 {
		return nil, fmt.Errorf("no reviewers configured")
	}
	if seen == nil {
		seen = NewSeenReviews()
	}
	repo, err := c.CurrentRepo(ctx)
	if err != nil {
		return nil, fmt.Errorf("current repo: %w", err)
	}

	deadline := time.Now().Add(timeout)
	const interval = 20 * time.Second

	for {
		if r := c.fetchLatestUnseenReview(ctx, repo, prNumber, reviewers, seen); r != nil {
			return r, nil
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

// fetchLatestUnseenReview hits both endpoints once, returns the
// newest unseen reviewer-authored item, or nil if nothing matched.
// Extracted from the polling loop so testing the matching logic
// doesn't require running the full poll.
func (c *Client) fetchLatestUnseenReview(ctx context.Context, repo string, prNumber int,
	reviewers []string, seen *SeenReviews) *Review {
	var best *Review

	// Formal PR reviews (CodeRabbit Pro, human reviewers, etc.)
	if raw, err := exec.CommandContext(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, prNumber),
	).Output(); err == nil {
		var formal []struct {
			ID          int64  `json:"id"`
			State       string `json:"state"`
			Body        string `json:"body"`
			SubmittedAt string `json:"submitted_at"`
			User        struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if jerr := json.Unmarshal(raw, &formal); jerr == nil {
			for _, r := range formal {
				if !reviewerMatches(reviewers, r.User.Login) {
					continue
				}
				if seen.has("review", r.ID) {
					continue
				}
				candidate := &Review{
					Kind:        "review",
					ID:          r.ID,
					State:       r.State,
					Body:        r.Body,
					SubmittedAt: r.SubmittedAt,
				}
				candidate.User.Login = r.User.Login
				if best == nil || candidate.SubmittedAt > best.SubmittedAt {
					best = candidate
				}
			}
		}
	}

	// PR-level issue comments (CodeRabbit Free's summary posts here).
	if raw, err := exec.CommandContext(ctx, "gh", "api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, prNumber),
	).Output(); err == nil {
		var comments []struct {
			ID        int64  `json:"id"`
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if jerr := json.Unmarshal(raw, &comments); jerr == nil {
			for _, c := range comments {
				if !reviewerMatches(reviewers, c.User.Login) {
					continue
				}
				if seen.has("comment", c.ID) {
					continue
				}
				candidate := &Review{
					Kind:        "comment",
					ID:          c.ID,
					State:       "COMMENTED", // synthetic; comments have no review-state
					Body:        c.Body,
					SubmittedAt: c.CreatedAt,
				}
				candidate.User.Login = c.User.Login
				if best == nil || candidate.SubmittedAt > best.SubmittedAt {
					best = candidate
				}
			}
		}
	}

	return best
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
