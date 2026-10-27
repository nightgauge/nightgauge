# Discovered-Work Substrate — a per-run capture file, separate from finding intake

**Date:** 2026-10-26
**Author:** nightgauge
**Status:** Decided
**Issue:** #478
**Implemented by:** #478 (schema, capture file, CLI, doctor check); producers in #479 and
#948; the materializer in #480

---

## Executive Summary

No pipeline structure carried "work we found but must not do in this run". A stage either
widened its own scope or dropped the observation. This ADR adds a capture substrate for that
work: a versioned record, a per-run file, and Go primitives that validate every record
before it is written.

| #   | Topic        | Decision                                                    | Rejected                                        |
| --- | ------------ | ----------------------------------------------------------- | ----------------------------------------------- |
| 1   | Substrate    | A separate per-run `discovered-work-{N}.json`               | Extending #102's finding intake                 |
| 2   | Naming       | "discovered work": verb, file and Go package                | "discovery", "finding"                          |
| 3   | Producers    | The six stages, or `producer:<kebab-name>`                  | Stage names only                                |
| 4   | Lifecycle    | Append during the run; the materializer is the only deleter | pr-merge cleanup; an automatic sweep of orphans |
| 5   | Security     | All constraints enforced at the primitive                   | Validating in the materializer                  |
| 6   | Priority set | `critical\|high\|medium\|low`, the spike vocabulary         | `P0`–`P3`                                       |

## 1. Substrate: separate per-run file

#102 proposes findings (`kind`, `remedy`, `scope`) as **learning signals**. Continuous
improvement and planning recall consume them. They are long-lived and repository-scoped,
and they never become issues on their own.

Discovered work is made of **issue candidates**. Type, Priority and Size are mandatory at
creation (SELF_IMPROVEMENT_BOUNDARIES Rule 5). It is run-scoped and consumed exactly once.

One file for both would mix two lifecycles (append forever against consume and delete) and
two governance regimes. So the substrates stay apart. When #102 is built it must not reuse
`discovered-work-*.json`. A finding that warrants an issue is recorded with
`nightgauge discovered-work record`, so issue filing has one path, not two.

## 2. Naming

"Discovery" already names Scheduled Discovery, and "finding" is #102's. The CLI family is
`nightgauge discovered-work`, the file is `discovered-work-{N}.json`, and the Go package is
`internal/cmd/discoveredwork`.

## 3. Producers

`source_stage` is one of the six pipeline stages, or a deterministic producer named
`producer:<kebab-name>` (for example `producer:post-merge-impact-set`, #948). The field stays
one bounded string, and every record stays attributable.

## 4. Lifecycle and cleanup ownership

- Stages append during the run. A record's `id` is `dw-` plus the first 12 hex characters of
  sha256(`source_stage` NUL `summary`), so a retried stage re-recording the same discovery is
  a no-op.
- pr-merge cleanup never matches the file. A test replays the cleanup patterns and asserts
  the capture file survives.
- The materializer (#480) is the only deleter, and deletes only after it has filed the
  records.
- A file with no live run (no `issue-{N}.json`) or older than seven days is **reported** by
  `nightgauge discovered-work orphans` and by doctor check `orphaned_discovered_work`
  (NGD050). Nothing sweeps it: deleting it would discard the follow-ups the substrate exists
  to keep.

## 5. Security

The primitive enforces every constraint, so no consumer can be handed an unvalidated record:

- Closed enums for type, priority, size and stage. Unknown keys are refused on both sides
  (`DisallowUnknownFields`, zod `.strict()`).
- `summary` is one line of at most 200 characters, because it becomes an issue title.
- `candidate_target_files` are repository-relative, with no `..` segment, NUL or leading
  `-`. They are hints only and are never opened, stat'ed or executed.
- The capture path is built only from the integer issue number. It must stay inside the
  symlink-resolved run directory, and a symlinked capture file is refused.
- A refused record exits non-zero, names the field, and leaves the file byte-identical.
  Appends hold an exclusive lock and replace the file atomically.

## 6. Priority vocabulary

The materializer generalizes the spike path, so the record uses `spike.ValidateSchema`'s
enums (`critical|high|medium|low`, `XS..XL`, `feature|bug|docs|chore|spike`). One validation
table then serves both.

## References

- [CONTEXT_ARCHITECTURE.md § Discovered-Work Capture File](../CONTEXT_ARCHITECTURE.md#discovered-work-capture-file)
- [DOCTOR.md § NGD050](../DOCTOR.md#ngd050)
- SDK schema: `packages/nightgauge-sdk/src/context/schemas/discovered-work.ts`
