#!/usr/bin/env bash
# prompt_builder.sh — render prompts for chomper's harness invocations.
#
# Two flavors:
#   build_prompt_file        - initial issue prompt
#   build_review_fix_prompt  - "your PR got reviewed; address feedback" prompt

set -euo pipefail

# Args: <issue_detail_json> <output_path>
build_prompt_file() {
  local issue_json="$1"
  local out_file="$2"

  local number title body
  number="$(printf '%s' "$issue_json" | jq -r '.number')"
  title="$(printf  '%s' "$issue_json" | jq -r '.title')"
  body="$(printf   '%s' "$issue_json" | jq -r '.body // ""')"

  # Render comments (if any) as a "- author: body" list.
  local comments_text=""
  local comments_count
  comments_count="$(printf '%s' "$issue_json" | jq '(.comments // []) | length')"
  if [ "$comments_count" -gt 0 ]; then
    comments_text="$(printf '%s' "$issue_json" \
      | jq -r '(.comments // [])[]
                | "- " + (.author.login // "unknown") + ":\n  "
                  + ((.body // "") | gsub("\n"; "\n  "))')"
  fi

  {
    cat <<EOF
You are working autonomously on a GitHub repository.

Your task is to implement a fix or feature for the following GitHub issue:

Issue #${number}: ${title}

${body}

---
EOF
    if [ -n "$comments_text" ]; then
      printf '\nIssue comments:\n%s\n' "$comments_text"
    fi
    cat <<EOF

Instructions:
- Implement the fix or feature described in the issue.
- Write tests if applicable.
- Commit your changes with a descriptive commit message referencing the issue
  number (e.g. "fix: resolve null pointer in auth handler, closes #${number}").
- Open a pull request against main with a clear title. The PR description
  MUST begin with "Closes #${number}" on its own line at the top — GitHub
  uses that to auto-close the issue when the PR merges. Add any further
  context (summary of changes, test plan) below that line.
- Do not merge the PR yourself. Stop after the PR is open.
- Do not ask for confirmation. Work autonomously to completion.
EOF
  } > "$out_file"
}

# Args: <issue_detail_json> <pr_number> <reviewer_login> <iteration> <output_path>
# The harness is expected to use `gh` to read the review state itself —
# this prompt tells it which tools to use and how to bound its work.
build_review_fix_prompt() {
  local issue_json="$1"
  local pr_number="$2"
  local reviewer="$3"
  local iteration="$4"
  local out_file="$5"

  local number title body
  number="$(printf '%s' "$issue_json" | jq -r '.number')"
  title="$(printf  '%s' "$issue_json" | jq -r '.title')"
  body="$(printf   '%s' "$issue_json" | jq -r '.body // ""')"

  cat > "$out_file" <<EOF
You are working autonomously on a GitHub repository.

You previously opened PR #${pr_number} to address issue #${number}:
  "${title}"

A reviewer (${reviewer}) has submitted feedback that requires your attention.
This is review-fix iteration ${iteration}.

Use the gh CLI to read the review and its inline comments yourself:

  gh pr view ${pr_number} --json reviews,reviewThreads,comments,files,headRefName
  gh pr diff ${pr_number}
  gh api repos/{owner}/{repo}/pulls/${pr_number}/comments
  gh api repos/{owner}/{repo}/pulls/${pr_number}/reviews

Then:
1. Read the latest review from ${reviewer} and its inline comments carefully.
2. Make code changes that address each concrete feedback point.
3. Commit with a clear message (e.g. "fix: address review feedback").
4. Push to the existing branch. Do NOT open a new PR.

Constraints:
- Keep the fix focused on the reviewer's feedback. Do not expand scope.
- If a comment is genuinely unclear or you disagree, post a brief reply
  to that thread via:
    gh api repos/{owner}/{repo}/pulls/${pr_number}/comments/<id>/replies -F body='...'
  and still apply the change unless doing so would be incorrect.
- Do NOT merge the PR.
- Do NOT open additional PRs.
- Do NOT ask for confirmation. Work autonomously to completion.

Original issue body for context:
---
${body}
---
EOF
}
