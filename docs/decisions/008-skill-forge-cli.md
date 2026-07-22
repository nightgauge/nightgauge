# Skills Target the `nightgauge forge` CLI

**Date:** 2026-05-11
**Author:** nightgauge
**Status:** Decided (Wave 4 of forge-abstraction epic; ≤4-call tail follow-up tracked under #3349)
**Issue:** #3363
**Depends on:** #3362 — Wave 4-1 `nightgauge forge` Cobra surface
**Builds on:** [ADR-006](006-forge-abstraction.md) — `internal/forge/` interface package

---

## Executive Summary

The 27 skills under `skills/` shell out to `gh` directly — issue list/view, PR
create/merge, label CRUD, project board GraphQL, sub-issue linking, blockedBy
mutations. With ADR-006 the Go binary now exposes a forge-agnostic `internal/forge`
interface and a sibling `internal/gitlab` adapter is landing in waves under epic
#3349. For the GitLab
adapter to actually unlock GitLab support end-to-end, the **skill layer** must
also stop calling `gh` directly.

This ADR records the decision to migrate every direct `gh` invocation in the 15
top-consumer skills to `nightgauge forge {issue,pr,project,label,repo,auth,graphql}`,
gate regressions with a deprecation linter, and route the four GitHub-specific
surfaces with no typed forge equivalent (project view-create, project link,
project list, ad-hoc GraphQL) through a thin `forge graphql` pass-through
subcommand.

---

## Context

### What we have today

- 27 `skills/*/SKILL.md` files; the top 15 consumers contain ~158 direct `gh`
  invocations (the remaining ~10 contain ≤4 each).
- `nightgauge forge` (#3362) exposes typed verbs for `issue`, `pr`,
  `project`, `label`, and `auth` — but **not** for `repo view`, `auth whoami`,
  or arbitrary GraphQL.
- The `IB_FORGE` env var selects the active forge per process; the GitLab
  adapter (W3-3 / #3357 / #3358 / #3359) is in flight.
- Migration cost estimate: ~2 engineer-hours per skill (audit the `gh` call
  sites, rewrite each to the typed forge verb, update the smoke-test fixture,
  verify the parity test). Across the 15 top-consumer skills this totals
  **15 × ~2h ≈ 30 engineer-hours** — drawn from the Technical Notes on
  #3352 and used as
  the upper-bound budget for Wave 4-2.

### Why the skill layer is the load-bearing migration

A skill is the unit of portability across Claude / Codex / Copilot / Cursor.
If a skill shells out to `gh`, then:

1. The whole pipeline silently breaks on a non-GitHub forge — users see a
   "command not found" or, worse, a 404 from the wrong API.
2. The `IB_FORGE=gitlab` selector is a paper tiger: the Go binary respects it
   but the skill above bypasses it.
3. The smoke-test matrix (`.github/workflows/skills-smoke.yml`) cannot
   meaningfully exercise the GitLab slot.

Migrating the binary surface (#3357 / #3358 / #3359) without migrating the
skill layer leaves AC #6 — **"no skill retains a fallback `gh` path"** —
permanently unmet.

---

## Alternatives Considered

Two paths were considered for removing direct `gh` calls from the skill layer.

### (a) `nightgauge forge` Go subcommand (chosen)

- **Pro:** One typed surface, one binary, one test matrix. Adding a new forge
  (GitLab, Bitbucket) is an `internal/<forge>/` adapter — the skill text
  never changes.
- **Con:** Forge surface must grow with skill needs (`repo view`, `auth
whoami`, `graphql` pass-through landed in W4-1 to close the audit gaps).
  Help-text and parity-test maintenance is now Go's job.
- **Cost:** ~30 engineer-hours for the 15 top-consumer skills (15 × ~2h),
  plus the W4-1 surface fills already shipped. Cross-platform burden is
  zero — the binary is already compiled for darwin/linux/windows.

### (b) Per-skill `gh` / `glab` shim (rejected)

- **Pro:** No Go work for skill authors; each skill can hand-roll a shell
  wrapper around `gh` and `glab` that picks based on `IB_FORGE`.
- **Con:** Per-skill conditional logic — every skill duplicates the same
  branch (`if gitlab; glab issue view; else gh issue view`). The migration
  table from this ADR would have to be re-implemented inline in 15 SKILL.md
  files, inflating every skill by 50–100 lines and re-introducing the
  "skill is portable" violation that ADR-006 was written to close.
- **Cost:** ~30h initial migration **plus** ongoing cross-platform shim
  maintenance: Linux + macOS + Windows × 15 skills means a forge-surface
  bug fix lands in 45 places instead of 1, and a future Bitbucket adapter
  forces another 15-skill rewrite. Issue #3352's Technical Notes flag
  exactly this duplication-of-logic cost as the disqualifying factor.

**Decision:** path (a). The forge-surface maintenance cost is bounded
(`cmd/nightgauge/forge/`), versionable (one binary release), and
testable end-to-end (parity tests + 2×15 smoke matrix). Path (b)'s cost is
unbounded and scales with both forge count and skill count.

---

## Decision

### Subcommand Surface Stub

The forge surface exposes the following verbs, all selected at runtime via the
`--forge` flag or the `IB_FORGE` env override (precedence: flag > env >
`github` default). Each verb is the typed replacement skills target instead of
`gh`:

- `nightgauge forge issue list` _(implemented W4-1)_
- `nightgauge forge issue view` _(implemented W4-1)_
- `nightgauge forge issue create` _(implemented W4-1)_
- `nightgauge forge pr list` _(implemented W4-1)_
- `nightgauge forge pr view` _(implemented W4-1)_
- `nightgauge forge pr create` _(implemented W4-1)_
- `nightgauge forge pr merge` _(implemented W4-1)_
- `nightgauge forge project field set` _(implemented W4-1)_
- `nightgauge forge label list` _(implemented W4-1)_
- `nightgauge forge auth status` _(implemented W4-1)_

W4-2 (`#3363`) adds `repo view`, `auth whoami`, and `graphql` to close the
audit gaps surfaced in the 15-skill migration (see "Fill the binary surface
gaps" below). Adding a new verb is a Cobra command file under
`cmd/nightgauge/forge/` plus a matching interface method on the relevant
`internal/forge/<service>.go`; the GitLab adapter implements or returns
`forge.ErrUnsupported`.

### Migrate the 15 top-consumer skills mechanically

For each skill in the top-15 consumer list (39 → 5 occurrences each), apply the
table below. The migration is mechanical — one search-replace per row — and
each migration lands in a single commit so a reviewer can scan one and trust
the rest.

#### Migration Table

| `gh` pattern                                   | `nightgauge forge` replacement                                                        |
| ---------------------------------------------- | ------------------------------------------------------------------------------------- |
| `gh issue view N --json X`                     | `nightgauge forge issue view N --repo $REPO --json X`                                 |
| `gh issue list ...`                            | `nightgauge forge issue list --repo $REPO ...`                                        |
| `gh issue create ...`                          | `nightgauge forge issue create ...`                                                   |
| `gh issue edit N --add-label L`                | `nightgauge forge graphql -f query='mutation{addLabelsToLabelable(...)}'` (carve-out) |
| `gh issue comment N -b "..."`                  | `nightgauge forge issue comment --subject-id $ID -b "..."`                            |
| `gh pr view N --json X`                        | `nightgauge forge pr view N --repo $REPO --json X`                                    |
| `gh pr list ...`                               | `nightgauge forge pr list --repo $REPO ...`                                           |
| `gh pr create ...`                             | `nightgauge forge pr create ...`                                                      |
| `gh pr merge N --squash`                       | `nightgauge forge pr merge --node-id $ID --strategy squash`                           |
| `gh pr checks N --watch`                       | `nightgauge forge pr checks N --wait`                                                 |
| `gh label list/create/delete`                  | `nightgauge forge label list/create/delete`                                           |
| `gh repo view --json nameWithOwner,owner,name` | `nightgauge forge repo view --repo $REPO --json` (Wave 4-2)                           |
| `gh api user --jq .login`                      | `nightgauge forge auth whoami --json --jq .login` (Wave 4-2)                          |
| `gh api graphql -f query=...`                  | `nightgauge forge graphql -f query=...` (Wave 4-2)                                    |
| `gh api POST /projectsV2/.../views`            | `nightgauge forge graphql -f query='mutation{...}'` (carve-out)                       |
| `gh project link`                              | `nightgauge forge graphql -f query='mutation{linkProjectV2ToRepository(...)}'`        |
| `gh project list`                              | `nightgauge forge graphql -f query='query{user{projectsV2(first:20){...}}}'`          |
| `gh run watch` / `gh run view --log`           | `nightgauge forge pr checks --wait` (release-watch carve-out)                         |

### Fill the binary surface gaps in the same PR

The skill audit surfaced four missing surfaces. They ship inside this PR as the
minimum needed to remove every direct `gh` invocation:

| New subcommand                        | Why                                                                                       |
| ------------------------------------- | ----------------------------------------------------------------------------------------- |
| `nightgauge forge auth whoami --json` | `repo-init` discovers active user for personal-project lookup (`gh api user --jq .login`) |
| `nightgauge forge repo view --json`   | `repo-init`, `smart-setup`, `project-sync` discover repo metadata (`gh repo view`)        |
| `nightgauge forge graphql -f / -F`    | 11 skills use ad-hoc GraphQL for sub-issue linking, project field discovery, blockedBy    |

Each subcommand carries unit tests (`*_test.go`) and a help-text entry that
either references the GitLab equivalent or carries the
`(no GitLab equivalent — GitHub only)` marker — enforced by
`cmd/nightgauge/forge/help_text_test.go`.

### Gate regressions with a deprecation linter

`scripts/lint-skills/no-direct-gh.sh` (mirrored as
`nightgauge preflight skill-no-direct-gh`) walks `skills/*/SKILL.md` and
fails CI on any `\bgh ` token. The Go form is wired into
`.github/workflows/lint.yml`; the shell form is the developer-friendly path.

The linter honours an allowlist file (`scripts/lint-skills/allowlist.txt`)
listing the un-migrated tail (~10 skills with ≤4 calls each). Each entry MUST
be removed as the skill migrates — adding a new entry requires PR review
justification.

**Deprecation plan:**

- **CHANGELOG entry** — at the next release the `## Unreleased` section
  records ADR-008 as the migration trigger and points readers at
  `scripts/lint-skills/allowlist.txt` for the current un-migrated tail.
- **Linter-emitted warning text:** the no-direct-gh linter surfaces violations
  with the exact message
  `"direct gh call deprecated by ADR-008 — use nightgauge forge"`,
  including the offending file and line. Skill authors get a one-line pointer
  back to this ADR.
- **Target removal release:** the allowlist drains to zero by release
  `0.2.x` as each tail skill migrates. Release `0.3.0` removes the
  allowlist support entirely and the linter becomes default-deny with no
  exceptions — direct `gh` in any skill is a CI failure with no escape
  hatch. (Versioning here uses `0.1.<commit-count>`; `0.2.x` / `0.3.0` are
  forward-looking markers tied to the allowlist-drain milestone, not commit
  counts.)

### Smoke-test against both forges

`.github/workflows/skills-smoke.yml` runs a `forge: [github, gitlab] × skill: [...15...]`
matrix. The GitLab slot consumes the Wave 5-2 Dockerized GitLab CE harness
(see #3349); when
W5-2 has not yet landed, the slot falls back to recorded fixtures under
`cmd/nightgauge/forge/testdata/gitlab-snapshots/` so the matrix still runs
hermetically.

### Assert JSON-shape parity with `gh` snapshots

`cmd/nightgauge/forge/skill_parity_test.go` extends the existing parity
harness (`parity_test.go`) so that for each of the 15 skills, the JSON paths
the skill `jq`-extracts exist in the `forge ... --json` output and have
matching types. Extra fields in `forge` output are fine; missing paths fail.

---

## Output-Shape Compatibility Contract

For every verb, `nightgauge forge <verb> --json` emits a JSON object
whose **top-level field names are a superset of what `gh <verb> --json`
emits**, with **matching value types** (string stays string, number stays
number, array stays array). Skills that today extract `.title`, `.number`,
`.body`, `.labels[].name`, etc. via `jq` continue to work unmodified after
migration.

The contract is enforced by `cmd/nightgauge/forge/skill_parity_test.go`,
which loads recorded `gh ... --json` fixtures under
`cmd/nightgauge/forge/testdata/gh-snapshots/`, runs the equivalent
`forge ... --json` invocation, and asserts that every JSON path the 15 skills
consume exists in the forge output with the same type.

**Field-rename policy:** forge output may only **add** fields. Renaming or
removing an existing field is a contract break and requires a major-version
bump on the binary plus a coordinated skill update. The parity test catches
removals; renames are caught by manual review against this ADR.

This is what makes the migration mechanical: a skill author rewriting
`gh issue view N --json title,number,body` to
`nightgauge forge issue view N --json title,number,body` does not need
to also rewrite the downstream `jq` pipeline.

---

## Carve-outs

Three operations are kept inside `forge graphql` rather than getting their own
typed surface in this PR. Each routes the same GraphQL body through the new
pass-through, so the skill text contains zero `gh ` tokens — AC #6 holds — but
the call is still GitHub-specific:

1. **`gh project view-create`** (used once per board by `repo-init`) — typed
   surface tracked as a follow-up; routes through
   `nightgauge forge graphql` mutation today.
2. **`gh project link`** (used once per repo by `repo-init`) — same treatment.
3. **`gh project list`** (used by `repo-init` and `smart-setup` for project
   discovery) — same treatment.

These carve-outs are intentional. ADR-006 §"Methods intentionally not in core
forge" lists `ViewService` (and similar GitHub-Projects-V2-specific surfaces)
as out of scope for the cross-forge interface. The `forge graphql` pass-through
is the escape hatch that lets the skill layer be 100% `gh`-free without
forcing premature typed-surface design for operations whose GitLab
equivalent does not yet exist.

The GitLab adapter does **not** implement `forge.GraphQLService` today — calls
to `forge graphql --forge gitlab` return `forge.ErrUnsupported`. This is
acceptable because the four carve-out operations are project-board-specific and
the GitLab slot of the smoke-test matrix skips them via the allowlist.

---

## Implementation tracking

| Wave | Subwave | Issue     | Status                                                           |
| ---- | ------- | --------- | ---------------------------------------------------------------- |
| W4   | 4-1     | #3362     | Closed (forge Cobra surface)                                     |
| W4   | 4-2     | #3363     | This PR (15-skill migration + linter + parity tests + CI matrix) |
| W4   | 4-3     | follow-up | Open (≤4-call tail; allowlist drains as each skill migrates)     |
| W4   | 4-4     | follow-up | Open (typed `forge project view-create / link / list` surfaces)  |

---

## Consequences

### Positive

- The skill layer is forge-agnostic: `IB_FORGE=gitlab bash skills/<name>` works.
- The deprecation linter prevents regressions — a future PR adding a `gh issue
view` to a skill fails CI before merge.
- New skills inherit the contract automatically (default-deny via the linter).
- Smoke tests exercise both forges; GitLab parity is now provable, not aspirational.

### Negative

- The forge surface grew by three subcommands (`repo view`, `auth whoami`,
  `graphql`). Each carries its own help-text and test maintenance burden.
- The `forge graphql` pass-through is intentionally GitHub-specific. Skills that
  use it cannot run against GitLab — the GitLab matrix slot of the smoke-test
  workflow skips them via per-skill exclusions in
  `.github/workflows/skills-smoke.yml`. This is documented in the workflow.
- The allowlist (`scripts/lint-skills/allowlist.txt`) is a known piece of
  technical debt — every entry is a future migration. The follow-up issue
  tracks draining it.

### Neutral

- The 15 SKILL.md commits are all mechanical; reviewer reads one, validates the
  pattern, scans the rest. Optional split into 2 PRs (Phase A binary + Phase
  B/C/D skills) is available if review feedback requests it.

---

## References

- [ADR-006: Forge Abstraction Interface](006-forge-abstraction.md) — the
  `internal/forge/` interface package this ADR builds on.
- [scripts/lint-skills/README.md](../../scripts/lint-skills/README.md) — the
  deprecation linter and its allowlist mechanism.
- [skills/README.md](../../skills/README.md#forge-abstraction-contract-3363-adr-008) — the skill-author contract.
- `.github/workflows/skills-smoke.yml` — the 2×15 smoke-test matrix.
- [.github/workflows/lint.yml](../../.github/workflows/lint.yml) — where the linter is wired into CI.
