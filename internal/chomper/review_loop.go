package chomper

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/martinremy/chomper/internal/gh"
	"github.com/martinremy/chomper/internal/judge"
	"github.com/martinremy/chomper/internal/prompt"
	"github.com/martinremy/chomper/internal/tui"
)

// ReviewLoopResult is what ReviewLoop returns to its caller (ProcessIssue).
type ReviewLoopResult int

const (
	// ReviewProceed means: merge the PR (review timed out or approved).
	ReviewProceed ReviewLoopResult = iota
	// ReviewAbort means: do NOT merge; preserve worktree (escalate,
	// max iterations exhausted, fix-iteration failed). Caller is
	// responsible for the preserve-and-warn flow.
	ReviewAbort
)

// ReviewLoop polls for reviews from configured reviewers, adjudicates
// non-APPROVE reviews via the judge, runs fix iterations (re-invoking
// the harness) when needs_fix, and re-polls CI after each fix.
//
// Returns ReviewProceed to continue to merge, ReviewAbort to skip merge
// and preserve the worktree. All logging goes through tui spinners +
// info/warn lines; the caller should not log around this call.
func ReviewLoop(ctx context.Context, deps *Deps, prNumber int, detail *gh.FullIssue, worktreeDir string) ReviewLoopResult {
	reviewers := deps.Cfg.WaitForReviews.Reviewers
	timeout := time.Duration(deps.Cfg.WaitForReviews.TimeoutMinutes) * time.Minute
	maxIter := deps.Cfg.WaitForReviews.MaxIterations
	approveRequired := deps.Cfg.WaitForReviews.ApproveStateRequired
	adjudicate := deps.Cfg.WaitForReviews.JudgeAdjudicates

	issueCtx := judge.IssueContext{
		Repo:   deps.Repo,
		Number: detail.Number,
		Title:  detail.Title,
		Body:   detail.Body,
	}

	// Track which reviews/comments we've already processed.
	// Empty on iter 1 so we pick up reviews that came in BEFORE
	// chomper entered the loop (CodeRabbit posts within ~10s of PR
	// open; chomper gets here only after CI passes, minutes later).
	seen := gh.NewSeenReviews()

	for iter := 1; iter <= maxIter; iter++ {
		fmt.Printf("review iteration %d/%d\n", iter, maxIter)

		var review *gh.Review
		err := tui.With(fmt.Sprintf("waiting for review from %v", reviewers), func() error {
			var rerr error
			review, rerr = deps.GH.WaitForReview(ctx, prNumber, reviewers, seen, timeout)
			return rerr
		})
		if err != nil {
			if approveRequired {
				warn("no review within %s and approve_state_required=true; preserving worktree at %s", timeout, worktreeDir)
				return ReviewAbort
			}
			fmt.Printf("no review within %s; proceeding to merge\n", timeout)
			return ReviewProceed
		}

		seen.Mark(review)
		fmt.Printf("review received from %s (%s): state=%s\n", review.User.Login, review.Kind, review.State)
		if review.State == "APPROVED" {
			fmt.Println("approved; proceeding to merge")
			return ReviewProceed
		}

		// Non-APPROVE. Decide action: adjudicate or default to needs_fix.
		action := "needs_fix"
		if adjudicate {
			var verdict judge.ReviewVerdict
			err := tui.With(fmt.Sprintf("judge: adjudicating review from %s", review.User.Login), func() error {
				reviewJSON, _ := json.Marshal(map[string]any{
					"state":        review.State,
					"body":         review.Body,
					"submitted_at": review.SubmittedAt,
				})
				inline := deps.GH.InlineReviewComments(ctx, prNumber)
				verdict = judge.Adjudicate(ctx, deps.Harness, deps.Cfg.AutoAnswerModel,
					issueCtx, review.User.Login, string(reviewJSON), inline)
				return nil
			})
			if err == nil && verdict.Action != "" {
				action = verdict.Action
				fmt.Printf("judge verdict: %s (%s)\n", verdict.Action, verdict.Reason)
			} else {
				warn("judge adjudication failed; defaulting to needs_fix")
			}
		}

		switch action {
		case "merge_as_is":
			return ReviewProceed
		case "escalate":
			warn("judge: review meaningfully expands scope. Preserving worktree at %s for human handling", worktreeDir)
			return ReviewAbort
		case "needs_fix":
			fixPrompt := prompt.BuildReviewFixPrompt(prompt.Issue{
				Number: detail.Number,
				Title:  detail.Title,
				Body:   detail.Body,
			}, prNumber, review.User.Login, iter)

			fmt.Printf("addressing review feedback (iteration %d)\n", iter)
			err := tui.With(fmt.Sprintf("%s addressing review feedback (iter %d)", deps.Harness.Name(), iter), func() error {
				_, hErr := deps.Harness.RunWorker(ctx, worktreeDir, fixPrompt)
				return hErr
			})
			if err != nil {
				warn("harness exited non-zero on review-fix iteration %d; preserving worktree at %s: %s", iter, worktreeDir, err)
				return ReviewAbort
			}

			// Re-poll CI on the new commits.
			// (The seen-set already excludes the prior review; no
			// additional bookkeeping needed for the next iteration.)
			err = tui.With(fmt.Sprintf("polling CI on PR #%d (after review-fix %d)", prNumber, iter), func() error {
				return deps.GH.WaitForChecks(ctx, prNumber, time.Duration(deps.Cfg.CITimeoutMinutes)*time.Minute)
			})
			if err != nil {
				warn("CI did not pass after review-fix iteration %d; preserving worktree at %s: %s", iter, worktreeDir, err)
				return ReviewAbort
			}
		default:
			warn("unknown judge action %q on review iteration %d; defaulting to abort", action, iter)
			return ReviewAbort
		}
	}

	warn("exhausted %d review iterations without resolution; preserving worktree at %s", maxIter, worktreeDir)
	return ReviewAbort
}
