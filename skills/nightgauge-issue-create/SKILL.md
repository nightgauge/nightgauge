---
name: nightgauge-issue-create
description: Create well-structured GitHub issues with SDLC metadata, project board sync,
  and optional parent/child linking. Use when filing a new issue or epic that the
  pipeline will pick up, so it lands board-ready with correct labels and links.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.22.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---

# Issue Create

Create GitHub issues that are immediately usable by the Nightgauge
pipeline.

## Outcomes

- Creates issue with complete SDLC metadata
- Ensures consistent labels for type, priority, size, and status
- Assigns milestone when required by repo workflow
- Optionally links to parent issue/epic
- Adds created issue to the GitHub Project board via deterministic hook
- Optionally scaffolds a knowledge directory at creation time
  (`--with-knowledge`)
- Separates execution-ready implementation work from decision-oriented spikes

## Required Metadata

Each created issue must include:

- Exactly one `type:*` label
- Priority and Size set as project board fields (not labels) — add to board
  with `nightgauge project add`, then set fields via GraphQL mutations
  (see Phase 4)
- Milestone when required by team process
- A clear issue intent: implementation or spike

Use repository label conventions documented in `docs/ISSUE_TO_PR_WORKFLOW.md`
when present. If label docs are missing, infer from existing repo labels using
`nightgauge label list --json`.

## References

- Configuration: `docs/CONFIGURATION.md`
- Project board behavior: `docs/PROJECT_SETUP.md`
- Issue quality expectations: `docs/ISSUE_TO_PR_WORKFLOW.md` and
  `CONTRIBUTING.md`
- Context and pipeline expectations: `docs/CONTEXT_ARCHITECTURE.md`

Do not inline large label taxonomies or template text in this skill.

## Supporting files (load on demand)

- `skills/nightgauge-issue-create/_includes/environment-and-content.md` — read in Phases 1, 2
- `skills/nightgauge-issue-create/_includes/epic-routing.md` — read in Phases 2.4, 2.5, 2.6
- `skills/nightgauge-issue-create/_includes/spike-routing.md` — read in Phases 2.7, 2.X
- `skills/nightgauge-issue-create/_includes/scope-gates.md` — read in Phases 2.8, 2.85, 2.9
- `skills/nightgauge-issue-create/_includes/create-and-dependencies.md` — read in Phases 3, 3.5
- `skills/nightgauge-issue-create/_includes/board-and-audit.md` — read in Phases 4, 4.5, 4.6, 4.7, 4.8, 4.9, 5, 6

## Gotchas

- **Persist the routing manifest BEFORE any forge mutation (#3232).** Epics with
  sub-issues silently misroute to the wrong repo/project without it. The manifest
  must exist and validate before a single issue is created.
- **Oversized scope must be gated BEFORE creation (#3811).** One oversized issue
  burned $112.77 in feature-dev. Decompose into an epic or attach an explicit
  oversized-scope marker first.
- **Board sync needs BOTH `--repo` and `--project` — no defaults.** Relying on
  defaults caused the #3232 silent-misroute. Every created issue is added with
  both flags explicit.
- **Never block a sub-issue on its own epic.** That creates a dependency cycle;
  use `addBlockedBy` only between siblings/prerequisites.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Validate Environment

Verify the Go binary, `GITHUB_TOKEN`, and a GitHub-backed remote are present before any work; stop with an exact remediation command if a prerequisite fails.

> **Read `skills/nightgauge-issue-create/_includes/environment-and-content.md` now and follow its instructions before continuing this phase.**

### Phase 2: Build Issue Content

Classify the issue (implementation vs spike), draft a concise, actionable title and body with acceptance criteria, determine labels, and size the issue from the complexity model and file-based heuristics.

> **Read `skills/nightgauge-issue-create/_includes/environment-and-content.md` now and follow its instructions before continuing this phase.**

### Phase 2.4: Multi-Repo Sub-Issue Routing (Mandatory for Epics)

**Gate**: Runs UNCONDITIONALLY for any epic with sub-issues; skipped for standalone issues. Build, hard-gate, and persist a routing manifest mapping each sub-issue to its target repo/project BEFORE any GitHub mutation — the #3232 footgun defense.

> **Read `skills/nightgauge-issue-create/_includes/epic-routing.md` now and follow its instructions before continuing this phase.**

### Phase 2.5: Parallel Decomposition (Agent Teams)

**Gate**: Check `agent_teams.enabled`; if disabled, skip to Phase 2.6. Detect dependencies, compute execution waves, assign per-issue complexity and model suggestions, and embed dependency metadata in each sub-issue body.

> **Read `skills/nightgauge-issue-create/_includes/epic-routing.md` now and follow its instructions before continuing this phase.**

### Phase 2.6: Dependency Analysis (Always for Epics)

**Gate**: Runs for epics with 2+ sub-issues when Phase 2.5 was skipped. Use the Go binary (`epic plan-waves`) for deterministic wave assignment, with a prose-based fallback when sub-issues are not yet created.

> **Read `skills/nightgauge-issue-create/_includes/epic-routing.md` now and follow its instructions before continuing this phase.**

### Phase 2.7: Spike + Dependent Implementation Routing

**Gate**: Runs for epics with a spike + dependents, or cross-repo dependents sharing an architectural decision. Route each dependent group through exactly one of Path A (recommendations), Path B (concurrent siblings — triggers the single-point-of-failure guard), or Path C (spike-with-implementation, the cross-repo default).

> **Read `skills/nightgauge-issue-create/_includes/spike-routing.md` now and follow its instructions before continuing this phase.**

### Phase 2.8: Cross-Repo Reality Check (Recommended for API Issues)

**Gate**: Runs when the issue references platform API endpoints, cross-repo dependencies, or companion-repo integration; skip for purely internal issues. Verify endpoint/companion-repo assumptions against reality and record a `## Cross-Repo Dependencies` section in the body.

> **Read `skills/nightgauge-issue-create/_includes/scope-gates.md` now and follow its instructions before continuing this phase.**

### Phase 2.85: Oversized-Scope Hard-Gate

**Gate**: Runs UNCONDITIONALLY for **every issue type**. **Blocking** — an issue bundling many independent targets/refactors MUST be decomposed into an epic, or carry an explicit `<!-- nightgauge:oversized-scope-accepted -->` marker, before any GitHub mutation (the #3811 $112-runaway defense).

> **Read `skills/nightgauge-issue-create/_includes/scope-gates.md` now and follow its instructions before continuing this phase.**

### Phase 2.9: Epic Decomposition Hard-Gate

**Gate**: Runs UNCONDITIONALLY when `TYPE_LABEL=epic`; skipped for non-epics. **Blocking** — every epic creation MUST fall into one of three explicit shapes (Path A sub-issues, Path B placeholder chore, Path C standalone declaration) before any GitHub mutation.

> **Read `skills/nightgauge-issue-create/_includes/scope-gates.md` now and follow its instructions before continuing this phase.**

### Phase 2.X: Spike Artifact Path Selection

**Gate**: Only runs for `type:spike` issues. Compute, validate, and scaffold the artifact path (`docs/spikes/<N>-<slug>.md` and the `yaml recommendations` block) so the issue is spike-contract-conformant by default and `feature-validate` has a concrete path to check.

> **Read `skills/nightgauge-issue-create/_includes/spike-routing.md` now and follow its instructions before continuing this phase.**

---

### Phase 3: Create Issue Deterministically

Create the issue with `nightgauge issue create`/`create-sub`, capturing the issue number. For epics, the Phase 2.4 routing manifest is the mandatory source of truth for every sub-issue's target repo — never re-derive routing. Includes the `type:spike` pre-creation validation hard-gate.

> **Read `skills/nightgauge-issue-create/_includes/create-and-dependencies.md` now and follow its instructions before continuing this phase.**

### Phase 3.5: Set Dependency Relationships (Epic Sub-Issues)

**Gate**: Always runs for epics with 2+ sub-issues. Set GitHub's native `addBlockedBy` relationships (cross-epic in Step 1, intra-epic in Step 2) so the pipeline respects execution ordering — body text and labels are invisible to it.

> **Read `skills/nightgauge-issue-create/_includes/create-and-dependencies.md` now and follow its instructions before continuing this phase.**

### Phase 4: Sync Project Board and Set Fields (Mandatory)

All created issues MUST be added to the project board with Status set, passing BOTH `--repo` and `--project` from the Phase 2.4 manifest (#3232) — defaults are forbidden. Priority and Size fields are set from labels by `project add`.

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 4.6: Promote to Ready (After All Relationships Are Configured)

**Epics only.** After board sync, fields, `blockedBy`, and `addSubIssue` are all complete, promote the epic and its sub-issues from Backlog to Ready — closing the race window where the scheduler dispatches before relationships exist.

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 4.7: Verification (Mandatory)

Verify board fields were actually set (via binary exit codes; assert `.status == "Ready"` for standalone issues). Do NOT report success if any field is empty — empty fields make issues invisible in the tree views.

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 4.8: Cross-Repo Project Membership Audit (Mandatory for Epics)

**Gate**: Runs UNCONDITIONALLY for any epic that produced sub-issues. Audit each sub-issue's actual project membership against the Phase 2.4 manifest; a mismatch is fatal. The manifest is deleted only after this audit passes.

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 4.5: Knowledge Scaffolding (--with-knowledge)

**Gate**: Only runs when `--with-knowledge` is passed. Scaffold a knowledge directory for the new issue (and append a knowledge reference to each sub-issue body for epics).

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 4.9: Write Creation Manifest

Write the strict-mode creation manifest (`.nightgauge/pipeline/issue-create-manifest-<ts>.json`) consumed by the Phase 6 terminal audit, and validate it parses as JSON.

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 5: Return Structured Result

Return the issue number/URL, final metadata, parent-link status, knowledge path, manifest path, and the suggested next command (`/nightgauge-issue-pickup <issue-number>`).

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

### Phase 6: Terminal Audit Pass (Mandatory)

**Gate**: Runs UNCONDITIONALLY after Phase 5 unless `--no-audit`. Invoke `/nightgauge:issue-audit --manifest <path>`; its exit code is authoritative (0 READY, 1 NEEDS FIXES, 2 failure) and propagates.

> **Read `skills/nightgauge-issue-create/_includes/board-and-audit.md` now and follow its instructions before continuing this phase.**

## Decision Rules

- Prefer deterministic repository state over assumptions.
- If type/priority is ambiguous, ask the minimum needed question.
- If milestone policy is unclear, inspect recent issues for precedent.
- Never skip project sync hook after issue creation.

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Failure Conditions

Fail with clear remediation when:

- Go binary is unavailable or `GITHUB_TOKEN` is not set
- Required labels do not exist and cannot be inferred
- Milestone is required but none available
- Issue creation succeeds but project sync fails

## Completion Checklist

- [ ] Issue created successfully
- [ ] Cross-repo reality check completed (API endpoints verified or skip noted)
- [ ] Required metadata applied (type label, priority, size)
- [ ] Added to project board via `nightgauge project add`
- [ ] Status field set to "Ready" via `nightgauge project add --status Ready` (standalone) or `nightgauge project sync-status` (epic Phase 4.6)
- [ ] Parent link handled if requested (via `addSubIssue`, not body text alone)
- [ ] **Epic only**: Decomposition gate passed (Phase 2.9) — epic has sub-issues
      planned (Path A), OR placeholder chore created (Path B), OR standalone
      declaration present (Path C)
- [ ] **Epic only**: All sub-issues added to project board with Status "Ready"
- [ ] **Epic only**: Intra-epic blocking relationships set via `addBlockedBy`
- [ ] **Epic only**: Cross-epic blocking set (when epic depends on another epic)
- [ ] **Epic only**: Wave and dependency annotations in sub-issue bodies
- [ ] Knowledge directory scaffolded (when `--with-knowledge` was used)
- [ ] **Phase 4.9**: Creation manifest written to
      `.nightgauge/pipeline/issue-create-manifest-<ts>.json` and
      validates as JSON
- [ ] **Phase 6**: Terminal audit pass invoked unless `--no-audit`; verdict
      is READY (exit 0)
- [ ] Number and URL returned with next step
