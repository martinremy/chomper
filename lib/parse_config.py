#!/usr/bin/env python3
"""Read .chomper.yaml and emit shell assignments to stdout.

Parses a narrow YAML subset sufficient for chomper's config:
- Top-level scalar keys (harness, merge_strategy, trunk_branch, …)
- One nested section (`filter:`) with scalar or list values
- Inline empty list `[]`, indented `- item` list bodies
- Quoted strings ("..." or '...')
- Comments (full-line or trailing #)

Output is shell-evalable, matching chomper's CONFIG_* variable names:

    CONFIG_HARNESS='claude'
    CONFIG_MERGE_STRATEGY='squash'
    CONFIG_LABELS=( 'chomper' 'bug' )
    CONFIG_TITLE_MATCH='fix'

Values are escaped via shlex.quote so `eval` of the output is safe.

This replaces a previous yq+awk-fallback path. Python is required for
chomper config loading; if the YAML can't be parsed, exit non-zero and
let chomper surface the error.
"""

from __future__ import annotations

import re
import shlex
import sys
from pathlib import Path
from typing import Any, Dict, Optional, Tuple


# Keys we recognize. Anything else in the YAML is silently ignored —
# chomper has a fixed set of config knobs and unknown keys are user typos.
TOP_SCALARS = (
    "harness",
    "merge_strategy",
    "ci_timeout_minutes",
    "trunk_branch",
    "auto_answer",
    "auto_answer_model",
    "auto_answer_max_questions",
    "auto_answer_silence_threshold_s",
)
FILTER_SCALARS = ("title_match",)
FILTER_LISTS = ("labels",)

# wait_for_reviews: explicit YAML key -> emitted CONFIG var name. We
# rename here because the YAML keys are generic ("timeout_minutes")
# and would collide with other concepts in chomper's CONFIG_* namespace.
WAIT_FOR_REVIEWS_SCALARS = {
    "timeout_minutes": "REVIEW_TIMEOUT_MINUTES",
    "max_iterations": "REVIEW_MAX_ITERATIONS",
    "judge_adjudicates": "REVIEW_JUDGE_ADJUDICATES",
    "approve_state_required": "REVIEW_APPROVE_REQUIRED",
}
WAIT_FOR_REVIEWS_LISTS = {
    "reviewers": "REVIEW_REVIEWERS",
}


ListTarget = Optional[Tuple[Optional[str], str]]


def _strip_comments(line: str) -> str:
    """Remove full-line or trailing comments. Preserves '#' inside quoted strings."""
    stripped = line.lstrip()
    if stripped.startswith("#"):
        return ""
    # Trailing comment: split on ' #' but only if not inside a quoted string.
    if " #" in line:
        before, _, _ = line.partition(" #")
        if before.count('"') % 2 == 0 and before.count("'") % 2 == 0:
            return before
    return line


def _strip_quotes(value: str) -> str:
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ('"', "'"):
        return value[1:-1]
    return value


def _assign(container: Dict[str, Any], key: str, raw: str,
            list_target: Tuple[Optional[str], str]) -> ListTarget:
    """Store key=raw on container. Returns a list_target token if the value
    is empty (meaning a list body may follow on subsequent indented lines)."""
    value = raw.strip()
    if value == "":
        # `key:` with nothing after — could be either an empty value or a
        # list-incoming. Start the slot as an empty list; subsequent `-`
        # items will append, and if none follow, an empty list is the
        # natural interpretation.
        container[key] = []
        return list_target
    if value == "[]":
        container[key] = []
        return None
    container[key] = _strip_quotes(value)
    return None


def parse_yaml(text: str) -> Dict[str, Any]:
    cfg: Dict[str, Any] = {}
    section: Optional[str] = None
    list_target: ListTarget = None
    unparsed: list = []  # (line_no, raw_line) for lines we couldn't match

    for line_no, raw in enumerate(text.splitlines(), start=1):
        line = _strip_comments(raw).rstrip()
        if not line.strip():
            list_target = None  # blank line terminates a list body
            continue

        # Indented list item: "  - foo"
        m = re.match(r"^\s+-\s+(.*)$", line)
        if m and list_target is not None:
            sec, key = list_target
            container = cfg.setdefault(sec, {}) if sec else cfg
            existing = container.get(key)
            if not isinstance(existing, list):
                existing = []
                container[key] = existing
            existing.append(_strip_quotes(m.group(1).strip()))
            continue

        # Indented key-value: "  title_match: \"fix\""
        m = re.match(r"^\s+(\w[\w_]*):\s*(.*)$", line)
        if m and section is not None:
            list_target = _assign(cfg[section], m.group(1), m.group(2),
                                  (section, m.group(1)))
            continue

        # Top-level section header: "filter:" (key with no value at col 0)
        m = re.match(r"^(\w[\w_]*):\s*$", line)
        if m:
            section = m.group(1)
            cfg.setdefault(section, {})
            list_target = None
            continue

        # Top-level key-value: "harness: claude" or "labels: []"
        m = re.match(r"^(\w[\w_]*):\s*(.*)$", line)
        if m:
            section = None
            list_target = _assign(cfg, m.group(1), m.group(2),
                                  (None, m.group(1)))
            continue

        # Anything else is unrecognized syntax — collect for a strict
        # report. We intentionally do NOT silently ignore: silent drops
        # were the original bug class that motivated this parser.
        unparsed.append((line_no, raw))

    if unparsed:
        msg = "unrecognized lines (chomper YAML is intentionally narrow):\n"
        msg += "\n".join(f"  line {n}: {raw}" for n, raw in unparsed)
        raise ValueError(msg)

    return cfg


def emit_shell(cfg: Dict[str, Any]) -> str:
    lines = []

    def emit_scalar(name: str, value: Any) -> None:
        lines.append(f"CONFIG_{name}={shlex.quote(str(value))}")

    def emit_array(name: str, values: list) -> None:
        if values:
            quoted = " ".join(shlex.quote(str(v)) for v in values)
            lines.append(f"CONFIG_{name}=( {quoted} )")
        else:
            lines.append(f"CONFIG_{name}=()")

    for key in TOP_SCALARS:
        if key in cfg and not isinstance(cfg[key], (list, dict)):
            emit_scalar(key.upper(), cfg[key])

    filt = cfg.get("filter")
    if isinstance(filt, dict):
        for key in FILTER_SCALARS:
            if key in filt and not isinstance(filt[key], (list, dict)):
                emit_scalar(key.upper(), filt[key])
        for key in FILTER_LISTS:
            if key in filt and isinstance(filt[key], list):
                emit_array(key.upper(), filt[key])

    wfr = cfg.get("wait_for_reviews")
    if isinstance(wfr, dict):
        for yaml_key, var_name in WAIT_FOR_REVIEWS_SCALARS.items():
            if yaml_key in wfr and not isinstance(wfr[yaml_key], (list, dict)):
                emit_scalar(var_name, wfr[yaml_key])
        for yaml_key, var_name in WAIT_FOR_REVIEWS_LISTS.items():
            if yaml_key in wfr and isinstance(wfr[yaml_key], list):
                emit_array(var_name, wfr[yaml_key])

    return "\n".join(lines)


def main() -> int:
    if len(sys.argv) != 2:
        sys.stderr.write("usage: parse_config.py <path/to/.chomper.yaml>\n")
        return 2
    path = Path(sys.argv[1])
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as e:
        sys.stderr.write(f"parse_config.py: cannot read {path}: {e}\n")
        return 2
    try:
        cfg = parse_yaml(text)
    except Exception as e:
        sys.stderr.write(f"parse_config.py: parse error in {path}: {e}\n")
        return 1
    print(emit_shell(cfg))
    return 0


if __name__ == "__main__":
    sys.exit(main())
