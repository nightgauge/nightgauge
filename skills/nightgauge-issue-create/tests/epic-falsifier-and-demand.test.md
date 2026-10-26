# TC: Epic Falsifier and Demand Lines

Pins the #105 authoring rule: every `type:epic` body carries `Falsifier:` and
`Demand:` lines inside `## Summary`. Other types get no new requirement.

## Setup Assumptions

- Skill invoked interactively; Phase 2 drafts the body against the Epic
  Refinement Rules and the Acceptance Criteria Quality Bar in
  `_includes/environment-and-content.md`.
- The Phase 6 terminal audit runs `nightgauge-issue-audit`.

## TC-1: Epic with a clear motivating incident

**Scenario**: The requester asks for an epic to cut pipeline retries after a
linked incident where one issue retried 14 times.

**Expected behavior**:

- `## Summary` contains a `Falsifier:` line naming an observable result that
  would have killed the epic (e.g. "the retry log shows fewer than 2 retries
  per issue over the last 30 days").
- `## Summary` contains a `Demand:` line naming who asked and linking the
  incident.
- Neither line is a `##` heading.

**Failure modes the test must catch**: either line missing; boilerplate
("nothing would have killed it", "everyone wants it"); lines placed as headings
or outside `## Summary`.

## TC-2: `type:feature` request

**Scenario**: A standard feature request.

**Expected behavior**: No `Falsifier:` or `Demand:` line is required; the body
shape is unchanged from the Phase 5 contract.

**Failure modes the test must catch**: the epic rule leaking onto non-epic
types.

## TC-3: Epic where no falsifier can be named

**Scenario**: The requester cannot say what result would have stopped the
work.

**Expected behavior**: The skill stops and asks the requester. No issue is
created until a falsifier is supplied.

**Failure modes the test must catch**: the skill writes a boilerplate
falsifier and creates the epic anyway.

## TC-4: Draft missing `Falsifier:`

**Scenario**: A drafted epic body has `Demand:` but no `Falsifier:`.

**Expected behavior**: The Acceptance Criteria Quality Bar fails before Phase
3; the draft is revised before any issue is created.

**Failure modes the test must catch**: the draft reaches Phase 3 and the gap
surfaces only as a Phase 6 `MISSING_EPIC_FALSIFIER` warning.
