#!/usr/bin/env bash
# harness_claude.sh — adapter for Anthropic Claude Code CLI.
#
# Two roles:
#   harness_run_worker  - the agent that does the actual issue work
#   harness_run_judge   - a tiny, restricted call used by the auto-answer
#                         proxy to classify silence and answer questions
#
# When CHOMPER_STREAM_MODE=1 the worker is launched in stream-json I/O so the
# judge proxy (lib/judge.py) can intercept tool_use events and write
# synthesized tool_result events back. Otherwise the worker uses plain text
# stdio and chomper invokes it directly.

set -euo pipefail

# Args: <repo_dir> <prompt_file>
harness_run_worker() {
  local repo_dir="$1"
  local prompt_file="$2"

  if ! command -v claude >/dev/null 2>&1; then
    printf 'error: `claude` CLI not found on PATH.\n' >&2
    return 127
  fi
  if [ ! -r "$prompt_file" ]; then
    printf 'error: prompt file not readable: %s\n' "$prompt_file" >&2
    return 1
  fi

  local stream_args=()
  if [ "${CHOMPER_STREAM_MODE:-}" = "1" ]; then
    # Stream-json I/O so the proxy can interject tool_result events.
    # `--include-partial-messages` makes silence detection more accurate by
    # surfacing partial tokens during long thinking passes.
    stream_args=(
      --input-format stream-json
      --output-format stream-json
      --include-partial-messages
      --verbose
    )
  fi

  (
    cd "$repo_dir"
    claude \
      --print \
      --enable-auto-mode \
      "${stream_args[@]+"${stream_args[@]}"}" \
      < "$prompt_file"
  )
}

# Reads prompt from stdin, prints answer to stdout. Used by the judge proxy.
# Optional env:
#   CHOMPER_JUDGE_MODEL          model id (default: claude-haiku-4-5-20251001)
#   CHOMPER_JUDGE_SYSTEM_PROMPT  system prompt overlay
harness_run_judge() {
  if ! command -v claude >/dev/null 2>&1; then
    printf 'error: `claude` CLI not found on PATH.\n' >&2
    return 127
  fi

  local model="${CHOMPER_JUDGE_MODEL:-claude-haiku-4-5-20251001}"
  local system="${CHOMPER_JUDGE_SYSTEM_PROMPT:-You are a concise assistant.}"

  # The judge does pure text reasoning — no tools, no session state, no
  # autonomy flags. `--allowedTools ""` collapses the action space to "emit
  # text". `--no-session-persistence` keeps these tiny calls out of the
  # local session log.
  claude \
    --print \
    --model "$model" \
    --output-format text \
    --no-session-persistence \
    --allowedTools "" \
    --append-system-prompt "$system"
}
