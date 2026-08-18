# TC: Authoring Contract Round Trip (#711)

Pins the invariant that a body authored per `nightgauge-issue-create`'s Phase 2
rules passes this skill's Phase 5 with **zero** `MISSING_REQUIRED_HEADING`
findings.

`issue-create` Phase 6 runs `issue-audit` as its own terminal gate. Until #711
the two contracts shared no heading — `issue-create` prescribed
`Problem statement` / `Business/user value` / `Acceptance criteria` /
`Technical notes`, this skill required `Summary` plus per-type sections with
different casing — so Phase 5 fired on every issue the pipeline authored (15
findings across the 5 issues of epic #702). No fixture pinned the round trip,
which is why the contradiction survived to ship.

The mechanical half of this is enforced by
`scripts/check-issue-body-contract.py` (run in CI, self-tested by
`scripts/test-issue-body-contract.sh`). This fixture states the behavioral
contract that gate defends.

## Setup Assumptions

- Skill invoked with `--manifest <path>` (strict mode).
- Every body below was authored by following the per-type table in
  `skills/nightgauge-issue-create/_includes/environment-and-content.md`
  Phase 2 item 3, verbatim and with no extra editing pass.

## Synthetic Manifest

```json
{
  "schema_version": "1.0",
  "created_at": "2026-08-18T12:00:00.000Z",
  "created_by_skill": "nightgauge-issue-create",
  "project_number": 3,
  "entries": [
    {
      "repo": "nightgauge/nightgauge",
      "number": 4700,
      "type": "feature",
      "priority": "P2",
      "size": "M",
      "status": "Ready",
      "body_sections": ["Summary", "Acceptance Criteria"]
    },
    {
      "repo": "nightgauge/nightgauge",
      "number": 4701,
      "type": "bug",
      "priority": "P1",
      "size": "S",
      "status": "Ready",
      "body_sections": ["Summary", "Steps to Reproduce", "Expected", "Actual"]
    },
    {
      "repo": "nightgauge/nightgauge",
      "number": 4702,
      "type": "epic",
      "priority": "P1",
      "size": "L",
      "status": "Ready",
      "body_sections": ["Summary", "Sub-Issues", "Acceptance Criteria"]
    }
  ]
}
```

## Synthetic GitHub State

### `#4700` — `type:feature`

```markdown
## Summary

The scheduler has no way to report which repositories it is watching.

## Acceptance Criteria

- [ ] `nightgauge workspace repo list` prints every configured repository

## Business/user value

Operators currently read the YAML by hand to answer this.
```

### `#4701` — `type:bug`

```markdown
## Summary

Adding a repository to the manifest does not take effect until VSCode restarts.

## Steps to Reproduce

1. Add a repository to `.vscode/nightgauge-workspace.yaml`.
2. Open the Nightgauge tree view.

## Expected

The new repository appears in the tree.

## Actual

The tree still shows the previous set.
```

### `#4702` — `type:epic`

```markdown
## Summary

Workspace manifest management is manual and error-prone.

## Sub-Issues

- [ ] nightgauge/nightgauge#4700 — feat(go): repo add|remove|list
- [ ] nightgauge/nightgauge#4701 — fix(vscode): reload on manifest change

## Acceptance Criteria

- [ ] Every sub-issue merged and the manifest is editable without hand-editing
```

## Expected Behavior

- Phase 5 emits **zero** `MISSING_REQUIRED_HEADING` findings for #4700, #4701,
  and #4702.
- Phase 5 emits zero `EMPTY_REQUIRED_HEADING` findings — every required section
  above carries content.
- `## Business/user value` on #4700 produces **no** finding. Headings absent
  from the per-type table are ignored; required headings are a floor, not a
  ceiling.
- Verdict: `READY`. Exit 0.

## Failure Modes the Test Must Catch

- **The #711 regression itself.** A body authored as
  `## Problem statement` / `## Business/user value` / `## Acceptance criteria` /
  `## Technical notes` must produce `MISSING_REQUIRED_HEADING` for `Summary`
  and `Acceptance Criteria` — the audit is not silently accepting the old
  shape, and `issue-create` is no longer emitting it.
- **Case-folding.** Rewriting #4700's `## Acceptance Criteria` as
  `## Acceptance criteria` must still be reported missing. Phase 5 matches
  `^##[[:space:]]+${HEADING}\s*$` through `grep -E` with no `-i`. If this
  fixture passes with the lowercase form, the matcher has silently gone
  case-insensitive and every casing assertion above is vacuous.
- **Severity collapse.** In strict `--manifest` mode a `MISSING_REQUIRED_HEADING`
  is CRITICAL, so the verdict must flip to `NEEDS FIXES` and exit 1. A run that
  reports the finding while still printing `READY` is the pre-#711 behavior —
  the audit describing a defect it declines to act on. In inferential mode
  (`--epic`, `--issues`, `--all-recent`) the same finding stays WARNING and the
  run exits 0; that asymmetry is deliberate and is recorded in
  [docs/ISSUE_AUDIT.md § Severity Tiers](../../../docs/ISSUE_AUDIT.md#severity-tiers).
- **Extra-heading intolerance.** If `## Business/user value` ever produces a
  finding, Phase 5 has stopped ignoring unlisted headings and would force
  authors to strip context that carries real information.
