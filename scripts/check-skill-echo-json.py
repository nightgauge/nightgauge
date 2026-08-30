#!/usr/bin/env python3
"""Forbid `echo "$VAR" | jq/grep/awk` in skill files (#1215).

WHY

Skill snippets run in whatever shell the agent session has. In zsh — the shell
this workspace's sessions use — the *builtin* `echo` interprets backslash
escapes by default (`BSD_ECHO` off), so the `\\n` inside a JSON string value
becomes a real newline before `jq` ever sees it:

    $ zsh -c 'J=$(printf "{\\"b\\":\\"a\\\\nb\\"}"); echo "$J" | jq -r .b'
    parse error: Invalid string: control characters from U+0000 through
    U+001F must be escaped at line 2, column 2

The caller then reads an empty string and every downstream check silently
agrees with it. On 2026-08-30 the issue-audit terminal gate reported all five
required `bug` headings as MISSING_REQUIRED_HEADING — CRITICAL in strict mode —
against an issue that had every one of them. Nothing was wrong with the issue;
`echo` had eaten the body.

`printf '%s\\n' "$VAR"` passes the bytes through unchanged in every shell, as
does a here-string. This gate keeps the fixed tree fixed.

WHY THIS IS NOT A GREP

The naive pattern flags nine lines in the current tree that are correct — an
outer `echo` of a literal *message* that happens to contain a fixed-up command
substitution:

    echo "WARNING: scaffolding failed: $(printf '%s\\n' "$R" | jq -r .error)"

The pipe there belongs to the substitution, not to the outer echo. And it must
not flag a pipe inside a quoted string at all:

    echo "  1. Check branch exists: git branch -a | grep $BRANCH"

Deciding those needs quote/substitution context, so the scanner tracks it:
single quotes, double quotes, and `$(...)` re-entering code context inside
double quotes. A regex cannot, and a gate that cries wolf gets disabled.

Exit 0 clean, 1 on a violation, 2 if the gate cannot run (a root it cannot find
is a failure, never a silent pass).

Run: python3 scripts/check-skill-echo-json.py
Also run by .github/workflows/lint.yml and scripts/ci-local.sh.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOTS = ("skills", "claude-plugins/nightgauge/skills")
SUFFIXES = (".md", ".sh", ".bash", ".zsh")
TARGETS = ("jq", "grep", "awk")
# Words after which a bare `echo` still starts a command.
KEYWORDS = {"if", "then", "else", "elif", "do", "while", "until", "!"}

REPO_ROOT = Path(__file__).resolve().parent.parent


def contexts(line: str) -> str:
    """Per-character context: 'c' = shell code, 'q' = inside a quoted literal.

    A `$(` inside double quotes re-enters code context. That is exactly the
    case a naive quote-toggle gets wrong, and it is the shape of all nine
    legitimate lines in the tree.
    """
    mask: list[str] = []
    stack: list[str] = []  # "sq" | "dq" | "sub"
    i, n = 0, len(line)
    while i < n:
        c = line[i]
        top = stack[-1] if stack else None
        if top == "sq":
            mask.append("q")
            if c == "'":
                stack.pop()
            i += 1
            continue
        if top == "dq":
            if c == "\\" and i + 1 < n:
                mask.extend("qq")
                i += 2
                continue
            if line.startswith("$(", i):
                mask.extend("cc")
                stack.append("sub")
                i += 2
                continue
            mask.append("q")
            if c == '"':
                stack.pop()
            i += 1
            continue
        if line.startswith("$(", i):
            mask.extend("cc")
            stack.append("sub")
            i += 2
            continue
        mask.append("c")
        if c == "'":
            stack.append("sq")
        elif c == '"':
            stack.append("dq")
        elif c == ")" and top == "sub":
            stack.pop()
        i += 1
    return "".join(mask)


def find_pipe(line: str, mask: str, start: int) -> int | None:
    """Index of the `|` (code context, not `||`) that ends this simple command."""
    depth = 0
    i, n = start, len(line)
    while i < n:
        if mask[i] != "c":
            i += 1
            continue
        c = line[i]
        if line.startswith("$(", i):
            depth += 1
            i += 2
            continue
        if c == ")":
            if depth:
                depth -= 1
                i += 1
                continue
            return None
        if depth:
            i += 1
            continue
        if c == "|":
            return None if line.startswith("||", i) else i
        if c in ";&\n":
            return None
        i += 1
    return None


def in_command_position(line: str, mask: str, i: int) -> bool:
    j = i - 1
    while j >= 0 and line[j] in " \t":
        j -= 1
    if j < 0:
        return True
    if mask[j] != "c":
        return False
    if line[j] in ";&|({\n":
        return True
    k = j
    while k >= 0 and mask[k] == "c" and re.match(r"[A-Za-z!]", line[k]):
        k -= 1
    return line[k + 1 : j + 1] in KEYWORDS


def violations(line: str) -> list[str]:
    """Return the offending `echo … | target` fragments on this line."""
    mask = contexts(line)
    found = []
    for m in re.finditer(r"(?<![A-Za-z0-9_.\-/])echo(?=[ \t])", line):
        i = m.start()
        if mask[i] != "c" or not in_command_position(line, mask, i):
            continue
        end = find_pipe(line, mask, i + 4)
        if end is None:
            continue
        nxt = re.match(r"\s*([A-Za-z0-9_.\-/]+)", line[end + 1 :])
        if not nxt or nxt.group(1) not in TARGETS:
            continue
        if "$" not in line[i + 4 : end]:
            continue  # a fixed string has no escapes to mangle
        found.append(line[i : end + 1 + len(nxt.group(0))].strip())
    return found


def scan(root: Path) -> list[tuple[Path, int, str]]:
    hits = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.suffix not in SUFFIXES:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for lineno, line in enumerate(text.split("\n"), start=1):
            for frag in violations(line):
                hits.append((path, lineno, frag))
    return hits


def main() -> int:
    roots = []
    for name in ROOTS:
        root = REPO_ROOT / name
        if not root.is_dir():
            print(f"ERROR: {name}/ not found under {REPO_ROOT}", file=sys.stderr)
            return 2
        roots.append(root)

    hits = [hit for root in roots for hit in scan(root)]
    if not hits:
        print(f"OK: no `echo \"$VAR\" | jq/grep/awk` in {len(roots)} skill trees")
        return 0

    print(
        f"FAIL: {len(hits)} shell variable(s) piped into jq/grep/awk via `echo`.\n"
        "zsh's builtin echo expands backslash escapes, so JSON `\\n` becomes a\n"
        "real newline and the reader silently sees an empty value (#1215).\n"
        "Use `printf '%s\\n' \"$VAR\" |` or `jq … <<<\"$VAR\"` instead.\n",
        file=sys.stderr,
    )
    for path, lineno, frag in hits:
        rel = path.relative_to(REPO_ROOT)
        print(f"  {rel}:{lineno}: {frag}", file=sys.stderr)
    print(
        "\nEdit skills/ only, then regenerate the mirror:\n"
        "  bash scripts/install-agent-skills.sh --generate-only",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
