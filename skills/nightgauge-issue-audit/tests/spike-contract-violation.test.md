# TC: Spike Contract Violation (Negative Test)

**Negative fixture**: pins the invariant that spike-contract violations stay
CRITICAL even when `--fix` is requested. This is the encoded hard rule from
[docs/SPIKE_CONTRACT.md](../../../docs/SPIKE_CONTRACT.md) — the audit MUST
NOT auto-rewrite human-authored spike artifact content.

## Setup Assumptions

- Skill invoked with `--manifest <path>` AND `--fix`.
- Manifest declares a spike issue with `spike_artifact.path: docs/spikes/4400-foo.md`.

## Synthetic Manifest Entry

```json
{
  "repo": "nightgauge/nightgauge",
  "number": 4400,
  "type": "spike",
  "priority": "P2",
  "size": "S",
  "status": "Ready",
  "body_sections": ["Summary", "Acceptance Criteria", "Recommendations"],
  "spike_artifact": {
    "path": "docs/spikes/4400-foo.md",
    "exists": false
  }
}
```

## Synthetic GitHub State

- Issue `nightgauge/nightgauge#4400` exists, OPEN, has `type:spike` label,
  on project 1, all required body sections present.
- BUT the issue body's `## Recommendations` section is **missing the fenced
  `yaml recommendations` block** required by the spike contract.

## Expected Behavior — Dry Run AND `--fix`

- Phase 5 emits:
  ```json
  {
    "issue": { "repo": "nightgauge/nightgauge", "number": 4400 },
    "phase": 5,
    "type": "MISSING_SPIKE_RECS_BLOCK",
    "severity": "CRITICAL",
    "detail": "Spike issue is missing the 'yaml recommendations' block (docs/SPIKE_CONTRACT.md).",
    "repair_command": null,
    "repair_status": "not_attempted"
  }
  ```
- Phase 11 (auto-repair) **does not act on this finding**. Even with `--fix`,
  the finding remains CRITICAL with `repair_status: "not_attempted"` because
  the repair-primitive map has no entry for `MISSING_SPIKE_RECS_BLOCK`.
- Verdict: `NEEDS FIXES (1 CRITICAL, 0 WARNING, 0 INFO)`. Exit 1 in both
  dry-run and `--fix` modes.

## Failure Modes the Test Must Catch

- `--fix` adds a placeholder `yaml recommendations` block to the issue body.
  MUST NOT happen — the contract requires human-authored content. Generative
  repair would defeat determinism and corrupt issue intent.
- The audit downgrades the finding to WARNING because the spike artifact
  file does not yet exist. MUST stay CRITICAL — the missing recommendations
  block is independent of the artifact's existence.
- The audit re-runs the repair on the next pass and reports `READY`. MUST
  remain `NEEDS FIXES` until a human supplies the recommendations content
  (typically as part of merging the spike).

## Why This Test Matters

This is the **single most important invariant** of the auto-fix design. If
the audit ever auto-rewrites human-authored spike content, the deterministic
repair guarantee is broken: the audit could "fix" an issue into a worse
state, defeating the point of having a separate spike contract at all. This
fixture must be checked before any change to Phase 11 logic.
