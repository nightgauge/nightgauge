#!/usr/bin/env bash
# test-measure-cache-boundary-loss.sh — smoke test for
# scripts/measure-cache-boundary-loss.sh (#3806).
#
# Runs the measurement against a committed fixture aggregate and asserts the
# boundary-classification logic: same-model vs model-switch boundaries are
# identified correctly against the authoritative DEFAULT_STAGE_MODELS mapping,
# the table shape is stable, and the pooled same-model cache-hit rate exceeds
# the model-switch rate for the fixture's data. Pure bash + jq, zero LLM tokens.
#
# Invoked from scripts/ci-local.sh. Exit 0 = pass, 1 = assertion failed.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

FIXTURE="tests/fixtures/pipeline/cache-boundary-aggregate.json"
SCRIPT="scripts/measure-cache-boundary-loss.sh"
FAILS=0

fail() { echo "  ✗ $1" >&2; FAILS=$((FAILS + 1)); }
pass() { echo "  ✓ $1"; }

echo "▶ measure-cache-boundary-loss smoke test"

if [ ! -f "$FIXTURE" ]; then
  fail "fixture missing: $FIXTURE"; exit 1
fi

OUT="$(bash "$SCRIPT" --fixture "$FIXTURE" --json)" || { fail "script exited non-zero"; exit 1; }

# 1. Exactly 5 adjacent-stage boundaries.
N="$(echo "$OUT" | jq '.boundaries | length')"
[ "$N" = "5" ] && pass "5 boundaries reported" || fail "expected 5 boundaries, got $N"

# 2. The two model-switch boundaries are pickup->planning and validate->pr-create.
SWITCHES="$(echo "$OUT" | jq -r '[.boundaries[] | select(.classification == "model-switch") | .boundary] | sort | join(",")')"
EXPECTED_SWITCHES="feature-validate -> pr-create,issue-pickup -> feature-planning"
[ "$SWITCHES" = "$EXPECTED_SWITCHES" ] \
  && pass "model-switch boundaries: $SWITCHES" \
  || fail "model-switch boundaries mismatch — got [$SWITCHES], expected [$EXPECTED_SWITCHES]"

# 3. The three same-model boundaries.
SAME="$(echo "$OUT" | jq -r '[.boundaries[] | select(.classification == "same-model") | .boundary] | sort | join(",")')"
EXPECTED_SAME="feature-dev -> feature-validate,feature-planning -> feature-dev,pr-create -> pr-merge"
[ "$SAME" = "$EXPECTED_SAME" ] \
  && pass "same-model boundaries: $SAME" \
  || fail "same-model boundaries mismatch — got [$SAME], expected [$EXPECTED_SAME]"

# 4. Cache-hit rate present (non-null) for every boundary with count > 0.
NULL_WITH_DATA="$(echo "$OUT" | jq '[.boundaries[] | select(.count > 0 and .cache_hit_rate == null)] | length')"
[ "$NULL_WITH_DATA" = "0" ] \
  && pass "all data-bearing boundaries have a cache-hit rate" \
  || fail "$NULL_WITH_DATA data-bearing boundaries reported null cache-hit rate"

# 5. Pooled same-model rate exceeds model-switch rate for this fixture.
SM_RATE="$(echo "$OUT" | jq -r '.summary.same_model.pooled_cache_hit_rate')"
MS_RATE="$(echo "$OUT" | jq -r '.summary.model_switch.pooled_cache_hit_rate')"
GT="$(awk "BEGIN{print ($SM_RATE > $MS_RATE) ? 1 : 0}")"
[ "$GT" = "1" ] \
  && pass "pooled same-model rate ($SM_RATE%) > model-switch rate ($MS_RATE%)" \
  || fail "expected same-model rate > model-switch rate, got $SM_RATE% vs $MS_RATE%"

if [ "$FAILS" -eq 0 ]; then
  echo "  ✓ measure-cache-boundary-loss smoke test passed"
  exit 0
else
  echo "  ✗ $FAILS assertion(s) failed" >&2
  exit 1
fi
