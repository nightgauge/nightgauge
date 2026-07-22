#!/usr/bin/env bash
# measure-cache-boundary-loss.sh — quantify prompt-cache loss at model-switch
# boundaries vs same-model boundaries across adjacent pipeline stages (#3806).
#
# The prompt cache is model-specific: every model ID is part of the cache key,
# so a model change across an adjacent-stage boundary forecloses any reuse of a
# cacheable prefix even when the skill body is byte-identical. This script
# classifies each adjacent-stage boundary as same-model or model-switch using
# the authoritative DEFAULT_STAGE_MODELS mapping (docs/CONFIGURATION.md §
# pipeline.stage_models — the production default), then reports the downstream
# stage's cache-read / cache-creation means and the canonical cache-hit rate
# per boundary, plus a same-model vs model-switch aggregate comparison.
#
# Zero LLM tokens — pure bash + jq over `nightgauge pipeline aggregate`.
#
# Usage:
#   scripts/measure-cache-boundary-loss.sh [--runs N] [--json]
#   scripts/measure-cache-boundary-loss.sh --fixture path/to/aggregate.json
#
#   --runs N      Window size passed to `pipeline aggregate` (default 200).
#   --json        Emit the boundary table as JSON instead of a Markdown table.
#   --fixture F   Read aggregate JSON from F instead of invoking the binary.
#                 Used by the ci-local smoke test against a committed fixture.
#
# Canonical cache-hit formula (ADR-003, identical across pipeline-audit, the
# Token Economics health dimension, and TokenEfficiencyAnalyzer):
#   cache_hit_rate = cache_read / (cache_read + cache_creation + input)
# Computed from the `mean` of each stage's token_stats. A downstream stage with
# cache_read.count == 0 (no cacheable data) is reported as null, never 0%.
set -u

RUNS=200
EMIT_JSON=false
FIXTURE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --runs) RUNS="$2"; shift 2 ;;
    --json) EMIT_JSON=true; shift ;;
    --fixture) FIXTURE="$2"; shift 2 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "ERROR: unknown argument: $1" >&2; exit 2 ;;
  esac
done

# --- Authoritative default stage->model mapping -----------------------------
# Mirrors DEFAULT_STAGE_MODELS in docs/CONFIGURATION.md § pipeline.stage_models
# and the Go heuristic in internal/intelligence/routing/router.go:selectModel.
# Lightweight stages (pickup/create/merge) route to haiku; the long-lived
# reasoning stages (planning/dev/validate) route to opus by default. Keep this
# in sync with both surfaces (see Risk R2 in the analysis doc).
stage_model() {
  case "$1" in
    issue-pickup)     echo "haiku" ;;
    feature-planning) echo "opus" ;;
    feature-dev)      echo "opus" ;;
    feature-validate) echo "opus" ;;
    pr-create)        echo "haiku" ;;
    pr-merge)         echo "haiku" ;;
    *)                echo "unknown" ;;
  esac
}

# Adjacent-stage boundaries in pipeline execution order.
BOUNDARIES=(
  "issue-pickup:feature-planning"
  "feature-planning:feature-dev"
  "feature-dev:feature-validate"
  "feature-validate:pr-create"
  "pr-create:pr-merge"
)

# --- Obtain the aggregate JSON ----------------------------------------------
if [ -n "$FIXTURE" ]; then
  if [ ! -f "$FIXTURE" ]; then
    echo "ERROR: fixture not found: $FIXTURE" >&2
    exit 2
  fi
  AGG_JSON="$(cat "$FIXTURE")"
else
  BINARY="$(command -v nightgauge 2>/dev/null || echo "")"
  if [ -z "$BINARY" ]; then
    REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
    [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
  fi
  if [ -z "$BINARY" ]; then
    echo "ERROR: nightgauge binary not found; run from a checkout with bin/nightgauge or pass --fixture" >&2
    exit 2
  fi
  AGG_JSON="$("$BINARY" pipeline aggregate --json --runs "$RUNS" 2>/dev/null)"
  if [ -z "$AGG_JSON" ]; then
    echo "ERROR: pipeline aggregate produced no output (no history?)" >&2
    exit 1
  fi
fi

if ! echo "$AGG_JSON" | jq empty 2>/dev/null; then
  echo "ERROR: aggregate output is not valid JSON" >&2
  exit 1
fi

WINDOW_FROM="$(echo "$AGG_JSON" | jq -r '.date_from // "?"')"
WINDOW_TO="$(echo "$AGG_JSON" | jq -r '.date_to // "?"')"
RUNS_ANALYZED="$(echo "$AGG_JSON" | jq -r '.runs_analyzed // 0')"

# --- Per-boundary classification + metrics ----------------------------------
# For each boundary, the "downstream" stage carries the cache prefix that a
# model switch would discard, so we attribute the boundary's cache metrics to
# the downstream stage's token_stats. classification is same-model when the
# upstream and downstream default models match, else model-switch.
ROWS_JSON="[]"
for b in "${BOUNDARIES[@]}"; do
  up="${b%%:*}"
  down="${b##*:}"
  up_model="$(stage_model "$up")"
  down_model="$(stage_model "$down")"
  if [ "$up_model" = "$down_model" ]; then
    cls="same-model"
  else
    cls="model-switch"
  fi

  row="$(echo "$AGG_JSON" | jq \
    --arg up "$up" --arg down "$down" \
    --arg upm "$up_model" --arg downm "$down_model" --arg cls "$cls" '
    (.stage_metrics[$down].token_stats // {}) as $ts
    | (($ts.cache_read // {}).count // 0) as $count
    | (($ts.cache_read // {}).mean // 0) as $cr
    | (($ts.cache_creation // {}).mean // 0) as $cc
    | (($ts.input // {}).mean // 0) as $in
    | ($cr + $cc + $in) as $den
    | {
        boundary: ($up + " -> " + $down),
        upstream: $up, downstream: $down,
        upstream_model: $upm, downstream_model: $downm,
        classification: $cls,
        count: $count,
        cache_read_mean: ($cr | floor),
        cache_creation_mean: ($cc | floor),
        input_mean: ($in | floor),
        cache_hit_rate: (if $count == 0 or $den == 0 then null
                         else (($cr / $den) * 10000 | floor) / 100 end)
      }')"
  ROWS_JSON="$(echo "$ROWS_JSON" | jq --argjson r "$row" '. + [$r]')"
done

# --- Aggregate comparison: same-model vs model-switch -----------------------
# Pooled cache-hit rate per class = sum(cache_read) / sum(cache_read +
# cache_creation + input) over boundaries with data in that class. Pooling on
# the means keeps the comparison robust to one class having more boundaries.
SUMMARY_JSON="$(echo "$ROWS_JSON" | jq '
  def pool(rows):
    (reduce rows[] as $r (0; . + $r.cache_read_mean)) as $cr
    | (reduce rows[] as $r (0; . + $r.cache_creation_mean)) as $cc
    | (reduce rows[] as $r (0; . + $r.input_mean)) as $in
    | ($cr + $cc + $in) as $den
    | { boundaries_with_data: ([rows[] | select(.count > 0)] | length),
        cache_read_sum: $cr, cache_creation_sum: $cc, input_sum: $in,
        pooled_cache_hit_rate: (if $den == 0 then null
                                else (($cr / $den) * 10000 | floor) / 100 end) };
  {
    same_model: pool([.[] | select(.classification == "same-model" and .count > 0)]),
    model_switch: pool([.[] | select(.classification == "model-switch" and .count > 0)])
  }')"

# --- Output -----------------------------------------------------------------
if [ "$EMIT_JSON" = true ]; then
  jq -n \
    --arg from "$WINDOW_FROM" --arg to "$WINDOW_TO" \
    --argjson runs "$RUNS_ANALYZED" \
    --argjson boundaries "$ROWS_JSON" \
    --argjson summary "$SUMMARY_JSON" \
    '{window: {from: $from, to: $to, runs_analyzed: $runs},
      boundaries: $boundaries, summary: $summary}'
  exit 0
fi

echo "# Cross-Model Cache-Loss Boundary Measurement"
echo ""
echo "Window: ${WINDOW_FROM} -> ${WINDOW_TO} (runs_analyzed = ${RUNS_ANALYZED})"
echo ""
echo "| Boundary | Models | Class | Downstream cache-read (mean) | cache-creation (mean) | Cache-hit rate | Count |"
echo "| -------- | ------ | ----- | ---------------------------- | --------------------- | -------------- | ----- |"
echo "$ROWS_JSON" | jq -r '.[] |
  "| \(.boundary) | \(.upstream_model)->\(.downstream_model) | \(.classification) | \(.cache_read_mean) | \(.cache_creation_mean) | \(if .cache_hit_rate == null then "null" else "\(.cache_hit_rate)%" end) | \(.count) |"'
echo ""
echo "## Same-model vs model-switch (pooled)"
echo ""
echo "| Class | Boundaries w/ data | Pooled cache-hit rate |"
echo "| ----- | ------------------ | --------------------- |"
echo "$SUMMARY_JSON" | jq -r '
  "| same-model | \(.same_model.boundaries_with_data) | \(if .same_model.pooled_cache_hit_rate == null then "null" else "\(.same_model.pooled_cache_hit_rate)%" end) |",
  "| model-switch | \(.model_switch.boundaries_with_data) | \(if .model_switch.pooled_cache_hit_rate == null then "null" else "\(.model_switch.pooled_cache_hit_rate)%" end) |"'
