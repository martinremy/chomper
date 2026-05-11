// Package judge wraps the three judge roles chomper uses: silence
// classification (Supervisor), question answering (Supervisor), and
// review adjudication (review loop in v0.6).
//
// All three share one Harness.RunJudge entry point and differ only in
// system prompt. The prompts themselves live in prompts.go.
package judge

import (
	"context"
	"fmt"
)

// Caller is the narrow interface a judge needs from a harness.
// Defined here (not in internal/harness) so internal/judge stays
// dependency-free of harness — breaks the import cycle where the
// Supervisor in harness wants to call judge.Classify.
//
// Both *harness.Claude and *harness.Codex satisfy this implicitly.
type Caller interface {
	RunJudge(ctx context.Context, systemPrompt, userPrompt, model string) (string, error)
}

// IssueContext is the per-issue metadata the judge needs to make a
// good decision (especially for question answering and review
// adjudication — both want to reason against the original ticket).
type IssueContext struct {
	Repo   string
	Number int
	Title  string
	Body   string
}

// Verdict is the classifier judge's structured response.
type Verdict struct {
	State    string   `json:"state"`
	Reason   string   `json:"reason"`
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// ReviewVerdict is the review-adjudicator judge's structured response.
type ReviewVerdict struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// Classify runs the silence classifier. Returns a verdict with state ∈
// {working, waiting, errored} or — on parse / call failure — a
// best-effort "working" so the caller can keep polling without
// injecting noise.
func Classify(ctx context.Context, h Caller, model string,
	issueCtx IssueContext, bufferTail string, elapsedSec float64) Verdict {
	prompt := fmt.Sprintf(`Issue context:
  Repo: %s
  Issue #%d: %s

Time since last event: %.0fs

Recent agent output buffer (newest at bottom):
<<<
%s
>>>`, issueCtx.Repo, issueCtx.Number, issueCtx.Title, elapsedSec, bufferTail)

	raw, err := h.RunJudge(ctx, classifierSystemPrompt, prompt, model)
	if err != nil {
		return Verdict{State: "working", Reason: "classifier unreachable: " + err.Error()}
	}
	var v Verdict
	if err := ExtractFirstJSON(raw, &v); err != nil || v.State == "" {
		return Verdict{State: "working", Reason: "unparseable classifier output"}
	}
	return v
}

// Answer runs the answerer judge. Returns the answer text trimmed of
// surrounding whitespace, or an error if the judge call itself failed.
// (The caller should treat any error as "abort the issue" — we'd rather
// preserve the worktree than inject a guessed answer.)
func Answer(ctx context.Context, h Caller, model string,
	issueCtx IssueContext, bufferTail, question string, options []string) (string, error) {
	optsBlock := ""
	if len(options) > 0 {
		optsBlock = "Options:\n"
		for _, o := range options {
			optsBlock += "  - " + o + "\n"
		}
	}
	prompt := fmt.Sprintf(`Original issue:
  Repo: %s
  Issue #%d: %s
  Body:
%s

Recent agent activity:
<<<
%s
>>>

Question:
  %s
%s`, issueCtx.Repo, issueCtx.Number, issueCtx.Title, issueCtx.Body,
		bufferTail, question, optsBlock)

	raw, err := h.RunJudge(ctx, answererSystemPrompt, prompt, model)
	if err != nil {
		return "", fmt.Errorf("answerer judge: %w", err)
	}
	return trimSpace(raw), nil
}

// Adjudicate runs the review-adjudication judge. Returns a verdict with
// action ∈ {merge_as_is, needs_fix, escalate}. On parse / call failure
// returns {needs_fix, "..."}, the conservative default — apply the
// feedback rather than skip it.
func Adjudicate(ctx context.Context, h Caller, model string,
	issueCtx IssueContext, reviewer, reviewJSON, inlineCommentsJSON string) ReviewVerdict {
	prompt := fmt.Sprintf(`Issue:
  Repo: %s
  Issue #%d: %s

Reviewer: %s

Review (state, body):
%s

Inline comments on the PR (path/line/author/body):
%s`, issueCtx.Repo, issueCtx.Number, issueCtx.Title, reviewer, reviewJSON, inlineCommentsJSON)

	raw, err := h.RunJudge(ctx, reviewSystemPrompt, prompt, model)
	if err != nil {
		return ReviewVerdict{Action: "needs_fix", Reason: "adjudicator unreachable: " + err.Error()}
	}
	var v ReviewVerdict
	if err := ExtractFirstJSON(raw, &v); err != nil || v.Action == "" {
		return ReviewVerdict{Action: "needs_fix", Reason: "unparseable adjudicator output"}
	}
	return v
}

// trimSpace is a tiny helper that avoids importing strings in this file.
func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && isSpace(s[i]) {
		i++
	}
	for j > i && isSpace(s[j-1]) {
		j--
	}
	return s[i:j]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
