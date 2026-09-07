#!/usr/bin/env bash
# Answer one question: did the merge commit's own CI actually go green?
#
# Usage:
#   scripts/post-merge-check.sh <merge-sha> [owner/repo]   # default repo: this checkout's origin
#
# Verdicts / exit codes:
#   0  GREEN        every check-run completed, and none of them failed
#   1  RED          at least one completed check-run did not succeed — yours to fix now
#   2  NOT-YET      not observable: no check-runs exist yet, or some are still running
#
# Why this is a script and not the one-liner it replaces
# ------------------------------------------------------
# `AGENTS.md` mandates verifying `main`'s own run after every merge, because a
# green PR check is a PREDICTION against a merge that has not happened, and
# three failure classes are visible only afterwards: a nondeterministic test
# that passes the PR and fails `main` on the identical tree (this is exactly how
# #572 was found), merge skew between two PRs that were green apart, and the
# secrets and permissions `main` has that PR runs do not.
#
# The idiom it mandated was:
#
#   gh api ".../check-runs" --jq '[.check_runs[]|select(.conclusion!="success"
#     and .conclusion!="skipped" and .conclusion!="neutral")]|length'
#
# with "non-zero means red". That counts BAD things without first establishing
# that it looked at anything, and it fails in both directions:
#
#   1. Immediately after a merge the workflows for the merge commit have not
#      been created yet, so `.check_runs` is an EMPTY ARRAY. The filter returns
#      0 — the documented signal for GREEN. The check that exists to catch a red
#      `main` reports green precisely when run promptly, which is exactly when an
#      agent runs it. Found independently by two sessions in two repositories,
#      the second of which got a false green on a real merge (#1038).
#
#   2. A check still `in_progress` carries `conclusion: null`, which is not
#      success/skipped/neutral, so it counts as a failure and a healthy merge
#      briefly reads RED. Less dangerous, but it trains an operator to re-run
#      the command until it says what they want — which is how failure 1 gets
#      believed.
#
# The general principle, the same shape as the "a green check on a job that
# skipped is not evidence" rule already in AGENTS.md: AN ABSENCE OF FAILURES IS
# NOT THE PRESENCE OF SUCCESSES. Any check that counts bad things must first
# establish that it looked at anything at all. That is why NOT-YET is its own
# exit code rather than being folded into either verdict — "I cannot tell yet"
# is a third answer, and collapsing it into GREEN is the entire defect.
#
# Read the EXIT CODE, not the text. `$?` without a pipe: a pipeline's status is
# the last command's, so `post-merge-check.sh <sha> | tail` always reports 0.
#
# #1540: the three numbers above (total>0/pending==0/bad==0) have no way to
# tell "a required check is absent from the rollup" from "nothing to wait
# for" — that is the defect this issue reports, and bash has no cheap way to
# resolve the branch's required-check set (branch protection + rulesets)
# without duplicating internal/github.GetRequiredCheckNames a second time.
# So this script now DELEGATES to the compiled `nightgauge ci
# checks-complete` verb (internal/github.EvaluateChecksCompleteCrossChecked)
# whenever the binary can be resolved, and its own total/pending/bad logic
# below becomes the FALLBACK for when it cannot — a degraded path that lacks
# the required-check-set assertion (and the per-run cross-check), which is
# named explicitly wherever it fires.

set -uo pipefail

SHA="${1:-}"
REPO="${2:-}"

if [[ -z "$SHA" ]]; then
  echo "usage: scripts/post-merge-check.sh <merge-sha> [owner/repo]" >&2
  exit 2
fi

if [[ -z "$REPO" ]]; then
  origin=$(git config --get remote.origin.url 2>/dev/null || true)
  if [[ -z "$origin" ]]; then
    echo "NOT-YET  no owner/repo given and this directory has no origin remote" >&2
    exit 2
  fi
  # Handles both git@host:owner/repo(.git) and https://host/owner/repo(.git).
  #
  # Pure parameter expansion, not sed. The first draft of this line used an
  # -E regex with a lazy `+?`, which BSD sed — macOS, where this runs most —
  # rejects outright: every invocation printed "repetition-operator operand
  # invalid" and fell through to NOT-YET. It failed in the safe direction, but
  # a verification script whose repo detection never works is furniture.
  REPO="${origin%.git}"   # drop a trailing .git
  REPO="${REPO%/}"        # drop a trailing slash
  _name="${REPO##*/}"     # repo
  REPO="${REPO%/$_name}"  # everything before it
  _owner="${REPO##*[:/]}" # owner, past the last : or /
  REPO="$_owner/$_name"
fi

# #1540: delegate to the compiled binary when it can be resolved — same
# discovery cascade skills use (NIGHTGAUGE_BIN -> PATH -> repo bin/ ->
# canonical-repo bin/ -> ~/go/bin). `nightgauge ci checks-complete` owns the
# verdict/exit code entirely (0 GREEN / 1 RED / 2 NOT-YET, a drop-in match for
# this script's own contract) and carries the required-check-set assertion
# and the per-run cross-check this bash fallback cannot.
#
# NIGHTGAUGE_POST_MERGE_CHECK_BASH_ONLY forces this script's own fallback
# logic below even when a binary IS resolvable — used only by
# scripts/test-post-merge-check.sh so its bash-fallback cases stay
# deterministic on a machine that happens to have a `nightgauge` binary
# installed (this repo's own checkout, for one).
if [[ -z "${NIGHTGAUGE_POST_MERGE_CHECK_BASH_ONLY:-}" ]]; then
  BINARY="${NIGHTGAUGE_BIN:-}"
  [[ -n "$BINARY" && ! -x "$BINARY" ]] && BINARY=""
  [[ -z "$BINARY" ]] && BINARY=$(command -v nightgauge 2>/dev/null || true)
  if [[ -z "$BINARY" ]]; then
    REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
    [[ -x "$REPO_ROOT/bin/nightgauge" ]] && BINARY="$REPO_ROOT/bin/nightgauge"
  fi
  if [[ -z "$BINARY" ]]; then
    GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
    if [[ -n "$GIT_COMMON_DIR" ]]; then
      CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
      [[ -n "$CANONICAL_REPO" && -x "$CANONICAL_REPO/bin/nightgauge" ]] && BINARY="$CANONICAL_REPO/bin/nightgauge"
    fi
  fi
  [[ -z "$BINARY" && -x "$HOME/go/bin/nightgauge" ]] && BINARY="$HOME/go/bin/nightgauge"

  if [[ -n "$BINARY" ]]; then
    exec "$BINARY" ci checks-complete "$SHA" --repo "$REPO"
  fi
fi

# --- Fallback: the binary could not be resolved. Everything below lacks the
# required-check-set assertion and the per-run cross-check (#1540) — bash has
# no cheap way to resolve branch protection/ruleset required-check names
# without a second implementation of internal/github.GetRequiredCheckNames.

runs=$(gh api "repos/$REPO/commits/$SHA/check-runs" --paginate 2>/dev/null) || {
  # An API failure is not evidence of anything. Saying NOT-YET keeps the caller
  # from reading a network blip as a clean bill of health.
  echo "NOT-YET  could not read check-runs for $REPO@${SHA:0:8} (API error or unknown sha)"
  exit 2
}

total=$(printf '%s' "$runs" | jq '.check_runs | length')
if [[ "$total" -eq 0 ]]; then
  echo "NOT-YET  $REPO@${SHA:0:8} has no check-runs yet — the workflows have not been created."
  echo "         An empty list is not evidence of success. Wait and re-run."
  exit 2
fi

pending=$(printf '%s' "$runs" | jq '[.check_runs[] | select(.status != "completed")] | length')
if [[ "$pending" -gt 0 ]]; then
  names=$(printf '%s' "$runs" | jq -r '[.check_runs[] | select(.status != "completed") | .name] | join(", ")')
  echo "NOT-YET  $pending of $total check-run(s) still running on $REPO@${SHA:0:8}: $names"
  exit 2
fi

# Only now is counting failures meaningful: every run completed, and there is at
# least one of them.
failed=$(printf '%s' "$runs" | jq '[.check_runs[] | select(.conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral")] | length')
if [[ "$failed" -gt 0 ]]; then
  echo "RED      $failed of $total check-run(s) failed on $REPO@${SHA:0:8}:"
  printf '%s' "$runs" | jq -r '.check_runs[] | select(.conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral") | "           \(.conclusion // "?")  \(.name)  \(.html_url)"'
  echo "         main is red and it is yours to fix immediately."
  exit 1
fi

echo "GREEN    all $total check-run(s) completed successfully on $REPO@${SHA:0:8}"
exit 0
