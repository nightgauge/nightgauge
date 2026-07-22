# Pipeline & CI Fast-Track (Epic #4123)

How trivial changes (documentation, configuration) are fast-tracked through the
pipeline **and** CI so they no longer pay the cost of a full source change. This
is the customizable surface delivered by epic #4123.

A single deterministic primitive — the change classifier (#4124) — and a single
config table — `routing.change_rules` (#4125) — drive three consumers:

| Layer              | Issue | What it skips on a trivial change                               |
| ------------------ | ----- | --------------------------------------------------------------- |
| Pipeline scheduler | #4126 | `feature-planning` + `feature-validate` stages (LLM cost)       |
| CI                 | #4127 | the heavy `build-and-test` steps (npm build/test, go test, e2e) |
| PR gates           | #4128 | the PR gates' retry + sleep rate-limit cushions                 |

See [CONFIGURATION.md § routing.change_rules](CONFIGURATION.md#routingchange_rules--the-fast-track-table)
for the rule schema, built-in defaults, and precedence.

## 1. Pipeline stage skipping (#4126)

The Go scheduler re-derives the routing `Decision` from the issue's labels/board
fields + repo config (`routing.Derive()`), and skips the stages the Decision
marks skippable. This is **deterministic** — it does not trust the AI-authored
`skip_stages` in `issue-{N}.json`. Only `feature-planning` and `feature-validate`
are skippable; `issue-pickup`, `feature-dev`, `pr-create`, `pr-merge` always run.

Skipped stages are recorded as `skipped` (not `failed`/`completed`) and count
toward success (`completed + skipped == 6`). `force_full_pipeline: true` and the
label-based `risk_high` floor both disable skipping.

## 2. CI fast-track (#4127)

A cheap, always-running `changes` gate job classifies the PR diff via
`nightgauge ci classify` and exposes `run_heavy`. `build-and-test` gains
`needs: changes` and gates its **expensive steps** on
`needs.changes.outputs.run_heavy != 'false'`.

**Deadlock-safe by construction.** The required `build-and-test` _job_ always
runs and reports success — only its inner _steps_ are gated. A skipped required
status check would deadlock branch protection; a skipped _step_ leaves the job
`success`. The `changes` job is intentionally **not** a required check, and the
gate is **fail-open** (`!= 'false'`): an unclassifiable diff runs the full suite.
`push` (merge-skew) and `schedule` (nightly env-drift) always force the full
suite.

### `ci_jobs` → workflow gate mapping

| `change_class`                          | `run_heavy` | `build-and-test` expensive steps |
| --------------------------------------- | ----------- | -------------------------------- |
| `docs_only`, `empty`                    | `false`     | **skipped** (job passes in ~30s) |
| `config_only`, `source`, `mixed`, error | `true`      | run in full                      |

Config is deliberately **not** fast-tracked for CI even though the pipeline
skips `feature-validate` for it: a `package.json`/`tsconfig`/CI-workflow edit can
need build+test, and the CI workflow files themselves classify as config.

```bash
nightgauge ci classify --base origin/main --head HEAD --json
# {"change_class":"docs_only","run_heavy":false,"jobs":{...},"reason":"..."}
```

> Scope: this epic gates the dominant-cost `build-and-test` job (self-contained
> in `ci.yml`). The other required workflows (`validate-config`, `codex-smoke`,
> `skill-eval-baseline`, `dependency-guard`) live in separate files (job outputs
> do not cross workflows) and are seconds-long; gating them is a fast-follow.

## 3. PR gate relaxation (#4128)

For a verified-trivial change, the PR gates' `3× retry + 1s sleep` rate-limit
cushions are pure overhead. Opt-in config relaxes them:

```yaml
pipeline:
  gates:
    pr_merge:
      relax_on_change_class: [docs_only, config_only]
    pr_create:
      relax_on_change_class: [docs_only]
```

Default absent → never relax (strictly opt-in). When relaxed, the gates collapse
to a single attempt with no sleep.

### Drift-revoke (the safety guarantee)

Relaxation classifies the **real post-dev diff** (`git diff --name-only
origin/main...HEAD`), not the predictive issue-pickup route. A "docs" issue that
actually edited source classifies as `source`/`mixed` and is **never relaxed** —
the classifier itself is the drift-revoke check, so there is no separate scope
gate to keep in sync. The decision is fail-safe: if the diff can't be computed,
the change classifies as `empty` and the gate is **not** relaxed (full behavior).
A `gate.relaxation` telemetry event records `{relaxed, change_class}` for audit.

## Verifying the win (before/after)

**Baseline (#4121, a docs-only PR before this epic):** the full pipeline ran all
six stages and cost **> $6**; CI `build-and-test` took **~4m36s**.

**After this epic, an equivalent docs-only change should show:**

- **Pipeline:** `feature-planning` + `feature-validate` recorded as `skipped`;
  the run reported success; the remaining stages run on the cheapest model
  (trivial routes already select Haiku via complexity). Materially lower cost.
- **CI:** the `changes` job reports `run_heavy=false`; `build-and-test` reports
  **success in ~30s** with its expensive steps skipped (no npm build/test, no go
  test, no e2e). Materially lower wall-time.
- **A mislabeled "docs" change that touched source** classifies as `source` and
  is **not** fast-tracked — stages run, CI runs full, gates are not relaxed.

### Measuring pipeline cost: `nightgauge cost by-class`

Each completed run records its **authoritative** `change_class` (classified from
the real post-dev diff) on the run record's `routing.change_class`. The reporter
groups recorded runs by that class and shows cost (p50/p95/mean) and duration
(p50/p95):

```bash
nightgauge cost by-class --days 90        # table
nightgauge cost by-class --days 90 --json # machine-readable
```

```
class         runs     cost_p50     cost_p95    cost_mean    dur_p50    dur_p95
------------------------------------------------------------------------------
docs_only       12      0.18$        0.41$        0.24$       5.9m       7.2m
source          88      4.90$       11.80$        5.40$      31.2m      58.0m
```

Runs recorded before this landed have no `change_class` and bucket under
`unknown`, so the comparison populates as new runs complete. A live docs-only run
compared against the >$6 / ~4m36s #4121 baseline is the empirical proof; the
mechanisms above are what produce the delta.

## See also

- [CONFIGURATION.md § routing](CONFIGURATION.md#routing) — `change_rules` schema, defaults, precedence
- `internal/intelligence/changeClassifier` — the deterministic classifier (#4124)
- `internal/intelligence/routing/change_rules.go` — rule struct, defaults, precedence (#4125)
- `internal/ci/classify.go` — CI fast-track decision (#4127)
- `internal/orchestrator/gates/relaxation.go` — gate relaxation + drift-revoke (#4128)
