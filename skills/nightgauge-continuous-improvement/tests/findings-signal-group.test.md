# Out-of-Band Findings Signal Group Tests

Behavioral tests for Signal Group 7 (Out-of-Band Findings) and its Phase 4
routing in the `nightgauge-continuous-improvement` skill (#102). Every change
to the skill MUST keep these cases passing.

## Setup Assumptions

- Findings come from `nightgauge finding list --json`; the skill never reads
  `findings.jsonl` directly.
- The fixture file holds two findings:
  - `fd-a`: kind `gotcha`, scope `pipeline`, summary `Gate needs mikefarah yq`
  - `fd-b`: kind `precondition`, scope `project`, summary
    `Migrations must run before seed`
- `--create-issues` is left at its default (`false`) unless a case says
  otherwise.

---

## TC-1: Dogfood Mode — Both Listed, Only the Pipeline Finding Proposes

**Input**: mode `dogfood`, the fixture file.

**Expected behavior**:

- `findings` is in `DATA_SOURCES`, and Signal Group 7 runs.
- The report's Findings section lists both `fd-a` and `fd-b`.
- Exactly one proposal comes from a finding: `fd-a`, category `skill-fix`
  (kind `gotcha`), citing `fd-a` in its evidence.
- `fd-b` produces no proposal.

## TC-2: Customer Mode — Pipeline Finding Is Investigation Only

**Input**: mode `customer`, the fixture file.

**Expected behavior**:

- `fd-a` produces one `investigation` proposal titled
  `Report upstream: Gate needs mikefarah yq`, never `skill-fix` or
  `code-change`.
- `fd-b` is listed in the Findings section and produces no proposal.

## TC-3: Absent File — No Findings Section

**Input**: any mode, no `findings.jsonl` (`finding list --json` prints `[]`).

**Expected behavior**:

- `findings` is not in `DATA_SOURCES`, and Signal Group 7 is skipped.
- The report has no Findings section, and its output matches a run made
  before #102.
- No `findings.jsonl` is created.

## TC-4: Default Run Files No Issues

**Input**: mode `dogfood`, the fixture file, `--create-issues` not passed.

**Expected behavior**:

- No GitHub issue is created for any finding proposal (Step 5.4 is skipped).
- A finding that warrants an issue is pointed to
  `nightgauge discovered-work record` (ADR 027), not filed by this skill.
