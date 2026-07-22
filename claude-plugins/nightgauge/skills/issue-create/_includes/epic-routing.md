# Phases 2.4–2.6: Epic Routing & Decomposition — Procedural Detail

Detail bodies for Phase 2.4 (Multi-Repo Sub-Issue Routing), Phase 2.5 (Parallel Decomposition), and Phase 2.6 (Dependency Analysis) of the `nightgauge-issue-create` skill.

## Contents

- [Phase 2.4: Multi-Repo Sub-Issue Routing](#phase-24-multi-repo-sub-issue-routing)
- [Phase 2.5: Parallel Decomposition](#phase-25-parallel-decomposition)
- [Phase 2.6: Dependency Analysis](#phase-26-dependency-analysis)

## Phase 2.4: Multi-Repo Sub-Issue Routing

**Gate**: Runs UNCONDITIONALLY for any epic with sub-issues. Skipped for
standalone (non-epic) issues. Cannot be bypassed without an explicit
`--no-route` operator override.

**Why this phase exists**: The workspace declares a routing config in
`.vscode/nightgauge-workspace.yaml` mapping content keywords to target
repositories. Pre-#3232, the skill ignored this config and dropped every
sub-issue into the primary repo regardless of whether its content belonged
elsewhere — leaving Angular/dashboard work in the pipeline repo and Flutter
work in the wrong project. This phase makes routing deterministic, visible,
and auditable BEFORE any GitHub mutation.

#### Step 2.4.1: Load workspace routing config

```bash
WORKSPACE_YAML=".vscode/nightgauge-workspace.yaml"
if [ ! -f "$WORKSPACE_YAML" ]; then
  echo "Single-repo workspace (no $WORKSPACE_YAML found) — skipping routing"
  ROUTING_ENABLED=false
else
  ROUTING_ENABLED=true
fi
```

The yaml shape this phase consumes:

```yaml
repositories:
  - name: nightgauge
    path: .
    project_number: 1 # NEW (#3232) — explicit project mapping
  - name: acme-dashboard
    path: ../acme-dashboard
    project_number: 4
  # ...

routing:
  default_repository: nightgauge
  patterns:
    - id: web
      keywords: [angular, web, dashboard, ngrx, signal, scss, material]
      preferred_repo: acme-dashboard
    # ...
```

If a repository entry is missing `project_number`, the skill falls back to
`gh project list --owner <owner> --format json` and matches by display name
(`<repo-display-name>` ↔ project title). Cache the result in memory for the
duration of the skill run.

#### Step 2.4.2: Build the routing manifest

For each sub-issue planned in Phase 2 (and refined in 2.5/2.6 if those ran),
score its title + body against every routing pattern's keywords:

```
score(sub-issue, pattern) = count of distinct pattern.keywords found in (title + body), case-insensitive
```

The pattern with the highest non-zero score wins. Ties broken by pattern order
in the yaml (top-most wins). If no pattern scores > 0, the sub-issue routes to
`routing.default_repository`.

Emit a manifest like:

```json
{
  "epic_repo": "nightgauge",
  "epic_number": 3211,
  "sub_issues": [
    {
      "title": "Wire performance mode through every adapter",
      "target_repo": "nightgauge",
      "target_project": 1,
      "matched_pattern": null,
      "rationale": "no keyword match → default repo"
    },
    {
      "title": "Angular dashboard mode filter + aggregations",
      "target_repo": "acme-dashboard",
      "target_project": 4,
      "matched_pattern": "web",
      "rationale": "matched keywords: angular, dashboard"
    },
    {
      "title": "Per-stage adapter visualization in dashboard",
      "target_repo": "acme-dashboard",
      "target_project": 4,
      "matched_pattern": "web",
      "rationale": "matched keywords: dashboard, adapter, visualization"
    }
  ]
}
```

#### Step 2.4.3: Hard-gate routing decisions

Display the manifest before any issue creation. The skill MUST NOT create any
issue until one of the following is true:

1. The operator approves the manifest (interactive mode).
2. `--auto-route` flag was passed (non-interactive mode treats the manifest as
   authoritative).
3. `--no-route` flag was passed AND the operator confirmed they understand all
   sub-issues will land in the default repo.

**Hard gate**: If ANY sub-issue's content matches a non-default `preferred_repo`
AND the manifest does not route it accordingly, the skill MUST refuse to
proceed with a fatal error:

```
ERROR: routing-skip-detected
  Sub-issue: "Angular dashboard mode filter + aggregations"
  Matched pattern: web (keywords: angular, dashboard, ...)
  Expected target: acme-dashboard (project 4)
  Manifest target: nightgauge (project 1)

This is the #3232 footgun. Either:
  - Run with --auto-route to apply routing
  - Run with --no-route AND --confirm-default-repo to override
  - Edit the workspace yaml routing patterns if matching is wrong

Refusing to create issues until resolved.
```

#### Step 2.4.4: Persist the manifest

Write the routing manifest to `.nightgauge/pipeline/issue-create-routing-<epic-number>.json`
(temp file; deleted after Phase 4.8 audit succeeds). Phase 3 reads this file
to dispatch `gh issue create` with the correct `--repo`. Phase 4 reads it to
pass `--repo` and `--project` to `nightgauge project add`. Phase 4.8
re-reads it to perform the post-creation audit.

This separation means Phase 3 and Phase 4 never re-derive routing — they
consume the locked-in manifest from this phase. No drift possible.

## Phase 2.5: Parallel Decomposition (Agent Teams)

**Gate**: Check `agent_teams.enabled` from resolved config. If disabled, skip to
Phase 2.6.

When creating an epic with sub-issues and agent teams are enabled, analyze
sub-issues for parallel execution potential:

1. After building sub-issue content (Phase 2), extract file ownership hints from
   technical notes and acceptance criteria for each sub-issue.
2. Run dependency detection heuristics (`detectDependencies` from
   `@nightgauge/sdk`):
   - Shared file paths → dependency edge
   - Import chain references → dependency edge
   - Sequential keywords ("after", "requires", "depends on") → explicit
     dependency
3. Calculate execution waves via topological sort (`calculateWaves` from
   `@nightgauge/sdk`).
4. Assign `estimated_complexity` based on size heuristics (line count of
   file_ownership, keyword density):
   - 0-2 files → low
   - 3-5 files → medium
   - 6+ files → high
5. Assign `teammate_model_suggestion` using complexity mapping:
   - low → haiku, medium → sonnet, high → opus
6. Run guard rails (from `@nightgauge/sdk` agent-teams
   module):
   - `validateWaveExecution()` — skip teams if too few independent issues or
     wave depth exceeds threshold
   - `detectFileConflicts()` — flag blocking file ownership conflicts (error
     severity) and directory overlaps (warning severity)
7. Present wave-based tree preview to user:
   ```
   Wave 1 (parallel):
     #1 Add config schema [sonnet] ✓ parallel
     #2 Add validation utils [haiku] ✓ parallel
   Wave 2 (after wave 1):
     #3 Integrate config into pipeline [opus] → depends on #1
   ```
8. User can override:
   - Mark issues as sequential that were auto-detected as parallel
   - Mark issues as parallel that were auto-detected as sequential
   - Change model suggestions
9. Embed dependency metadata as HTML-comment-wrapped YAML in each sub-issue
   body, passed via `--metadata` flag on `nightgauge issue create-sub`:

```yaml
<!-- nightgauge:dependency-metadata
parallel_eligible: true
depends_on: []
file_ownership:
  - packages/nightgauge-sdk/src/agent-teams/types.ts
estimated_complexity: medium
teammate_model_suggestion: sonnet
-->
```

## Phase 2.6: Dependency Analysis (Always for Epics)

**Gate**: Only runs when creating an epic with 2+ sub-issues AND Phase 2.5 was
skipped (agent_teams disabled). If Phase 2.5 ran, its results are used instead.

When agent teams are disabled, use the Go binary for deterministic dependency
analysis. After creating all sub-issues (Phase 3), call:

```bash
nightgauge epic plan-waves \
  --sub-issues <comma-separated-issue-numbers> \
  --json
```

Parse the JSON output to extract wave assignments (`waves[].waveIndex`,
`waves[].issues[].number`) **and the `conflicts` array**, and use these results
directly for:

- Phase 3 body annotation (`--wave <N>` flag on `issue create-sub`)
- Phase 3.5 blocker setup (issue ordering from wave dependencies **and from every
  injected file-overlap serialization**)

The binary fetches each listed issue, reads its native `blockedBy` relationships
from GitHub, extracts each sub-issue's predicted target files from its body, and
runs a topological sort via Kahn's algorithm. This is deterministic and uses no
AI tokens.

**Deterministic file-overlap serialization (always-on).** Before computing
waves, the binary checks every pair of would-be same-wave sub-issues for a
shared top-level EXACT target file. When two issues would own the same file in
parallel — the #143/#144 collision class — it **auto-injects a `blockedBy` edge**
(later issue number depends on earlier) so they land in adjacent waves, and emits
an `error`-severity entry in `conflicts` (`{ "path": ..., "issues": [A, B],
"severity": "error" }`). Phase 3.5 MUST apply **every** such edge via
`nightgauge issue add-blocked-by <B> <A>` — this is computed, not author
judgment. Directory-only overlaps (different files, same dir) are reported as
`warning` only and remain parallel.

**Fallback (when issues not yet created):** If you need provisional wave
annotation before the sub-issues exist (so Phase 3 can pass `--wave <N>`),
perform lightweight prose-based ordering: scan titles for sequential keywords
("after", "requires", "depends on", "needs", "blocked by", "assumes") and note
shared target files from the planned technical notes. This is provisional only —
**re-run `plan-waves` after the sub-issues are created (Phase 3) and treat its
`conflicts` output as authoritative**, applying every injected file-overlap edge
in Phase 3.5. The binary's file-overlap serialization, not this fallback, is what
prevents same-file collisions.
