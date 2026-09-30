### Run Reflection (memory across runs)

**PURPOSE**: Cadence skills (those run weekly/periodically) should remember their
own prior runs so each execution reports **deltas, not a full re-dump**. Without
memory, every run re-surfaces the same findings and can't say "what changed since
last time." This mirrors `nightgauge-release-watch`'s `last-seen-<provider>.json`.

> Set `RUN_LOG_CLASS`, `RUN_LOG_NAME` and
> `RUN_LOG="$(nightgauge layout path "$RUN_LOG_CLASS" "$RUN_LOG_NAME")"`
> **before** this section. The class is a per-clone class directory
> (`pipeline`, `plans`, `retros`, `logs`) or this checkout's directory
> (`checkout`), e.g. `RUN_LOG_CLASS="checkout"` with
> `RUN_LOG_NAME="doc-snapshots/runs.jsonl"`. Never `${CLAUDE_PLUGIN_DATA}`
> (single source of truth). Both directories are inside the git directory, so
> the log is read by path and appended through `nightgauge layout append`,
> never a redirect.

#### Step 1: Load the previous run

```bash
if [ -f "$RUN_LOG" ]; then
  PREV=$(tail -n 1 "$RUN_LOG")
  PREV_TS=$(printf '%s\n' "$PREV" | jq -r '.ts // "never"')
  echo "Last run: ${PREV_TS}"
else
  PREV='{}'
  PREV_TS="never"
  echo "No prior run — this is the first reflection (full report)."
fi
```

#### Step 2: Report deltas, not the whole world

Compare this run's key signals against `PREV` and lead the output with the
**delta**: what is newly appeared, newly resolved, or newly changed since
`PREV_TS`. Fall back to a full report only when `PREV_TS` is `never`. Examples of
delta signals by skill: new vs. resolved stale issues (backlog-groom), new /
changed / removed doc pages (docs-watch), friction-rate and health-trend
movement (continuous-improvement).

#### Step 3: Append this run (skip on `--dry-run`)

```bash
if [[ "$*" == *"--dry-run"* ]]; then
  echo "Dry-run: not appending to $RUN_LOG"
else
  NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  RECORD=$(jq -nc --arg ts "$NOW" --arg skill "$SKILL_NAME" \
        --argjson counts "${RUN_COUNTS:-{}}" --arg summary "${RUN_SUMMARY:-}" \
        '{ts:$ts, skill:$skill, counts:$counts, summary:$summary}')
  printf '%s\n' "$RECORD" | nightgauge layout append "$RUN_LOG_CLASS" "$RUN_LOG_NAME" >/dev/null
  echo "Appended run record to $RUN_LOG"
fi
```

**Schema** (one JSON object per line): `{ ts, skill, counts:{…}, summary }`.
`counts` holds the skill's headline metrics so the next run can diff them.
