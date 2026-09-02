#!/usr/bin/env bash
#
# apply-proposal-artifact.sh — file GitHub issues from a VALIDATED proposal
# artifact and record what happened in the discovery run record.
#
# This is the model-free half of release-watchdog.yml and
# continuous-improvement.yml. The model job has no forge token and writes
# only the artifact; this script runs in the job that does hold `issues:
# write`, after scripts/validate-proposal-artifact.mjs has accepted the file.
# It never re-derives anything from the proposal text beyond passing title,
# body and labels straight to `gh issue create` — the same command the skills
# documented — so the only thing a crafted release note can do is propose an
# issue that a human then reads.
#
# Usage:
#   scripts/apply-proposal-artifact.sh --kind <release-watch|continuous-improvement> \
#       --file proposals.json --record .nightgauge/.../record.json [--dry-run]
#
# The record's `issues_created` (release-watch) or `proposals_created`
# (continuous-improvement) list gains one {number,title,url} per filed issue,
# and release-watch's `issues_deduped` gains every title skipped because an
# issue with that exact title already exists. Requires gh, jq and GH_TOKEN.
#
# Exit codes:
#   0  every proposal filed or deduplicated (or --dry-run)
#   1  a proposal could not be filed, or bad arguments

set -euo pipefail

KIND=""
FILE=""
RECORD=""
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --kind)
      KIND="$2"
      shift 2
      ;;
    --file)
      FILE="$2"
      shift 2
      ;;
    --record)
      RECORD="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h | --help)
      sed -n '2,25p' "$0"
      exit 0
      ;;
    *)
      echo "apply-proposal-artifact: unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

case "$KIND" in
  release-watch)
    CREATED_KEY="issues_created"
    DEDUPED_KEY="issues_deduped"
    ;;
  continuous-improvement)
    CREATED_KEY="proposals_created"
    DEDUPED_KEY=""
    ;;
  *)
    echo "apply-proposal-artifact: --kind must be release-watch or continuous-improvement" >&2
    exit 1
    ;;
esac
if [ -z "$FILE" ] || [ -z "$RECORD" ]; then
  echo "apply-proposal-artifact: --file and --record are required" >&2
  exit 1
fi

# The validator is the gate; this script refuses to run on a file that did not
# pass it, so a workflow that forgets the validation step still cannot file
# anything from an unchecked artifact.
node scripts/validate-proposal-artifact.mjs --kind "$KIND" "$FILE"

COUNT="$(jq '.proposals | length' "$FILE")"
echo "apply-proposal-artifact: $COUNT proposal(s) in $FILE"
if [ "$COUNT" -eq 0 ]; then
  exit 0
fi

record_append() {
  # record_append <key> <json-value>
  local key="$1" value="$2"
  mkdir -p "$(dirname "$RECORD")"
  if [ ! -f "$RECORD" ]; then
    echo '{}' > "$RECORD"
  fi
  jq --arg key "$key" --argjson value "$value" \
    '.[$key] = ((.[$key] // []) + [$value])' "$RECORD" > "$RECORD.tmp"
  mv "$RECORD.tmp" "$RECORD"
}

# Labels must exist before `gh issue create --label` will accept them. The
# allowlist lives in the validator, so anything reaching here is a known name.
ensure_label() {
  local label="$1"
  if ! gh label list --limit 200 --json name --jq '.[].name' | grep -Fxq -- "$label"; then
    gh label create "$label" --description "Auto-created by nightgauge $KIND" --color "0e8a16"
  fi
}

FAILED=0
i=0
while [ "$i" -lt "$COUNT" ]; do
  TITLE="$(jq -r ".proposals[$i].title" "$FILE")"
  LABELS="$(jq -r ".proposals[$i].labels | join(\",\")" "$FILE")"
  BODY_FILE="$(mktemp)"
  jq -r ".proposals[$i].body" "$FILE" > "$BODY_FILE"

  if [ "$DRY_RUN" -eq 1 ]; then
    echo "DRY RUN: would create '$TITLE' [$LABELS]"
    rm -f "$BODY_FILE"
    i=$((i + 1))
    continue
  fi

  # Dedup by exact title across open AND closed issues: a release the loop
  # already filed against must not come back as a second issue, and a closed
  # one is a decision the maintainer already made.
  EXISTING="$(gh issue list --state all --limit 200 --search "in:title \"$TITLE\"" --json title,url \
    | jq -r --arg t "$TITLE" '.[] | select(.title == $t) | .url' | head -n1)"
  if [ -n "$EXISTING" ]; then
    echo "DEDUP: '$TITLE' already exists at $EXISTING"
    if [ -n "$DEDUPED_KEY" ]; then
      record_append "$DEDUPED_KEY" "$(jq -n --arg t "$TITLE" '$t')"
    fi
    rm -f "$BODY_FILE"
    i=$((i + 1))
    continue
  fi

  IFS=',' read -r -a LABEL_ARRAY <<< "$LABELS"
  for label in "${LABEL_ARRAY[@]}"; do
    ensure_label "$label"
  done

  if URL="$(gh issue create --title "$TITLE" --body-file "$BODY_FILE" --label "$LABELS")"; then
    NUMBER="${URL##*/}"
    echo "CREATED: #$NUMBER $URL"
    record_append "$CREATED_KEY" \
      "$(jq -n --argjson n "$NUMBER" --arg t "$TITLE" --arg u "$URL" '{number: $n, title: $t, url: $u}')"
  else
    echo "FAILED: could not create '$TITLE'" >&2
    FAILED=1
  fi
  rm -f "$BODY_FILE"
  i=$((i + 1))
done

exit "$FAILED"
