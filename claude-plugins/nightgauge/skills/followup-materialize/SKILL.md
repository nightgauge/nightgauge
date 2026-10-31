---
name: followup-materialize
description: File the run's discovered-work records as follow-up GitHub issues
  under the follow_up_filing gate. Runs after pr-merge on every issue whose run
  recorded discoveries; the scheduler skips it without dispatch when none were.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read Write
context: fork
disable-model-invocation: true
---

# Follow-up Materialize

> Turn the work a run discovered but did not do into board-synced issues.

## Description

A thin wrapper around `nightgauge followup materialize <N>`. The binary reads
`discovered-work-<N>.json` (ADR 027), applies the `follow_up_filing` gate
(fail closed to report-only, per-run cap, untrusted-author check), skips
records already filed, comments on an open issue with the same title instead
of duplicating it, files the rest with `type:*`, Priority, Size,
Status=Backlog and a `Discovered in #N (run R, stage S)` line, and removes
handled records from the capture. See
[docs/SELF_IMPROVEMENT_BOUNDARIES.md](../../../../docs/SELF_IMPROVEMENT_BOUNDARIES.md)
Rule 5 and [docs/CONFIGURATION.md](../../../../docs/CONFIGURATION.md).

## Invocation

| Tool        | Command                                                            |
| ----------- | ------------------------------------------------------------------ |
| Claude Code | `/nightgauge-followup-materialize <issue-number>` (via plugin)     |
| Pipeline    | Automatic after pr-merge; skipped with no dispatch on zero records |

## Gotchas

- **Exit 3 is a cap refusal, not a crash.** The refused records stay in the
  capture file; report them and fail the stage. Never raise the cap or delete
  the capture to make the stage green.
- **Report-only is an answer.** A `report_only: true` result with a reason
  (kill switch, missing config, untrusted author) is success: the would-file
  list is the run's record.

## Workflow

### Phase 1: Run Materialize

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found"
  exit 1
fi
OUTPUT_TMP=$(mktemp)
"$BINARY" followup materialize "$ISSUE_NUMBER" --json > "$OUTPUT_TMP"
STATUS=$?
if [ -s "$OUTPUT_TMP" ]; then
  "$BINARY" layout write pipeline "followup-materialize-${ISSUE_NUMBER}.json" --from "$OUTPUT_TMP"
  jq -r '.items[] | "\(.outcome)\t\(.id)\t\(.issue_number // "-")\t\(.title)"' "$OUTPUT_TMP"
fi
rm -f "$OUTPUT_TMP"
[ "$STATUS" -eq 3 ] && echo "ERROR: per-run cap refused records; they remain in the capture file"
exit "$STATUS"
```

## Output Contract

`followup-materialize-{N}.json` in the pipeline state directory: the gate,
`report_only`, `reason`, `refused`, and one item per record with `outcome`
(`filed`, `already-exists`, `deduped`, `would-file`, `refused`, `invalid`).
Nothing is written when the capture held no records.

## Author

nightgauge
