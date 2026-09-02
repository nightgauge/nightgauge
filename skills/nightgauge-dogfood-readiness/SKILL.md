---
name: nightgauge-dogfood-readiness
description: Answers one question with evidence instead of opinion — is the Nightgauge pipeline
  ready to take over development of a given workspace, replacing an interactive agent session?
  Evaluates twelve objective, mechanically-checkable gates across instrumentation, provisioning,
  safety and release readiness, and returns a go / no-go with the current value of every gate.
  Use before handing a workspace to autonomous mode, before a release decision, or whenever
  someone asks "are we ready to let the pipeline build this yet?"
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Write Glob Grep Bash Task
---

# Nightgauge Dogfood Readiness

## Description

**This skill exists because the question kept being re-derived.** "Can we go back
to the pipeline for development instead of a terminal session?" has been asked
repeatedly, and each time the answer was rebuilt by hand from telemetry, docs and
`gh` queries — which meant each answer was shaped by whichever evidence that
session happened to look at.

The gates below are the durable form of that answer. Each is **objective** (a
command produces the value), **checkable** (the value is compared to a stated
threshold), and **falsifiable** (a gate can go from met to unmet when the tree
changes). Opinions about maturity are not gates.

**Scope**: this skill answers _readiness_, not _quality_. For quality use
`/nightgauge:product-audit` (8 dimensions) or `/nightgauge:pipeline-health`
(7 dimensions). Those tell you how good the system is; this one tells you whether
you can stop babysitting it.

## When to Use

- Before pointing autonomous mode at a workspace for the first time
- Before deciding whether to keep using an interactive agent for a repo's own work
- After a batch of pipeline runs, to see whether the evidence moved
- When a release decision depends on "has this actually been exercised"

## Invocation

```bash
/nightgauge:dogfood-readiness                      # this workspace
/nightgauge:dogfood-readiness --workspace <path>   # another workspace (e.g. a product repo)
/nightgauge:dogfood-readiness --gate <id>          # re-check one gate
/nightgauge:dogfood-readiness --json               # machine-readable, for CI
```

## The two failure modes this skill is built against

Both were observed in practice and both produce a confidently wrong answer:

1. **Mistaking absence of evidence for evidence of absence.** A metric that is
   empty because nothing populates it looks identical to a metric that is empty
   because the thing never happens. A survival corpus sat at 114 `pending` / 1
   `survived` and was read as "we cannot show pipeline code survives". Running
   the sweep that nothing had scheduled finalized 51 records at **100%
   survived**. The verdict store was not broken; it was unattended. **Before
   reporting a gate as unmet because data is missing, run the thing that
   produces the data.**

2. **Mistaking a config trailer for provenance.** Every commit in this repo
   carries `Co-authored-by: Nightgauge CI` because that is `git config
user.email` — not because the pipeline wrote it. Any gate counting that
   trailer reports 100% pipeline-authored when the true figure is 0%. **Attribute
   authorship from run records keyed by issue number, never from a trailer.**

## The gates

Run every gate. Report the value even when it is met — a met gate with a value
near its threshold is a warning, and a gate whose value cannot be computed is a
**finding**, not a blank.

### Tier A — instrumentation (can you measure whether it does good work?)

Tier A dominates. A pipeline that produces merged PRs you cannot evaluate is not
ready regardless of how green the other tiers are.

| #   | Gate                      | Command                                                                                                     | Threshold                               |
| --- | ------------------------- | ----------------------------------------------------------------------------------------------------------- | --------------------------------------- |
| A1  | Survival verdicts resolve | `nightgauge survival sweep` then reduce `survival-records.jsonl` by SHA, taking the terminal record per SHA | ≥20 resolved AND ≥90% `survived`        |
| A2  | Failure classification    | category histogram over the retro corpus                                                                    | `unknown` < 20%                         |
| A3  | Outcome corpus is real    | `outcomes.jsonl` — non-zero tokens/cost, AND non-empty `predictedModel`                                     | ≥20 records, `predictedModel` populated |
| A4  | Recency                   | newest `history/*.jsonl` date                                                                               | a run within the last 14 days           |

**A1 note — the journal is append-only.** `pending` lines are never rewritten;
finalization appends a terminal line for the same `merge_commit_sha`. A raw
`uniq -c` over `.verdict` therefore double-counts. Reduce by SHA first, or the
gate reports failure on a healthy store.

**A3 note** — `predictedModel` empty while tokens and cost are populated means
routing calibration is uncomputable even though cost accounting works. Report
those two halves separately; they fail independently.

### Tier B — provisioning (will it survive contact with the target repo?)

These are the assumptions the pipeline makes that the dogfood repo happens to
satisfy, which makes them invisible until a second workspace is tried.

| #   | Gate                                  | Check                                                                   | Threshold                                  |
| --- | ------------------------------------- | ----------------------------------------------------------------------- | ------------------------------------------ |
| B1  | Required labels exist                 | `gh label list` per target repo vs the labels the code reads            | every required label present in every repo |
| B2  | Skills reachable                      | resolve the skill root the renderer searches, from the target workspace | every stage skill resolves                 |
| B3  | Board provisioned                     | `nightgauge project resolve --repo <r> --json`                          | a project resolves for every repo          |
| B4  | Write containment on the path you run | containment implementation exists for CLI _and_ extension               | present on the path actually used          |
| B5  | Run records land in the right store   | launch-root vs `repo` field in history records                          | no cross-repo contamination                |

**B1 is the highest-yield gate and the cheapest to check.** A missing label that
a hard-erroring writer needs, whose error is swallowed by its caller, produces an
issue that can never leave the candidate set — and therefore an unbounded rewrite
loop bounded only by the rate limiter. Check what the code _reads_, not what the
docs list.

**B4**: a containment implementation in one language does not cover a path
written in another. Verify against the binary you will actually run.

### Tier C — safety (what happens when it is wrong?)

| #   | Gate                                  | Check                                                                       | Threshold                    |
| --- | ------------------------------------- | --------------------------------------------------------------------------- | ---------------------------- |
| C1  | Safety rails default-on               | read the defaults in source, not in docs                                    | all rails on by default      |
| C2  | No silent rail disablement            | every rail field: is an omitted key distinguishable from an explicit false? | yes for every rail           |
| C3  | Human gates reach the executing layer | trace the gate config to the layer that enforces it                         | enforced, not merely parsed  |
| C4  | Local operation needs no account      | trace the licensing/auth path                                               | nothing fails closed offline |

**C2 is subtle and worth its own check.** A `bool` field assigned unconditionally
from config takes Go's zero value when the key is omitted — so writing a
`safety_rails:` block to set one field silently sets every unlisted bool to
`false`. Numeric neighbours guarded by `if > 0` hide this by looking correct.
Flag any rail whose type cannot represent "unset".

### Tier D — release (only if the question involves shipping)

| #   | Gate                                 | Check                                                                                                                  | Threshold                                                              |
| --- | ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| D1  | Clean-install gate walked end to end | the latest `clean-install-e2e.yml` run (or `scripts/clean-install-e2e.sh` log) and the checklist's _Automated_ section | a PASS summary table with a MERGED PR, plus the checklist recording it |
| D2  | Publish path exercised               | `gh run list` for the release workflow                                                                                 | it has run at least once                                               |
| D3  | Integration contract observed green  | the platform/staging smoke workflow                                                                                    | ≥1 success                                                             |
| D4  | Dead-surface discovery has flattened | new-capability vs deletion ratio over recent PRs touching the surface                                                  | deletions no longer dominate                                           |

**D4 is the honest maturity signal** and the one most often skipped because it
has no single command. A surface under active subtraction is a surface whose
extent is not yet known. Compute the ratio over the last ~20 PRs touching the
package and state it as a number.

## Sequencing — and the trap in it

**The self-repo case is the hazardous one, not the safe one.** The intuition is
that dogfooding on your own repository is the conservative first step. The
codebase disagrees: autonomous dispatch already refuses issues in the repository
that built the running binary. A pipeline modifying the tree that produces its
own next binary can invalidate the thing evaluating it mid-run.

So when both a product workspace and the Nightgauge repo are candidates, the
lower-risk order is **product workspace first, self-hosting last** — the reverse
of the intuition. Say so explicitly in the report; a reader who believes the
opposite will read a passing score as permission for the wrong thing.

## Output

Report in this order:

1. **Verdict** — GO / NO-GO / GO-WITH-CONDITIONS, and for which workspace.
2. **Score** — gates met / total, Tier A stated separately.
3. **The gate table** — every gate, its threshold, its **current value**, met or not.
4. **What fails first** — if NO-GO, the single named failure that fires earliest,
   with the mechanism. "It is not ready" is not an answer; "within five minutes
   the refinement loop rewrites every unrefined issue because label X is missing"
   is.
5. **The cheapest experiment** — the one action that would move the most gates,
   with what result would mean "not yet".
6. **Conditions not checked** and why.

## Do not

- Do not report a gate as met from a doc claim. Docs describe intent; gates read state.
- Do not soften a threshold to produce a GO. Change the threshold in this file,
  with a reason, or report the miss.
- Do not treat "no data" as "no problem". An unpopulated metric is a Tier A miss.
- Do not infer pipeline authorship from commit trailers (see failure mode 2).

## Related

- `/nightgauge:product-audit` — 8-dimension quality audit across all repos
- `/nightgauge:pipeline-health` — 7-dimension telemetry health
- `docs/SELF_IMPROVEMENT_BOUNDARIES.md` — what the pipeline may change about itself
- `docs/AUTONOMOUS_ORCHESTRATOR.md`, `docs/CASCADE_CIRCUIT_BREAKER.md` — the safety model

## Author

nightgauge
