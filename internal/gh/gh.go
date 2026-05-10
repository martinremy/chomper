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
	"strings"
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
