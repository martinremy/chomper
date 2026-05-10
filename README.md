# chomper

A small bash CLI that grinds through your GitHub backlog using an AI coding
agent. You run it from inside any repo; it fetches open issues, filters them,
and for each match it branches, prompts a harness (Claude Code or Codex CLI),
waits for the PR, waits for CI, and merges.

It's a thin orchestrator — the AI does the work, `gh` does the GitHub plumbing,
and chomper just keeps the loop honest.

## Prerequisites

- **`gh`** (GitHub CLI), authenticated: `gh auth login`
- **`jq`** for JSON wrangling
- **`git`**, run from inside a repo with a remote on GitHub
- **`python3`** (3.9+) for config parsing and the auto-answer proxy
- **A harness CLI** — at least one of:
  - [`claude`](https://docs.anthropic.com/claude/docs/claude-code) (Claude Code)
  - [`codex`](https://github.com/openai/codex) (OpenAI Codex CLI)

`chomper doctor` will tell you which of these are missing on your machine.

## Install

```sh
git clone https://github.com/martinremy/chomper.git ~/src/chomper
~/src/chomper/bin/install
```

The install script:

1. Symlinks `chomper` into `~/.local/bin` (created if missing); pass
   `--target /your/dir` to install elsewhere. We deliberately avoid
   `/opt/homebrew/bin` and `/usr/local/bin` since those are Homebrew's
   prefix on macOS and a user-installed CLI doesn't belong there.
2. Verifies the target directory is on your `PATH`; prints exact
   instructions if it isn't.
3. Warns if you're installing from a git worktree (the symlink will
   break if the worktree is removed — install from your main checkout
   instead).
4. Runs `chomper doctor` to verify dependencies, gh auth, and config.

Re-run `bin/install` at any time to update or re-link the symlink. The
`chomper` script resolves its own `lib/` directory through symlinks, so the
single symlink is sufficient — no need to copy `lib/`.

## Verify

From any GitHub repo:

```sh
chomper doctor      # audit deps, auth, config, worktree paths
chomper --dry-run   # show which issues would be worked
```

## Configuration

Drop a `.chomper.yaml` at the root of the repo you want to chomp. Everything
is optional; the example below shows the defaults:

```yaml
harness: claude          # claude | codex
filter:
  labels: [chomper]      # match if ANY label is present (OR); default ["chomper"]
  title_match: ""        # case-insensitive substring on title
merge_strategy: squash   # squash | merge | rebase
ci_timeout_minutes: 30   # wait this long for CI before giving up
trunk_branch: main       # branch chomper resets to between issues

# Auto-answer (optional; opt-in)
auto_answer: false                 # route worker via lib/judge.py
auto_answer_model: ""              # judge model (empty = adapter default)
auto_answer_max_questions: 5       # abort issue after this many synth answers
auto_answer_silence_threshold_s: 30  # silence before classifier fires
```

See `.chomper.yaml.example` in this repo for the annotated version. The real
`.chomper.yaml` is gitignored so each user's filters stay local.

**Precedence:** CLI flags override config; config overrides built-in defaults.

## Usage

```text
chomper [options]

Options:
  --harness <claude|codex>   override harness from config
  --label <label>            filter by label (repeatable; OR logic)
  --title <string>           filter by title substring (case-insensitive)
  --dry-run                  print matching issues and exit
  --help, -h                 show usage
```

Examples:

```sh
# Just see what would be worked, no changes:
chomper --dry-run --label bug

# Work all open issues tagged "good first issue" with Codex:
chomper --harness codex --label "good first issue"

# Use whatever's in .chomper.yaml:
chomper
```

## What chomper does per issue

Each issue runs in its own **git worktree**, never in your invoking checkout.
Your main working tree stays on whatever branch you had — you can keep working
in it while chomper runs in parallel underneath.

1. `git fetch origin <trunk_branch>` — refresh the local tracking ref only,
   no checkout, no working-tree change
2. `git worktree add -b fix/issue-<N> /tmp/chomper-worktrees/<owner>/<repo>/issue-<N> origin/<trunk_branch>`
3. Build a prompt at `/tmp/chomper_prompt_<N>.md` describing the issue
4. Pipe the prompt into the harness (`claude --print` or `codex exec`) with
   the worktree as cwd — the harness writes code, commits, pushes, and
   opens a PR autonomously
5. Poll `gh pr list --head fix/issue-<N>` (60s timeout) for the PR
6. Poll `gh pr checks <PR>` until all checks pass (or `ci_timeout_minutes`
   elapses)
7. `gh pr merge --auto` with the configured strategy and `--delete-branch`
8. On success: `git worktree remove --force` the worktree dir, `git branch -D`
   the now-merged branch. Move to the next issue.

**On failure** (harness exits non-zero, no PR opens, CI red, merge refused),
chomper logs a warning **and preserves the worktree** at
`/tmp/chomper-worktrees/<owner>/<repo>/issue-<N>/` so you can `cd` in and
see what state the agent left behind. Recover with:

```sh
git worktree remove --force /tmp/chomper-worktrees/<owner>/<repo>/issue-<N>
git branch -D fix/issue-<N>
```

Chomper does not abort the whole batch on a single miss — it logs and moves
on to the next issue. If a stale worktree exists when chomper gets back to
that issue number on a future run, chomper **refuses the issue and warns**
rather than overwriting any in-progress recovery work.

## Auto-answer (optional)

By default chomper invokes the worker harness and lets it run to completion.
If the worker pauses to ask a question — via `AskUserQuestion`, a skill that
needs user input, a plan-mode approval, or even a free-text `Continue? (y/n)`
— the loop will hang.

Enable `auto_answer: true` to route the worker through `lib/judge.py`, a
Python proxy that:

1. Spawns the worker in stream-json I/O mode (claude) so individual events
   are visible
2. Watches for silence — no events for `auto_answer_silence_threshold_s`
3. On silence, calls a smaller, restricted "judge" instance of the **same
   harness** (so it uses your existing subscription/auth) to classify the
   silence as `working`, `waiting`, or `errored`
4. On `waiting`, calls the judge again to answer the question with full
   issue context, then writes a synthesized `tool_result` event back into
   the worker's stdin

Detection is symptom-driven, not protocol-specific: the proxy detects
*that* the agent stopped, not *which* tool stopped it. That makes it
robust across skill ecosystems — new plugins that introduce new prompting
mechanisms get handled by the same loop.

**Hard guards** built in:

- `auto_answer_max_questions` — if exceeded, abort the issue. More than a
  handful of questions usually means the agent is stuck or scope is wrong.
- Judge call failure → abort. We don't guess answers; that's worse than
  failing loudly.

**Current limitations:**

- Full structured interception (stream-json) is implemented for claude.
  With codex, the proxy works in best-effort text mode — silence detection
  fires, but synthesizing structured answers back to codex is less
  reliable. For now, prefer claude as the worker when auto-answer is on.
- `python3` must be on PATH.

## Wait-for-reviews (optional)

Many repos run automated review bots (CodeRabbit, Greptile, GitHub Copilot
review, etc.) that comment on every PR. Chomper can wait for those reviews
and have the agent respond to the feedback before merging.

Opt in by listing the reviewer logins in `.chomper.yaml`:

```yaml
wait_for_reviews:
  reviewers:
    - coderabbitai
    - "greptileai[bot]"
  timeout_minutes: 15
  max_iterations: 10
  judge_adjudicates: true
  approve_state_required: false
```

Per-PR loop:

1. CI turns green.
2. Poll `gh api repos/.../pulls/<N>/reviews` for a review from any
   configured reviewer (timeout: `timeout_minutes`).
3. **No review in time** → proceed to merge (unless
   `approve_state_required: true`, which preserves the worktree and aborts).
4. **`APPROVED`** → proceed to merge.
5. **`COMMENT` or `CHANGES_REQUESTED`** → if `judge_adjudicates: true`,
   ask the judge what the review demands:
   - `merge_as_is` — proceed to merge (review was informational / nit-only)
   - `needs_fix` — re-invoke the harness in the worktree with a review-fix
     prompt; the harness uses `gh pr view` / `gh pr diff` /
     `gh api .../pulls/N/comments` itself to read the feedback, makes the
     edits, commits, pushes; chomper re-polls CI and loops back to step 2
   - `escalate` — preserve worktree, abort issue (review meaningfully
     expands scope or raises architectural concerns)
6. After `max_iterations` rounds without resolution, preserve the worktree
   and abort.

The **iteration cap** is the load-bearing safety belt: it prevents an
opinionated reviewer from looping chomper forever. The **judge** filters
nit-level feedback from substantive feedback, so chomper doesn't push
no-op revisions in response to praise or stylistic suggestions.

## The harness abstraction

`lib/harness_claude.sh` and `lib/harness_codex.sh` each export the same
function: `harness_run <repo_dir> <prompt_file>`. The main script doesn't know
or care which one is loaded — it just calls `harness_run`. Adapter
responsibilities:

- `cd` into the target repo so the harness's working directory is correct
- Stream the prompt **content** (not the file path) into the harness via
  stdin — the harness only ever sees the bytes
- Invoke the harness in non-interactive mode with autonomy flags set:
  `claude --print --enable-auto-mode` /
  `codex exec -a on-request --search`
- Surface a non-zero exit if the CLI isn't installed or the prompt file
  isn't readable

Adding a new harness is a matter of dropping a `lib/harness_<name>.sh` that
defines `harness_run`, and adding the `<name>` case to `select_harness` in
the main script.

## Safety notes

The autonomy flags exist because chomper runs the harness with **no
human-in-the-loop confirmation** — it's an unattended loop. That means the
harness will execute arbitrary shell, push branches, and open PRs against
whatever GitHub identity `gh` is authenticated as. Run chomper only against
repos where you're comfortable with that, ideally in a fresh worktree or
clone. If you want a tighter leash, drop the autonomy flag from the adapter
and run interactively for a few issues first.

## Layout

```
chomper                  # main executable orchestrator
bin/
  install                # symlink chomper into PATH and run doctor
lib/
  github.sh              # gh wrappers: fetch, filter, poll, merge
  prompt_builder.sh      # renders the per-issue prompt
  parse_config.py        # YAML config reader (emits shell assignments)
  harness_claude.sh      # adapters for `claude`: worker + judge
  harness_codex.sh       # adapters for `codex`:  worker + judge
  judge.py               # auto-answer proxy (silence -> classify -> answer)
.chomper.yaml.example    # documented config template
```
