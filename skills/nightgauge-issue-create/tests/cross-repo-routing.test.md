# Cross-Repo Routing Tests

Behavioral tests for Phase 2.4 (Multi-Repo Sub-Issue Routing) and Phase 4.8
(Cross-Repo Project Membership Audit) of the `nightgauge-issue-create`
skill. These specifications encode the #3232 incident — sub-issues routed to
the wrong repo and project despite explicit workspace yaml routing patterns.
Every fix to the skill MUST keep these test cases passing.

## Setup Assumptions

- `.vscode/nightgauge-workspace.yaml` exists with the four repos
  (`nightgauge`, `acme-platform`, `acme-mobile`,
  `acme-dashboard`) and the `web` / `mobile` routing patterns.
- All repos are mapped to projects 1, 2, 3, 4 respectively via the
  `project_number` field on each `repositories[]` entry.
- The skill is invoked in non-interactive mode with `--auto-route` unless
  otherwise specified.

---

## TC-1: Same-Repo Epic — All Sub-Issues Route to Default

**Scenario**: An epic about the Go orchestrator with three sub-issues, all
content matching no cross-repo routing pattern.

**Input**:

- Epic title: `Epic: Tighten BudgetEnforcer mode awareness`
- Sub-issues:
  1. `Add mode field to BudgetEnforcer constructor`
  2. `Wire IPC pipeline.setMode message`
  3. `Unit tests for mode-aware budget decisions`

**Expected behavior**:

- Phase 2.4 builds a manifest where every sub-issue routes to
  `nightgauge` / project 1 (default repo, no keyword match).
- Phase 3 creates each sub-issue with `nightgauge issue create-sub` (same
  repo as epic; native sub-issue linking).
- Phase 4 calls `nightgauge project add` with `--repo nightgauge
--project 1` for each.
- Phase 4.8 audit passes; manifest file is deleted.

**Failure modes the test must catch**:

- Phase 4 calling `project add` without `--repo` / `--project` (the original
  bug — would still pass for default-repo case but is forbidden by the skill).

---

## TC-2: Cross-Repo Epic — Angular Sub-Issue Routes to Dashboard

**Scenario**: An epic with both pipeline-internal AND Angular dashboard
sub-issues. The Angular sub-issue must NOT land in the pipeline repo.

**Input**:

- Epic title: `Epic: Performance Mode Hardening`
- Epic repo: `nightgauge`
- Sub-issues:
  1. `Wire performance mode through every adapter` (no cross-repo keywords)
  2. `Calibration bucketing by (size, mode)` (no cross-repo keywords)
  3. `Angular dashboard mode filter and per-mode aggregations` (matches `web`
     pattern: `angular`, `dashboard`)

**Expected behavior**:

- Phase 2.4 manifest:
  - sub-issue 1 → `nightgauge` / project 1
  - sub-issue 2 → `nightgauge` / project 1
  - sub-issue 3 → `acme-dashboard` / project 4 (matched: `web`,
    keywords `angular`, `dashboard`)
- Phase 3 creates sub-issue 3 via `gh issue create --repo
acme/dashboard ...` with body containing
  `Part of nightgauge/nightgauge#<epic-number>`.
- Phase 3 updates the epic body to include the cross-repo sub-issue link.
- Phase 4 calls `nightgauge project add <num> --repo
acme-dashboard --project 4` for sub-issue 3.
- Phase 4.8 audit queries each issue's actual project memberships and confirms
  sub-issue 3 is in project 4 (NOT project 1).

**Failure modes the test must catch**:

- Sub-issue 3 created in `nightgauge` repo (the #3232 silent-misroute
  bug). Phase 4.8 MUST detect this and exit non-zero with a clear message.
- Sub-issue 3 added to project 1 instead of project 4. Phase 4.8 MUST detect.

---

## TC-3: Hard Gate — Routing Pattern Match But Manifest Says Default

**Scenario**: A bug or operator override forces a sub-issue to the default
repo despite content matching `web` pattern. The skill must refuse before
creating any issue.

**Input**:

- Epic title: `Epic: Mixed work with override`
- Sub-issue: `Add Angular dashboard adapter mix donut chart`
- Operator override: NONE (`--auto-route` is on, but no `--no-route`)
- Forced manifest (simulated bug): sub-issue routed to `nightgauge`
  despite content matching `web` pattern

**Expected behavior**:

- Phase 2.4 hard gate fires: detects that content matches `preferred_repo:
acme-dashboard` but manifest says `nightgauge`. Exits with
  `routing-skip-detected` error before any issue creation.

**Failure modes the test must catch**:

- Skill creates the issue anyway (bypassing the hard gate). MUST NOT happen.

---

## TC-4: Single-Repo Workspace — Routing Phase Skipped

**Scenario**: A workspace yaml is absent (single-repo setup). Phase 2.4 must
detect this and skip routing entirely.

**Input**:

- `.vscode/nightgauge-workspace.yaml` does not exist.
- Epic with three sub-issues.

**Expected behavior**:

- Phase 2.4 logs `Single-repo workspace ... skipping routing` and emits no
  manifest. Phase 3 falls back to legacy single-repo creation. Phase 4 falls
  back to project-bind via the binary's default. Phase 4.8 is skipped.

---

## TC-5: Project Number Discovery Fallback

**Scenario**: Workspace yaml exists but a repo entry is missing
`project_number`. Phase 2.4 must fall back to `gh project list` discovery.

**Input**:

- `.vscode/nightgauge-workspace.yaml` has `acme-dashboard`
  WITHOUT `project_number`.
- Sub-issue routes to `acme-dashboard`.

**Expected behavior**:

- Phase 2.4 calls `gh project list --owner nightgauge --format json`, finds the
  project whose title matches the repo's display name (`Nightgauge
Dashboard`), and uses its number for the manifest.
- All subsequent phases work as in TC-2.

**Failure modes the test must catch**:

- Skill aborts because `project_number` is missing. Fallback discovery is
  REQUIRED.
