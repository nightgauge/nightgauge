# TC: Epic Missing Falsifier / Demand (Phase 5 Rider)

Pins the #105 epic riders `MISSING_EPIC_FALSIFIER` and `MISSING_EPIC_DEMAND`:
WARNING in every mode, never auto-fixed, epics only.

## Setup Assumptions

- Phase 5 runs on each issue below; all required headings are present.

## Cases

| Case | Issue                                                               | Expected Phase 5 rider findings                                      |
| ---- | ------------------------------------------------------------------- | -------------------------------------------------------------------- |
| 1    | `type:epic`, `## Summary` has `Falsifier: x` and `- **Demand:** y`  | none                                                                 |
| 2    | `type:epic`, `## Summary` has `Demand:` only                        | exactly one `MISSING_EPIC_FALSIFIER` (WARNING)                       |
| 3    | Case 2 with `--manifest` and `--fix`                                | `MISSING_EPIC_FALSIFIER` stays WARNING, not CRITICAL; body untouched |
| 4    | `type:feature`, neither line                                        | none                                                                 |
| 5    | `type:epic`, `Falsifier: x` under `## Acceptance Criteria` only     | `MISSING_EPIC_FALSIFIER` (WARNING)                                   |
| 6    | `type:epic`, `## Summary` has `Falsifier:` with nothing after colon | `MISSING_EPIC_FALSIFIER` (WARNING)                                   |
| 7    | `type:epic`, neither line                                           | `MISSING_EPIC_FALSIFIER` and `MISSING_EPIC_DEMAND`                   |

## Failure modes the test must catch

- Rider escalated to CRITICAL (manifest mode) or auto-fixed.
- Rider firing on non-epic types.
- A line outside `## Summary`, or an empty one, accepted as present.
