# TC: Missing Board Membership

Behavioral fixture for Phase 4 (Project Board Membership and Fields). Pins the
`MISSING_FROM_BOARD` finding and its repair primitive. Mirrors the format used
by `nightgauge-issue-create/tests/cross-repo-routing.test.md`.

## Setup Assumptions

- Workspace yaml maps `nightgauge` → project 1.
- Skill invoked with `--manifest <path>` strict mode.

## Synthetic Manifest

```json
{
  "schema_version": "1.0",
  "created_at": "2026-05-06T20:30:00.000Z",
  "created_by_skill": "nightgauge-issue-create",
  "project_number": 1,
  "entries": [
    {
      "repo": "nightgauge/nightgauge",
      "number": 4001,
      "type": "feature",
      "priority": "P1",
      "size": "M",
      "status": "Ready",
      "body_sections": ["Summary", "Acceptance Criteria"]
    }
  ]
}
```

## Simulated GitHub State

- Issue `nightgauge/nightgauge#4001` exists, OPEN, has `type:feature`
  label, body has `## Summary` and `## Acceptance Criteria` sections with
  content.
- `gh api graphql ... projectItems` returns an empty `nodes` array (issue is
  NOT on any project board).

## Expected Behavior — Dry Run

- Phase 1, 2, 3, 5, 6, 7, 8, 9 each report no findings.
- Phase 4 emits exactly one finding:
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4001 },
    "phase": 4,
    "type": "MISSING_FROM_BOARD",
    "severity": "CRITICAL",
    "detail": "Issue is not a member of project 1.",
    "repair_command": "nightgauge project add 4001 --repo nightgauge/nightgauge --project 1",
    "repair_status": "not_attempted"
  }
  ```
- Verdict: `NEEDS FIXES (1 CRITICAL, 0 WARNING, 0 INFO)`.
- Exit code: 1.

## Expected Behavior — `--fix`

- Phase 11 runs the repair primitive:
  ```
  nightgauge project add 4001 --repo nightgauge/nightgauge --project 1
  ```
- On success: finding `repair_status` becomes `succeeded`; severity
  downgraded out of the verdict count (audit re-walks Phase 4 and confirms
  membership). Final verdict: `READY`. Exit 0.
- One JSONL line appended to the audit trail with
  `action: "project add"`, `before: {in_project: false}`,
  `after: {in_project: true}`, `actor: "nightgauge-issue-audit"`.

## Failure Modes the Test Must Catch

- Skill silently passes despite missing membership (the original gap that
  motivated this skill — the `epic-validate` predecessor caught it for epics
  but not for sub-issues created outside the epic flow).
- Repair primitive fails (e.g., GitHub rate limit) but the audit reports
  READY anyway. MUST NOT happen — failures reclassify to CRITICAL with
  `repair_error`.
- `--fix` retries internally on failure. MUST NOT happen — operator
  re-runs.
