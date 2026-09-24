#!/usr/bin/env bash
# Answer one question: did this merge land a tree whose gate passed, and is
# everything still running on the merge commit green?
#
# Usage:
#   scripts/post-merge-check.sh <merge-sha> [owner/repo]
#   scripts/post-merge-check.sh <merge-sha> --repo <owner/repo>
#
# The repository defaults to the one named by this checkout's `origin` remote.
#
# Exit-code contract (read the code, not the text, and never through a pipe —
# a pipeline's status is the last command's, so `... | tail` always reports 0):
#   0  GREEN    the gate passed and nothing on the merge commit failed (see
#               "What is verified" below)
#   1  RED      a completed check that decides the verdict did not succeed.
#               `main` is red and it is the merger's to fix now; never re-run
#               hoping for a better answer
#   2  NOT-YET  not observable: nothing exists yet, something is still
#               running, a required context has not appeared yet, the
#               required-check set or the API could not be read. Wait and
#               re-run
#
# What is verified (#2055)
# ------------------------
# The PR run is the gate. A strict ruleset (strict_required_status_checks_policy)
# merges a PR only when it is up to date with the base branch, so the squash
# commit's tree is the tree the PR head's required checks passed on, and the
# full suites no longer re-run on push. For a merge commit of a merged PR:
#
#   a. the merge commit's tree must equal the PR head commit's tree;
#   b. every required check on the PR head must have concluded success
#      (skipped and neutral count as passing);
#   c. every other check that still runs on the merge commit itself must be
#      green; still running is NOT-YET. An empty list is NOT-YET for five
#      minutes after the merge (the push workflows may not exist yet) and
#      passes after that. cache-warm never counts: it tests nothing, so its
#      failure is not main being red. CodeQL's merge-commit runs (the
#      "Analyze (<language>)" jobs and the "CodeQL" code-scanning check) are
#      informational: they are the default-branch baseline for code scanning,
#      but they analyse the same tree with the same queries as the PR's own
#      required CodeQL run, which passed in (b). They are reported as INFO,
#      never RED or NOT-YET.
#
# If the trees differ (a ruleset bypass, or a branch without the strict
# policy) the PR run is not evidence about the landed tree, so the merge commit
# must carry every required check itself, the pre-#2055 rule, and CodeQL there
# counts like any other check. Where the suites
# no longer run on push the required checks are absent: NOT-YET for five
# minutes after the merge, then RED if no required check is running there,
# because the landed tree was never tested and waiting cannot change that. The
# remedy is to run the suites on main via workflow_dispatch. A commit with no
# merged PR (a direct push) is judged by that same merge-commit-only rule, with
# the default branch's required set.
#
# The required-check set comes from rulesets (and, best-effort, classic
# protection). If it cannot be read the verdict is never GREEN: NOT-YET.
#
# Portability: this file contains nothing specific to one repository. Every
# Nightgauge workspace repository carries a byte-identical copy; the canonical
# one lives in nightgauge/nightgauge. Change it there first, then re-copy.
#
# Why a script and not a one-liner
# --------------------------------
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
# delegated to `nightgauge ci checks-complete` (github.EvaluateMergedCommit),
# which also cross-checks the per-run actions API. The bash logic below is the
# fallback when no binary is available; it applies the same rule without that
# cross-check.
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
# this script's own contract) and carries the per-run cross-check this bash
# fallback does not.
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

  # Hand off only to a binary that applies the #2055 merged-PR rule: an older
  # one demands the required checks on the merge commit, which no longer run
  # on push, and would say NOT-YET forever. It advertises the rule with a
  # fixed line in `ci checks-complete --help`. A here-string, not a pipe: with
  # pipefail, `grep -q` closing the pipe early can fail the producer.
  if [[ -n "$BINARY" ]]; then
    help=$("$BINARY" ci checks-complete --help 2>/dev/null) || help=""
    if grep -q 'capability: merged-pr-gate' <<<"$help"; then
      exec "$BINARY" ci checks-complete "$SHA" --repo "$REPO"
    fi
    echo "note: $BINARY predates the merged-PR rule (#2055); using this script's own" >&2
  fi
fi

# --- Fallback: no binary with the #2055 rule could be resolved. Same rule as
# the binary (github.EvaluateMergedCommit), minus the per-run cross-check, and
# with the required-check set read from the base branch's rulesets and
# (best-effort) classic branch protection.

# How long after a merge an EMPTY merge-commit check list still means "the push
# workflows have not been created yet" rather than "nothing runs on push".
# Matches github.MergeCommitCheckGrace.
GRACE_SECONDS=300

# One jq program judges any list of checks. $req is null (judge every check)
# or an array of names (judge only those, and each must be present). Names
# match case-insensitively and trimmed, as in the Go evaluator. skipped and
# neutral are passing conclusions.
# shellcheck disable=SC2016 # jq program, not shell
JUDGE='
def key: ascii_downcase | gsub("^\\s+|\\s+$"; "");
def bad: .conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral";
(if $req == null then . else ($req | map(key)) as $want | [ .[] | select((.name | key) as $k | $want | index($k)) ] end) as $scope
| { total: ($scope | length),
    pending: [ $scope[] | select(.status != "completed") | .name ],
    failed: [ $scope[] | select(.status == "completed") | select(bad) | "           \(.conclusion // "?")  \(.name)  \(.url // "")" ],
    missing: (if $req == null then [] else ($scope | map(.name | key)) as $have | [ $req[] | select((key) as $k | $have | index($k) | not) ] end) }'

# read_checks <sha> — every check-run and commit status on <sha>, as one JSON
# array. `gh api --paginate --jq` runs the jq program once PER PAGE, so a
# whole-document program such as `.check_runs | length` prints one number per
# page and every comparison would silently fail on a commit with more than one
# page. Emit one JSON line per item instead and slurp them afterwards: that is
# correct for any number of pages. Commit statuses are normalized into the
# check-run shape, pending ones as still running. cache-warm is dropped: it
# tests nothing, so its failure is never main being red (#2055). An API
# failure is not
# evidence of anything, so it is NOT-YET, never a verdict. Callers run it in a
# command substitution, where its `exit 2` ends only the subshell, so each one
# prints the captured NOT-YET line and exits itself.
read_checks() {
  local sha="$1" runs statuses
  runs=$(gh api --paginate "repos/$REPO/commits/$sha/check-runs?per_page=100" \
    --jq '.check_runs[] | select((.name | ascii_downcase) != "cache-warm") | {name, status, conclusion, url: .html_url} | tojson' 2>/dev/null) || {
    echo "NOT-YET  could not read check-runs for $REPO@${sha:0:8} (API error or unknown sha)"
    exit 2
  }
  statuses=$(gh api --paginate "repos/$REPO/commits/$sha/status?per_page=100" \
    --jq '.statuses[] | {name: .context, status: (if .state == "pending" then "in_progress" else "completed" end), conclusion: (if .state == "pending" then null else .state end), url: .target_url} | tojson' 2>/dev/null) || {
    echo "NOT-YET  could not read commit statuses for $REPO@${sha:0:8} (API error or unknown sha)"
    exit 2
  }
  printf '%s\n%s\n' "$runs" "$statuses" | jq -s '.' || {
    echo "NOT-YET  could not parse the check-runs and statuses for $REPO@${sha:0:8}"
    exit 2
  }
}

# judge_commit <sha> <checks> <required-json|null> — the pre-#2055 rule, for a
# commit that is its own evidence: something must exist, everything must have
# concluded, every required context must be present, and nothing may have
# failed, required or not. Exits with the verdict.
judge_commit() {
  local sha="$1" checks="$2" req="$3" j total pending failed missing names
  j=$(printf '%s' "$checks" | jq -c --argjson req null "$JUDGE")
  total=$(jq '.total' <<<"$j")
  if [[ "$total" -eq 0 ]]; then
    echo "NOT-YET  $REPO@${sha:0:8} has no check-runs or commit statuses yet — the workflows have not been created."
    echo "         An empty list is not evidence of success. Wait and re-run."
    exit 2
  fi
  pending=$(jq '.pending | length' <<<"$j")
  if [[ "$pending" -gt 0 ]]; then
    names=$(jq -r '.pending | join(", ")' <<<"$j")
    echo "NOT-YET  $pending of $total check(s) still running on $REPO@${sha:0:8}: $names"
    exit 2
  fi
  missing=$(printf '%s' "$checks" | jq -r --argjson req "$req" "$JUDGE | .missing | join(\", \")")
  if [[ -n "$missing" ]]; then
    echo "NOT-YET  required check(s) absent from $REPO@${sha:0:8}: $missing"
    exit 2
  fi
  failed=$(jq '.failed | length' <<<"$j")
  if [[ "$failed" -gt 0 ]]; then
    echo "RED      $failed of $total check(s) failed on $REPO@${sha:0:8}:"
    jq -r '.failed[]' <<<"$j"
    echo "         main is red and it is yours to fix immediately."
    exit 1
  fi
  if [[ "$REQ_KNOWN" -ne 1 ]]; then
    echo "NOT-YET  all $total check(s) passed on $REPO@${sha:0:8}, but the required-check set could not be read:"
    echo "         the gate cannot be verified, so this is not observable."
    exit 2
  fi
  echo "GREEN    all $total check(s) completed successfully on $REPO@${sha:0:8}"
  exit 0
}

# resolve_required <branch> — sets REQ (a JSON array of the branch's required
# contexts, or null when none are required) and REQ_KNOWN (1 when the set was
# read). Rulesets are readable with repository read access; classic
# protection needs more and is best-effort. An unknown set is never GREEN.
REQ=null
REQ_KNOWN=0
resolve_required() {
  local branch="$1" rules classic
  if [[ ! "$branch" =~ ^[A-Za-z0-9._/-]+$ ]]; then
    return
  fi
  if rules=$(gh api --paginate "repos/$REPO/rules/branches/$branch" \
    --jq '.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context' 2>/dev/null); then
    # `gh api` prints the error body on stdout for a 404 ("Branch not
    # protected"), so a failed read must discard its output, not keep it.
    classic=$(gh api "repos/$REPO/branches/$branch/protection/required_status_checks" --jq '.contexts[]' 2>/dev/null) || classic=""
    REQ=$(printf '%s\n%s\n' "$rules" "$classic" | jq -R -s -c 'split("\n") | map(select(length > 0)) | unique')
    [[ "$REQ" == "[]" ]] && REQ=null
    REQ_KNOWN=1
  fi
}

# merge_age — seconds since MERGED_AT. Not jq's fromdateiso8601: jq 1.6 reads
# it an hour late across DST, so the grace never expired (BSD date, then GNU).
merge_age() {
  local t
  t=$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$MERGED_AT" +%s 2>/dev/null ||
    date -u -d "$MERGED_AT" +%s 2>/dev/null) || {
    echo 0
    return
  }
  echo $(($(date -u +%s) - t))
}

# push_may_run — false only when every workflow file at the merge commit was
# read and none mentions the word `push` (#2061): an empty merge commit is then
# final, and the grace does not apply. Coarser than the binary's parser and
# never greener: any mention (even `git push` in a step) and any read failure
# count as "may run", which keeps the grace.
push_may_run() {
  local files f body
  files=$(gh api "repos/$REPO/contents/.github/workflows?ref=$FULL_SHA" \
    --jq '.[] | select(.type == "file" and (.name | test("\\.ya?ml$"; "i"))) | .path' 2>/dev/null) || return 0
  for f in $files; do
    body=$(gh api -H 'Accept: application/vnd.github.raw' "repos/$REPO/contents/$f?ref=$FULL_SHA" 2>/dev/null) || return 0
    printf '%s\n' "$body" | sed 's/#.*//' | grep -Eq '(^|[^A-Za-z0-9_./-])push($|[^A-Za-z0-9_./-])' && return 0
  done
  return 1
}

# --- Which evidence applies (#2055) ---------------------------------------
# A merged PR whose merge commit is <sha>, and both trees, read through the API
# rather than `git rev-parse <sha>^{tree}`: the checkout may not have fetched
# either commit, and the PR head's branch is usually deleted by the merge.
commit=$(gh api "repos/$REPO/commits/$SHA" --jq '[.sha, .commit.tree.sha] | @tsv' 2>/dev/null) || {
  echo "NOT-YET  could not read commit $REPO@${SHA:0:8} (API error or unknown sha)"
  exit 2
}
IFS=$'\t' read -r FULL_SHA MERGE_TREE <<<"$commit"
if [[ ! "$FULL_SHA" =~ ^[0-9a-f]{40}$ ]]; then
  echo "NOT-YET  could not resolve $REPO@${SHA:0:8} to a commit"
  exit 2
fi
pr=$(gh api "repos/$REPO/commits/$FULL_SHA/pulls" \
  --jq ".[] | select(.merged_at != null and .merge_commit_sha == \"$FULL_SHA\") | [.number, .head.sha, .base.ref, .merged_at] | @tsv" 2>/dev/null) || {
  echo "NOT-YET  could not read the pull requests for $REPO@${SHA:0:8} (API error)"
  exit 2
}
pr=$(head -n 1 <<<"$pr")

# No merged PR (a direct push, or a PR head that has not merged): the commit
# is its own evidence, judged by the pre-#2055 rule.
if [[ -z "$pr" ]]; then
  default_branch=$(gh api "repos/$REPO" --jq '.default_branch' 2>/dev/null) || default_branch=""
  resolve_required "$default_branch"
  merge_checks=$(read_checks "$SHA") || { printf '%s\n' "$merge_checks"; exit 2; }
  judge_commit "$SHA" "$merge_checks" "$REQ"
fi

IFS=$'\t' read -r PR_NUMBER HEAD_SHA BASE_REF MERGED_AT <<<"$pr"
if [[ ! "$HEAD_SHA" =~ ^[0-9a-f]{40}$ || ! "$BASE_REF" =~ ^[A-Za-z0-9._/-]+$ ]]; then
  echo "NOT-YET  PR #$PR_NUMBER behind $REPO@${SHA:0:8} has no readable head or base"
  exit 2
fi
HEAD_TREE=$(gh api "repos/$REPO/commits/$HEAD_SHA" --jq '.commit.tree.sha' 2>/dev/null) || {
  echo "NOT-YET  could not read PR #$PR_NUMBER's head ${HEAD_SHA:0:8} (API error)"
  exit 2
}

resolve_required "$BASE_REF"

# Trees differ: the strict ruleset prevents this, so it means a bypass. The PR
# run did not test the landed tree, and the fallback is the pre-#2055 rule on
# the merge commit WITH every required check present there. Where the suites
# no longer run on push the required checks are absent: NOT-YET inside the
# grace, and RED once it has passed with no required check running or queued
# there, because the landed tree was never tested and waiting cannot change
# that. The remedy is dispatching the suites on main.
if [[ -z "$MERGE_TREE" || "$MERGE_TREE" != "$HEAD_TREE" ]]; then
  echo "NOTE     $REPO@${SHA:0:8}'s tree ${MERGE_TREE:0:8} differs from PR #$PR_NUMBER head ${HEAD_SHA:0:8}'s tree ${HEAD_TREE:0:8}:"
  echo "         the PR run did not test the landed tree, so the merge commit must carry every required check itself."
  merge_checks=$(read_checks "$SHA") || { printf '%s\n' "$merge_checks"; exit 2; }
  if [[ "$REQ_KNOWN" -eq 1 && "$REQ" != null ]]; then
    req_j=$(printf '%s' "$merge_checks" | jq -c --argjson req "$REQ" "$JUDGE")
    if [[ $(jq '.missing | length' <<<"$req_j") -gt 0 && $(jq '.pending | length' <<<"$req_j") -eq 0 &&
      $(merge_age) -ge "$GRACE_SECONDS" ]]; then
      echo "RED      the landed tree was never tested (bypass?): run the suites on main via workflow_dispatch"
      echo "         required check(s) never ran on $REPO@${SHA:0:8}: $(jq -r '.missing | join(", ")' <<<"$req_j")"
      exit 1
    fi
  fi
  judge_commit "$SHA" "$merge_checks" "$REQ"
fi

# The required set gates (b): without it the PR head cannot be judged.
if [[ "$REQ_KNOWN" -ne 1 ]]; then
  echo "NOT-YET  the required-check set for $BASE_REF could not be read, so PR #$PR_NUMBER's gate cannot be verified."
  exit 2
fi

# Trees match: the PR head's required checks are the gate, and whatever else
# still runs on the merge commit must be green or still running. cache-warm is
# dropped by read_checks. CodeQL is split off as information: the PR's own
# required CodeQL run analysed this same tree, so the merge commit's re-run is
# the default-branch baseline, not a gate. Its presence still shows the push
# workflows were created, so it ends the empty-list grace.
# shellcheck disable=SC2016 # jq program, not shell
CODEQL='def codeql: (.name | ascii_downcase | gsub("^\\s+|\\s+$"; "")) as $k | $k == "codeql" or ($k | test("^analyze \\(.*\\)$"));'
head_checks=$(read_checks "$HEAD_SHA") || { printf '%s\n' "$head_checks"; exit 2; }
head_j=$(printf '%s' "$head_checks" | jq -c --argjson req "$REQ" "$JUDGE")
merge_checks=$(read_checks "$SHA") || { printf '%s\n' "$merge_checks"; exit 2; }
merge_seen=$(jq 'length' <<<"$merge_checks")
codeql_checks=$(jq -c "$CODEQL [ .[] | select(codeql) ]" <<<"$merge_checks")
merge_checks=$(jq -c "$CODEQL [ .[] | select(codeql | not) ]" <<<"$merge_checks")
merge_j=$(printf '%s' "$merge_checks" | jq -c --argjson req null "$JUDGE")
merge_total=$(jq '.total' <<<"$merge_j")
codeql_j=$(printf '%s' "$codeql_checks" | jq -c --argjson req null "$JUDGE")
info=()
[[ $(jq '.failed | length' <<<"$codeql_j") -gt 0 ]] &&
  info+=("merge commit ${SHA:0:8}: CodeQL did not pass: $(jq -r '[ .[] | select(.status == "completed" and .conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral") | "\(.name) (\(.conclusion // "?"))" ] | join(", ")' <<<"$codeql_checks") (informational: the PR's required CodeQL run analysed this same tree; this run is the default-branch baseline)")
[[ $(jq '.pending | length' <<<"$codeql_j") -gt 0 ]] &&
  info+=("merge commit ${SHA:0:8}: CodeQL still running (informational): $(jq -r '.pending | join(", ")' <<<"$codeql_j")")
print_info() {
  [[ ${#info[@]} -gt 0 ]] || return 0
  local line
  for line in "${info[@]}"; do
    echo "INFO     $line"
  done
}

notyet=()
[[ $(jq '.missing | length' <<<"$head_j") -gt 0 ]] &&
  notyet+=("PR #$PR_NUMBER head ${HEAD_SHA:0:8}: required check(s) absent: $(jq -r '.missing | join(", ")' <<<"$head_j")")
[[ "$REQ" == null && $(jq '.total' <<<"$head_j") -eq 0 ]] &&
  notyet+=("PR #$PR_NUMBER head ${HEAD_SHA:0:8}: no checks at all")
[[ $(jq '.pending | length' <<<"$head_j") -gt 0 ]] &&
  notyet+=("PR #$PR_NUMBER head ${HEAD_SHA:0:8}: still running: $(jq -r '.pending | join(", ")' <<<"$head_j")")
[[ $(jq '.pending | length' <<<"$merge_j") -gt 0 ]] &&
  notyet+=("merge commit ${SHA:0:8}: still running: $(jq -r '.pending | join(", ")' <<<"$merge_j")")
if [[ "$merge_seen" -eq 0 ]] && [[ $(merge_age) -lt "$GRACE_SECONDS" ]] && push_may_run; then
  notyet+=("merge commit ${SHA:0:8}: no checks yet (within ${GRACE_SECONDS}s of the merge)")
fi
if [[ ${#notyet[@]} -gt 0 ]]; then
  echo "NOT-YET  $REPO@${SHA:0:8} (PR #$PR_NUMBER, same tree as its head):"
  printf '         %s\n' "${notyet[@]}"
  print_info
  exit 2
fi

head_failed=$(jq '.failed | length' <<<"$head_j")
merge_failed=$(jq '.failed | length' <<<"$merge_j")
if [[ "$head_failed" -gt 0 || "$merge_failed" -gt 0 ]]; then
  echo "RED      $REPO@${SHA:0:8} (PR #$PR_NUMBER):"
  if [[ "$head_failed" -gt 0 ]]; then
    echo "         $head_failed required check(s) failed on PR #$PR_NUMBER head ${HEAD_SHA:0:8}:"
    jq -r '.failed[]' <<<"$head_j"
  fi
  if [[ "$merge_failed" -gt 0 ]]; then
    echo "         $merge_failed check(s) failed on the merge commit ${SHA:0:8}:"
    jq -r '.failed[]' <<<"$merge_j"
  fi
  echo "         main is red and it is yours to fix immediately."
  print_info
  exit 1
fi

echo "GREEN    $REPO@${SHA:0:8} has the same tree as PR #$PR_NUMBER head ${HEAD_SHA:0:8}, whose required checks passed;"
echo "         $merge_total deciding check(s) on the merge commit completed successfully"
print_info
exit 0
