# 🦷 chomper

[![CI](https://github.com/martinremy/chomper/actions/workflows/ci.yml/badge.svg)](https://github.com/martinremy/chomper/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/martinremy/chomper.svg)](https://pkg.go.dev/github.com/martinremy/chomper)
[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](LICENSE)

> A Go CLI that grinds through your GitHub backlog using an AI coding agent.

Run it from inside any repo. Chomper fetches open issues, filters them,
and for each match: creates an isolated git worktree, prompts a harness
(Claude Code or Codex CLI), waits for the PR, waits for CI, and merges.

It's a thin orchestrator — the AI does the work, `gh` does the GitHub
plumbing, and chomper keeps the loop honest.

## ✨ At a glance

- 🤖 **Drives Claude Code or Codex CLI** as the worker — pick your favorite harness.
- 🌳 **Isolated worktrees** per issue — your main checkout never gets touched.
- 🔁 **Resume-friendly** — re-run after an interruption (Ctrl-C, lost network, CI timeout) and chomper picks up where it left off.
- 🩹 **Auto-fix red CI** (on by default, iteration-capped) — the harness gets the failing log tails and tries again.
- 🧑‍⚖️ **Optional supervisor** auto-answers worker prompts so the loop never hangs on `AskUserQuestion`-style pauses.
- 👀 **Optional review-bot loop** — wait for CodeRabbit / Greptile / Copilot review, route feedback through the harness, then merge.
- 🛡️ **Strict failure preservation** — anything weird leaves the worktree intact for inspection.

## 📋 Prerequisites

- 🐙 **`gh`** (GitHub CLI), authenticated: `gh auth login`
- 🌿 **`git`**, run from inside a repo with a remote on GitHub
- 🐹 **`go`** (1.22+), for building the binary
- 🧠 **A harness CLI** — at least one of:
  - [`claude`](https://docs.anthropic.com/claude/docs/claude-code) (Claude Code)
  - [`codex`](https://github.com/openai/codex) (OpenAI Codex CLI)

`chomper doctor` will tell you which of these are missing.

## 📦 Install

```sh
git clone https://github.com/martinremy/chomper.git
cd chomper
bin/install
```

The install script:

1. Runs `go build -o chomper .` to produce a single static binary.
2. Symlinks `chomper` into `~/.local/bin` (created if missing). We
   deliberately avoid `/opt/homebrew/bin` and `/usr/local/bin` — those
   are Homebrew's prefix, not user-installed-CLI territory.
3. Verifies the target directory is on `PATH`; prints exact
   instructions if it isn't.
4. Warns if you're installing from a git worktree (the symlink breaks
   if the worktree is later removed).
5. Runs `chomper doctor` to verify dependencies, gh auth, and config.

Pass `--target /your/dir` to install elsewhere. Re-run `bin/install` at
any time to rebuild and re-link.

## ⚙️ Configuration

Drop a `.chomper.yaml` at the root of the repo you want to chomp. All
keys are optional; the example below shows the defaults:

```yaml
harness: claude          # claude | codex
filter:
  labels: [chomper]      # match if ANY label is present (OR); default ["chomper"]
  title_match: ""        # case-insensitive substring on title
merge_strategy: squash   # squash | merge | rebase
ci_timeout_minutes: 30   # wait this long for CI before giving up
trunk_branch: main       # branch chomper resets to between issues

# Auto-answer (optional; opt-in)
auto_answer: false                  # supervise the worker via the judge
auto_answer_model: ""               # judge model (empty = adapter default)
auto_answer_max_questions: 5        # abort issue after this many synth answers
auto_answer_silence_threshold_s: 30 # silence before classifier fires

# Wait for code-review bots (optional)
wait_for_reviews:
  reviewers: []                     # ["coderabbitai", "greptileai[bot]", ...]
  timeout_minutes: 15
  max_iterations: 10
  judge_adjudicates: true           # judge decides if review is actionable
  approve_state_required: false

# Auto-fix red CI (on by default; the iteration cap bounds blast radius)
fix_ci:
  enabled: true                     # enter fix loop when CI fails on a chomper PR
  max_iterations: 3                 # cap before preserving worktree and aborting
```

See [`.chomper.yaml.example`](.chomper.yaml.example) for an annotated
version. The real `.chomper.yaml` is gitignored so each user's filters
stay local.

**Precedence:** CLI flags > `.chomper.yaml` > built-in defaults.

YAML parsing is strict: unknown keys are a hard error, not silently
ignored. Catches typos early.

## 🚀 Usage

```text
chomper [subcommand] [options]

Subcommands:
  doctor                     audit local environment

Options:
  --harness <claude|codex>   override harness from config
  --label <label>            filter by label (repeatable; OR logic)
  --title <string>           filter by title substring (case-insensitive)
  --dry-run                  print matching issues and exit
  --help, -h                 show usage
```

Examples:

```sh
chomper --dry-run                       # see what would be worked
chomper --label "good first issue"      # one-off label override
chomper --harness codex                 # override harness
chomper                                 # work all matching issues
```

## 🔄 What chomper does per issue

Each issue runs in its own **git worktree** under
`/tmp/chomper-worktrees/<owner>/<repo>/issue-<N>/`. Your main checkout
is never touched — you can keep working in it while chomper runs.

1. `git fetch origin <trunk_branch>` — refresh the local tracking ref.
2. `git worktree add -b fix/issue-<N> <worktree-path> origin/<trunk_branch>`.
3. Build a prompt describing the issue, including a `Closes #N`
   instruction for the PR description (so GitHub auto-closes on merge).
4. Invoke the harness with the prompt — direct mode by default, or
   through the **Supervisor** if `auto_answer: true`.
5. Poll for the PR to be opened (60s timeout).
6. **Poll CI** checks until green (with a 60s grace period for check
   registration). Branches based on outcome:
   - ✅ **Green** → continue to step 7.
   - ❌ **CI fails** → if `fix_ci.enabled` (default: true), enter the
     CI-fix loop (below); otherwise preserve worktree and abort with a
     "fix the failing checks" hint.
   - ⏱️ **CI times out** (still pending past `ci_timeout_minutes`) →
     preserve and abort with a "re-run to keep polling" hint. Because
     chomper's resume path trusts the remote PR, re-invoking it resumes
     the poll on the same open PR without re-running the harness.

   **CI-fix loop** (only when triggered): fetch failing checks and the
   tails of their failed-step logs, build a focused fix prompt,
   re-invoke the harness, push to the existing branch, re-poll CI.
   Iterate up to `fix_ci.max_iterations` (default: 3). Exits on CI
   green (continue to step 7), iterations exhausted, harness nonzero,
   or CI transitioning to timeout/unknown — all of the latter preserve
   worktree and abort with a contextual warning.
7. **(Optional)** Wait for code-review bots if configured; iterate
   fix-and-review loops up to `max_iterations` times.
8. `gh pr merge --auto` with the configured strategy.
9. Poll until the PR's state is `MERGED` (handles auto-merge being
   queued by branch protection).
10. Remove the worktree, delete local + remote branches.
11. Move to the next issue.

On any failure (no PR opened, CI red, merge refused, judge unreachable),
chomper logs a warning and **preserves the worktree** for inspection.
Recovery is:

```sh
git worktree remove --force /tmp/chomper-worktrees/<owner>/<repo>/issue-<N>
git branch -D fix/issue-<N>
```

## 🔁 Resume support

Long-running loops get interrupted (Ctrl-C, network blip, laptop close,
CI exhausting its timeout window). Just **re-run `chomper`** in the same
repo and it'll resolve the right thing to do per matched issue:

- If a PR already exists for the issue's branch on the remote, chomper
  re-attaches to that PR (skips the harness step) and continues the
  pipeline — usually picking up at the CI poll.
- If the worktree exists but no PR was opened, chomper rebuilds the
  worktree off fresh trunk and invokes the harness from scratch.
- If nothing exists yet, it runs the full pipeline.

Origin is treated as the source of truth, so this is safe even after a
fresh clone on a different machine.

## 🧑‍⚖️ Auto-answer (optional)

By default chomper invokes the worker harness and lets it run to
completion. If the worker pauses to ask a question — via
`AskUserQuestion`, a skill that needs user input, a plan-mode
approval, or even a free-text `Continue? (y/n)` — the loop hangs.

Enable `auto_answer: true` to route the worker through the Supervisor:

1. Spawns the worker in stream-json I/O mode (claude only — codex
   doesn't support the streaming contract).
2. Reads events; pretty-prints them as `→ Bash: …`, `← (5 lines)`,
   `· thinking…`.
3. Watches for silence — no events for `auto_answer_silence_threshold_s`.
4. On silence, calls a smaller, restricted **judge** instance of the
   same harness to classify the silence (Haiku by default for claude):
   - `working` → exponential backoff; keep waiting.
   - `waiting` → call the judge again to answer, write a synthesized
     `tool_result` event into the worker's stdin.
   - `errored` → kill the worker, preserve the worktree.
5. Caps total questions at `auto_answer_max_questions` per issue.

Detection is symptom-driven, not protocol-specific: the proxy doesn't
need to know which tool the worker invoked. New skills that introduce
new prompting mechanisms get handled by the same loop.

## 👀 Wait-for-reviews (optional)

Many repos run automated review bots (CodeRabbit, Greptile, GitHub
Copilot review, etc.). Chomper can wait for those reviews and have
the agent respond to the feedback before merging.

Opt in by listing the reviewer logins:

```yaml
wait_for_reviews:
  reviewers:
    - coderabbitai
    - "greptileai[bot]"
  timeout_minutes: 15
  max_iterations: 10
  judge_adjudicates: true
```

Per-PR loop:

1. CI turns green.
2. Poll `gh api repos/.../pulls/<N>/reviews` for a review from any
   configured reviewer (timeout: `timeout_minutes`).
3. **No review in time** → proceed to merge (unless
   `approve_state_required: true`, which preserves and aborts).
4. **`APPROVED`** → proceed to merge.
5. **`COMMENT` or `CHANGES_REQUESTED`** → if `judge_adjudicates: true`,
   ask the judge what to do:
   - `merge_as_is` — proceed to merge (review was informational).
   - `needs_fix` — re-invoke the harness with a review-fix prompt; the
     harness uses `gh pr view` / `gh api .../pulls/N/comments` itself
     to read the feedback, makes edits, commits, pushes; chomper
     re-polls CI and loops back to step 2.
   - `escalate` — preserve worktree, abort issue (review meaningfully
     expands scope).
6. After `max_iterations` rounds without resolution, preserve the
   worktree and abort.

## 🏗️ Architecture

```
chomper/                            # repo root
├── main.go                         # CLI parsing, dispatch
├── go.mod                          # gopkg.in/yaml.v3
├── bin/install                     # build + symlink + doctor
└── internal/
    ├── chomper/                    # orchestration
    │   ├── process.go              #   per-issue pipeline
    │   ├── resume.go               #   resume-point decision
    │   ├── fix_ci.go               #   CI-fix loop
    │   ├── review_loop.go          #   wait-for-reviews state machine
    │   └── doctor.go               #   environment audit
    ├── config/                     # YAML loader (strict) + defaults + validation
    ├── gh/                         # `gh` CLI wrappers
    ├── git/                        # `git` CLI wrappers (fetch + worktree + branch)
    ├── harness/                    # adapter interface + claude + codex + Supervisor
    │   ├── harness.go              #   interface
    │   ├── claude.go               #   Claude Code adapter
    │   ├── codex.go                #   Codex adapter
    │   ├── supervisor.go           #   stream-json supervisor (replaces v0.1 judge.py)
    │   ├── event.go                #   stream-json event + formatting + ring buffer
    │   └── process.go              #   WorkerProcess type
    ├── judge/                      # three judge roles (classify, answer, adjudicate)
    │   ├── judge.go                #   public API
    │   ├── prompts.go              #   system prompts
    │   └── parse.go                #   tolerant JSON extraction
    ├── prompt/                     # issue + review-fix prompt rendering
    └── tui/                        # spinner with phase labels
```

The `internal/` boundary means every package's API is enforced by the
Go compiler — only `main.go` and packages under `chomper/...` can
import these. If chomper-as-a-library ever makes sense, the seams are
already drawn correctly.

## ✅ Testing

```sh
go test ./...           # all packages
go test -race ./...     # with race detector
go test -cover ./...    # with coverage
```

The test suite focuses on the load-bearing pure logic:

| Package | Coverage | What's tested |
|---|---|---|
| `internal/config` | 86% | YAML merge, strict-unknown-key, CLI overlay, validation |
| `internal/gh` | 28% | `Filter`, `parseHost`, `classifyChecks` (subprocess wrappers are integration-test territory) |
| `internal/harness` | 29% | `Event.ToolUseID`, `IsTerminal`, `FormatEvent`, `SynthesizeToolResult`, `ringBuffer` |
| `internal/judge` | 28% | `ExtractFirstJSON` against code-fenced, nested, prose-wrapped, malformed input |
| `internal/prompt` | 100% | issue + review-fix prompts render correctly, include `Closes #N`, no contradictory merge instructions |

Subprocess-shelling packages (`gh`, `git`, `harness` adapters) are
deliberately not exhaustively tested — that's integration-test
territory and the maintenance cost doesn't pay back at this scale.

## 🛡️ Safety notes

Both harness CLIs are launched with autonomy flags so the agent runs
end-to-end without prompting. That means the harness will execute
arbitrary shell, push branches, and open PRs against whatever GitHub
identity `gh` is authenticated as. **Run chomper only against repos
where you're comfortable with that.**

Chomper's safety properties:

- 🌳 **Each issue runs in an isolated worktree.** Your main checkout
  stays on whatever branch you had. Failed issues don't pollute it.
- 🧊 **Strict failure preservation.** Any error edge (harness fail, no
  PR opened, CI red, merge refused, judge unreachable) preserves the
  worktree at `/tmp/chomper-worktrees/<owner>/<repo>/issue-<N>` so you
  can inspect what happened.
- 🚧 **Iteration caps** on the review loop, the CI-fix loop, and the
  auto-answer question count prevent infinite revision cycles.
- 🧐 **Strict config parsing** catches typos in `.chomper.yaml` at
  load time rather than silently dropping invalid keys.

## 🤝 Contributing

Issues and PRs welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for build
and test instructions.

## 📜 Origin

chomper started as a bash + Python prototype (tagged `v0.1-bash` in
this repo). The Go rewrite was the first official implementation; the
prototype is preserved as a reference snapshot if you want to see how
the design landed in shell.

## ⚖️ License

[Mozilla Public License 2.0](LICENSE).

MPL-2 was chosen as the deliberate middle ground: file-level copyleft
keeps improvements to chomper's source flowing back, but doesn't infect
adjacent code in the same binary. Practically:

- 🆓 You can use chomper, run it, embed it, build on top of it — including
  in proprietary projects.
- 🔁 If you modify chomper's own source files and redistribute, those
  modifications must stay MPL-2 and be made available.
- 🧩 The `internal/` packages are designed for future library use; MPL-2's
  per-file boundary means importers can combine chomper with any-licensed
  code without their code getting "swallowed."

See Mozilla's [MPL-2 FAQ](https://www.mozilla.org/en-US/MPL/2.0/FAQ/) for
the canonical answers to common questions.
