#!/usr/bin/env python3
"""Filter a semgrep JSON report through .semgrep/allowlist.yaml.

Usage: semgrep-allowlist.py REPORT.json ALLOWLIST.yaml

Exit 0 when every finding is allow-listed and every entry matches a finding;
1 when a finding is not allow-listed or an entry is stale; 2 on a malformed
input or a semgrep error in the report.
"""
import json
import sys

import yaml


def rule_name(check_id):
    # A rule loaded from a local directory is reported as
    # ".semgrep.rules.<id>"; the id itself never contains a dot.
    return check_id.rsplit(".", 1)[-1]


def flagged_text(path, start, end):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            lines = f.read().splitlines()
    except OSError:
        return ""
    return "\n".join(lines[start - 1 : end])


def main(argv):
    if len(argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    try:
        with open(argv[1], encoding="utf-8") as f:
            report = json.load(f)
        with open(argv[2], encoding="utf-8") as f:
            entries = (yaml.safe_load(f) or {}).get("entries") or []
    except (OSError, ValueError, yaml.YAMLError) as err:
        print(f"HARNESS ERROR: cannot read the report or allow-list: {err}", file=sys.stderr)
        return 2
    for e in entries:
        missing = [k for k in ("rule", "path", "match", "reason") if not e.get(k)]
        if missing:
            print(f"ERROR: allow-list entry {e!r} lacks {', '.join(missing)}", file=sys.stderr)
            return 2
    # A rule that timed out or ran out of memory on a file checked nothing
    # there, so it blocks. A partial parse (TypeScript syntax the parser does
    # not know) is a warning: the rest of the file is still checked.
    errors = report.get("errors") or []
    blocking = [
        e for e in errors
        if e.get("level", "error") == "error" or e.get("type") in ("Timeout", "OutOfMemory")
    ]
    if blocking:
        for e in blocking:
            print(f"HARNESS ERROR: semgrep: {e.get('type')}: {str(e.get('message'))[:300]}", file=sys.stderr)
        return 2

    used = [False] * len(entries)
    unallowed = []
    for r in report.get("results") or []:
        rule = rule_name(r["check_id"])
        path = r["path"]
        text = flagged_text(path, r["start"]["line"], r["end"]["line"])
        hit = next(
            (i for i, e in enumerate(entries)
             if e["rule"] == rule and e["path"] == path and e["match"] in text),
            None,
        )
        if hit is None:
            unallowed.append((rule, path, r["start"]["line"], r["extra"].get("message", "")))
        else:
            used[hit] = True

    stale = [e for e, u in zip(entries, used) if not u]
    for rule, path, line, msg in unallowed:
        print(f"FAIL: {path}:{line}: [{rule}] {msg}")
    for e in stale:
        print(f"FAIL: stale allow-list entry (no finding matches): {e['path']} [{e['rule']}] match={e['match']!r}")
    if unallowed or stale:
        print(
            f"\nsemgrep: {len(unallowed)} finding(s) not allow-listed, {len(stale)} stale entr(y/ies). "
            "Fix the call site, or add a reviewed entry to .semgrep/allowlist.yaml; "
            "remove an entry once its call site is fixed."
        )
        return 1
    print(f"semgrep: {sum(used)} allow-listed finding(s), no others. ✓")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
