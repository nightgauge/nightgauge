#!/usr/bin/env bash
# capture-terminal-kind-fixture.sh — capture REAL terminal-failure error text
# from live pipeline telemetry as the evidence corpus for the #306
# terminal-kind classifier parity tests.
#
# Issue #306: terminal-kind classification exists three times — Go's
# ClassifyTerminalKind (internal/orchestrator/failure_handler.go, the
# authoritative writer), the SDK's classifyTerminalKind
# (packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts) and the
# extension's signal ladder
# (packages/nightgauge-vscode/src/services/terminalKindSignal.ts). They were
# held aligned by comments only. The shared corpus in
# internal/orchestrator/testdata/terminal-kind/corpus.json pins all three, and
# every corpus row marked `"source": "captured"` must appear in the file THIS
# script emits — captured-shapes.json — which is how #166's evidence rule stops
# being a claim and starts being a test.
#
# Reproducible: the emitted file is a pure function of (roots, log corpus, Go
# classifier source). Selection is deterministic — highest occurrence count,
# ties broken lexicographically, iterated in the Go classifier's own
# literal-declaration order — and NO field is taken from the wall clock, so
# re-running on a different day against the same logs produces a byte-identical
# file. The dated fields (`telemetry_first_seen` / `telemetry_last_seen`, and
# each shape's own `first_seen` / `last_seen`) come from the log lines
# themselves; they move only when the telemetry does.
#
# ------------------------------------------------------------------------
# WHAT IS MINED — two miners, both requiring machine-emitted structure:
#
#  1. STRUCTURED pipeline logger lines:
#
#         [<ISO8601>] [LEVEL] [stage] [#issue] <message>
#
#     kept only when the message carries a classifier marker AND survives the
#     source-code denylist below.
#
#  2. ADAPTER RESULT ENVELOPES — `{"type":"result", …,"is_error":true}`, whose
#     `result` field is the terminal error text exactly as the CLI handed it to
#     the pipeline. This is the highest-grade evidence available (it is
#     literally the classifier's input) and it is the only reason the raw
#     session JSON is read at all. Envelopes are kept even when they match no
#     marker: an unmatched real envelope is the only honest evidence for the
#     unknown/default case. Shapes record which miner produced them in
#     `origin` ("pipeline-log" / "result-envelope").
#
# The `.nightgauge/logs/*_session.log` files also contain raw agent session
# output — which includes agents READING AND EDITING the classifier source.
# A naive grep for `[cost-cap-exceeded]` therefore hits the classifier's own
# source code as often as it hits a real failure, and a fixture built that way
# would be evidence of nothing. The structured-prefix requirement plus the
# source-code denylist below is what separates "the pipeline reported this" from
# "an agent typed this".
#
# The search vocabulary is not hand-written either: it is extracted from the
# `strings.Contains(t, "...")` literals inside ClassifyTerminalKind and
# isModelUnavailableText, so the miner automatically follows Go when Go grows a
# pattern.
#
# ------------------------------------------------------------------------
# REDACTION — deny by default, fail closed.
#
# This is a PUBLIC repository and the mined logs are a multi-repo workspace's
# telemetry, mixing PRIVATE repositories' runs. Terminal error text is prose by
# construction, so #304's "bare machine tokens only" rule cannot apply here —
# the prose IS the shape under test. The rule is therefore inverted at the
# TOKEN level instead: every construct that can carry an identity is rewritten
# to a fixed placeholder, and then a verification pass RE-SCANS the emitted
# strings and ABORTS if any identity-shaped construct survived:
#
#   absolute paths, URLs, e-mail addresses, owner/repo slugs, branch names,
#   `#<number>` forge references, and git SHAs.
#
# Nothing downstream inspects fixture string contents — the
# publication-boundary guard scans the tracked tree for file classification,
# not for a private repo slug inside a JSON string — so this pass is the only
# mechanical gate there is. It must fail closed, and it does: an unscrubbable
# shape aborts the whole run rather than being emitted.
# ------------------------------------------------------------------------
#
# Usage:
#   scripts/capture-terminal-kind-fixture.sh [WORKSPACE_ROOT ...]
#
# Roots default to the repository this script lives in. Each root is scanned
# for `.nightgauge/logs/*.log`. Output is written to
# internal/orchestrator/testdata/terminal-kind/captured-shapes.json.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$REPO_ROOT/internal/orchestrator/testdata/terminal-kind"

if [ "$#" -eq 0 ]; then
  set -- "$REPO_ROOT"
fi

mkdir -p "$OUT_DIR"

OUT_DIR="$OUT_DIR" CLASSIFIER_SRC="$REPO_ROOT/internal/orchestrator/failure_handler.go" \
  python3 - "$@" <<'PY'
import glob
import json
import os
import re
import sys

out_dir = os.environ["OUT_DIR"]
classifier_src = os.environ["CLASSIFIER_SRC"]
roots = sorted(os.path.abspath(r) for r in sys.argv[1:])

# --- 1. vocabulary, extracted from the authoritative Go classifier ---------


def go_func_body(src: str, name: str) -> str:
    i = src.index("func " + name + "(")
    j = src.index("{", i)
    depth = 0
    k = j
    while True:
        if src[k] == "{":
            depth += 1
        elif src[k] == "}":
            depth -= 1
            if depth == 0:
                return src[j : k + 1]
        k += 1


with open(classifier_src, encoding="utf-8") as fh:
    go_src = fh.read()

body = go_func_body(go_src, "ClassifyTerminalKind") + go_func_body(
    go_src, "isModelUnavailableText"
)
raw_literals = re.findall(r'strings\.Contains\(t,\s*"((?:[^"\\]|\\.)*)"\)', body)

# Literals too generic to mine on: they match ordinary English in unrelated
# pipeline log lines, so a "captured" row selected by them would be evidence of
# nothing. Coverage for these comes from synthetic corpus rows instead, which
# is exactly why the corpus distinguishes `captured` from `synthetic`.
GENERIC = {
    "model",
    "rejected",
    "exit ",
    "api error",
    "hard cap",
    "is not merged",
    "push rejected",
    "fetch first",
    "connection closed",
    "connection reset",
    "connection refused",
    "not available on your",
    "not included in your",
    "not offered on your",
    "not supported on your",
}

vocabulary = []
for lit in raw_literals:
    if lit in GENERIC or lit in vocabulary:
        continue
    vocabulary.append(lit)

# Markers must survive the log-prefix stripper below — both the bracketed
# literals and the bare ones the producers wrap in brackets anyway
# (`[pipeline-start-failure] github-quota-low: …`).
bracket_markers = tuple(lit for lit in vocabulary if lit.startswith("["))
vocabulary_set = set(vocabulary)

# --- 2. structured-log line reader ----------------------------------------

LINE_RE = re.compile(
    r"^\[(\d{4}-\d{2}-\d{2})T[\d:.]+Z?\]\s+\[(?:INFO|WARN|ERROR|DEBUG)\]\s+(.*)$"
)
# A leading `[token]` that is a stage / component / issue tag rather than a
# classifier marker.
TAG_RE = re.compile(r"^\[(#?[A-Za-z0-9][A-Za-z0-9_.:-]*)\]\s+")

# Constructs that mean "this log line quotes source code, not a failure".
SOURCE_SHAPED = (
    "strings.Contains",
    ".includes(",
    "errorText =",
    "terminalFailureKind",
    "return Terminal",
    "//",
    "/*",
    "*/",
    "`",
    "=>",
    "${",
    '\\"',
    "func ",
    "if (",
    ".ts:",
    ".go:",
    ".md:",
    "<<",
    # Raw agent session output the logger relayed verbatim: JSON envelopes,
    # tool-call records, and logger-truncated dumps are transcript, not a
    # pipeline failure report.
    '{"',
    '"type":',
    "tool_use_id",
    "session_id",
    "[truncated,",
)

MAX_LEN = 300

# The pipeline logger caps a relayed message and appends its own marker. The
# prefix before it is still real text (and still carries the marker the
# classifier matches on), so it is kept and flagged rather than discarded.
TRUNCATED_RE = re.compile(r"\.\.\.\s*\[truncated,\s*(\d+)\s*chars total\]\s*$")

# Adapter result envelope: the terminal error text EXACTLY as the CLI handed it
# to the pipeline. This is the highest-grade evidence there is — it is the
# string the classifier actually receives — and it is the only reason the raw
# session JSON is read at all. Everything else in that JSON is transcript.
RESULT_ENVELOPE_RE = re.compile(r'\{"type":"result".*?"is_error":true.*')
RESULT_FIELD_RE = re.compile(r'"result":"((?:[^"\\]|\\.)*)"')


def strip_tags(msg: str) -> str:
    while True:
        m = TAG_RE.match(msg)
        if not m:
            return msg
        tag = "[" + m.group(1) + "]"
        if any(tag.lower().startswith(bm) for bm in bracket_markers):
            return msg
        if m.group(1).lower() in vocabulary_set:
            return msg
        msg = msg[m.end() :]


# --- 3. redaction ---------------------------------------------------------

# Placeholders are deliberately shouty. The redactor is blunt by design — it
# cannot tell the English phrase "repository/repositories" from a private
# `owner/repo` slug, so it rewrites both. A quiet, plausible-looking stand-in
# (`acme/widget`) would leave the reader unable to tell a redacted slug from a
# real one; these cannot be misread as anything but a redaction.
PH_REPO = "REDACTED-OWNER/REDACTED-REPO"
PH_PATH = "/REDACTED/PATH"
PH_BRANCH = "REDACTED-BRANCH"
PH_URL = "https://redacted.invalid/"
PH_EMAIL = "redacted@redacted.invalid"
PH_REF = "#000"
PH_SHA = "0000000"
PH_TITLE = "(REDACTED)"

PLACEHOLDERS = (PH_URL, PH_EMAIL, PH_PATH, PH_BRANCH, PH_REPO, PH_REF, PH_SHA)

BRANCH_RE = re.compile(
    r"\b(?:feat|fix|docs|chore|refactor|test|perf|security|ci)/[A-Za-z0-9._/-]+"
)
URL_RE = re.compile(r"\bhttps?://\S+")
EMAIL_RE = re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")
ABSPATH_RE = re.compile(r"(?:/Users/|/home/|/private/|/var/|/tmp/)\S*")
REF_RE = re.compile(r"#\d+")
RESETS_RE = re.compile(r"(resetsAt=)\d+")
SHA_RE = re.compile(r"\b(?=[0-9a-f]{7,40}\b)(?=[a-f0-9]*[a-f])(?=[a-f0-9]*\d)[0-9a-f]{7,40}\b")
# Slug-shaped, EXCEPT a pure numeric ratio — `8/5000 remaining` in the GitHub
# quota preflight is a machine count, not an owner/repo, and redacting it would
# destroy the shape while protecting nothing.
SLUG_RE = re.compile(
    r"\b(?![0-9]+/[0-9]+\b)[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*\b"
)
# An issue/PR title trailing its forge reference — `…blocked by acme/x#12 (Add
# the thing)`. This is the one construct in terminal error text that carries
# free-form private prose, so it is rewritten and then re-checked below.
REF_TITLE_RE = re.compile(r"(#\d+|" + re.escape(PH_REF) + r")\s*\([^)]*\)")


def redact(text: str) -> str:
    text = URL_RE.sub(PH_URL, text)
    text = EMAIL_RE.sub(PH_EMAIL, text)
    text = ABSPATH_RE.sub(PH_PATH, text)
    text = BRANCH_RE.sub(PH_BRANCH, text)
    text = REF_TITLE_RE.sub(lambda m: m.group(1) + " " + PH_TITLE, text)
    # Placeholders are themselves slug-shaped and path-shaped, so park them
    # behind opaque sentinels before the remaining substitutions run — without
    # this, `/w/acme/widget` re-enters SLUG_RE and comes out mangled.
    for i, ph in enumerate(PLACEHOLDERS):
        text = text.replace(ph, "\x00%d\x00" % i)
    text = RESETS_RE.sub(r"\g<1>0", text)
    text = REF_RE.sub(PH_REF, text)
    text = SHA_RE.sub(PH_SHA, text)
    text = SLUG_RE.sub(PH_REPO, text)
    for i, ph in enumerate(PLACEHOLDERS):
        text = text.replace("\x00%d\x00" % i, ph)
    return text


DANGER = (
    ("absolute path", re.compile(r"/Users/|/home/|/private/|/var/|/tmp/")),
    ("url", URL_RE),
    ("email", EMAIL_RE),
    ("forge reference", REF_RE),
    ("git sha", SHA_RE),
    ("owner/repo or path slug", SLUG_RE),
)

SENTINEL = "PLACEHOLDERTOKEN"


def verify(text: str) -> None:
    """Fail closed: abort the whole capture if any identity survived."""
    m = re.search(r"#\d+\s*\((?!REDACTED\))", text)
    if m:
        raise SystemExit(
            "capture-terminal-kind-fixture: ABORT — un-redacted forge-reference title in shape %r"
            % text
        )
    probe = text
    for ph in PLACEHOLDERS:
        probe = probe.replace(ph, SENTINEL)
    for label, rx in DANGER:
        m = rx.search(probe)
        if m:
            raise SystemExit(
                "capture-terminal-kind-fixture: ABORT — unredacted %s %r in shape %r"
                % (label, m.group(0), text)
            )


# --- 4. mine --------------------------------------------------------------

shapes = {}  # normalized key -> record
log_files = 0
structured_lines = 0

DIGITS_RE = re.compile(r"\d+")


def shape_key(text: str) -> str:
    return DIGITS_RE.sub("N", text)


for root in roots:
    for path in sorted(glob.glob(os.path.join(root, ".nightgauge", "logs", "*.log"))):
        log_files += 1
        try:
            fh = open(path, encoding="utf-8", errors="replace")
        except OSError:
            continue
        with fh:
            for line in fh:
                m = LINE_RE.match(line.rstrip("\n"))
                if not m:
                    continue
                structured_lines += 1
                day, msg = m.group(1), strip_tags(m.group(2)).strip()
                if not msg:
                    continue

                origin = None
                env = RESULT_ENVELOPE_RE.search(msg)
                if env:
                    field = RESULT_FIELD_RE.search(env.group(0))
                    if not field:
                        continue
                    try:
                        msg = json.loads('"' + field.group(1) + '"')
                    except ValueError:
                        continue
                    msg = msg.strip()
                    origin, truncated = "result-envelope", False
                else:
                    tr = TRUNCATED_RE.search(msg)
                    truncated = bool(tr)
                    if tr:
                        # The logger cuts mid-word; drop the partial token so
                        # the captured shape ends on a real word boundary.
                        msg = msg[: tr.start()].rstrip()
                        msg = msg[: msg.rfind(" ")].rstrip(" (,-—") if " " in msg else msg
                    if any(s in msg for s in SOURCE_SHAPED):
                        continue
                    origin = "pipeline-log"

                if not msg or len(msg) > MAX_LEN or "\n" in msg:
                    continue
                low = msg.lower()
                markers = [lit for lit in vocabulary if lit in low]
                # A pipeline-log line earns its place by carrying a marker the
                # classifier matches. A result envelope earns it by BEING the
                # classifier's input — including when nothing matches, which is
                # the real-world unknown case the corpus must also pin.
                if not markers and origin != "result-envelope":
                    continue
                clean = redact(msg)
                verify(clean)
                key = shape_key(clean)
                rec = shapes.get(key)
                if rec is None:
                    shapes[key] = {
                        "text": clean,
                        "occurrences": 1,
                        "first_seen": day,
                        "last_seen": day,
                        "origin": origin,
                        "log_truncated": truncated,
                        "markers": markers,
                    }
                else:
                    rec["occurrences"] += 1
                    rec["first_seen"] = min(rec["first_seen"], day)
                    rec["last_seen"] = max(rec["last_seen"], day)
                    rec["log_truncated"] = rec["log_truncated"] or truncated
                    # Deterministic representative: lexicographically smallest.
                    if clean < rec["text"]:
                        rec["text"] = clean

# --- 5. minimal covering selection ----------------------------------------
#
# One representative shape per Go literal, iterated in the classifier's own
# declaration order: for each literal not yet covered by an already-selected
# shape, take the most frequently observed shape containing it (ties broken
# lexicographically). The result is the smallest reviewable set that still
# exercises every marker the live telemetry actually produced.

by_marker = {}
for rec in shapes.values():
    for mk in rec["markers"]:
        by_marker.setdefault(mk, []).append(rec)

# Every adapter result envelope is kept — they are few, they are the literal
# classifier input, and the ones that match NO marker are the only real
# evidence there is for the unknown/default case.
selected = [r for r in shapes.values() if r["origin"] == "result-envelope"]
covered = set()
for rec in selected:
    covered.update(rec["markers"])
for lit in vocabulary:
    if lit in covered:
        continue
    candidates = by_marker.get(lit)
    if not candidates:
        continue
    best = sorted(candidates, key=lambda r: (-r["occurrences"], r["text"]))[0]
    if best in selected:
        covered.update(best["markers"])
        continue
    selected.append(best)
    covered.update(best["markers"])

selected.sort(key=lambda r: r["text"])

payload = {
    "$comment": [
        "REAL terminal-failure error shapes captured from live pipeline telemetry (#166, #306).",
        "Generated by scripts/capture-terminal-kind-fixture.sh — do not hand-edit.",
        "Every corpus.json row marked \"source\": \"captured\" must appear verbatim in `shapes`",
        "below, and every shape below must have a corpus row. Both directions are asserted by",
        "internal/orchestrator/terminal_kind_corpus_parity_test.go, so a hand-authored string",
        "cannot be passed off as evidence and a newly observed real shape cannot be ignored.",
        "Strings are redacted (see the script header); identity-shaped constructs are replaced",
        "with fixed placeholders and a fail-closed verification pass re-scans every emitted string.",
    ],
    # Dates describe the TELEMETRY, never the run: a wall-clock stamp would
    # make every re-capture a diff and turn the reproducibility claim in the
    # header (and in .prettierignore) into a lie one calendar day later.
    "telemetry_first_seen": min((r["first_seen"] for r in selected), default=""),
    "telemetry_last_seen": max((r["last_seen"] for r in selected), default=""),
    "generator": "scripts/capture-terminal-kind-fixture.sh",
    "source": (
        ".nightgauge/logs/*.log — structured pipeline-logger lines and adapter "
        "result envelopes (see `origin` on each shape)"
    ),
    "roots_scanned": len(roots),
    "log_files_scanned": log_files,
    "structured_log_lines_scanned": structured_lines,
    "distinct_shapes_observed": len(shapes),
    "markers_observed": sorted(by_marker),
    "markers_in_go_classifier": len(vocabulary),
    "shapes": [
        {
            "text": r["text"],
            "origin": r["origin"],
            "occurrences": r["occurrences"],
            "first_seen": r["first_seen"],
            "last_seen": r["last_seen"],
            "log_truncated": r["log_truncated"],
            "markers": r["markers"],
        }
        for r in selected
    ],
}

dest = os.path.join(out_dir, "captured-shapes.json")
with open(dest, "w", encoding="utf-8") as fh:
    json.dump(payload, fh, indent=2, ensure_ascii=False)
    fh.write("\n")

print(
    "captured %d shapes covering %d/%d mineable markers from %d log files (%d structured lines)"
    % (len(selected), len(covered), len(vocabulary), log_files, structured_lines)
)
print("wrote %s" % dest)
PY
