# TC: Missing blockedBy Edge

Behavioral fixture for Phase 7 (`blockedBy` Alignment). Pins
`MISSING_BLOCKED_BY` and `STALE_BLOCKED_BY` findings.

## Setup Assumptions

- Skill invoked with `--epic 4100` (inferential mode).
- Epic 4100 has two sub-issues: 4101 and 4102.
- Workspace yaml maps `nightgauge` → project 1.

## Synthetic GitHub State

- `nightgauge/nightgauge#4100` (epic, OPEN, on project 1, Status: Ready,
  has `subIssues` linking 4101 and 4102).
- `nightgauge/nightgauge#4101` (feature, OPEN, on board, body declares
  `Depends on: None`, Wave 1).
- `nightgauge/nightgauge#4102` (feature, OPEN, on board, body declares
  `Depends on: #4101`, Wave 2). However, **no native `blockedBy` edge has
  been applied** — `gh api graphql ... blockedBy` returns an empty list.
- Issue `nightgauge/nightgauge#3999` exists, CLOSED as completed.
  `nightgauge/nightgauge#4101` has a stale native `blockedBy` edge to
  3999 that should have been removed when 3999 was merged.

## Expected Behavior — Dry Run

- Phase 7 emits two findings:
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4101 },
    "phase": 7,
    "type": "STALE_BLOCKED_BY",
    "severity": "WARNING",
    "detail": "blockedBy points to #3999, which is closed-as-completed.",
    "repair_command": "nightgauge issue remove-blocked-by 4101 3999",
    "repair_status": "not_attempted"
  }
  ```
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4102 },
    "phase": 7,
    "type": "MISSING_BLOCKED_BY",
    "severity": "CRITICAL",
    "detail": "Body declares 'Depends on: #4101' but no native blockedBy edge exists.",
    "repair_command": "nightgauge issue add-blocked-by 4102 4101",
    "repair_status": "not_attempted"
  }
  ```
- Verdict: `NEEDS FIXES (1 CRITICAL, 1 WARNING, 0 INFO)`.
- Exit code: 1.

## Expected Behavior — `--fix`

- Phase 11 invokes:
  - `nightgauge issue remove-blocked-by 4101 3999` (auto, because
    blocker is `closed-as-completed`)
  - `nightgauge issue add-blocked-by 4102 4101` (auto)
- Both succeed; both findings move to `repair_status: "succeeded"`. Final
  verdict: `READY`. Exit 0.
- Audit trail receives two JSONL lines.

## Failure Modes the Test Must Catch

- `--fix` removes a `closed-as-not-planned` blocker without `--fix-interactive`.
  MUST NOT happen — only `closed-as-completed` is auto-removable.
- Skill rejects the `Depends on:` line because the body uses spaces inside
  the issue reference (`#4101 ` with trailing whitespace). MUST tolerate
  whitespace.
- The Go binary's `epic validate --json` is not consulted for stale /
  circular detection; the skill re-implements the check. MUST delegate.
