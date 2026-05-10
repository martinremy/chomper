#!/usr/bin/env bash
# github.sh — thin wrappers over `gh` for chomper.
#
# Every function here either prints to stdout (the caller captures it) or
# returns a status code. Side-effect logging goes to stderr.

set -euo pipefail

# --- Environment checks --------------------------------------------------

gh_require_env() {
  if ! command -v gh >/dev/null 2>&1; then
    printf 'error: `gh` CLI not found on PATH. Install: https://cli.github.com/\n' >&2
    return 1
  fi
  if ! command -v jq >/dev/null 2>&1; then
    printf 'error: `jq` not found on PATH. Install via your package manager.\n' >&2
    return 1
  fi
  if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    printf 'error: not inside a git repository.\n' >&2
    return 1
  fi

  # Auth check is scoped to the host of the current repo's remote, so an
  # unrelated stalled entry on another host (e.g. an enterprise instance)
  # doesn't fail the whole probe. Falls back to github.com if no remote.
  local host
  host="$(git remote get-url origin 2>/dev/null \
            | sed -nE 's#^(https?://|git@)([^/:]+).*#\2#p')"
  : "${host:=github.com}"
  if ! gh auth status -h "$host" >/dev/null 2>&1; then
    printf 'error: `gh` is not authenticated for %s. Run: gh auth login -h %s\n' \
      "$host" "$host" >&2
    return 1
  fi
}

gh_current_repo() {
  gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null
}

# --- Issue fetching and filtering ----------------------------------------

# Fetch open issues with the fields needed for filtering. Issue bodies are
# fetched per-issue later (via `gh issue view`) to keep this call cheap.
gh_fetch_open_issues() {
  gh issue list --state open --limit 200 \
    --json number,title,labels,url
}

# Filter issues by title substring (case-insensitive) and labels (OR).
# Arguments: <issues_json> <title_match> [label ...]
# Output: filtered, sorted-by-number JSON array.
gh_filter_issues() {
  local issues_json="$1"
  local title_match="${2:-}"
  shift 2 || true

  local labels_json='[]'
  if [ "$#" -gt 0 ]; then
    labels_json="$(printf '%s\n' "$@" | jq -R . | jq -s .)"
  fi

  printf '%s' "$issues_json" | jq \
    --arg title_match "$title_match" \
    --argjson labels "$labels_json" '
      sort_by(.number)
      | map(select(
          ( ($title_match | length) == 0
            or (.title | ascii_downcase | contains($title_match | ascii_downcase)) )
          and
          ( ($labels | length) == 0
            or any(.labels[]?; .name as $n | $labels | index($n) != null) )
        ))
    '
}

# Fetch the full detail (body + comments) for one issue.
gh_fetch_issue_detail() {
  local number="$1"
  gh issue view "$number" --json number,title,body,comments,url
}

# --- PR lifecycle --------------------------------------------------------

# Poll for an open PR with HEAD = <branch>. Print the PR number on success.
# Args: <branch> <timeout_seconds>
gh_poll_pr() {
  local branch="$1"
  local timeout_seconds="${2:-60}"
  local interval=5
  local elapsed=0
  while [ "$elapsed" -lt "$timeout_seconds" ]; do
    local pr
    pr="$(gh pr list --head "$branch" --state open \
            --json number --jq '.[0].number // empty' 2>/dev/null || true)"
    if [ -n "$pr" ]; then
      printf '%s\n' "$pr"
      return 0
    fi
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done
  return 1
}

# Poll PR checks. Returns 0 on all-pass, 1 on failure/timeout.
# Args: <pr_number> <timeout_minutes>
#
# A freshly-opened PR can take ~30s for GitHub to register its checks.
# Before EMPTY_GRACE_SECONDS, an empty bucket list means "checks haven't
# registered yet, keep waiting." After the grace period, an empty list
# means "this repo has no checks configured; safe to merge."
gh_poll_checks() {
  local pr_number="$1"
  local timeout_minutes="${2:-30}"
  local timeout_seconds=$((timeout_minutes * 60))
  local interval=15
  local empty_grace_seconds=60
  local elapsed=0

  while [ "$elapsed" -lt "$timeout_seconds" ]; do
    local buckets
    buckets="$(gh pr checks "$pr_number" --json bucket \
                --jq '[.[].bucket]' 2>/dev/null || printf '[]')"

    if [ "$buckets" = '[]' ]; then
      if [ "$elapsed" -ge "$empty_grace_seconds" ]; then
        return 0  # no checks really means no checks
      fi
      # Within grace period: keep waiting for checks to register
    else
      local verdict
      verdict="$(printf '%s' "$buckets" | jq -r '
        if all(.[]; . == "pass" or . == "skipping") then "pass"
        elif any(.[]; . == "fail" or . == "cancel") then "fail"
        else "pending"
        end')"

      case "$verdict" in
        pass) return 0 ;;
        fail) return 1 ;;
        pending) ;;
      esac
    fi

    sleep "$interval"
    elapsed=$((elapsed + interval))
  done
  return 1
}

# Merge a PR using the configured strategy.
# Args: <pr_number> <strategy: squash|merge|rebase>
#
# NB: we deliberately do NOT pass --delete-branch here. gh's --delete-branch
# tries to delete the *local* branch as a post-merge step, which fails when
# the branch is currently checked out in our worktree — and that failure
# masks a successful merge with a non-zero exit code. Chomper does its own
# local + remote branch cleanup in process_issue after worktree removal.
gh_merge_pr() {
  local pr_number="$1"
  local strategy="$2"
  local flag
  case "$strategy" in
    squash) flag='--squash' ;;
    merge)  flag='--merge' ;;
    rebase) flag='--rebase' ;;
    *)
      printf 'error: unknown merge strategy: %s\n' "$strategy" >&2
      return 1
      ;;
  esac
  gh pr merge "$pr_number" "$flag" --auto
}

# Best-effort delete of the remote branch (head). Tolerates "branch doesn't
# exist" (e.g., repo has "Automatically delete head branches" enabled).
# Args: <branch>
gh_delete_remote_branch() {
  local branch="$1"
  git push origin --delete "$branch" >/dev/null 2>&1 || true
}

# Confirm that a PR has actually merged. `gh pr merge --auto` may return
# success after only *enabling* auto-merge when branch protection adds
# gates beyond CI; without this poll, the next issue's worktree could be
# based on a trunk that doesn't yet include this merge.
#
# We check PR.state == "MERGED" rather than a `merged` field; `merged`
# exists in GraphQL but is NOT one of the fields exposed by
# `gh pr view --json`, so the previous version always saw "field unknown",
# fell through to "not merged", and timed out on every successful merge.
#
# Args: <pr_number> [timeout_seconds, default 60]
gh_wait_for_pr_merged() {
  local pr_number="$1"
  local timeout_seconds="${2:-60}"
  local interval=5
  local elapsed=0
  while [ "$elapsed" -lt "$timeout_seconds" ]; do
    local state
    if state="$(gh pr view "$pr_number" --json state --jq .state 2>/dev/null)"; then
      [ "$state" = "MERGED" ] && return 0
    fi
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done
  return 1
}

# Poll the PR for the most recent review submitted by any of the given
# reviewer logins. Prints the matching review JSON on stdout on success,
# returns non-zero on timeout.
#
# `--arg since_iso` filters out reviews submitted before chomper started
# polling — important for fix iterations, where we don't want to re-match
# a review that triggered the previous iteration.
#
# Args: <pr_number> <timeout_minutes> <since_iso8601> <reviewer> [reviewer ...]
gh_wait_for_review() {
  local pr_number="$1"
  local timeout_minutes="$2"
  local since_iso="$3"
  shift 3
  local reviewers=("$@")

  if [ "${#reviewers[@]}" -eq 0 ]; then
    return 1
  fi

  local repo
  repo="$(gh_current_repo)"

  local reviewers_json
  reviewers_json="$(printf '%s\n' "${reviewers[@]}" | jq -R . | jq -s .)"

  local timeout_seconds=$((timeout_minutes * 60))
  local interval=20
  local elapsed=0

  while [ "$elapsed" -lt "$timeout_seconds" ]; do
    local reviews
    reviews="$(gh api "repos/${repo}/pulls/${pr_number}/reviews" 2>/dev/null || printf '[]')"

    local match
    match="$(printf '%s' "$reviews" | jq -c \
              --argjson rs "$reviewers_json" \
              --arg since "$since_iso" \
              '[.[] | select((.user.login as $u | $rs | index($u)) and (.submitted_at > $since))]
               | sort_by(.submitted_at) | last // empty')"

    if [ -n "$match" ] && [ "$match" != "null" ]; then
      printf '%s\n' "$match"
      return 0
    fi

    sleep "$interval"
    elapsed=$((elapsed + interval))
  done
  return 1
}
