#!/usr/bin/env bash
# Answer one question: did the merge commit's own CI actually go green?
#
# Usage:
#   scripts/post-merge-check.sh <merge-sha> [owner/repo]
#   scripts/post-merge-check.sh <merge-sha> --repo <owner/repo>
#
# The repository defaults to the one named by this checkout's `origin` remote.
#
# Exit-code contract (read the code, not the text, and never through a pipe —
# a pipeline's status is the last command's, so `... | tail` always reports 0):
#   0  GREEN    every check-run and commit status completed, and none of them
#               failed — required or not
#   1  RED      at least one completed check-run or commit status did not
#               succeed. `main` is red and it is the merger's to fix now; never
#               re-run hoping for a better answer
#   2  NOT-YET  not observable: nothing exists yet, something is still
#               running, a required context has not appeared, or the API could
#               not be read. Wait and re-run
#
# Portability: this file contains nothing specific to one repository. Every
# Nightgauge workspace repository carries a byte-identical copy; the canonical
# one lives in nightgauge/nightgauge. Change it there first, then re-copy.
#
# Why a script and not a one-liner
# --------------------------------
# A green PR check is a PREDICTION about a merge that has not happened. Three
# failure classes are visible only on the merge commit: a nondeterministic test
# that passes the PR and fails `main` on the identical tree, merge skew between
# two PRs that were green apart, and the secrets and permissions `main` has that
# PR runs do not.
#
# The obvious one-liner counts non-green check-runs and calls zero green. That
# counts BAD things without first establishing that it looked at anything, and
# it fails in both directions:
#
#   1. Immediately after a merge the workflows for the merge commit have not
#      been created yet, so the check-run list is EMPTY and the count is zero —
#      a false GREEN precisely when the check is run promptly.
#   2. A check still `in_progress` has `conclusion: null`, so a healthy merge
#      briefly reads RED, which trains an operator to re-run until the answer
#      is nicer — and then to believe failure 1.
#
# AN ABSENCE OF FAILURES IS NOT THE PRESENCE OF SUCCESSES. NOT-YET is its own
# exit code because "I cannot tell yet" is a third answer.
#
# When the compiled `nightgauge` binary can be resolved, the verdict is
# delegated to `nightgauge ci checks-complete`, which also asserts that every
# REQUIRED check name is present (a rollup can omit an in-flight required
# check entirely). The bash logic below is the fallback when no binary is
# available, and it cannot make that assertion.
#
# Both paths read every page of both GitHub status surfaces — check-runs and
# commit statuses — because a list endpoint returns 30 items per page by
# default, and a context on page 2 or on the other surface is otherwise never
# seen.

set -uo pipefail

usage() {
  echo "usage: scripts/post-merge-check.sh <merge-sha> [owner/repo | --repo <owner/repo>]" >&2
}

SHA=""
REPO=""
while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    usage
    exit 2
    ;;
  --repo)
    if [[ $# -lt 2 || -z "$2" ]]; then
      usage
      exit 2
    fi
    REPO="$2"
    shift 2
    ;;
  --repo=*)
    REPO="${1#--repo=}"
    shift
    ;;
  -*)
    echo "unknown flag: $1" >&2
    usage
    exit 2
    ;;
  *)
    if [[ -z "$SHA" ]]; then
      SHA="$1"
    elif [[ -z "$REPO" ]]; then
      REPO="$1"
    else
      usage
      exit 2
    fi
    shift
    ;;
  esac
done

if [[ -z "$SHA" ]]; then
  usage
  exit 2
fi

if [[ -z "$REPO" ]]; then
  origin=$(git config --get remote.origin.url 2>/dev/null || true)
  if [[ -z "$origin" ]]; then
    echo "NOT-YET  no owner/repo given and this directory has no origin remote" >&2
    exit 2
  fi
  # Handles git@host:owner/repo(.git), ssh://git@host/owner/repo(.git) and
  # https://host/owner/repo(.git). Pure parameter expansion, not sed: BSD sed
  # (macOS) rejects the lazy-quantifier regex a sed version would need.
  REPO="${origin%/}"      # drop a trailing slash
  REPO="${REPO%.git}"     # drop a trailing .git
  _name="${REPO##*/}"     # repo
  REPO="${REPO%/"$_name"}" # everything before it
  _owner="${REPO##*[:/]}" # owner, past the last : or /
  REPO="$_owner/$_name"
fi

if [[ ! "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "NOT-YET  '$REPO' is not an owner/repo name" >&2
  exit 2
fi

# Delegate to the compiled binary when it can be resolved — the same
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
# installed.
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
# required-check-set assertion and the per-run cross-check — bash has no cheap
# way to resolve branch-protection and ruleset required-check names.

# `gh api --paginate --jq` runs the jq program once PER PAGE, so a
# whole-document program such as `.check_runs | length` prints one number per
# page and every comparison below would silently fail on a commit with more
# than one page. Emit one JSON line per item instead and slurp them afterwards:
# that is correct for any number of pages. Commit statuses are normalized into
# the check-run shape, pending ones as still running.
runs=$(gh api --paginate "repos/$REPO/commits/$SHA/check-runs?per_page=100" \
  --jq '.check_runs[] | {name, status, conclusion, url: .html_url} | tojson' 2>/dev/null) || {
  # An API failure is not evidence of anything. Saying NOT-YET keeps the caller
  # from reading a network blip as a clean bill of health.
  echo "NOT-YET  could not read check-runs for $REPO@${SHA:0:8} (API error or unknown sha)"
  exit 2
}
statuses=$(gh api --paginate "repos/$REPO/commits/$SHA/status?per_page=100" \
  --jq '.statuses[] | {name: .context, status: (if .state == "pending" then "in_progress" else "completed" end), conclusion: (if .state == "pending" then null else .state end), url: .target_url} | tojson' 2>/dev/null) || {
  echo "NOT-YET  could not read commit statuses for $REPO@${SHA:0:8} (API error or unknown sha)"
  exit 2
}
checks=$(printf '%s\n%s\n' "$runs" "$statuses" | jq -s '.') || {
  echo "NOT-YET  could not parse the check-runs and statuses for $REPO@${SHA:0:8}"
  exit 2
}

total=$(printf '%s' "$checks" | jq 'length')
if [[ "$total" -eq 0 ]]; then
  echo "NOT-YET  $REPO@${SHA:0:8} has no check-runs or commit statuses yet — the workflows have not been created."
  echo "         An empty list is not evidence of success. Wait and re-run."
  exit 2
fi

pending=$(printf '%s' "$checks" | jq '[.[] | select(.status != "completed")] | length')
if [[ "$pending" -gt 0 ]]; then
  names=$(printf '%s' "$checks" | jq -r '[.[] | select(.status != "completed") | .name] | join(", ")')
  echo "NOT-YET  $pending of $total check(s) still running on $REPO@${SHA:0:8}: $names"
  exit 2
fi

# Only now is counting failures meaningful: everything completed, and there is
# at least one of them. Required or not, a failure makes the commit red.
bad='select(.conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral")'
failed=$(printf '%s' "$checks" | jq "[.[] | $bad] | length")
if [[ "$failed" -gt 0 ]]; then
  echo "RED      $failed of $total check(s) failed on $REPO@${SHA:0:8}:"
  printf '%s' "$checks" | jq -r ".[] | $bad | \"           \\(.conclusion // \"?\")  \\(.name)  \\(.url // \"\")\""
  echo "         main is red and it is yours to fix immediately."
  exit 1
fi

echo "GREEN    all $total check(s) completed successfully on $REPO@${SHA:0:8}"
exit 0
