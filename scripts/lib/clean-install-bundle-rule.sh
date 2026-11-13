#!/usr/bin/env bash
# Run-kind rule for the two G1 bundle-read rows (#3135, supersedes #2997).
# Sourced by scripts/clean-install-e2e.sh; defines functions only.
#
# schedule            both rows recorded as SKIP (never PASS, never FAIL)
# every other kind    both rows hard-fail (manual dispatch, rc dispatch, local)
# --smoke             neither row evaluated

# bundle_rule_text <run_kind> <smoke 0|1>: the summary's "Bundle-read rows (G1)" text.
bundle_rule_text() {
  if [[ "${2:-0}" == "1" ]]; then
    printf 'not evaluated (--smoke) — this run: %s\n' "$1"
  else
    printf 'skipped on schedule runs (recorded SKIP, never PASS); hard-fail on manual dispatch, RC dispatch and local — this run: %s\n' "$1"
  fi
}

# bundle_rows_skip <run_kind>: exit 0 iff the bundle-read rows are skipped for this kind.
bundle_rows_skip() {
  [[ "${1:-}" == "schedule" ]]
}
