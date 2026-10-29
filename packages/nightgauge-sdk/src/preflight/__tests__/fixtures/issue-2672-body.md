<!-- discovered-work: id=dw-5f279f7bbe2f issue=#2665 -->

Discovered in #2665 (run unknown, stage feature-planning)

## Summary

`ac-reconcile` only recognises acceptance criteria written as checkbox bullets (`- [ ]` / `* [ ]`). Issues that list their criteria as numbered items (for example `1. **AC1:** ...`) yield zero criteria. The preflight then reports `aggregate_status=no-acs-detected`, which forces `Refs #N` on the merge instead of allowing the issue to close. #2665 is the observed case: its own body uses numbered ACs.

## User Story

As a pipeline operator,
I want `ac-reconcile` to read numbered-list acceptance criteria,
So that issues written in that style are checked against the working tree and can close on merge.

## Acceptance Criteria

- [ ] An issue body whose acceptance criteria are numbered items (`1. ...`, `1) ...`, `1. **AC1:** ...`) yields one criterion per item in `parseACs`.
- [ ] Checkbox bullets (`- [ ]`, `* [x]`) keep parsing exactly as before, including the `<details>` exclusion and de-duplication.
- [ ] A body with no checkboxes and no numbered items still yields zero criteria and `no-acs-detected`.
- [ ] Re-running `nightgauge preflight ac-reconcile` on the #2665 body reports detected criteria instead of `no-acs-detected`.
- [ ] Go unit tests cover numbered, parenthesised, bold-prefixed, and mixed-list bodies.
- [ ] The SDK mirror of the parser (`packages/nightgauge-sdk/src/preflight/`) accepts the same forms, or its divergence from the Go port is documented.

## Technical Notes

**Affected files:**

- `internal/preflight/ac_reconcile.go` — `checkboxRE` (line 95) and `parseACs` (line 105) define the only criterion syntax recognised; the root cause is here.
- `internal/preflight/ac_reconcile_test.go` — existing parser tests; add numbered-list cases.
- `packages/nightgauge-sdk/src/preflight/reconcile.ts` — SDK counterpart the Go comment says it mirrors; confirm whether it carries the same checkbox-only pattern during pickup.

**Implementation approach:**
Extend the criterion pattern in `checkboxRE` (or add a second pattern) to accept numbered-list items, keeping the `<details>` stripping and de-duplication. Keep the `4096`-char cap and the RE2-safe bounded pattern style. Add table-driven tests in `ac_reconcile_test.go`, then apply the same change to the SDK parser so both stay in step.

## Root Cause Analysis

**Observed behavior:** `preflight ac-reconcile` returns `aggregate_status=no-acs-detected` for an issue whose acceptance criteria are numbered (`1. **AC1:** ...`). The merge then records `Refs #N` and the issue stays open.

**Expected behavior:** Numbered acceptance criteria are parsed as criteria and reconciled against the working tree, as checkbox criteria already are.

**Suspected location:** `internal/preflight/ac_reconcile.go:95` (`checkboxRE`), which only matches `[-*]` bullets followed by `[ ]`/`[x]`. `parseACs` at line 105 never sees numbered items.

**Reproduction steps:**

1. Create an issue whose body has `1. **AC1:** something` under an `## Acceptance Criteria` heading.
2. Run `nightgauge preflight ac-reconcile` against its body.
3. Observe `aggregate_status=no-acs-detected` with zero criteria.

## Complexity Estimate

Size: S — two source files (Go parser and its test) plus one SDK mirror, with a focused regex change.

## Original Issue (before refinement)

<details>
<summary>Original Issue (before refinement)</summary>

<!-- discovered-work: id=dw-5f279f7bbe2f issue=#2665 -->

Discovered in #2665 (run unknown, stage feature-planning)

## Summary

```text
ac-reconcile parseACs recognises only '- [ ]' checkboxes; numbered-list acceptance criteria yield no criteria and force Refs #N
```

## Evidence

```text
#2665's own body lists ACs as '1. **AC1:** ...'; preflight ac-reconcile returned aggregate_status=no-acs-detected
```

</details>
