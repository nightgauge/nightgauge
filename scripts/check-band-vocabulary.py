#!/usr/bin/env python3
"""Band-vocabulary reintroduction gate (#582, spike #568 §5).

The band vocabulary (haiku|sonnet|opus|fable) was retired as an internal
routing/telemetry vocabulary by epic #567. What SURVIVES, deliberately:

  * the per-language band authorities every consumer derives from
    (`packages/nightgauge-sdk/src/eval/tierBands.ts` and the `Tier*` consts in
    `internal/intelligence/routing/performance_mode.go`),
  * the registry DATA (`model-registry.json` `tiers` membership),
  * user-facing config vocabulary (`stage_models` values, `minimum_model`,
    `soft_route_model`, `haiku_max`/`sonnet_max`) — typed against the
    authority, documented in docs/CONFIGURATION.md,
  * the Anthropic adapter path, where haiku/sonnet/opus remain legitimate
    Claude CLI model aliases passed verbatim,
  * SKILL.md `model:` advisories (single Claude CLI aliases, not lists).

What must NOT come back is the incident class this gate exists for: a
HAND-INLINED band closed set or regex alternation in production source. Two
separate incidents shipped a three-band list that silently dropped `fable`
(recorded in stageResolver.ts's comments); #581/#582 replaced every such site
with a derivation from the authority. This gate fails CI when one is
re-inlined.

Detection (production source only — tests and fixtures legitimately enumerate
bands when pinning ladders and goldens):

  R1  closed-set list — three or more DISTINCT band names as exact-quoted
      string literals within any five consecutive lines (catches one-line
      arrays and vertical list literals alike);
  R2  regex alternation — two band names joined by `|` inside one literal
      (`sonnet|opus` …), the shape both silent-fable-drop incidents took.

Single quoted band literals stay legal: they are typed against the
TierBand/DefaultModel union, so the compiler already pins them to the
authority; what a type cannot catch is an ENUMERATION that claims to be the
whole vocabulary while missing a member — which is exactly R1/R2.

FAIL-CLOSED. Exit codes:
  0  clean
  1  band-vocabulary reintroduction found
  2  the gate itself could not run (treated as failure by CI)
"""

from __future__ import annotations

import re
import subprocess
import sys

BANDS = ("haiku", "sonnet", "opus", "fable")

# Exact-quoted band token: "haiku", 'haiku', or `haiku` and nothing else
# inside the quotes — concrete ids like "claude-opus-5" never match.
QUOTED_BAND = re.compile(r"""["'`](%s)["'`]""" % "|".join(BANDS))

# Two band names joined by a regex-alternation pipe inside a literal.
ALTERNATION = re.compile(r"\b(%s)\s*\|\s*(%s)\b" % ("|".join(BANDS), "|".join(BANDS)))

WINDOW = 5  # lines
DISTINCT_THRESHOLD = 3  # distinct bands within the window → closed set

# Production-source scan roots (tracked files only).
SCAN_PREFIXES = (
    "internal/",
    "cmd/",
    "packages/nightgauge-sdk/src/",
    "packages/nightgauge-vscode/src/",
    "scripts/",
)
SCAN_SUFFIXES = (".go", ".ts")

# Test/fixture exclusions: pinned ladders, goldens and compat tables
# legitimately enumerate every band.
EXCLUDE_PATTERNS = (
    re.compile(r"_test\.go$"),
    re.compile(r"\.test\.ts$"),
    re.compile(r"(^|/)__tests__/"),
    re.compile(r"(^|/)__fixtures__/"),
    re.compile(r"(^|/)testdata/"),
    re.compile(r"(^|/)tests/"),
    # Generated mirrors: their source of truth lives elsewhere (e.g.
    # terminalKindTable.generated.ts mirrors internal/terminalkind/table.json,
    # whose predicate DESCRIPTIONS name the vocabulary as data, not code).
    re.compile(r"\.generated\.ts$"),
)

# path -> justification. Every entry must name a surviving surface from the
# spike §5 allowed list. Keep this list SHORT — the default is a violation.
ALLOWLIST: dict[str, str] = {
    # THE TypeScript band authority — the one place TS spells the vocabulary.
    "packages/nightgauge-sdk/src/eval/tierBands.ts": "TS band authority (#581)",
    # THE Go band spellings — Band* consts, BandAlternation, ClaudeIDTier;
    # routing's Tier* consts and everything else derive from these.
    "internal/models/bands.go": "Go band authority (#582)",
    # The pre-cutover selector, pinned byte-identical by the #581
    # selection-compat goldens. Its remaining band tables (scoreToModel
    # thresholds, downgrade walk, cheaper-tier candidates) are typed against
    # MODEL_TIER_ORDER (authority-derived) and are superseded by the selection
    # query, not re-derived here — #606 tracks the post-cutover remainder.
    "packages/nightgauge-sdk/src/analysis/AutoModelSelector.ts": (
        "golden-pinned pre-cutover selector (#581/#606)"
    ),
    # User-facing picker labels for the SURVIVING band config vocabulary
    # (`pipeline.stage_models` values): per-band prose in an exhaustive switch
    # typed against StageModelChoice, which derives from TIER_BANDS.
    "packages/nightgauge-vscode/src/commands/selectPerformanceMode.ts": (
        "user-facing band labels, type-anchored"
    ),
}


def die(code: int, msg: str) -> None:
    print(f"\n\033[31mband-vocabulary: {msg}\033[0m", file=sys.stderr)
    sys.exit(code)


def tracked_paths() -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", "-z"], capture_output=True, check=True
    ).stdout.decode()
    return [p for p in out.split("\0") if p]


def in_scope(path: str) -> bool:
    if not path.startswith(SCAN_PREFIXES):
        return False
    if not path.endswith(SCAN_SUFFIXES):
        return False
    return not any(p.search(path) for p in EXCLUDE_PATTERNS)


BLOCK_COMMENT = re.compile(r"/\*.*?\*/", re.DOTALL)
LINE_COMMENT = re.compile(r"(^|\s)//.*$", re.MULTILINE)


def strip_comments(text: str) -> str:
    """Drop comments — prose may legitimately NAME the vocabulary; only CODE
    literals reintroduce it. Newlines inside block comments are preserved so
    reported line numbers stay true. Heuristic (not a real lexer): a `//`
    inside a string literal is also stripped, which can only HIDE a violation
    on that suffix, never invent one — acceptable for a reintroduction tripwire
    whose job is the common shape, and the R1 window still catches the list
    itself."""
    text = BLOCK_COMMENT.sub(lambda m: "\n" * m.group(0).count("\n"), text)
    return LINE_COMMENT.sub(lambda m: m.group(1), text)


def scan_file(path: str) -> list[str]:
    violations: list[str] = []
    try:
        text = open(path, encoding="utf-8", errors="replace").read()
    except OSError as e:
        die(2, f"cannot read tracked file {path}: {e}")
    lines = strip_comments(text).splitlines()

    # R2 — alternation, anywhere.
    for i, line in enumerate(lines, 1):
        m = ALTERNATION.search(line)
        if m:
            violations.append(
                f"{path}:{i}: R2 band alternation `{m.group(0)}` — derive from "
                f"TIER_BAND_ALTERNATION / the band authority instead"
            )

    # R1 — >=3 distinct quoted bands within a sliding window.
    per_line: list[set[str]] = [set(QUOTED_BAND.findall(line)) for line in lines]
    last_flagged = -(WINDOW + 1)
    for start in range(len(lines)):
        window = per_line[start : start + WINDOW]
        distinct: set[str] = set().union(*window) if window else set()
        if len(distinct) >= DISTINCT_THRESHOLD:
            # Overlapping windows are one site — report only the first.
            if start - last_flagged > WINDOW:
                violations.append(
                    f"{path}:{start + 1}: R1 hand-inlined band closed set "
                    f"({', '.join(sorted(distinct))}) — derive from TIER_BANDS / "
                    f"the registry instead of re-listing the vocabulary"
                )
            last_flagged = start
    return violations


def main() -> int:
    try:
        paths = tracked_paths()
    except Exception as e:  # noqa: BLE001 — fail closed
        die(2, f"git ls-files failed: {e}")

    for allowed in ALLOWLIST:
        if allowed not in paths:
            die(2, f"allowlisted file {allowed} is not tracked — stale allowlist")

    violations: list[str] = []
    for path in paths:
        if not in_scope(path):
            continue
        if path in ALLOWLIST:
            continue
        violations.extend(scan_file(path))

    if violations:
        print("band-vocabulary reintroduction gate FAILED (#582):\n", file=sys.stderr)
        for v in violations:
            print(f"  {v}", file=sys.stderr)
        print(
            "\nBand closed sets/alternations in production source must derive "
            "from the band authority\n(tierBands.ts / performance_mode.go "
            "Tier* consts) or the model registry — two past\nincidents "
            "silently dropped `fable` from hand-inlined three-band lists. "
            "If this surface\ngenuinely belongs to the allowed list (spike "
            "#568 §5), add it to ALLOWLIST with a\njustification.",
            file=sys.stderr,
        )
        return 1

    print(
        f"band-vocabulary: clean ({sum(1 for p in paths if in_scope(p))} files scanned)"
    )
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except SystemExit:
        raise
    except Exception as e:  # noqa: BLE001 — fail closed
        die(2, f"unexpected error: {e}")
