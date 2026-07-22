# Skill Linters

Deprecation linters for SKILL.md authoring rules. Each script is matched 1:1
to a Go-binary preflight subcommand so CI runs the binary form (faster, no bash
required) while developers can run the shell form locally without a build.

## `no-direct-gh.sh`

Fails when any `skills/*/SKILL.md` file contains a direct `gh ` invocation.

**Rationale**: Skills target the `nightgauge forge` abstraction
(ADR-008). Direct `gh` calls bypass the cross-forge boundary and break the
GitLab matrix slot of the `.github/workflows/skills-smoke.yml` workflow.

**Migration table**: See `docs/decisions/008-skill-forge-cli.md`. The short
version:

| `gh` pattern               | `nightgauge forge` replacement                        |
| -------------------------- | ----------------------------------------------------- |
| `gh issue view N --json X` | `nightgauge forge issue view N --repo $REPO --json X` |
| `gh repo view --json ...`  | `nightgauge forge repo view --repo $REPO --json`      |
| `gh api user --jq .login`  | `nightgauge forge auth whoami --json --jq .login`     |
| `gh api graphql -f q=...`  | `nightgauge forge graphql -f query=...`               |
| `gh project link/list`     | `nightgauge forge graphql -f query='...'` (carve-out) |

**Scope**: `skills/*/SKILL.md` only. Files under `skills/*/tests/`,
`skills/_shared/`, and `skills/templates/` are exempted by glob — a follow-up
issue migrates those.

**Run**:

```bash
bash scripts/lint-skills/no-direct-gh.sh
# or
nightgauge preflight skill-no-direct-gh --json
```

**Exit codes**:

- `0` — no direct gh calls
- `1` — one or more skills regressed (gate fails)

**Negative test**: `scripts/lint-skills/test/regression-fixture.md` is a
sentinel file containing a deliberate `gh issue view 1` call. The CI job
copies the fixture into a tmp `skills/__lint_negative__/SKILL.md` and asserts
the linter exits 1 — guaranteeing the linter actually fires.

## `anti-patterns.sh`

Fails when any skill or supporting file hits one of the three
mechanically-detectable authoring anti-patterns Anthropic warns against
(#3813, epic #3808).

**Rationale**: Epic #3808 modernizes skill authoring to Anthropic's Agent
Skills guidance. Three of the named anti-patterns are reliably mechanizable;
the other four (time-sensitive info, inconsistent terminology,
options-without-default, magic numbers) require human review and are not
claimed as mechanically enforced by this gate.

**Checks**:

| Check              | Scope                          | Fires on                                                                                                                       |
| ------------------ | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------ |
| `nested_reference` | `_includes/`, `_shared/` files | A supporting file directing the agent to read another supporting file (references must be one level deep)                      |
| `backslash_path`   | `SKILL.md` + supporting files  | A `word\word` path token using Windows `\` separators (skills must use `/`); regex escapes like `\d` `\n` `\.` are NOT flagged |
| `missing_toc`      | `_includes/`, `_shared/` files | A supporting file over 150 lines with no `## Contents` heading in its first 40 lines                                           |

The 150-line threshold lives in one named constant
(`tocMinLines` in `internal/preflight/skill_anti_patterns.go`, mirrored by
`TOC_MIN_LINES` in `anti-patterns.sh`) — keep the two in sync.

**Scope note**: `missing_toc` deliberately applies to supporting files only,
NOT to monolithic `SKILL.md` bodies — those are owned by the separate
progressive-disclosure refactor sub-issues under epic #3808. Only files ending
in exactly `.md` are inspected; editor backups (`SKILL.md.bak`) are skipped by
extension.

**Run**:

```bash
bash scripts/lint-skills/anti-patterns.sh
# or
nightgauge preflight skill-anti-patterns --json
```

**Exit codes**:

- `0` — no anti-pattern occurrences
- `1` — one or more findings (gate fails)

**Negative tests**: `scripts/lint-skills/test/nested-ref-fixture.md` (+ its
`nested-ref-child.md` leaf), `backslash-path-fixture.md`, and
`missing-toc-fixture.md` are sentinel files. The CI job copies them into a tmp
skills tree, runs the linter, and asserts it exits 1 with one finding per check
— guaranteeing each check actually fires.

## Adding a new skill linter

1. Add a shell script in this directory with a sibling Go implementation in
   `internal/preflight/`.
2. Register the Go form as `nightgauge preflight <name>` in
   `cmd/nightgauge/preflight.go`.
3. Add a row to the table above.
4. Wire the binary form into `.github/workflows/lint.yml`.
