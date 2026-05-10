#!/usr/bin/env python3
"""
chomper auto-answer proxy.

Sits between chomper and the worker harness. Spawns the worker in stream-json
I/O (when supported), watches its event stream, and when silence triggers,
calls a smaller "judge" instance of the same harness to:

  1) classify the silence as working / waiting / errored
  2) if waiting, answer the question and inject a tool_result back into
     the worker's stdin

Detection is symptom-driven: we don't try to recognize specific tool names
(like AskUserQuestion) since those vary across harnesses and skill ecosystems.
We only detect that the worker has gone quiet, then ask a small LLM to
interpret the recent output buffer.

Inputs (env):
  CHOMPER_DIR                 path to chomper checkout (for adapter sourcing)
  CHOMPER_HARNESS             'claude' or 'codex'
  CHOMPER_REPO_DIR            target repo working directory
  CHOMPER_PROMPT_FILE         path to per-issue prompt file
  CHOMPER_ISSUE_CONTEXT_JSON  JSON: {repo, number, title, body}
  CHOMPER_JUDGE_MODEL         (optional) judge model id
  CHOMPER_MAX_QUESTIONS       (optional) per-issue cap, default 5
  CHOMPER_SILENCE_THRESHOLD_S (optional) initial silence in seconds, default 30

Exit code: the worker's exit code, or non-zero on hard failure.

Safety note: subprocesses are spawned with argv lists (no shell string
interpolation of user data). The bash invocation uses a fixed command
template; variable data flows through positional args and environment
variables, both of which bash quotes safely.
"""

import asyncio
import json
import os
import sys
from collections import deque
from typing import Any, Deque, Dict, List, Optional


# ---------------------------------------------------------------------------
# Configuration (env-driven)
# ---------------------------------------------------------------------------

CHOMPER_DIR = os.environ.get("CHOMPER_DIR", "")
HARNESS = os.environ.get("CHOMPER_HARNESS", "")
REPO_DIR = os.environ.get("CHOMPER_REPO_DIR", "")
PROMPT_FILE = os.environ.get("CHOMPER_PROMPT_FILE", "")
ISSUE_CONTEXT_JSON = os.environ.get("CHOMPER_ISSUE_CONTEXT_JSON", "{}")

SILENCE_THRESHOLD_S = float(os.environ.get("CHOMPER_SILENCE_THRESHOLD_S", "30"))
SILENCE_THRESHOLD_MAX_S = float(os.environ.get("CHOMPER_SILENCE_THRESHOLD_MAX_S", "300"))
MAX_QUESTIONS = int(os.environ.get("CHOMPER_MAX_QUESTIONS", "5"))
JUDGE_TIMEOUT_S = float(os.environ.get("CHOMPER_JUDGE_TIMEOUT_S", "60"))
BUFFER_LINES = int(os.environ.get("CHOMPER_BUFFER_LINES", "200"))


# ---------------------------------------------------------------------------
# Prompts
# ---------------------------------------------------------------------------

CLASSIFIER_SYSTEM_PROMPT = """\
You are monitoring an autonomous coding agent that just went silent. Decide
whether it is still working, waiting for a human, or has errored. Be precise
- a wrong "waiting" verdict will inject a synthesized answer that confuses
the agent; a wrong "working" verdict will let a real hang continue.

Classify into exactly ONE state:
- "working": silence is normal. Indicators: ongoing tool calls, partial
  results, recent network/git activity, model still mid-thought.
- "waiting": agent is paused expecting human input. Indicators: an explicit
  question, a numbered options list, "Continue? (y/n)", an authentication
  prompt, a plan-mode confirmation, an AskUserQuestion-shaped event in the
  stream, or any text addressed to a human.
- "errored": agent has crashed or hit a fatal error. Indicators: stack
  traces, "Error:"/"FATAL"/"panic" lines, an exit-code event, abrupt
  termination of a tool call.

Respond with ONE single-line JSON object, nothing else:

  {"state": "working", "reason": "<one short sentence>"}
or
  {"state": "errored", "reason": "<one short sentence>"}
or
  {"state": "waiting", "reason": "<one short sentence>",
   "question": "<paraphrased question>",
   "options": ["..."] or null}
"""


JUDGE_SYSTEM_PROMPT = """\
You are simulating the developer who delegated a GitHub issue to an
autonomous coding agent. The agent paused to ask a question. Answer
decisively to keep it moving toward a merged PR. You will not get a
follow-up turn - this answer ends the dialog.

Rules:
1. Pick the most reasonable default given the issue context.
2. Keep the answer short - the agent expects a directive, not an essay.
3. Never ask a follow-up question.
4. For destructive operations (force-push, delete branch protection, drop
   tables, rm -rf, etc.) - refuse, and tell the agent to proceed safely.
5. For plan-mode approvals - approve unless the plan is clearly off-track
   from the issue.
6. For ambiguous questions - minimize scope creep; do not invent new
   requirements.

Output: just the answer text. No preamble. No explanation. No quotes.
- Multiple choice -> the option text, e.g. "Yes, refactor the helper"
- Yes/no -> "yes" or "no"
- Free text -> a one-line directive
"""


# ---------------------------------------------------------------------------
# Logging helpers + presentation
# ---------------------------------------------------------------------------

# TTY-gated ANSI coloring. Stay quiet when redirected to a file.
_USE_COLOR = sys.stdout.isatty()
_DEBUG = os.environ.get("CHOMPER_DEBUG") == "1"


def _c(code: str, text: str) -> str:
    return f"\033[{code}m{text}\033[0m" if _USE_COLOR else text


def log(msg: str) -> None:
    # Judge-emitted lines stand out in bold cyan so the user knows when
    # the proxy is acting (vs. the worker).
    label = _c("1;36", "[judge]")
    print(f"{label} {msg}", file=sys.stderr, flush=True)


def die(msg: str, code: int = 1) -> None:
    log(f"fatal: {msg}")
    sys.exit(code)


# ---------------------------------------------------------------------------
# Pretty-print worker stream-json events
# ---------------------------------------------------------------------------

def _summarize_tool_input(name: str, inp: Dict[str, Any]) -> str:
    """Pull the most informative field from a tool's input for display."""
    for key in ("command", "file_path", "path", "url", "query", "pattern", "description"):
        if key in inp:
            return str(inp[key]).split("\n", 1)[0][:120]
    return json.dumps(inp)[:120]


def format_event(event: Dict[str, Any]) -> Optional[str]:
    """Render a stream-json event as a short human-readable line, or None
    to suppress display entirely. Worker activity uses arrow glyphs; raw
    JSON is hidden unless CHOMPER_DEBUG=1."""
    etype = event.get("type", "?")

    if etype == "system":
        sub = event.get("subtype", "")
        if sub == "init":
            return None  # too noisy
        return _c("2", f"[system:{sub}]")

    if etype == "assistant":
        msg = event.get("message", {}) or {}
        for block in msg.get("content", []) or []:
            btype = block.get("type")
            if btype == "text":
                text = (block.get("text") or "").strip()
                if not text:
                    return None
                first = text.split("\n", 1)[0][:200]
                return _c("2", f"· {first}")
            if btype == "tool_use":
                name = block.get("name", "?")
                summary = _summarize_tool_input(name, block.get("input") or {})
                return f"{_c('33', '→')} {name}: {summary}"
        return None

    if etype == "user":
        msg = event.get("message", {}) or {}
        content = msg.get("content")
        if isinstance(content, list):
            for block in content:
                if block.get("type") == "tool_result":
                    body = str(block.get("content", ""))
                    lines = body.count("\n") + 1 if body else 0
                    return _c("2", f"← ({lines} lines, {len(body)} bytes)")
        return None

    if etype == "result":
        return _c("32", f"[done: {event.get('subtype', '?')}]")

    return None


# ---------------------------------------------------------------------------
# Buffer serialization (compresses events to a string for prompt context)
# ---------------------------------------------------------------------------

def serialize_event(event: Dict[str, Any]) -> str:
    """Render a buffered event as a single short line for the judge prompt."""
    if "raw" in event:
        return event["raw"].rstrip()
    etype = event.get("type", "?")
    if etype == "assistant":
        for block in event.get("message", {}).get("content", []):
            btype = block.get("type")
            if btype == "text":
                return f"[assistant text] {block.get('text', '')[:500]}"
            if btype == "tool_use":
                name = block.get("name", "?")
                inp = json.dumps(block.get("input", {}))[:300]
                return f"[tool_use:{name}] {inp}"
        return "[assistant]"
    if etype == "user":
        msg = event.get("message", {})
        content = msg.get("content")
        if isinstance(content, list):
            for block in content:
                if block.get("type") == "tool_result":
                    body = str(block.get("content", ""))[:300]
                    return f"[tool_result] {body}"
        return f"[user] {str(content)[:300]}"
    if etype == "system":
        return f"[system:{event.get('subtype', '?')}]"
    if etype == "result":
        return f"[result] {event.get('subtype', '?')}"
    return f"[{etype}]"


def render_buffer(buffer: Deque[Dict[str, Any]], char_limit: int) -> str:
    lines = [serialize_event(e) for e in buffer]
    text = "\n".join(lines)
    return text[-char_limit:] if len(text) > char_limit else text


# ---------------------------------------------------------------------------
# Judge invocation (calls bash adapter's harness_run_judge)
# ---------------------------------------------------------------------------

async def call_judge(adapter_path: str, system_prompt: str, user_prompt: str) -> Optional[str]:
    """Invoke the harness's judge function. Returns answer text, or None on failure.

    Spawns bash with a fixed command template; the adapter path flows via
    env (CHOMPER_ADAPTER), so no user data is interpolated into the shell
    string. stdin carries the user prompt; stdout carries the answer.
    """
    sub_env = {
        **os.environ,
        "CHOMPER_ADAPTER": adapter_path,
        "CHOMPER_JUDGE_SYSTEM_PROMPT": system_prompt,
    }
    proc = await asyncio.create_subprocess_exec(
        "bash", "-c", '. "$CHOMPER_ADAPTER" && harness_run_judge',
        stdin=asyncio.subprocess.PIPE,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=sub_env,
    )
    try:
        stdout, stderr = await asyncio.wait_for(
            proc.communicate(user_prompt.encode("utf-8")),
            timeout=JUDGE_TIMEOUT_S,
        )
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        log(f"judge call timed out after {JUDGE_TIMEOUT_S}s")
        return None
    if proc.returncode != 0:
        err = stderr.decode("utf-8", errors="replace")[:500]
        log(f"judge exited {proc.returncode}; stderr: {err}")
        return None
    return stdout.decode("utf-8", errors="replace")


def extract_first_json_object(text: str) -> Optional[Dict[str, Any]]:
    """Pull the first balanced JSON object out of a string, tolerant of
    code fences or pre/post chatter."""
    text = text.strip()
    start = text.find("{")
    if start < 0:
        return None
    depth = 0
    for i in range(start, len(text)):
        c = text[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                try:
                    return json.loads(text[start:i + 1])
                except json.JSONDecodeError:
                    return None
    return None


async def classify(buffer: Deque[Dict[str, Any]], issue_ctx: Dict[str, Any],
                   adapter_path: str, elapsed: float) -> Dict[str, Any]:
    """Run the silence classifier. Returns a dict with at least 'state'."""
    user_prompt = f"""Issue context:
  Repo: {issue_ctx.get("repo", "?")}
  Issue #{issue_ctx.get("number", "?")}: {issue_ctx.get("title", "?")}

Time since last event: {elapsed:.0f}s

Recent agent output buffer (newest at bottom):
<<<
{render_buffer(buffer, 6000)}
>>>
"""
    raw = await call_judge(adapter_path, CLASSIFIER_SYSTEM_PROMPT, user_prompt)
    if raw is None:
        return {"state": "working", "reason": "classifier unreachable; assuming still working"}
    obj = extract_first_json_object(raw)
    if obj is None or "state" not in obj:
        log(f"classifier returned unparseable output: {raw[:300]!r}")
        return {"state": "working", "reason": "unparseable classifier output"}
    return obj


async def answer_question(question: str, options: Optional[List[str]],
                          buffer: Deque[Dict[str, Any]],
                          issue_ctx: Dict[str, Any],
                          adapter_path: str) -> Optional[str]:
    """Run the answerer judge. Returns the answer text, or None on failure."""
    options_block = ""
    if options:
        options_block = "Options:\n" + "\n".join(f"  - {o}" for o in options) + "\n"
    user_prompt = f"""Original issue:
  Repo: {issue_ctx.get("repo", "?")}
  Issue #{issue_ctx.get("number", "?")}: {issue_ctx.get("title", "?")}
  Body:
{issue_ctx.get("body", "")}

Recent agent activity:
<<<
{render_buffer(buffer, 4000)}
>>>

Question:
  {question}
{options_block}"""
    raw = await call_judge(adapter_path, JUDGE_SYSTEM_PROMPT, user_prompt)
    if raw is None:
        return None
    return raw.strip()


# ---------------------------------------------------------------------------
# Synthesizing answer events
# ---------------------------------------------------------------------------

def synthesize_tool_result(answer: str, tool_use_id: Optional[str]) -> bytes:
    """Build a stream-json user event carrying the answer.

    If we have a tool_use_id (claude), emit a structured tool_result block
    keyed to it. Otherwise fall back to a plain text user message - useful
    for cases where the agent printed a free-text question rather than
    invoking a structured tool.
    """
    if tool_use_id:
        event = {
            "type": "user",
            "message": {
                "role": "user",
                "content": [
                    {
                        "type": "tool_result",
                        "tool_use_id": tool_use_id,
                        "content": answer,
                    }
                ],
            },
        }
    else:
        event = {
            "type": "user",
            "message": {"role": "user", "content": answer},
        }
    return (json.dumps(event) + "\n").encode("utf-8")


# ---------------------------------------------------------------------------
# Worker spawn + state machine
# ---------------------------------------------------------------------------

async def run_proxy() -> int:
    if not CHOMPER_DIR or not HARNESS or not REPO_DIR or not PROMPT_FILE:
        die("missing required env: CHOMPER_DIR, CHOMPER_HARNESS, CHOMPER_REPO_DIR, CHOMPER_PROMPT_FILE")

    try:
        issue_ctx = json.loads(ISSUE_CONTEXT_JSON)
    except json.JSONDecodeError as e:
        die(f"bad CHOMPER_ISSUE_CONTEXT_JSON: {e}")
        issue_ctx = {}  # unreachable; satisfies type checker

    adapter_path = os.path.join(CHOMPER_DIR, "lib", f"harness_{HARNESS}.sh")
    if not os.path.exists(adapter_path):
        die(f"adapter not found: {adapter_path}")

    log(f"starting worker via {HARNESS} adapter; silence threshold={SILENCE_THRESHOLD_S:.0f}s, "
        f"max questions={MAX_QUESTIONS}")

    sub_env = {
        **os.environ,
        "CHOMPER_ADAPTER": adapter_path,
        "CHOMPER_STREAM_MODE": "1",
    }
    worker = await asyncio.create_subprocess_exec(
        "bash", "-c",
        '. "$CHOMPER_ADAPTER" && harness_run_worker "$1" "$2"',
        "judge.py",   # $0 (cosmetic)
        REPO_DIR,     # $1
        PROMPT_FILE,  # $2
        stdin=asyncio.subprocess.PIPE,
        stdout=asyncio.subprocess.PIPE,
        stderr=None,  # inherit; let chomper see harness errors live
        env=sub_env,
    )
    assert worker.stdin is not None and worker.stdout is not None

    buffer: Deque[Dict[str, Any]] = deque(maxlen=BUFFER_LINES)
    last_tool_use_id: Optional[str] = None
    threshold = SILENCE_THRESHOLD_S
    questions_answered = 0

    loop = asyncio.get_event_loop()
    last_activity = loop.time()

    while True:
        try:
            line = await asyncio.wait_for(worker.stdout.readline(), timeout=threshold)
        except asyncio.TimeoutError:
            elapsed = loop.time() - last_activity
            log(f"silence detected ({elapsed:.0f}s); classifying...")
            verdict = await classify(buffer, issue_ctx, adapter_path, elapsed)
            state = verdict.get("state", "working")

            if state == "working":
                threshold = min(threshold * 1.5, SILENCE_THRESHOLD_MAX_S)
                log(f"verdict=working ({verdict.get('reason', '')}); next check in {threshold:.0f}s")
                continue

            if state == "errored":
                log(f"verdict=errored ({verdict.get('reason', '')}); aborting issue")
                worker.kill()
                await worker.wait()
                return 1

            if state == "waiting":
                if questions_answered >= MAX_QUESTIONS:
                    log(f"reached max questions ({MAX_QUESTIONS}); aborting issue")
                    worker.kill()
                    await worker.wait()
                    return 1

                question = verdict.get("question", "")
                options = verdict.get("options")
                log(f"verdict=waiting; question: {question[:120]}")

                answer = await answer_question(question, options, buffer, issue_ctx, adapter_path)
                if answer is None:
                    log("answerer failed; aborting issue rather than guessing")
                    worker.kill()
                    await worker.wait()
                    return 1

                event_bytes = synthesize_tool_result(answer, last_tool_use_id)
                try:
                    worker.stdin.write(event_bytes)
                    await worker.stdin.drain()
                except (BrokenPipeError, ConnectionResetError):
                    log("worker stdin closed before answer could be written; aborting")
                    return 1

                questions_answered += 1
                threshold = SILENCE_THRESHOLD_S
                last_activity = loop.time()
                log(f"answered question #{questions_answered}: {answer[:80]!r}")
                continue

            log(f"unknown classifier state {state!r}; treating as working")
            threshold = min(threshold * 1.5, SILENCE_THRESHOLD_MAX_S)
            continue

        if not line:  # EOF
            break

        last_activity = loop.time()
        threshold = SILENCE_THRESHOLD_S

        # Try to parse as stream-json event; fall back to raw for non-JSON
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            # Non-JSON output (codex text mode, harness diagnostics, etc.)
            # Forward verbatim so the user sees something is happening.
            sys.stdout.buffer.write(line)
            sys.stdout.buffer.flush()
            buffer.append({"raw": line.decode("utf-8", errors="replace")})
            continue

        buffer.append(event)

        # Pretty-print the event. Raw JSON is hidden unless CHOMPER_DEBUG=1.
        if _DEBUG:
            sys.stdout.buffer.write(line)
            sys.stdout.buffer.flush()
        else:
            formatted = format_event(event)
            if formatted is not None:
                print(formatted, flush=True)

        # Track the most recent tool_use id so we can key tool_result events
        if event.get("type") == "assistant":
            for block in event.get("message", {}).get("content", []) or []:
                if block.get("type") == "tool_use":
                    last_tool_use_id = block.get("id", last_tool_use_id)

        # Terminal event: agent finished. Drain and exit.
        if event.get("type") == "result":
            break

    rc = await worker.wait()
    log(f"worker exited with code {rc}; questions answered: {questions_answered}")
    return rc


def main() -> None:
    try:
        rc = asyncio.run(run_proxy())
    except KeyboardInterrupt:
        log("interrupted")
        sys.exit(130)
    sys.exit(rc)


if __name__ == "__main__":
    main()
