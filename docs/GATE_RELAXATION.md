# Pipeline & CI Fast-Track (Epic #4123)

How trivial changes (documentation, configuration) are fast-tracked through the
pipeline **and** CI so they no longer pay the cost of a full source change. This
is the customizable surface delivered by epic #4123.

A single deterministic primitive — the change classifier (#4124) — and a single
config table — `routing.change_rules` (#4125) — drive three consumers:

| Layer              | Issue       | What it skips on a trivial change                               |
| ------------------ | ----------- | --------------------------------------------------------------- |
| Pipeline scheduler | #4126       | `feature-planning` + `feature-validate` stages (LLM cost)       |
| CI                 | #4127, #647 | the expensive steps of `ci.yml`'s `go`, `sdk` and `vscode` jobs |
| PR gates           | #4128       | the PR gates' retry + sleep rate-limit cushions                 |

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

### A skippable stage may not be the sole owner of a required side effect (#1179)

Skipping is safe only for work the rest of the chain does not depend on. It was
not: `feature-validate` was the only stage documented as committing and pushing
(`feature-dev` deliberately does not — #1608), so a trivial route that skipped
it left the implementation uncommitted, the branch zero commits ahead of base,
and `pr-create` opening an empty PR. Nothing detected it; the stage said so in
a prose self-assessment that nothing consumes.

The answer is not a rule about which routes are allowed. It is that the commit
is owned by a stage routing can never skip: the compiled commit owner at the
head of the `pr-create` deterministic runner
([PR_CREATE_STAGE.md](PR_CREATE_STAGE.md#the-commit-owner-issue-1179)). It is a
no-op whenever `feature-validate` ran.

Before adding a `change_rule` that skips a stage, ask what side effects that
stage solely owns. If any exists, move the side effect into a stage that cannot
be skipped — a documented convention is exactly what failed here.

## 2. CI fast-track (#4127, wired in #647)

`.github/workflows/ci.yml` runs a cheap `changes` job (`name: Change class`)
that shells `scripts/ci-change-class.sh`, which classifies the pull request's
diff via `nightgauge ci classify` and publishes three job outputs:

```text
run_heavy=false   change_class=docs_only   reason=…
```

The three build-and-test jobs — `go` (`Go build & test`), `sdk` (`SDK build &
test`) and `vscode` (`VSCode build & test`) — take `needs: changes` and gate
their **expensive steps** on `needs.changes.outputs.run_heavy != 'false'`.

**Deadlock-safe by construction.** Those three job names are required status
checks on the `main` ruleset. The _jobs_ therefore always run and always report;
only their inner _steps_ are conditional. A required check that never reports
blocks the pull request permanently, with no way out short of `--admin` — which
waives the entire ruleset, not one rule (AGENTS.md). Three properties hold that
line:

| Property                                        | Mechanism                                                                 |
| ----------------------------------------------- | ------------------------------------------------------------------------- |
| A skipped step still leaves the job `success`   | step-level `if:`, never job-level                                         |
| A **failed** gate cannot skip a required job    | `if: ${{ !cancelled() }}` on each gated job, not the implicit `success()` |
| A gate that reports nothing runs the full suite | `!= 'false'` — unset ≠ `'false'`                                          |

The `changes` job is intentionally **not** a required check: nothing may depend
on a gate whose only job is to decide how much work to do.

`scripts/ci-change-class.sh` never exits non-zero and fails **open** on every
error path — unknown event, missing or unresolvable SHA, unusable binary,
classifier error all emit `run_heavy=true`. Non-`pull_request` events force the
full suite: `push` to main is the merge-skew observation two individually green
PRs cannot make for themselves.

### `change_class` → workflow gate mapping

| `change_class`                              | `run_heavy` | `go` / `sdk` / `vscode` expensive steps |
| ------------------------------------------- | ----------- | --------------------------------------- |
| `docs_only`, `empty`                        | `false`     | **skipped** (jobs still report success) |
| `config_only`, `source`, `mixed`, `unknown` | `true`      | run in full                             |

"Expensive steps" is the exact scope: two steps of the `go` job stay ungated on
purpose and run on every PR, `docs_only` included — see
[What is NOT gated](#what-is-not-gated-and-why).

Config is deliberately **not** fast-tracked for CI even though the pipeline
skips `feature-validate` for it: a `package.json`/`tsconfig`/CI-workflow edit can
need build+test, and the CI workflow files themselves classify as config.

Note also that a shell script under `scripts/` matches neither the docs nor the
config globs, so it classifies as **source**. The change that first wired this
gate up touched `ci.yml` (config), `GATE_RELAXATION.md` (docs) and two scripts
(source) — `mixed`, and so gated by itself into the full matrix. That is the
intended behaviour, not an exception to it.

```bash
nightgauge ci classify --base origin/main --head HEAD --json
# {"change_class":"docs_only","run_heavy":false,"jobs":{...},"reason":"..."}
```

### What is NOT gated, and why

**Two steps inside the gated `go` job are themselves ungated**, and the rule
they share is the one to carry away: a guard whose _subject_ is the
fast-tracked class must never be gated on that class, or it switches off on
exactly the diffs it exists to inspect.

| Ungated step in `go`                    | Runs                              | Why it cannot be gated                                                                                                                                                                                 |
| --------------------------------------- | --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `CI change-class gate regression suite` | `scripts/test-ci-change-class.sh` | The gate's own self-test. Gated, the docs-only PRs — the only runs the gate acts on — become the ones that never check it, and a gate nothing exercises decays into an unconditional pass (#539/#549). |
| `Working-tree content guards`           | `go test ./internal/preflight/`   | `internal/preflight` holds every test that inspects the real tracked tree, and all four of them govern Markdown.                                                                                       |

Those four are `TestSourceFilesAreCleanUTF8` (a C1 control anywhere in a `.go`
or `.md` file — the mojibake fingerprint from #289),
`TestGoCommentsHaveNoLiteralUnicodeEscapes`,
`TestSkillIncludes_WorkingTreeIsClean` (dead `<!-- include: -->` targets, #337)
and `TestSkillPortability_WorkingTreeIsClean`. `go test ./...` is their only
enforcement path, so gating the `Test` step and stopping there would have taken
the repo's only automated Markdown guards offline for `docs_only` PRs — and a
`docs/` or `SKILL.md` edit _is_ a `docs_only` PR by definition.

No other required check substitutes. A C1 control is valid UTF-8: Prettier
preserves it byte for byte, `check-md-links.sh` inspects links rather than
bytes, and `validate-skill-metadata.sh` reads frontmatter. The failure is
concrete — a docs sweep re-encodes an em-dash through a Latin-1 round trip,
every check reports green, the PR merges, and the `push`-to-`main` run (always
heavy) goes red. That is precisely the prediction-versus-observation gap
AGENTS.md exists to close, and gating here would have made it systematically
reachable for a whole class of pull request.

The cost is ~1s: `internal/preflight` is already in `cmd/nightgauge`'s
dependency graph, so the self-test above it has warmed the build cache by the
time it runs. `scripts/test-ci-change-class.sh` pins both steps — that they
exist, that neither carries an `if:`, and that the package the second one names
still contains all four guards — so relocating a guard fails the suite instead
of going quietly dark.

`security` (`Security & license gates`, 60s) stays unconditional. govulncheck's
answer is a function of the advisory database as much as of the diff, so "this
diff cannot have introduced a vulnerability" does not imply "there is no new
vulnerability to find" — and it is the only unconditional supply-chain signal a
pull request gets.

Job outputs do not cross workflows, so the gate reaches only the jobs in
`ci.yml`. The other required checks run unfiltered:

| Required check (workflow)                             | On #646     | Gating outlook                                                                                                                                                                                |
| ----------------------------------------------------- | ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lint` (`lint.yml`)                                   | 5m15s       | Not worth it as-is: Prettier covers `**/*.md`, and the SKILL.md-metadata and skills-mirror steps are Markdown gates. Only ESLint is skippable, and it does not pay for a second classify job. |
| `link-check` (`lint.yml`)                             | 47s         | Never — it is _the_ docs gate.                                                                                                                                                                |
| `Analyze (go)` / `(javascript-typescript)` / `CodeQL` | 99s/100s/3s | Fast-follow. CodeQL supports its own `paths-ignore`, which is the right mechanism there.                                                                                                      |
| `publication boundary`                                | 1m51s       | Fast-follow, and it is a content gate — a docs PR is exactly what it exists to inspect.                                                                                                       |
| `credential scan`                                     | 14s         | Never — cost is already negligible.                                                                                                                                                           |

An earlier draft of this document called those workflows "seconds-long". They
are not: `lint` is the single most expensive required check on a documentation
pull request, and with `ci.yml` gated it becomes the wall-clock floor for one.
Runner minutes are where the win lands; wall-clock time does not fall to ~30s
and this document should not have implied it would.

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

## The CI win, measured

**Baseline — PR #646, a one-file `AGENTS.md` edit, nothing else in the diff.**
The `#646` column is that pull request's own check-run record. The projection
column was **arithmetic**, not an observation: the sum of the surviving steps'
own measured durations from the same run.

**PR #664 is the measurement.** It was the first docs-only pull request to run
against the gated workflows on `main` — a two-file markdown edit
(`AGENTS.md` + `docs/GIT_WORKFLOW.md`), classified
`change_class=docs_only run_heavy=false`.

| `ci.yml` job               | #646 (measured) | Docs-only (what still runs)                       | #664 (observed) |
| -------------------------- | --------------- | ------------------------------------------------- | --------------- |
| `Go build & test`          | 3m35s           | checkout + setup-go + the gate's own self-test    | 40s             |
| `VSCode build & test`      | 4m58s           | checkout only                                     | 5s              |
| `SDK build & test`         | 1m39s           | checkout only                                     | 6s              |
| `Security & license gates` | 1m00s           | 1m00s — deliberately ungated                      | 1m00s           |
| `Change class` (new)       | —               | checkout + setup-go + `go build ./cmd/nightgauge` | 31s             |
| **Total runner time**      | **11m12s**      | **≈2m20s (projected)**                            | **2m22s**       |

The projection landed within two seconds of the observation, which is the
expected result for arithmetic over per-step durations that were already
measured — worth recording precisely because it means the model of what the
gate removes is correct, not merely optimistic.

**All 12 required contexts still reported.** That is the load-bearing half of
the result, not the time saved: the gate suppresses expensive _steps_ inside
jobs that still run and still report, so no required check goes missing. A
skipped required context never reports, and a required status check that never
reports blocks the pull request forever — a deadlock strictly worse than the
runner minutes this saves.

The `changes` job is a `needs:` dependency, so on a change that _does_ run heavy
it delays the three build jobs by its own duration — roughly half a minute of
added wall-clock on every source PR, bought for ~9 minutes of runner time
returned on every docs PR.

Wall-clock time for a documentation PR is unchanged at ~5m15s, because `lint` is
not gated (see [What is NOT gated](#what-is-not-gated-and-why)). The saving is
runner minutes, not the wait. #664 confirmed this exactly: its `lint` job took
5m15s and was the critical path, while the entire gated `ci.yml` workflow
finished in 1m20s of wall-clock. If the wait is ever the thing worth optimizing,
`lint` is the only job left to argue about — and the reason it stays ungated is
that a `SKILL.md` or `docs/` edit is precisely a `docs_only` PR, so gating it
would take the repo's only automated markdown guard offline for exactly the
change class that needs it.

Note that the gate could not be observed on the pull request that introduced it.
A `pull_request` run uses the workflow files from the merge of head into base, so
a branch whose diff against `main` is documentation-only necessarily carries
`main`'s workflows — the ungated ones — while a branch carrying the gated
workflows necessarily has `.github/workflows/**` in its diff and classifies
`config_only`. The first observation was therefore the first docs-only PR opened
after it merged (#664, recorded above), which is also what AGENTS.md means by "a
green PR check is a prediction; `main`'s own run is the observation".

**A mislabeled "docs" change that touched source** classifies as `mixed`, runs
the full suite, and is not fast-tracked by the pipeline or the PR gates either —
`scripts/test-ci-change-class.sh` pins that with a fixture commit that really
touches a `.md` and a `.go` file rather than asserting it in prose.

### Keeping the gate honest

The defect this section exists to prevent (#647) was not a broken mechanism: the
classifier, the `nightgauge ci classify` verb and this document all shipped and
worked. Nothing connected them to a workflow, so documentation PRs paid the full
matrix for months while the doc said they did not.

`scripts/test-ci-change-class.sh` therefore asserts the **wiring** as well as the
behaviour — that `ci.yml` still declares the `changes` job, that each heavy job
still carries `needs: changes` and `!cancelled()`, and that the gated steps still
read `run_heavy`. It runs **ungated** inside the `Go build & test` job, so the
docs-only runs that the gate acts on are exactly the runs that verify it. A gate
nothing exercises decays into an unconditional pass.

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
compared against the >$6 #4121 baseline is the empirical proof for the pipeline
half; the CI half is measured from check-run durations, above.

## See also

- [CONFIGURATION.md § routing](CONFIGURATION.md#routing) — `change_rules` schema, defaults, precedence
- `internal/intelligence/changeClassifier` — the deterministic classifier (#4124)
- `internal/intelligence/routing/change_rules.go` — rule struct, defaults, precedence (#4125)
- `internal/ci/classify.go` — CI fast-track decision (#4127)
- `.github/workflows/ci.yml` — the `changes` job and the gated steps (#647)
- `scripts/ci-change-class.sh` — the workflow-facing gate; fail-open, never exits non-zero (#647)
- `scripts/test-ci-change-class.sh` — behaviour fixtures + the wiring assertions (#647)
- `internal/orchestrator/gates/relaxation.go` — gate relaxation + drift-revoke (#4128)
