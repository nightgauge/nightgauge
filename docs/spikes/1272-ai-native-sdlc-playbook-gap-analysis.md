# Spike #1272: Nightgauge vs. the AI-Native SDLC Playbook — Gap Analysis

**Issue**: #1272
**Status**: Complete
**Date**: 2026-09-01

## Executive Summary

Nightgauge already implements the playbook's central claim — that every stage
commits an artifact the next stage reads, and that the agent runs up to a human
gate and not past it — so the useful output of this spike is not "adopt the
playbook" but a list of the four places where Nightgauge's version of a
prescribed practice is measurably weaker than the prescription, each backed by a
grep rather than a memory. Two of the six stages match (Plan, Build); four are
`partial`. The single sharpest gap is in Test: the skill-eval harness exists and
the check-triage skill produces a machine-readable diagnosis record, but nothing
joins them, so a triaged failure whose cause was a skill instruction leaves no
permanent regression scenario behind — the harness's own corpus is authored by
hand ([`docs/SKILL_EVALUATION.md`](../SKILL_EVALUATION.md) § _Adding a
scenario_). That is the one `adopt`. The remaining three — a review pass over
PRs the pipeline did not author, tiered statistical control bands, and an
issue-anchored audit chain that spans multiple runs and both human gates — are
real but are `defer`: each is a genuine improvement whose current absence is not
costing anything at this repository's volume, and each would add a standing
surface to maintain. Intent capture is `skip`: the property the playbook asks
for (the originator's own words survive the rewrite) is already enforced, and
the only difference is the storage medium, which the issue puts out of scope.
The playbook's metrics table is one leading and one lagging cell per stage —
twelve cells, six of which name two distinct measurements — so it names eighteen
measurements in all. Of those eighteen, two have a real producer in Nightgauge
today, eight have a partial one, and eight have none.

## 1. Method and what counts as evidence

Every `partial` and `absent` verdict below names either a document section that
shows the weaker form, or a search that returned nothing. Searches were run from
the repository root on the branch this artifact lands on, and their output is
reproduced verbatim; where a search would otherwise match this document's own
transcripts it carries `--exclude-dir=spikes`, which is the only edit made to
any command shown here. Where the playbook is
quoted it is quoted in a marked blockquote of one sentence or less; no
recommendation `title` or `body` in the block at the end of this document
contains fetched text — the materializer turns that block into GitHub issues, so
it is treated as a trust boundary and every string in it was written here.

The playbook's own summary of the property being compared:

> Every stage commits an artifact the next stage can read.

Nightgauge's equivalent chain is described end to end in
[`docs/ISSUE_TO_PR_WORKFLOW.md`](../ISSUE_TO_PR_WORKFLOW.md) and
[`docs/ARCHITECTURE.md`](../ARCHITECTURE.md): refined issue → requirements
summary → `PLAN.md` → diff → PR → merged commit → survival record → health
finding → new issue.

## 2. The six-stage comparison

| Stage    | Practice the playbook prescribes                                                                                       | Nightgauge artifact or stage playing that role                                                                                                    | Citation                                                                                                                                                       | Verdict   |
| -------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- |
| Plan     | Originator's half-formed idea becomes a committed proto-spec in their own words; owner approves it                     | Raw GitHub issue → `nightgauge-issue-refine`, which appends the untouched original body; `auto-process` triggers the refinement scan              | [`skills/nightgauge-issue-refine/`](../../skills/nightgauge-issue-refine/) SKILL.md:55; [`docs/AUTONOMOUS_ORCHESTRATOR.md`](../AUTONOMOUS_ORCHESTRATOR.md):303 | `present` |
| Design   | One session turns intent into a spec constrained by org policy, with areas of concern flagged and routed               | `issue-pickup` emits a requirements summary; the knowledge base carries the PRD and prior decisions; policy arrives as skills and `AGENTS.md`     | [`docs/ISSUE_TO_PR_WORKFLOW.md`](../ISSUE_TO_PR_WORKFLOW.md) § 2.3; [`docs/KNOWLEDGE_BASE.md`](../KNOWLEDGE_BASE.md)                                           | `partial` |
| Build    | Nothing is implemented without an accepted written plan; institutional knowledge is a file the agent reads             | `feature-planning` writes `PLAN.md` behind an approval gate; `CLAUDE.md`/`AGENTS.md`; hooks; worktree-isolated parallel sessions                  | [`docs/ISSUE_TO_PR_WORKFLOW.md`](../ISSUE_TO_PR_WORKFLOW.md) § 3.4–3.5; [`docs/HOOK_CONTRACT.md`](../HOOK_CONTRACT.md)                                         | `present` |
| Test     | The session verifies its own work first, and every production incident becomes a permanent eval                        | `feature-validate` gates on recorded execution evidence; the skill-eval harness runs a hand-authored scenario corpus                              | [`docs/STAGE_GATES.md`](../STAGE_GATES.md) § _Evidence of execution_; [`docs/SKILL_EVALUATION.md`](../SKILL_EVALUATION.md) § _Adding a scenario_               | `partial` |
| Deploy   | All PRs get an identical set of review passes with severity-ranked findings; the agent cannot pass the production gate | `nightgauge-adversarial-review` ranks findings `blocker`/`major`/`nitpick`; merge policy forbids `--admin` and leaves the forge to enforce checks | [`skills/nightgauge-adversarial-review/`](../../skills/nightgauge-adversarial-review/) SKILL.md:95; `AGENTS.md` § _Agent Operating Rules_                      | `partial` |
| Maintain | A deterministic watcher invokes the agent when a control band breaches; findings re-enter as new intent                | Health analysis converts findings to issues and epics; survival sweep detects reverted merges; the cascade breaker pauses the scheduler           | [`docs/HEALTH_MONITORING.md`](../HEALTH_MONITORING.md) § _Finding-to-Issue Workflow_; [`docs/CASCADE_CIRCUIT_BREAKER.md`](../CASCADE_CIRCUIT_BREAKER.md)       | `partial` |

## 3. Evidence for the four non-`present` rows

### 3.1 Design — the spec is never a separately-approved artifact

`issue-pickup` writes a requirements summary
([`docs/ISSUE_TO_PR_WORKFLOW.md`](../ISSUE_TO_PR_WORKFLOW.md) § 2.3) and the
knowledge scaffold holds the PRD and decision log
([`docs/KNOWLEDGE_BASE.md`](../KNOWLEDGE_BASE.md)), but both are downstream of a
single artifact: the refined issue body. The playbook's Design stage has its own
commit and its own sign-off; Nightgauge's approval for the same content is the
board move to Ready, performed by the refinement scan itself when `auto-process`
is present ([`docs/AUTONOMOUS_ORCHESTRATOR.md`](../AUTONOMOUS_ORCHESTRATOR.md):223).
The playbook's "flag areas of concern and route them to policy owners" step has
a Nightgauge analogue only mid-run, as an Action Center decision request
([`docs/ATTENTION_PRODUCERS.md`](../ATTENTION_PRODUCERS.md)) — there is no
pre-build pass that raises policy concerns for a named owner. This is a
structural difference, not a defect, and no recommendation follows from it.

### 3.2 Test — the incident-to-scenario path does not exist

The harness reads scenarios from `evals/scenarios/<skill>/*.json`. Searching for
every writer of that directory outside the harness itself returns only the
harness, its loader, its test, and its own documentation:

```
$ grep -rn "evals/scenarios" docs/ skills/ scripts/ internal/ packages/ --exclude-dir=spikes
docs/SKILL_EVALUATION.md:34:evals/scenarios/<skill>/*.json   ─┐
docs/SKILL_EVALUATION.md:68:A scenario is a declarative JSON file at `evals/scenarios/<skill>/<name>.json`:
docs/SKILL_EVALUATION.md:217:2. Write `evals/scenarios/<skill>/<name>.json` with a unique kebab-case `id`,
scripts/evaluate-skills.ts:59:const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");
packages/nightgauge-sdk/tests/eval/SkillEvalHarness.test.ts:19:const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");
packages/nightgauge-sdk/src/eval/loader.ts:20:export const DEFAULT_SCENARIOS_DIR = "evals/scenarios";
```

No pipeline stage, hook, or skill appears. The complementary search confirms the
other side: neither
[`skills/nightgauge-check-triage/`](../../skills/nightgauge-check-triage/) nor
[`skills/nightgauge-retro/`](../../skills/nightgauge-retro/) mentions a
scenario or the scenarios directory, and their only two matches on "harness"
(SKILL.md:102 and :195 in check-triage) are about a diagnostic harness in the
triage sense, not the skill-eval one. Corpus authoring is
documented as a wholly manual seven-step procedure
([`docs/SKILL_EVALUATION.md`](../SKILL_EVALUATION.md) § _Adding a scenario_),
and the same document states in its own words that nothing runs the harness on a
PR.

The half that _does_ exist is worth naming precisely, because it bounds the gap.
Check-triage already refuses to close a diagnosis without a test that was red
without the fix — its output contract carries
`test: <name> (red without the fix: yes|no — <why not>)`
([`skills/nightgauge-check-triage/`](../../skills/nightgauge-check-triage/)
SKILL.md, § _Output contract_). So an incident caused by **product code** already
becomes a permanent regression test by the ordinary route. The uncovered subset
is an incident whose cause was a **skill instruction or agent configuration** —
exactly the class the eval harness exists for, and exactly the class that leaves
no artifact today.

### 3.3 Deploy — review runs in one direction

No workflow runs an agent review pass on pull requests. The search for the two
mechanisms that would do it returns a single file:

```
$ grep -rlniE "pull_request_target|claude-code-action" .github/workflows/
.github/workflows/cla.yml
```

That one match does not review a diff — `cla.yml` uses `pull_request_target`
to check whether a contributor has signed the licence agreement. Five other
workflows do fire on `pull_request` (`ci.yml`, `codeql.yml`, `lint.yml`,
`credential-scan.yml`, `publication-boundary.yml`), and
CodeQL in particular emits severity-ranked findings, so the claim is not that a
PR here is unchecked. It is that every one of those is a fixed analyser applying
a rule set: none reads the change against the plan it was supposed to implement,
which is what the playbook's review passes are for. And there is no
repository-level definition of what those passes are:

```
$ ls REVIEW.md .github/REVIEW.md
ls: .github/REVIEW.md: No such file or directory
ls: REVIEW.md: No such file or directory
```

The playbook's prescription for the pass set is a single sentence:

> All PRs get an identical set of review passes, with findings ranked by
> severity.

Nightgauge's review passes are real and severity-ranked, but they live inside
skills that the authoring run invokes on itself:
[`skills/nightgauge-adversarial-review/`](../../skills/nightgauge-adversarial-review/)
fans out four fresh-context critics and merges findings as
`blocker`/`major`/`nitpick`, and
[`skills/nightgauge-feature-validate/`](../../skills/nightgauge-feature-validate/)
runs the build-and-test gate. A PR opened by a human — or by any tool that is not
this pipeline — receives neither. The half the playbook cares most about is
present and stronger than prescribed: `AGENTS.md` forbids `--admin` as a routine
path precisely so the gate is enforced by the forge rather than by an agent's
judgement — the same rule as the playbook's governance line that an agent may
act up to the production gate and not past it.

### 3.4 Maintain — thresholds exist, bands do not

The playbook's Maintain loop starts from a specific mechanism:

> A deterministic script watches production and invokes Claude when a control
> band is breached.

Statistical dispersion is computed in several places, but nothing bands a
metric. The search across the analysis surface, excluding this artifact's own
self-hits under `docs/spikes/`:

```
$ grep -rniE "std ?dev|standard deviation|sigma|σ" docs/ packages/nightgauge-sdk/src/ --exclude-dir=spikes
docs/HEALTH_MONITORING.md:153:overallScore = round(Σ(dimensionScore[d] × weight[d]) / Σ(weight[d]))
docs/HEALTH_MONITORING.md:301:| `coefficientOfVariation` | `stdDev / mean` — cost predictability              |
docs/HEALTH_MONITORING.md:302:| `anomalyRate`            | Fraction of runs exceeding mean + 2σ threshold     |
docs/HEALTH_MONITORING.md:308:- Cost anomalies detected (runs exceeding mean + 2σ)
docs/HEALTH_MONITORING.md:1009:| `packages/nightgauge-sdk/src/analysis/health/statistics.ts`                    | Statistical utilities (trend, percentile, mean, stddev)                   |
docs/decisions/011-model-eval-system.md:95:**N times** (default 3) on a sampled cell; if the score's standard deviation
packages/nightgauge-sdk/src/analysis/health/dimensions/costHealth.ts:66:/** Identify the issueNumbers whose per-run cost exceeds mean + 2*stdDev. */
packages/nightgauge-sdk/src/analysis/health/dimensions/costHealth.ts:254:      description: `${anomalyCount} of ${sampleSize} pipeline run(s) exceeded the anomaly threshold (mean + 2σ = $${anomalyThreshold.toFixed(4)}).`,
packages/nightgauge-sdk/src/analysis/health/statistics.ts:174: * Compute standard deviation of a numeric array. Returns 0 for arrays with < 2 elements.
packages/nightgauge-sdk/src/__tests__/analysis/health/statistics.test.ts:140:  it("returns correct sample standard deviation for [2,4,6]", () => {
packages/nightgauge-sdk/src/__tests__/analysis/health/statistics.test.ts:141:    // sample stddev of [2,4,6]: mean=4, diffs=[-2,0,2], sq=[4,0,4], sum=8, /2=4, sqrt=2
packages/nightgauge-sdk/src/__tests__/analysis/health/dimensions/costHealth.test.ts:85:    // One run costs 10x the normal amount — exceeds mean + 2σ
packages/nightgauge-sdk/src/eval/qualityScorer.ts:196:    if (stddev(scores) > threshold) lowConfidence.add(dimension);
packages/nightgauge-sdk/src/eval/qualityScorer.ts:202:function stddev(xs: number[]): number {
```

Line order there is `grep`'s directory traversal order, which is a property of
the checkout rather than of the tree's content, so a fresh run elsewhere may
emit the same fourteen lines in a different sequence; the set of matches is what
carries the argument, not their order.

Two places in that list use dispersion to drive a live decision, and neither is
a band.
`costHealth.ts`:66 and :254 flag a run whose cost exceeds `mean + 2σ`, and
`qualityScorer.ts`:196 marks an eval dimension `low_confidence` when the spread
of its repeated scores crosses a configured threshold — the reliability guard
documented at
[`docs/decisions/011-model-eval-system.md`](../decisions/011-model-eval-system.md):95.
Both are a single cut yielding a binary in-band/out-of-band verdict. The
remainder are not triggers at all: `coefficientOfVariation` is a reported
statistic, `statistics.ts`:174 is the shared helper and its tests, and the Σ at
`HEALTH_MONITORING.md`:153 is a summation rather than dispersion. Nothing in the
list produces the playbook's graduated ladder, in which the same metric crossing
1σ, 2σ and 3σ draws three different responses.

Every trigger on the operational path is a fixed constant: health status cuts at
90/70/50/30, `severityThreshold: "high"` decides which findings become issues,
`epicGroupingThreshold: 3` decides when they become an epic
([`docs/HEALTH_MONITORING.md`](../HEALTH_MONITORING.md) § _Health Status and
Severity_), and the cascade breaker trips on a count inside a window
([`docs/CASCADE_CIRCUIT_BREAKER.md`](../CASCADE_CIRCUIT_BREAKER.md) § _How It
Trips_). Two consequences follow. There is no graduated response — the cascade
breaker's only move is a full stop that deliberately excludes itself from any
auto-resume path, so the playbook's three-tier ladder — record at the innermost
band, diagnose at the middle one, propose a change at the outermost — has no
analogue. And there is no deterministic watcher that invokes an agent on
breach: the closest thing is the health analysis run, which a human or a schedule
starts, and whose findings become issues rather than a diagnosis of the specific
excursion.

The Maintain loop's _other_ half is present and does not need work. Findings
re-enter the pipeline as new issues through the finding-to-issue engine
([`docs/HEALTH_MONITORING.md`](../HEALTH_MONITORING.md) § _Finding-to-Issue
Workflow_), and epic #477 is the existing home for the narrower case of work
discovered mid-run; per that epic's framing this spike cites it rather than
proposing a second mechanism beside it.

## 4. Verdicts on the five candidate gaps

### 4.1 Stage 1 (Plan) — intent capture → **skip**

Both halves of the question resolve in Nightgauge's favour and no work follows.
An operator does have a low-friction path from a half-formed idea to a
board-ready issue: open an issue in any shape, add `auto-process`, and the
refinement scan rewrites it with structured acceptance criteria and moves it to
Ready ([`docs/AUTONOMOUS_ORCHESTRATOR.md`](../AUTONOMOUS_ORCHESTRATOR.md):223,
:303). The original intent is preserved rather than overwritten: the refine skill
lists preservation as its first stated principle and always appends the prior
body verbatim inside a `<details>` block, even when that body was already
structured
([`skills/nightgauge-issue-refine/`](../../skills/nightgauge-issue-refine/)
SKILL.md:55 and § _Body Construction Rule_), and the autonomous path reaches the
same skill rather than rewriting the body itself. What differs from the playbook
is only the medium — a GitHub issue body instead of a committed file — and the
issue that commissioned this spike puts adopting the playbook's file names
verbatim out of scope where an existing artifact already plays the role. There is
a residual weakness worth stating without acting on it: an issue body has no
revision protection, so a later edit can drop the preservation block silently,
whereas a committed file cannot lose history. At single-maintainer volume that
has not happened and detecting it would cost more than it saves.

### 4.2 Stage 4 (Test) — every incident becomes a permanent eval → **adopt**

Confirmed absent by the two searches in § 3.2, and it is the one gap where the
missing piece is small and the pieces it would join are both already built and
already machine-readable. Check-triage writes a structured record to
`.nightgauge/triage/checks/<id>.json` naming the diagnosis, the hypotheses ruled
out, and the observation that ruled each one out; the harness consumes a
declarative JSON scenario with a `failure_mode`, a prompt, and assertions. The
recommendation is deliberately scoped to the subset § 3.2 isolates — failures
whose cause was a skill instruction or agent configuration — because failures in
product code are already converted into permanent regression tests by the triage
skill's own output contract, and generating scenarios for those too would produce
a corpus of tests that duplicate the real suite. Two constraints from the
existing tree must carry into the work: a generated scenario has to fail the
non-answer sentinel like every hand-written one, or it is a scenario that cannot
go red; and the generated scenario must be shown red against the pre-fix
configuration before it is committed, for the same reason. This spike does not
propose the CI gate that would run the harness on a PR — the eval document
already records that gap in its own words and against its own tracking reference,
and duplicating it here would create a second owner for one problem.

### 4.3 Stage 5 (Deploy) — bidirectional review → **defer**

Real, confirmed by § 3.3, and worth building eventually — but not now. The value
of the playbook's version is that a human's PR gets the same passes an agent's PR
gets, which matters in proportion to how many PRs arrive from outside the
pipeline. In this repository nearly every PR is pipeline-authored and therefore
already receives the passes; the uncovered population is the maintainer's own
hand-merged changes and future external contributions. Against that, standing it
up is not small: it needs a workflow with a review trigger, a repository-level
definition of the passes and their severities (the `REVIEW.md` role, which § 3.3
shows has no file today), a nit cap so the volume stays readable, and an identity
for the reviewing agent that is distinct from the authoring one — the playbook is
explicit that the agent which wrote code must not approve it, and a shared
identity would quietly violate that. Deferred rather than skipped because the
argument flips the moment external contributions become routine, and the design
should be on the backlog before that happens rather than after.

### 4.4 Stage 6 (Maintain) — control bands with tiered response → **defer**

§ 3.4 shows the substrate is already there — a statistics module, a rolling
window, and one dimension that already computes `mean + 2σ` — so this is not a
build-from-nothing proposal, which is what keeps it off `skip`. But the honest
read of the current volume is that tiered bands would not reduce false pages,
because there are no pages: the cascade breaker fires at most once per trip by
design and deliberately requires operator triage to clear, and health findings
arrive as backlog issues rather than interrupts. Bands earn their cost when a
metric is watched continuously by something that acts on its own, and Nightgauge
has no such watcher today; adding the ladder before adding the watcher would be
configuration nobody reads. It is also the recommendation with the largest
failure mode if done carelessly: a band over a metric with an unstable baseline
generates confident anomalies out of noise, and the repository's own rules forbid
landing a mitigation for a mechanism nobody has observed. Deferred with the scope
narrowed accordingly — one metric that demonstrably has a stable baseline,
version-controlled tier boundaries, and read-only tooling at the diagnose tier.

### 4.5 Audit trail as a chain of commits → **defer**

Partially recoverable today, and the shortfall is specific enough to name. The
lifecycle trace is a per-run append-only JSONL capturing every stage boundary and
every decision with its rationale and rejected alternatives, and `trace export`
joins it with the run record and the exit records on a shared `run_id`
([`docs/GO_BINARY.md`](../GO_BINARY.md) § _Trace Operations_). The human half of
the chain is better than expected: a decision-request event carries the resolving
actor and the option chosen, so an in-run human gate is attributable. Three
things break the "one issue number recovers the whole chain" property. `trace
show <issue>` resolves the most recent traced run for that issue, so an issue
that took three runs surfaces one of them and the earlier reasoning is reachable
only by run id. The terminal human gate — the person who actually merged the PR
— is not a trace event at all; it lives in the forge's PR history, and the
merged-commit half of the chain is recorded separately as a survival record keyed
on the merge SHA ([`docs/GO_BINARY.md`](../GO_BINARY.md) § _Survival
Operations_). And runs predating trace capture have no file, which is documented
and acceptable but means the chain is only complete going forward. Deferred
rather than adopted because every one of those three is recoverable by hand today
from artifacts that already exist — the cost is an operator's time during a
post-mortem, not a lost record.

### 4.6 Per-stage metrics mapping → **defer**

The mapping in § 5 finds eight of eighteen with no producer and eight more with
only a partial one. Filing one issue per missing metric would be noise; the three
that would actually change a decision — per-change first-pass merge rate, plan
fidelity, and a change failure rate over survival verdicts that are already
recorded — are grouped into a single deferred recommendation, and the rest are
recorded here as deliberately unbuilt.

## 5. Playbook metrics mapped to Nightgauge producers

| Playbook metric                                                       | Stage    | Nightgauge producer                                                                                                                                            | Status  |
| --------------------------------------------------------------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| Time from first conversation to committed intent                      | Plan     | none — the refinement scan records no latency for the raw-issue-to-refined transition                                                                          | none    |
| Survival rate of ideas (accepted vs. closed)                          | Plan     | none — survival records measure whether a merged change held up, not whether an idea was accepted                                                              | none    |
| Edits to the intent made after the spec exists                        | Plan     | none — a refined issue body's later edits sit in GitHub's edit history and nothing reads them (§ 4.1)                                                          | none    |
| Elapsed time between intent and spec                                  | Design   | none — Nightgauge has no separately-committed spec artifact to timestamp (§ 3.1)                                                                               | none    |
| Requirements rework after build starts                                | Design   | partial — `SCOPE_DISCOVERED` backtracks are trace events, but nothing aggregates them as a rate                                                                | partial |
| Share of changes merging from the first pass                          | Build    | partial — `overallFirstAttemptPassRate` is per stage execution, not per change through to merge                                                                | partial |
| Time from plan approval to merged PR                                  | Build    | partial — `avgRunDurationMs` / `p95RunDurationMs` cover the whole run, not the plan-approval span                                                              | partial |
| Rework cycles per change                                              | Build    | partial — `autoRecoveryRate` plus backtrack trace events; not surfaced as a per-change count                                                                   | partial |
| How often the merged diff matched the saved plan                      | Build    | none — nothing compares the files `PLAN.md` named against the files the merged diff touched                                                                    | none    |
| First-pass CI success rate                                            | Test     | partial — `successRate_{stage}` for `feature-validate` proxies it; post-merge CI is not counted                                                                | partial |
| Review time per PR                                                    | Test     | partial — `bottleneckAvgDurationMs` and the per-stage P95 time the in-run review passes; the PR's own open-to-first-review interval is never read              | partial |
| Change failure rate                                                   | Test     | partial — survival records carry a revert/breakage verdict per single-issue squash merge; nothing aggregates them into a rate and no incident tracker feeds it | partial |
| Time to first review                                                  | Deploy   | none — follows from § 3.3; with no review pass on unauthored PRs there is nothing to time                                                                      | none    |
| Share of review comments resolved without a human touching the branch | Deploy   | none — in-run review findings are consumed and never counted, and § 3.3 shows unauthored PRs get no pass to comment on                                         | none    |
| Defects caught pre-merge vs. post-production                          | Deploy   | partial — survival records give the post-merge half; adversarial findings are not persisted                                                                    | partial |
| Share of findings that become merged fixes                            | Maintain | `recommendationFollowThroughRate` and `recommendationEffectivenessRate`                                                                                        | present |
| Repeat incidents over time                                            | Maintain | the learning-effectiveness dimension's recurring-findings check (same title opened and closed 2+)                                                              | present |
| Time from band breach to a queued finding                             | Maintain | none — no bands exist to breach (§ 3.4)                                                                                                                        | none    |

Producer definitions are in
[`docs/HEALTH_MONITORING.md`](../HEALTH_MONITORING.md) § _The 8 Health
Dimensions_; the surfaces that read them are
[`skills/nightgauge-pipeline-health/`](../../skills/nightgauge-pipeline-health/)
and [`skills/nightgauge-pipeline-audit/`](../../skills/nightgauge-pipeline-audit/).
Four of the eight `none` rows are structural and stay unbuilt on purpose: the two
Plan-stage timings, the count of intent edits made after the spec exists, and the
intent-to-spec elapsed time all presuppose separately committed intent and spec
artifacts, which § 4.1 declines and § 3.1 explains Nightgauge does not have.
Three more are downstream of decisions already taken here — time to first review
and the share of review comments resolved without a human touching the branch
both follow the § 4.3 defer, and band-breach latency follows § 4.4. That leaves
one genuinely missing measurement, plan fidelity, whose absence hides something a
maintainer would act on: if most merged diffs depart from the plan that was
approved before them, the planning stage is producing documents nobody follows
and nothing today would say so. It pairs naturally with two of the `partial`
rows — first-pass merge rate, which is measured per stage execution rather than
per change, and change failure rate, whose per-merge verdicts are already
captured but never aggregated — and the three together are the last deferred
recommendation below.

## 6. Out of scope

Adopting the playbook's file names where an existing artifact already fills the
role; any hosted-service or commercial framing; and the missing CI gate for the
skill-eval harness, which is already recorded as a known gap in
[`docs/SKILL_EVALUATION.md`](../SKILL_EVALUATION.md) and must keep one owner.

## Spike Contract (Path A)

Path A — Recommendations. Same-repo spike; follow-up issues are filed by
`spike-materialize` from the block below when this PR merges, per
[`docs/SPIKE_CONTRACT.md`](../SPIKE_CONTRACT.md).

**Artifact**: [`docs/spikes/1272-ai-native-sdlc-playbook-gap-analysis.md`](1272-ai-native-sdlc-playbook-gap-analysis.md)

## Recommendations

```yaml recommendations
spike: 1272
recommendations:
  - id: triage-record-to-skill-eval-scenario
    action: adopt
    title: "eval: generate a regression scenario from a triage record whose cause was a skill instruction"
    type: feature
    priority: high
    size: M
    labels: ["component:skills"]
    body: |
      A triaged failure caused by product code already leaves a permanent
      regression test behind: the check-triage output contract refuses to close
      a diagnosis without naming a test and stating whether it was red without
      the fix. A triaged failure whose cause was a skill instruction or agent
      configuration leaves nothing — the skill-eval corpus under
      `evals/scenarios/` is authored entirely by hand, and no stage, hook or
      skill writes into it.

      Close that half. When a triage or retro run concludes that the cause lay
      in a skill's instructions rather than in product code, emit a scenario
      stub for the affected skill from the diagnosis record, carrying the
      failure mode, a prompt that reproduces the condition, and assertions
      derived from the corrected behaviour, plus the matching mock fixture.

      Two constraints are non-negotiable, both inherited from defects the
      harness already guards against elsewhere:

      - the generated scenario must fail the harness's non-answer sentinel like
        every hand-written one, or it is a scenario that cannot go red;
      - it must be observed failing against the pre-fix configuration before it
        is committed, for the same reason — a scenario that was never seen red
        is decoration.

      Out of scope: running the harness as a required check on a PR. That gap
      is separately recorded and must keep a single owner.
    depends_on: []

  - id: review-pass-on-unauthored-prs
    action: defer
    title: "review: run the severity-ranked review passes on pull requests the pipeline did not author"
    type: feature
    priority: medium
    size: L
    labels: ["component:ci"]
    body: |
      The adversarial-review critics and the feature-validate gate are invoked
      by the run that authored the change, so a pull request opened by a human
      or by any tool outside this pipeline receives neither. The workflows in
      `.github/workflows/` that fire on a pull request are all fixed analysers
      (CodeQL, lint, static analysis, credential and publication-boundary
      scans); none invokes an agent to read the change against the plan it was
      meant to implement, and there is no repository-level definition of what
      the review passes are or how their findings are ranked.

      Build the missing half: a review trigger on incoming pull requests, a
      committed definition of the passes (correctness, security, compliance
      with the plan) and their severity ladder, a cap on low-severity findings
      so the output stays readable, and findings posted in a form a human
      reviewer consumes rather than a wall of comments.

      One rule constrains the design and must not be traded away: the identity
      that reviews a change must not be the identity that authored it, and
      review findings must not approve or block on their own — the forge's
      required checks and human approval stay the gate.

      Deferred because nearly every pull request here is pipeline-authored and
      already receives the passes. The argument flips as soon as external
      contributions become routine.
    depends_on: []

  - id: tiered-control-bands-one-metric
    action: defer
    title: "health: tiered control bands over one stable metric, with version-controlled response tiers"
    type: feature
    priority: medium
    size: M
    labels: ["component:sdk"]
    body: |
      Every trigger in health analysis is a fixed constant except the cost
      dimension's `mean + 2σ` anomaly check, and the cascade breaker's only
      response is a full stop that requires operator triage to clear. There is
      no graduated ladder between "nothing happened" and "the scheduler is
      paused", and no deterministic watcher that invokes an agent on an
      excursion.

      Add one, narrowly. Pick a single metric that demonstrably has a stable
      baseline over the available history, band it against a rolling mean and
      standard deviation, and define the response tiers in a version-controlled
      file rather than in code: record only at the innermost band, diagnose
      with read-only tooling at the middle band, and open a finding at the
      outermost. Reuse the existing statistics module rather than adding a
      second one.

      The failure mode to design against is a band over a metric whose baseline
      is not stable, which manufactures confident anomalies out of noise. Prove
      the baseline before shipping the band, and do not add tolerance or
      widening for an excursion whose mechanism has not been observed.

      Deferred because at current volume there are no false pages for bands to
      reduce, and the ladder is only worth its maintenance once something acts
      on the metric continuously.
    depends_on: []

  - id: issue-anchored-decision-chain
    action: defer
    title: "trace: make the full decision chain recoverable from one issue number across every run"
    type: feature
    priority: medium
    size: M
    labels: ["component:go-binary"]
    body: |
      The lifecycle trace records every stage boundary and every decision with
      its rationale and its rejected alternatives, and in-run human gates are
      attributable because a decision-request event carries the resolving actor
      and the option chosen. Three things stop an issue number from recovering
      the whole chain:

      - `trace show <issue>` resolves only the most recent traced run, so the
        reasoning of an issue's earlier attempts is reachable only by run id;
      - the terminal human gate — who merged the pull request — is not a trace
        event, and lives only in the forge's history;
      - the merged-commit outcome is keyed on the merge SHA in a separate
        store, joined to the run only by convention.

      Make the issue the anchor: list every traced run for an issue rather than
      the last one, and have the export join the merge approval and the
      post-merge outcome onto the same document so one command answers "who
      asked, what did the agent decide and why, who approved it, and did it
      hold".

      Runs predating trace capture have no file. That stays true and is not a
      bug to fix — the property is forward-looking by construction.

      Deferred because all three are recoverable by hand today from artifacts
      that already exist; the cost is an operator's time during a post-mortem,
      not a lost record.
    depends_on: []

  - id: per-change-merge-fidelity-and-failure-metrics
    action: defer
    title: "health: measure per-change first-pass merge rate, plan fidelity, and change failure rate"
    type: feature
    priority: low
    size: M
    labels: ["component:sdk"]
    body: |
      Health analysis measures first-attempt pass rate per stage execution and
      total run duration, neither of which answers the questions a maintainer
      actually asks about a change: did it reach a merge without going back,
      did what merged resemble the plan that was approved before it was
      written, and did it stay merged.

      Add all three as derived metrics over data the pipeline already emits —
      stage executions, backtrack events, the committed plan, and the survival
      records — rather than as new instrumentation:

      - a per-change first-pass merge rate that counts backtracks and re-runs
        against the issue rather than the stage;
      - a plan-fidelity signal comparing the files the plan named against the
        files the merged diff touched;
      - a change failure rate over the terminal survival verdicts, which
        already say per merged change whether it was reverted or broke the
        default branch but are never aggregated into a rate.

      Plan fidelity is a signal, not a gate. A change that departs from its
      plan for a good reason is normal; the value is in noticing that it
      happens on most changes, which would mean the planning stage is producing
      documents nobody follows.

      The change failure rate has one honest caveat to carry into the
      implementation: records still inside their observation window are not
      failures, and counting them as successes would report a falsely low rate
      on recent work. Compute it over finalized verdicts only and say how many
      were excluded.

      Deferred: none of the three changes a decision at current volume, and all
      three are cheap to add later because the inputs are already recorded.
    depends_on: []
```
