#!/usr/bin/env bash
# harness_codex.sh — adapter for OpenAI Codex CLI.
#
# Two roles:
#   harness_run_worker  - the agent that does the actual issue work
#   harness_run_judge   - a tiny, restricted call used by the auto-answer
#                         proxy to classify silence and answer questions
#
# NOTE (v1): codex's structured-event protocol is not as well-defined for
# external interception as claude's stream-json. When CHOMPER_STREAM_MODE=1
# the codex worker is launched normally (no stream flags) and the judge
# proxy will fall back to text-buffer detection. Auto-answer with codex is
# best-effort; for full structured interception, use claude as the worker.

set -euo pipefail

# Args: <repo_dir> <prompt_file>
harness_run_worker() {
  local repo_dir="$1"
  local prompt_file="$2"

  if ! command -v codex >/dev/null 2>&1; then
    printf 'error: `codex` CLI not found on PATH.\n' >&2
    return 127
  fi
  if [ ! -r "$prompt_file" ]; then
    printf 'error: prompt file not readable: %s\n' "$prompt_file" >&2
    return 1
  fi

  (
    cd "$repo_dir"
    codex exec \
      -a on-request \
      --search \
      - < "$prompt_file"
  )
}

# Reads prompt from stdin, prints answer to stdout. Used by the judge proxy.
# Optional env:
#   CHOMPER_JUDGE_MODEL          model id override (default: codex picks)
#   CHOMPER_JUDGE_SYSTEM_PROMPT  prepended to the user prompt
harness_run_judge() {
  if ! command -v codex >/dev/null 2>&1; then
    printf 'error: `codex` CLI not found on PATH.\n' >&2
    return 127
  fi

  local model_args=()
  if [ -n "${CHOMPER_JUDGE_MODEL:-}" ]; then
    model_args=(--model "$CHOMPER_JUDGE_MODEL")
  fi

  # Codex's `exec` does not expose a system-prompt flag the way claude does,
  # so we prepend the system prompt to the user prompt here. `-a never`
  # forbids approval requests — the judge has no business asking. Empty
  # sandbox permissions strip any accidental shell capability.
  local system="${CHOMPER_JUDGE_SYSTEM_PROMPT:-You are a concise assistant.}"
  {
    printf '%s\n\n---\n\n' "$system"
    cat
  } | codex exec \
        "${model_args[@]+"${model_args[@]}"}" \
        -a never \
        -c 'sandbox_permissions=[]' \
        -
}
