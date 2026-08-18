#!/usr/bin/env python3
"""Pin the issue-body heading contract across its three copies (#711).

`issue-create` authors issue bodies; `issue-audit` Phase 5 checks their `##`
headings; `issue-create` Phase 6 runs `issue-audit` as its own terminal gate.
The required-heading table therefore has to say the same thing in three files:

  1. skills/nightgauge-issue-audit/SKILL.md      (canonical)
  2. docs/ISSUE_AUDIT.md                          (reader-facing mirror)
  3. skills/nightgauge-issue-create/_includes/environment-and-content.md
                                                  (authoring rules)

Before #711 they agreed on nothing. `issue-create` prescribed `Problem
statement` / `Business/user value` / `Acceptance criteria` / `Technical notes`;
the audit required `Summary` plus per-type sections with different casing. Every
issue the skill authored failed its own terminal audit — 15 findings across the
5 issues of epic #702 — and because `MISSING_REQUIRED_HEADING` was a WARNING and
the verdict turns only on CRITICAL count, the audit still printed READY.

This gate asserts two things no reviewer reliably catches by eye:

  A. TABLE AGREEMENT — the three copies parse to the same {type: [headings]}
     map, casing included. Copy 3 groups rows (`feature / docs / refactor`);
     the grouping is expanded before comparison, so it is a presentation
     choice rather than a place for drift to hide.

  B. ROUND TRIP — a body authored per `issue-create`'s rules satisfies
     `issue-audit` Phase 5. The heading regex is not re-implemented from the
     prose: it is EXTRACTED from the audit skill's own bash and applied via
     `grep -E`, so if the skill's matcher changes and the authoring rules do
     not, this fails. That is the specific failure #711 was.

Machine-authored bodies (the Path B decomposition chore, the spike worked
example) are checked too — those have no author in the loop to notice drift.

Exit 0 clean, 1 on a contract violation, 2 if the gate cannot run (a table it
cannot find is a failure, never a silent pass — the #539/#549 lesson).

Run: python3 scripts/check-issue-body-contract.py
Also run by .github/workflows/lint.yml and scripts/ci-local.sh.
"""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

AUDIT_SKILL = Path("skills/nightgauge-issue-audit/SKILL.md")
AUDIT_DOC = Path("docs/ISSUE_AUDIT.md")
CREATE_RULES = Path(
    "skills/nightgauge-issue-create/_includes/environment-and-content.md"
)
SCOPE_GATES = Path("skills/nightgauge-issue-create/_includes/spike-routing.md")
CHORE_BODY = Path("skills/nightgauge-issue-create/_includes/scope-gates.md")

ALL_TYPES = ("feature", "bug", "docs", "refactor", "spike", "chore", "epic")

errors: list[str] = []


def fail(msg: str) -> None:
    errors.append(msg)


def die(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


def read(path: Path) -> str:
    if not path.is_file():
        die(f"{path} not found — cannot verify the issue-body contract")
    return path.read_text(encoding="utf-8")


def parse_table(text: str, source: str, type_col_header: str) -> dict[str, list[str]]:
    """Pull a {type: [headings]} map out of a markdown table.

    Accepts grouped type cells (`feature / docs / refactor`) and normalises
    them, so the authoring rules can stay readable without letting the grouping
    become a hiding place for drift.
    """
    rows: dict[str, list[str]] = {}
    in_table = False
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped.startswith("|"):
            if in_table:
                break
            continue
        cells = [c.strip() for c in stripped.strip("|").split("|")]
        if len(cells) < 2:
            continue
        if not in_table:
            # Header row must name the type column AND declare required headings.
            # Both conditions matter: these files carry other tables whose first
            # column is also a type, and matching one of those would make the
            # gate assert against the wrong data.
            head0 = cells[0].replace("`", "").strip()
            head1 = cells[1].replace("`", "").strip()
            if head0 == type_col_header and head1.startswith("Required"):
                in_table = True
            continue
        if set(cells[0]) <= {"-", ":", " "}:
            continue  # separator
        types = [t.strip().strip("`") for t in cells[0].split("/")]
        headings = [h.strip() for h in cells[1].split(",") if h.strip()]
        for t in types:
            if t in rows:
                fail(f"{source}: duplicate row for type '{t}'")
            rows[t] = headings
    if not rows:
        die(f"{source}: could not locate the required-heading table")
    return rows


def extract_heading_regex(audit_text: str) -> tuple[str, str]:
    """Take the matcher from the audit skill's own bash, never from prose.

    Returns (grep_flags, pattern). Flags are captured rather than assumed so
    that adding `-i` shows up as observed behavior in the self-test below,
    instead of merely failing to parse. If Phase 5's matcher changes, the round
    trip re-runs against the NEW matcher — a gate holding its own hard-coded
    copy of the regex would keep passing through the very drift it exists to
    catch.
    """
    m = re.search(
        r"grep -(q[a-zA-Z]*)E \"(\^##\[\[:space:\]\]\+\$\{HEADING\}[^\"]*)\"",
        audit_text,
    )
    if not m:
        die(f"{AUDIT_SKILL}: could not extract the Phase 5 heading regex from its bash")
    return m.group(1), m.group(2)


def heading_present(body: str, heading: str, matcher: tuple[str, str]) -> bool:
    """Run the audit's actual matcher, through grep -E, exactly as Phase 5 does."""
    flags, regex_template = matcher
    pattern = regex_template.replace("${HEADING}", heading).replace("\\\\s", "\\s")
    proc = subprocess.run(
        ["grep", f"-{flags}E", pattern],
        input=body,
        text=True,
        capture_output=True,
    )
    if proc.returncode not in (0, 1):
        die(f"grep failed on pattern {pattern!r}: {proc.stderr.strip()}")
    return proc.returncode == 0


def author_body(issue_type: str, headings: list[str]) -> str:
    """Author a body the way issue-create's rules now say to.

    Deliberately mechanical: the point is that following the prescribed
    headings verbatim is sufficient to pass, with no authorial cleverness.
    Optional extra sections are included because the rules encourage them and
    the audit must keep ignoring unlisted headings.
    """
    parts = []
    for h in headings:
        parts.append(f"## {h}\n\nContent for {h} on a {issue_type} issue.\n")
    parts.append("## Technical notes\n\nAn extra section the table does not require.\n")
    return "\n".join(parts)


def main() -> int:
    audit_text = read(AUDIT_SKILL)
    doc_text = read(AUDIT_DOC)
    create_text = read(CREATE_RULES)

    canonical = parse_table(audit_text, str(AUDIT_SKILL), "Type")
    doc_table = parse_table(doc_text, str(AUDIT_DOC), "Type")
    create_table = parse_table(create_text, str(CREATE_RULES), "type: label")

    # (A) Table agreement across all three copies.
    missing_types = [t for t in ALL_TYPES if t not in canonical]
    if missing_types:
        fail(
            f"{AUDIT_SKILL}: canonical table is missing type(s): {', '.join(missing_types)}"
        )

    for label, table in (
        (str(AUDIT_DOC), doc_table),
        (str(CREATE_RULES), create_table),
    ):
        if table != canonical:
            for t in sorted(set(canonical) | set(table)):
                want, got = canonical.get(t), table.get(t)
                if want != got:
                    fail(
                        f"{label}: type '{t}' disagrees with {AUDIT_SKILL} — "
                        f"canonical {want!r}, found {got!r}"
                    )

    # (B) Round trip: an authored body satisfies the audit's own matcher.
    matcher = extract_heading_regex(audit_text)
    for issue_type in ALL_TYPES:
        headings = canonical.get(issue_type)
        if not headings:
            continue
        body = author_body(issue_type, headings)
        for h in headings:
            if not heading_present(body, h, matcher):
                fail(
                    f"round trip: a '{issue_type}' body authored per issue-create's "
                    f"rules yields MISSING_REQUIRED_HEADING for '{h}'"
                )

    # (B2) The gate must be able to SEE a violation — otherwise (B) is vacuous.
    #      Wrong casing is the exact defect #711 shipped, so it is the probe.
    probe = "## Acceptance criteria\n\n- [ ] lowercase c\n"
    if heading_present(probe, "Acceptance Criteria", matcher):
        fail(
            "self-test: the extracted matcher accepted '## Acceptance criteria' for "
            "'Acceptance Criteria' — it is case-insensitive, so the round-trip check "
            "above proves nothing. Re-derive the contract against observed behavior."
        )
    if not heading_present("## Summary\n\nx\n", "Summary", matcher):
        fail("self-test: the extracted matcher rejected a correct '## Summary' heading")

    # (C) Machine-authored bodies — no author in the loop to catch drift.
    chore_text = read(CHORE_BODY)
    m = re.search(r'PLACEHOLDER_CHORE_BODY="(.*?)"\n', chore_text, re.S)
    if not m:
        die(f"{CHORE_BODY}: PLACEHOLDER_CHORE_BODY assignment not found")
    for h in canonical.get("chore", []):
        if not heading_present(m.group(1), h, matcher):
            fail(
                f"{CHORE_BODY}: the machine-authored decomposition chore body is "
                f"missing required heading '{h}' for type:chore"
            )

    spike_text = read(SCOPE_GATES)
    m = re.search(
        r"#### Worked Example.*?\n````markdown\n(.*?)\n````", spike_text, re.S
    )
    if not m:
        die(f"{SCOPE_GATES}: spike worked-example body block not found")
    for h in canonical.get("spike", []):
        if not heading_present(m.group(1), h, matcher):
            fail(
                f"{SCOPE_GATES}: the spike worked example is missing required "
                f"heading '{h}' — it is labelled contract-conformant, so it must be"
            )

    if errors:
        print("issue-body contract violations:\n", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        print(
            "\nThe required-heading table has three copies and they must agree:\n"
            f"  {AUDIT_SKILL} (canonical)\n"
            f"  {AUDIT_DOC}\n"
            f"  {CREATE_RULES}\n",
            file=sys.stderr,
        )
        return 1

    print(
        f"issue-body contract OK — {len(canonical)} types agree across 3 copies; "
        "round trip clean."
    )
    return 0


if __name__ == "__main__":
    root = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True
    )
    if root.returncode == 0:
        import os

        os.chdir(root.stdout.strip())
    sys.exit(main())
