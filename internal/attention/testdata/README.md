# `captured-envelopes.json` — provenance

Captured, not authored (#166). This file is the redacted **envelope grammar**
of real `DecisionRequest` records written by the shipping attention store, and
it is the evidence the #305 run-scoped raise verb is tested against.

## Provenance

| Field              | Value                                                                   |
| ------------------ | ----------------------------------------------------------------------- |
| Source store       | `.nightgauge/attention/` of the maintainer's local multi-repo workspace |
| Captured at        | 2026-08-07                                                              |
| Repo commit        | `17b12f95` (`fix(taxonomy): unify six terminal-kind matchers…`)         |
| Capture command    | `scripts/capture-attention-fixture.sh`                                  |
| Records scanned    | 110                                                                     |
| Distinct envelopes | 20                                                                      |

Regenerate with:

```bash
scripts/capture-attention-fixture.sh [SOURCE_STORE_DIR] [OUT_FILE]
```

The output is a pure function of the input store — envelopes are deduped and
sorted, and no field is read from the wall clock — so re-running against the
same store on a different day produces a byte-identical file. The three dated
facts live in the table above rather than in the JSON, deliberately, so the
fixture itself never churns.

## What is in it, and what is deliberately not

Captured, per record: `schema_version`, `kind`, `severity`, `standing`, whether
a fingerprint is set, `default_action`, the lifecycle `state`, which lifecycle
sub-records are populated, which **context field names** are populated, each
option's `verb`, the producer name, and whether the steer rail is enabled. Plus
the journal's action vocabulary and its counts.

Every one of those is a machine token from a closed registry that already lives
in this public repository — `internal/attention/schema.go` (kinds, severities,
states), `internal/attention/verbs.go` (the verb allowlist),
`internal/attention/store.go` + `standing.go` (journal actions), and the
producer-name constants in `internal/orchestrator/attention_wiring.go` and
`internal/attention/sweep/`.

Dropped entirely — not masked, not truncated: titles, bodies, blocker prose,
URLs, idempotency keys, repo slugs, issue and PR numbers, fingerprints, request
ids, actors, steer text, notes, and every timestamp. A context field
contributes only its **name**; its value never leaves the machine. The source
workspace mixes private repositories' runs, so the capture is allowlist-only —
a field is captured because the script enumerates it, never because it looked
safe.

## The honest caveat: none of these records are run-scoped

`"run_scoped_records": 0`. Across 110 real records spanning nine producers,
**not one carries `context.run_id`** — that is not a sampling artifact, it is
the defect #305 fixes. Every producer that has ever fired on this machine is
fleet-scoped (`AutonomousScheduler.raiseAttention`) or sweep-scoped
(`internal/attention/sweep/`); the six run-scoped producers hung off the Go
scheduler, which the extension operating mode never runs.

So this fixture pins the **envelope**: schema version, lifecycle shape, the
option/verb binding, the context-field vocabulary, and the journal action
vocabulary. It does **not** pin a run-scoped payload, because none exists to
capture. The run-scoped payload is pinned instead by
`internal/ipc/attention_raise_test.go`, which asserts the IPC path and the Go
scheduler path produce the same record from the same shared builder — a test
that needs no fixture because both sides are generated from one function.

`run_scoped_records` is recorded but **not asserted on**. Once this change has
run for a while, a re-capture on a live workspace will show it above zero, and
a test that demanded zero would turn the fix itself red.
