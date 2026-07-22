# TC: Strict (Manifest) vs Inferential (Epic) Mode Contrast

Pins the difference in audit strictness between `--manifest` and `--epic`
modes. The same GitHub state must produce different finding sets depending
on which mode is used.

## Setup Assumptions

- Single-repo workspace: `nightgauge` → project 1.
- Same epic and sub-issue used in both runs.

## Synthetic GitHub State

- Epic `nightgauge/nightgauge#4500` exists, OPEN, on project 1, Status:
  Ready, body has `## Summary`, `## Sub-Issues`, `## Acceptance Criteria`.
- Sub-issue `nightgauge/nightgauge#4501` exists, OPEN, on project 1,
  Status: Backlog, body has `## Summary` and `## Acceptance Criteria`.
- Sub-issue #4501 is missing a `priority:*` field on the project board
  (Priority is unset, but Status and Size are set).
- Native `addSubIssue` link from #4500 → #4501 is in place.
- No `blockedBy` edges declared on either issue. Body has no
  `Depends on:` line.

## Run 1: `--manifest <path>` Strict Mode

### Synthetic Manifest

```json
{
  "schema_version": "1.0",
  "created_at": "2026-05-06T20:30:00.000Z",
  "created_by_skill": "nightgauge-issue-create",
  "project_number": 1,
  "entries": [
    {
      "repo": "nightgauge/nightgauge",
      "number": 4500,
      "type": "epic",
      "priority": "P1",
      "size": "L",
      "status": "Ready",
      "sub_issues": [4501]
    },
    {
      "repo": "nightgauge/nightgauge",
      "number": 4501,
      "type": "feature",
      "priority": "P1",
      "size": "M",
      "status": "Backlog",
      "parent_epic": "#4500",
      "body_sections": ["Summary", "Acceptance Criteria"]
    }
  ]
}
```

### Expected Findings (Strict)

- Phase 4 emits exactly one finding for #4501:
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4501 },
    "phase": 4,
    "type": "MISSING_PRIORITY_FIELD",
    "severity": "WARNING",
    "detail": "Project item Priority field is unset; manifest declares P1.",
    "repair_command": "nightgauge project set-field 4501 Priority P1",
    "repair_status": "not_attempted"
  }
  ```
- Verdict: `NEEDS FIXES (0 CRITICAL, 1 WARNING, 0 INFO)`. Exit 0 (no
  CRITICAL findings).

### Why Strict Mode Catches More

The manifest declares an explicit `priority: "P1"` for #4501. The audit
asserts the project board reflects this value. Without the manifest, the
inferential mode does not know what Priority _should_ be — only that it is
unset.

## Run 2: `--epic 4500` Inferential Mode

No manifest is consulted. Phase 4 still checks Priority field presence but
emits a softer finding because the _expected_ value is unknown.

### Expected Findings (Inferential)

- Phase 4 emits one finding for #4501:
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4501 },
    "phase": 4,
    "type": "MISSING_PRIORITY_FIELD",
    "severity": "WARNING",
    "detail": "Project item Priority field is unset (no manifest to derive expected value).",
    "repair_command": null,
    "repair_status": "not_attempted"
  }
  ```
- `repair_command` is `null` because inferential mode cannot supply the
  Priority value. The operator must run
  `nightgauge project set-field 4501 Priority <P0|P1|P2|P3>` manually.
- Verdict: `NEEDS FIXES (0 CRITICAL, 1 WARNING, 0 INFO)`. Exit 0.

## Failure Modes the Test Must Catch

- Inferential mode invents a Priority value (e.g., assumes P2). MUST NOT —
  inferential mode flags only what it can verify; it never guesses
  human-supplied metadata.
- Strict mode misses the missing Priority because the manifest entry's
  `priority` field is taken as fact rather than as an assertion to verify.
  MUST verify against actual board state.
- Strict mode emits CRITICAL severity for `MISSING_PRIORITY_FIELD`. MUST
  emit WARNING — Priority is a quality concern, not a pickup blocker (Status
  is the pickup gate, and that is a separate CRITICAL finding type).
