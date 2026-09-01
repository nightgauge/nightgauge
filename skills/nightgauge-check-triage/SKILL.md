---
name: nightgauge-check-triage
description: Triage a failing CI check that no issue exists for — reproduce it, observe the running system, then fix it, in that order. Refuses to propose a fix it could not reproduce, files a type:spike instead, and emits a triage record naming the hypotheses it ruled out and the observation that ruled each one out. Use when a named check, workflow run or workflow file is red and nothing is tracking it.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
---

# Nightgauge Check Triage

## Description

Every other skill in this repository is issue-driven. An unowned red CI job has
no issue behind it, so no pipeline stage can act on it — which is how a nightly
E2E sweep stayed red on **every run for five weeks** while merge after merge
went green, the sweep being a non-required check.

This is the entry point for "this check is failing — find out why and fix it."

**It is disciplined about the order** because the two earlier attempts at that
same investigation were not. Both produced confident diagnoses derived from
reading source rather than observing the running system, and both shipped code
encoding those guesses:

- a **magic-link redelivery retry** — deliver the link, and if nothing happens
  in 25 seconds, deliver it again. Documented as diagnostic rather than a cure.
  It would have re-hidden the real bug the moment that bug was fixed, converting
  a genuine regression of "one tap signs you in" back into a green run.
- a **cold-restart probe** that reported `no session was persisted, so
verification genuinely never completed`. Verification had succeeded and the
  session _had_ persisted. The probe reported the opposite of the truth and sent
  the following session down the wrong path for its entire duration.

Neither was caught in review, because both were framed as instrumentation. The
order below — reproduce, observe, then fix — is what separates them from a real
diagnosis, and the triage record is what makes the difference checkable instead
of a matter of tone.

**Use when:**

- A named check is red and no issue tracks it.
- A scheduled workflow (nightly sweep, cron job) has been failing and nobody has
  looked.
- `nightgauge attention` or a default-branch health card surfaces a red check
  and someone has to go find out why.

**Do not use for** a pipeline stage failure with an issue behind it — that is
`/nightgauge-pipeline-triage`, which is reconciliation-first because most such
reports are false alarms. This skill assumes the failure is real, because a
check that has been red for weeks is not a false alarm.

## Arguments

| Arg                 | Meaning                                                                    | Default        |
| ------------------- | -------------------------------------------------------------------------- | -------------- |
| `--check NAME`      | The failing check's display name, e.g. `E2E Sweep`                         | —              |
| `--run URL`         | A workflow run URL, when you have one to start from                        | —              |
| `--workflow FILE`   | Workflow file or path, e.g. `e2e.yml`                                      | —              |
| `--repo OWNER/REPO` | Repository the check belongs to (**required**)                             | —              |
| `--branch NAME`     | Branch to examine history on                                               | default branch |
| `--no-fix`          | Stop after the observe phase; produce the diagnosis and the record, no fix | off            |

One of `--check`, `--run` or `--workflow` is required. `--repo` always is: a
check name means nothing without the repository it belongs to.

## Output contract

The run ends with exactly one of:

```
DIAGNOSED — <one-line cause>
  reproduced: local | ci
  ruled out:  <hypothesis> — <the observation that killed it>
  fix:        <branch> / <PR>   test: <name> (red without the fix: yes|no — <why not>)
  record:     .nightgauge/triage/<id>.json
  tracked:    <issue URL>
```

```
NOT REPRODUCED — no fix proposed
  tried:   <n> approaches (see the record)
  spike:   <issue URL>
  record:  .nightgauge/triage/<id>.json
```

There is no third outcome. "Probably X, here is a change that might help" is the
failure mode this skill exists to prevent.

## Gotchas

- **A check that has never passed is not a regression.** Looking for "what
  changed" is then a search with no target. Phase 1 answers this before anything
  else, mechanically.
- **Never push to a default branch.** Work lands on a branch and through the
  normal PR path — the merge policy in `AGENTS.md` applies unchanged.
- **UNOBSERVED-MECHANISM RULE (#1263)** — do not land a retry, a fallback, a
  widened timeout, or added tolerance for a failure whose mechanism you have not
  directly observed, and do not exempt it by calling it instrumentation. A
  diagnostic you add to a harness must be exercised against a known-good and a
  known-bad case before you trust a word it says; if that is impractical, do not
  ship it. Deliberate mitigation carries
  `NIGHTGAUGE-MITIGATION: issue=<owner/repo#N> mechanism=unobserved` beside the
  code, never prose in a doc comment. Read
  [`_shared/UNOBSERVED_MECHANISM.md`](../_shared/UNOBSERVED_MECHANISM.md) — the
  redelivery retry and the misreporting probe are the worked example, including
  why each was persuasive at the time.
- See also [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

---

## Workflow

**Phase markers**: at the start of each phase, emit
`<!-- phase:start name="{phase-name}" index={N} total=6 stage="check-triage" -->`
as an HTML comment on its own line, BEFORE any other output.

### Phase 0: Establish the Target

<!-- include: ../_shared/PREFLIGHT.md -->

Resolve the argument into a concrete workflow file and repository. From a run
URL, read the workflow from the run; from a check name, match it against the
repo's workflow files by `name:`. If it resolves to nothing, say so and stop —
guessing which workflow was meant produces an investigation of the wrong thing,
which looks exactly like an investigation of the right thing until the end.

Start the triage record now, as a JSON document you will extend through every
phase. Nothing is written until Phase 5; assembling it as you go is what keeps
the record a log of what happened rather than a summary composed afterwards.

### Phase 1: Has It Ever Been Green?

```bash
nightgauge ci history --repo "$REPO" --workflow "$WORKFLOW" --branch "$BRANCH" --limit 100 --json
```

This is first, and it is mechanical, because the answer changes what the rest of
the investigation is looking for:

- **`ever_passed: false`** — there is no regression and no "what changed". The
  check has never worked. State this explicitly in the report; do not describe
  it as broken, and do not bisect.
- **`ever_passed: true`** — the summary names the last success and the count of
  consecutive failures since. That window is the range worth diffing.

Record the verdict verbatim into `history.detail`.

### Phase 2: Reproduce — and Stop If You Cannot

Establish a reproduction, and say plainly which kind it is:

- **local** — you made the failure happen on this machine. Strongest.
- **ci** — you made it happen in CI (a re-run, an instrumented branch). Weaker,
  and still a reproduction.

Read the failing run's logs (`nightgauge ci logs <run-id> --json`) and the
workflow's own commands (`nightgauge ci discover-commands`) to build the local
invocation. Run it. Confirm it fails, and confirm it fails **for the reason the
CI log shows** — a local failure with a different error is a different bug, and
chasing it is how a session finishes triumphant and unhelpful.

**If nothing reproduces it, this phase is terminal.** Write down every approach
you tried, file a `type:spike` carrying them, emit the record, and stop. Do
**not** propose a change.

That refusal is the point of the phase. An investigation that cannot make the
failure happen has no way to tell a fix from a coincidence, and a change shipped
in that state is worse than nothing: it looks like the case is handled. The
redelivery retry above was exactly this — a plausible mitigation for an
unobserved mechanism, which would have masked the real bug the moment anyone
fixed it.

### Phase 3: Observe — Evidence Before a Cause

Capture direct evidence from the **running system** before naming a cause.
Reading the source produces hypotheses; it does not produce evidence. Print
state, dump the tree, log the value at the moment it is wrong, attach a debugger
— whatever makes the mechanism visible.

Then write down, for each candidate explanation, what became of it:

- `falsified` — and **the observation that ruled it out**.
- `supported` — and the observation that supports it.
- `untested` — considered, not investigated. Honest, and satisfies nothing.

**A report that names only its winning hypothesis does not pass this skill.**
Several explanations always fit the symptom; the one that survives contact with
the system is the one whose rivals were killed by something observed. The record
schema enforces this — a record with no falsified hypothesis is rejected by
`nightgauge triage record`.

**If you add a diagnostic to the harness to get this evidence**, the
UNOBSERVED-MECHANISM RULE applies to it. Exercise it first against a case whose
answer you already know — one known-good and one known-bad. A probe you have not
validated is a hypothesis wearing the costume of a measurement, and when it is
wrong it does not merely fail to help; it authoritatively misdirects whoever
reads it next. If validating it is impractical, do not ship it.

### Phase 4: Fix

**Fix the mechanism you observed in Phase 3, and nothing else.** A retry, a
fallback, a widened timeout or added tolerance for anything you did NOT observe
does not land here — see the UNOBSERVED-MECHANISM RULE above. If the honest
answer is that the mechanism is still unknown, this skill's outcome is
`NOT REPRODUCED` or a diagnosis without a fix, not a change that makes the
symptom go away.

Produce the change **plus a test that fails without it**. Prove that: revert the
fix on a copy and watch the test go red. A test that passes either way is
decoration, and decoration that ships as coverage is worse than no test at all,
because it tells the next person the case is guarded.

If you could not write such a test, say so plainly in `fix.no_test_reason` — "an
emulator and a live stack are needed; nothing reproduces this in unit scope" is
a legitimate answer. Implying coverage you do not have is not.

Land it on a branch and open a PR. Never push to a default branch.

### Phase 5: Record and Track

Write the record and let the binary check it:

```bash
nightgauge triage record --file "$RECORD_JSON"
```

Exit 1 means the investigation does not meet the contract, and the message names
which part. The record is written either way — a failing investigation is still
the most useful thing the next session could read, and deleting it to keep the
gate green would be the same mistake at a different level. Fix the substance,
not the JSON.

Then make sure the work is tracked:

- **Fix landed** → file or link an issue and put its URL in `tracking_issue`.
  Work that exists only in a session transcript is work the next person redoes.
- **Not reproduced** → the `type:spike` from Phase 2 goes in `spike_issue`.

Once `nightgauge finding record` exists (#102), the out-of-band findings this
investigation turns up graduate there rather than gaining a second capture path
here.

### Phase 6: Report

Emit the output contract block from above. One outcome, no hedging.

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->
