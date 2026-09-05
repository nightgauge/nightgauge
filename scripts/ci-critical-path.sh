#!/usr/bin/env bash
# Rank a CI run's jobs and steps by wall clock, so "CI is slow" stays a
# question with an answer instead of a feeling.
#
# Usage:
#   scripts/ci-critical-path.sh <run-id>        # one workflow run
#   scripts/ci-critical-path.sh --pr <number>   # every workflow run on that PR's head commit
#   scripts/ci-critical-path.sh --sha <sha>     # every workflow run on that commit
#
# Options:
#   --repo <owner/repo>   default: this checkout's origin remote
#   --steps <n>           how many steps to list, longest first (default 15)
#
# Why --pr / --sha exist alongside <run-id>: the PR gate is not one workflow.
# `ci.yml`, `lint.yml`, CodeQL and the rest are SEPARATE workflow runs on the
# same commit, so a single run id ranks one workflow's jobs and cannot see the
# job that actually holds the critical path. The commit-scoped modes aggregate
# every run on the head SHA, which is the set a pull request waits on.
#
# REST only, deliberately: `gh api` against `/actions/...` costs 1 point per
# call on the REST budget the API ledger tracks, and the jobs endpoint already
# embeds each job's `steps[]` — so a full ranking is a handful of cheap calls
# with no GraphQL point cost at all. Do not "improve" this into a GraphQL query.
#
# WALL CLOCK, NOT BILLED TIME. A job's duration here is completed_at minus
# started_at, which includes queueing inside the job (image pulls, cache
# restores) and excludes time the job spent waiting for a runner. The critical
# path is the longest job; shortening anything else changes nothing.

set -uo pipefail

REPO=""
STEPS=15
MODE=""
ARG=""

# EVERY VALUE-TAKING OPTION VALIDATES ITS ARITY BEFORE SHIFTING. `shift 2`
# with only one argument left shifts NOTHING and returns non-zero, so `$1`
# stays the flag and the loop spins forever: `--pr` with no number burned 100%
# of a core, silently, until killed. A hang is the worst possible failure for a
# tool whose whole job is answering "why is CI slow?" on demand, and AGENTS.md
# § Reap every background process already carries the scar of an unbounded
# spin loop. `${2:-}` was not a fix — it defaulted the VALUE while leaving the
# shift short.
need_value() { # $1: flag name, $2: how many args are left including the flag
  [ "$2" -ge 2 ] || { echo "$1 needs a value" >&2; exit 2; }
}

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) need_value --repo $#; REPO="$2"; shift 2 ;;
    --steps) need_value --steps $#; STEPS="$2"; shift 2 ;;
    --pr) need_value --pr $#; MODE="pr"; ARG="$2"; shift 2 ;;
    --sha) need_value --sha $#; MODE="sha"; ARG="$2"; shift 2 ;;
    -h|--help)
      # Terminate on the first non-comment line instead of a hard-coded range:
      # `2,30p` overshot the header by one and printed `set -uo pipefail` as if
      # it were help text, and any edit to the comment block moves the boundary
      # again.
      sed -n '2,${/^#/!q; s/^# \{0,1\}//; p;}' "$0"
      exit 0 ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) MODE="run"; ARG="$1"; shift ;;
  esac
done

# `head -n "$STEPS"` would fail late and cryptically on a non-number, after
# every API call has already been paid for.
case "$STEPS" in
  ''|*[!0-9]*) echo "--steps needs a non-negative integer, got: $STEPS" >&2; exit 2 ;;
esac

if [ -z "$MODE" ] || [ -z "$ARG" ]; then
  echo "usage: scripts/ci-critical-path.sh <run-id> | --pr <number> | --sha <sha>" >&2
  exit 2
fi

if [ -z "$REPO" ]; then
  origin=$(git config --get remote.origin.url 2>/dev/null || true)
  if [ -z "$origin" ]; then
    echo "no --repo given and this directory has no origin remote" >&2
    exit 2
  fi
  # Pure parameter expansion for the same reason post-merge-check.sh uses it:
  # BSD sed rejects the lazy-quantifier regex the obvious one-liner wants.
  REPO="${origin%.git}"
  REPO="${REPO%/}"
  _name="${REPO##*/}"
  REPO="${REPO%/"$_name"}"
  _owner="${REPO##*[:/]}"
  REPO="$_owner/$_name"
fi

# ── Resolve the run ids to rank ─────────────────────────────────────────────
SHA=""
case "$MODE" in
  run) RUN_IDS="$ARG" ;;
  pr)
    SHA=$(gh api "repos/$REPO/pulls/$ARG" --jq '.head.sha' 2>/dev/null) || SHA=""
    if [ -z "$SHA" ]; then
      echo "could not read the head sha of $REPO#$ARG" >&2
      exit 2
    fi
    ;;
  sha) SHA="$ARG" ;;
esac

# `head_sha=` matches on the FULL 40-character sha and returns an empty list
# for an abbreviated one — a silent "no runs found" for a sha that has plenty.
# Expand it through the commits endpoint (REST, 1 point) before querying.
if [ -n "$SHA" ] && [ "${#SHA}" -ne 40 ]; then
  full=$(gh api "repos/$REPO/commits/$SHA" --jq '.sha' 2>/dev/null) || full=""
  if [ -z "$full" ]; then
    echo "could not resolve $SHA to a commit in $REPO" >&2
    exit 2
  fi
  SHA="$full"
fi

if [ -n "$SHA" ]; then
  RUN_IDS=$(gh api "repos/$REPO/actions/runs?head_sha=$SHA&per_page=100" \
    --jq '.workflow_runs[].id' 2>/dev/null)
  if [ -z "$RUN_IDS" ]; then
    echo "no workflow runs found for $REPO@${SHA:0:8}" >&2
    exit 2
  fi
fi

# ── Collect every job (steps included) from every run ───────────────────────
# One line per job:  <seconds> TAB <conclusion> TAB <job name>
# One line per step: <seconds> TAB <job name> TAB <step name>
#
# A job or step still running has `completed_at: null`; it is dropped rather
# than measured against `now`, and the count of what was dropped is reported —
# a ranking that silently omits the unfinished job is exactly the "absence of
# failures is not the presence of successes" trap, one domain over.
JOB_LINES=""
STEP_LINES=""
RUNNING=0
HEADER=""

for id in $RUN_IDS; do
  meta=$(gh api "repos/$REPO/actions/runs/$id" \
    --jq '[.name, .head_branch, (.head_sha[0:8]), .event, (.status + "/" + (.conclusion // "?"))] | @tsv' 2>/dev/null) || meta=""
  [ -n "$meta" ] || { echo "could not read run $id in $REPO" >&2; exit 2; }
  HEADER="$HEADER$id	$meta
"
  jobs=$(gh api --paginate "repos/$REPO/actions/runs/$id/jobs?per_page=100" \
    --jq '.jobs[]' 2>/dev/null) || {
      echo "could not read jobs for run $id in $REPO" >&2
      exit 2
    }
  RUNNING=$((RUNNING + $(printf '%s' "$jobs" |
    jq -s '[.[] | select(.completed_at == null)] | length')))
  JOB_LINES="$JOB_LINES$(printf '%s' "$jobs" | jq -rs '
    .[] | select(.completed_at != null and .started_at != null)
    | [((.completed_at|fromdate) - (.started_at|fromdate)), (.conclusion // "?"), .name]
    | @tsv')
"
  STEP_LINES="$STEP_LINES$(printf '%s' "$jobs" | jq -rs '
    .[] as $j | $j.steps[]?
    | select(.completed_at != null and .started_at != null)
    | [((.completed_at|fromdate) - (.started_at|fromdate)), $j.name, .name]
    | @tsv')
"
done

JOB_LINES=$(printf '%s' "$JOB_LINES" | grep -v '^$' | LC_ALL=C sort -t'	' -k1,1rn)
STEP_LINES=$(printf '%s' "$STEP_LINES" | grep -v '^$' | LC_ALL=C sort -t'	' -k1,1rn)

if [ -z "$JOB_LINES" ]; then
  echo "no completed jobs to rank yet — the run has not produced any." >&2
  exit 2
fi

fmt() { # seconds -> 6m49s
  local s="$1"
  if [ "$s" -ge 60 ]; then printf '%dm%02ds' $((s / 60)) $((s % 60));
  else printf '%ds' "$s"; fi
}

printf '%s' "$HEADER" | while IFS=$'\t' read -r id name branch sha event status; do
  [ -n "$id" ] || continue
  printf 'run %-12s %-24s %s @%s (%s, %s)\n' "$id" "$name" "$branch" "$sha" "$event" "$status"
done
echo ""

CRITICAL=$(printf '%s\n' "$JOB_LINES" | head -1 | cut -f1)
printf 'critical path: %s  (the longest job; shortening anything else changes nothing)\n' \
  "$(fmt "$CRITICAL")"
if [ "$RUNNING" -gt 0 ]; then
  printf '%s job(s) had not finished and are NOT ranked below.\n' "$RUNNING"
fi
echo ""

echo "JOBS, longest first"
printf '%s\n' "$JOB_LINES" | while IFS=$'\t' read -r secs concl name; do
  printf '  %7s  %-12s %s\n' "$(fmt "$secs")" "$concl" "$name"
done

echo ""
printf 'STEPS, longest first (top %s; share is of the critical path, %s)\n' \
  "$STEPS" "$(fmt "$CRITICAL")"

# The job column is sized to the longest job name actually being printed, not
# to a guess. A fixed `%-22s` silently degrades to no column at all on the real
# names this repo has — `Analyze (javascript-typescript)` and `publication
# boundary (allowlist, fail-closed)` both overflow it, and the step name then
# runs straight into the job name while every shorter row still aligns at 22.
# Alignment is the whole readability of a ranking.
TOP_STEPS=$(printf '%s\n' "$STEP_LINES" | head -n "$STEPS")
JOBW=$(printf '%s\n' "$TOP_STEPS" | cut -f2 |
  LC_ALL=C awk '{ if (length($0) > m) m = length($0) } END { print m + 0 }')
[ "$JOBW" -gt 0 ] || JOBW=22

printf '%s\n' "$TOP_STEPS" |
  while IFS=$'\t' read -r secs job name; do
    printf '  %7s  %3s%%  %-*s %s\n' \
      "$(fmt "$secs")" "$((secs * 100 / (CRITICAL > 0 ? CRITICAL : 1)))" \
      "$JOBW" "$job" "$name"
  done
