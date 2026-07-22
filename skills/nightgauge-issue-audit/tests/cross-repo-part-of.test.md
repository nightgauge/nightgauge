# TC: Cross-Repo Part-Of Annotation

Behavioral fixture for Phase 6 (Sub-Issue & Parent Linking) and Phase 8
(Cross-Repo Consistency). Pins `MISSING_PART_OF_ANNOTATION` and
`MISSING_PARENT_BACKREF` findings, since GitHub's native `addSubIssue` is
not supported across repositories.

## Setup Assumptions

- Multi-repo workspace with `nightgauge` (project 1) and
  `acme-dashboard` (project 4).
- Skill invoked with `--manifest <path>` after a cross-repo epic
  decomposition.

## Synthetic Manifest

```json
{
  "schema_version": "1.0",
  "created_at": "2026-05-06T20:30:00.000Z",
  "created_by_skill": "nightgauge-issue-create",
  "entries": [
    {
      "repo": "nightgauge/nightgauge",
      "number": 4200,
      "type": "epic",
      "priority": "P1",
      "size": "L",
      "status": "Ready",
      "sub_issues": [4201, 4301],
      "body_sections": ["Summary", "Sub-Issues", "Acceptance Criteria"]
    },
    {
      "repo": "nightgauge/nightgauge",
      "number": 4201,
      "type": "feature",
      "priority": "P1",
      "size": "M",
      "status": "Backlog",
      "parent_epic": "nightgauge/nightgauge#4200",
      "body_sections": ["Summary", "Acceptance Criteria"]
    },
    {
      "repo": "acme/dashboard",
      "number": 4301,
      "type": "feature",
      "priority": "P1",
      "size": "M",
      "status": "Backlog",
      "parent_epic": "nightgauge/nightgauge#4200",
      "body_sections": ["Summary", "Acceptance Criteria"]
    }
  ]
}
```

## Synthetic GitHub State

- Epic #4200 exists, has body sections, but body does NOT include a
  cross-repo link to `acme/acme-web#200`. Native
  `subIssues` on #4200 contains only `#4201` (cross-repo native linking is
  unsupported).
- Sub-issue #4201 exists in same repo, linked via `addSubIssue`, body has
  `Part of #4200`.
- Sub-issue `acme/acme-web#200` exists, on project 4,
  but its body lacks the `Part of nightgauge/nightgauge#4200`
  annotation. (The audit MUST detect this — without the annotation the
  pipeline cannot trace the cross-repo relationship.)

## Expected Behavior — Dry Run

- Phase 6 emits:
  ```json
  {
    "issue": { "repo": "acme/dashboard", "number": 4301 },
    "phase": 6,
    "type": "MISSING_PART_OF_ANNOTATION",
    "severity": "CRITICAL",
    "detail": "Cross-repo sub-issue body lacks 'Part of nightgauge/nightgauge#4200'.",
    "repair_command": "nightgauge issue edit 4301 --repo acme/dashboard --append-body \"Part of nightgauge/nightgauge#4200\"",
    "repair_status": "not_attempted"
  }
  ```
- Phase 8 emits:
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4200 },
    "phase": 8,
    "type": "MISSING_PARENT_BACKREF",
    "severity": "WARNING",
    "detail": "Epic body has no cross-repo link to acme/acme-web#200.",
    "repair_command": "nightgauge issue edit 4200 --append-body \"- acme/acme-web#200\"",
    "repair_status": "not_attempted"
  }
  ```
- Verdict: `NEEDS FIXES (1 CRITICAL, 1 WARNING, 0 INFO)`. Exit 1.

## Expected Behavior — `--fix`

- Both repairs invoked and succeed. Verdict: `READY`. Exit 0.

## Failure Modes the Test Must Catch

- The audit treats a missing `Part of <owner>/<repo>#<n>` annotation as a
  WARNING. MUST be CRITICAL — without it, the cross-repo relationship is
  invisible to the pipeline.
- The audit attempts to apply native `addSubIssue` cross-repo. MUST NOT —
  native cross-repo sub-issue linking is unsupported by GitHub.
- The repair `issue edit --append-body` rewrites the existing body content
  rather than appending. MUST append only.
