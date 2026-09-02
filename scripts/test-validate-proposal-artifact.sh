#!/usr/bin/env bash
# Regression tests for the proposal-artifact validator
# (`scripts/validate-proposal-artifact.mjs`) — the gate between the read-only
# model job and the write job in release-watchdog.yml and
# continuous-improvement.yml.
#
# A gate's entire value is that it FAILS CLOSED. Each case here plants one
# shape the validator exists to reject and asserts the exit code goes red;
# the valid fixture proves the gate is not simply "always 1". Every case runs
# the real CLI as a subprocess so the exit codes the workflow branches on are
# what is asserted, not an in-process return value.
#
# Run: bash scripts/test-validate-proposal-artifact.sh
# Also run by scripts/ci-local.sh.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

GATE="scripts/validate-proposal-artifact.mjs"
APPLY="scripts/apply-proposal-artifact.sh"
PASS=0
FAIL=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

expect() {
  local name="$1" want="$2" got="$3"
  if [ "$got" -eq "$want" ]; then
    PASS=$((PASS + 1))
    echo "PASS: $name"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $name (exit $got, want $want)"
  fi
}

# Build a fixture from a jq program applied to the valid baseline, so each
# case states exactly the one thing that differs from "valid".
VALID='{
  "schema": 1,
  "kind": "release-watch",
  "proposals": [
    {
      "title": "Integrate hooks API change from 2.1.0",
      "body": "## Summary\n\nThe release adds a PostToolUse field.\n",
      "labels": ["source:auto-discovery", "claude-code-release", "type:feature"]
    }
  ]
}'
fixture() {
  # fixture <name> <jq-filter>
  printf '%s' "$VALID" | jq "$2" > "$TMP/$1.json"
  echo "$TMP/$1.json"
}
check() {
  # check <name> <want-exit> <file> [extra args...]
  local name="$1" want="$2" file="$3"
  shift 3
  node "$GATE" --kind release-watch "$@" "$file" > /dev/null 2>&1
  expect "$name" "$want" $?
}

# Case 1: the baseline is accepted — proves the gate can go green at all.
check "valid artifact passes" 0 "$(fixture valid .)"

# Case 2: an empty proposals list is a valid "ran, found nothing" artifact.
check "empty proposals list passes" 0 "$(fixture empty '.proposals = []')"

# Case 3: a missing required key.
check "missing top-level key fails" 1 "$(fixture nokind 'del(.kind)')"
check "missing proposal key fails" 1 "$(fixture nobody 'del(.proposals[0].body)')"

# Case 4: an extra key the apply job does not expect.
check "unexpected key fails" 1 "$(fixture extra '.proposals[0].assignee = "x"')"

# Case 5: wrong schema version and wrong kind.
check "wrong schema version fails" 1 "$(fixture schema2 '.schema = 2')"
check "kind mismatch fails" 1 "$(fixture wrongkind '.kind = "continuous-improvement"')"

# Case 6: oversized body and title.
check "oversized body fails" 1 "$(fixture bigbody '.proposals[0].body = ("x" * 20001)')"
check "oversized title fails" 1 "$(fixture bigtitle '.proposals[0].title = ("x" * 201)')"

# Case 7: a label outside the allowlist, and a missing provenance label.
check "disallowed label fails" 1 "$(fixture badlabel '.proposals[0].labels += ["auto-process"]')"
check "missing required label fails" 1 \
  "$(fixture nosource '.proposals[0].labels = ["claude-code-release"]')"

# Case 8: too many proposals.
check "too many proposals fails" 1 \
  "$(fixture toomany '.proposals = [range(11) | {title: ("p" + tostring), body: "b", labels: ["source:auto-discovery"]}]')"
check "proposal cap is configurable" 1 "$(fixture capped .)" --max-proposals 0

# Case 9: control characters — a newline in a title, a NUL in a body.
check "newline in title fails" 1 "$(fixture nltitle '.proposals[0].title = "a\nb"')"
check "control character in body fails" 1 "$(fixture nulbody '.proposals[0].body = "a\u0001b"')"

# Case 10: not JSON at all, and not an object.
printf 'not json' > "$TMP/notjson.json"
check "malformed JSON fails" 1 "$TMP/notjson.json"
printf '[]' > "$TMP/array.json"
check "non-object top level fails" 1 "$TMP/array.json"

# Case 11: usage errors are distinguishable from rejections.
node "$GATE" "$TMP/valid.json" > /dev/null 2>&1
expect "missing --kind is a usage error" 2 $?

# Case 12: the apply script refuses an invalid artifact before touching gh.
# With no GH_TOKEN and a rejected file the only way this exits 1 is the
# validator refusing; a script that filed first would have failed on gh
# instead, which this case cannot tell apart — so it is asserted on the
# VALID-but-empty fixture too, where the script must exit 0 without gh.
env -u GH_TOKEN -u GITHUB_TOKEN bash "$APPLY" --kind release-watch \
  --file "$TMP/badlabel.json" --record "$TMP/record.json" > /dev/null 2>&1
expect "apply refuses a rejected artifact" 1 $?
env -u GH_TOKEN -u GITHUB_TOKEN bash "$APPLY" --kind release-watch \
  --file "$TMP/empty.json" --record "$TMP/record.json" > /dev/null 2>&1
expect "apply accepts an empty artifact without a forge token" 0 $?

echo
echo "validate-proposal-artifact: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
