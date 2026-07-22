# Workspace Configuration Schema Migration (`schema_version: 2`)

**Date:** 2026-05-15
**Author:** nightgauge
**Status:** Decided (W1-2 of forge-abstraction epic; v2 schema landed alongside the GitLab adapter)
**Issue:** #3368
**Builds on:** [ADR-006](006-forge-abstraction.md) — `internal/forge/` interface package

---

## Executive Summary

The forge abstraction (ADR-006) added a `forges:` block and per-repo
`forge:` selector to `.nightgauge/config.yaml`. To avoid breaking
every existing single-forge installation, we introduce a `schema_version`
field, default unset configs to v1, and migrate them **in memory at load
time** to v2 by inserting a synthetic `forges.github` block. The on-disk
YAML is **never** rewritten automatically. A future
`nightgauge config migrate` command will write the migrated form on
explicit user request.

This ADR exists because the migration story was load-bearing for the v2
rollout but had no decision record — the schema landed in code without a
matching ADR. This document captures the rationale retrospectively so
future schema bumps follow the same pattern.

---

## Context

### What we had before

Pre-#3349 the workspace config implicitly assumed GitHub as the only
forge. Top-level fields like `owner`, `project_number`, `host`, and
`token_env` were singular — there was no concept of "which forge is
this?". A single configuration looked like:

```yaml
owner: nightgauge
project_number: 1
token_env: GITHUB_TOKEN
autonomous:
  repositories:
    nightgauge/nightgauge: { max_concurrent: 2 }
```

### What v2 needed

The forge abstraction required:

1. A way to declare **multiple forge instances** with distinct credentials
   (`forges:` map keyed by user-defined ID).
2. A way to **route a repository** to a specific forge
   (`autonomous.repositories.<name>.forge`).
3. A way to recognise **which schema version** a config is using so the
   loader can emit migration warnings without breaking v1 users.

The simplest mechanical solution would have been to bump the schema and
break v1 configs at load time. That was rejected — every existing
installation, autonomous routine, scheduled task, and CI workflow runs
against a v1 config today. A hard break would force every user to perform
a config edit before the next pipeline run.

### Constraints

- **Zero-effort upgrade for single-forge users.** A user with a v1 config
  who never touches GitLab should never need to edit their config.
- **No silent rewrites.** The Go binary must not modify YAML files on
  disk without explicit user invocation. Editor diffs surprise users and
  break their version control habits.
- **Stable on-disk shape.** The on-disk schema is a contract with
  external tooling (CI workflows, infrastructure-as-code, audits). It
  changes only when the user opts in.
- **Idempotent.** Loading a v2 config or migrating a v1 config twice
  produces the same in-memory shape.

---

## Decision

We add `schema_version: "2"` to the workspace config and adopt a
**load-time, in-memory, never-on-disk** migration:

1. **`schema_version` field.** A top-level string field. Absent or `"1"`
   means v1; `"2"` means current. Future bumps follow the same
   discriminator pattern.
2. **v1 fallback semantics.** When `schema_version` is absent, the
   loader treats the file as v1, emits a single migration warning to
   stderr, and synthesises an in-memory v2 shape: a `forges.github`
   block pointing at `https://github.com` is inserted using the legacy
   top-level fields (`owner`, `project_number`, `token_env`,
   `owner_type`) as its body.
3. **In-memory migration only.** The synthesised `forges:` block lives
   only in the loaded `Config` struct. The YAML on disk is **never**
   rewritten by the loader.
4. **Idempotency guarantee.** Calling the loader twice on the same v1
   file produces the same v2 in-memory shape; calling it on a v2 file is
   a no-op.
5. **Future `nightgauge config migrate` CLI.** A subsequent command
   (out of scope for this ADR) will write the synthesised v2 shape to
   disk on explicit user request, with a confirmation prompt and a
   recommended `git diff` review.

### Migration boundary

```
.nightgauge/config.yaml (on disk)
  │
  ▼
config.Load()                     ← reads file
  │
  ├── schema_version == "2"  ──► return Config (no migration)
  │
  └── schema_version absent  ──► emit warning
                                  insert forges.github block in-memory
                                  return Config
                                  (file on disk is unchanged)
```

A user who runs `nightgauge config migrate` (future) will then see:

```
config.Load()                     ← reads file
  │
  ▼
synthesise v2 shape in-memory
  │
  ▼
write to .nightgauge/config.yaml.v2 (or in-place with --in-place)
  │
  ▼
prompt: "Replace existing config? [y/N]"
```

Until that command exists, users who want to track v2 explicitly add
`schema_version: "2"` and the `forges:` block by hand.

---

## Consequences

### Positive

- **Zero-effort upgrade.** Every existing v1 config continues to work
  without any user action.
- **Stable on-disk shape.** Users in CI / IaC pipelines do not see
  unexpected diffs from a binary upgrade.
- **Forward-compatible.** Adding a v3 in the future follows the same
  discriminator + in-memory-migration pattern.
- **Auditable.** The migration warning is a single-line stderr message;
  installations that have not opted into v2 are easy to identify.

### Negative

- **Two code paths in the loader.** v1 fallback adds branching that must
  be tested explicitly.
- **Validation surface area grows.** `nightgauge config validate`
  must emit useful messages for both v1 and v2 inputs (currently:
  v1 always passes through migration; v2 errors surface real
  misconfiguration).
- **Future `migrate` command is a UX commitment.** Users who see the
  warning will eventually want a one-shot way to write the migrated
  form. We owe them that command.

### Risks and mitigations

- **Risk:** users assume the warning means their config is broken and
  edit by hand inconsistently. **Mitigation:** the warning text is
  explicit ("config will be migrated in-memory; on-disk YAML is
  unchanged") and includes the future-command hint
  ("`nightgauge config migrate` will write the v2 form when
  available").
- **Risk:** legacy top-level fields (`owner`, `project_number`, `host`,
  `owner_type`) silently diverge from `forges:` content if both are set.
  **Mitigation:** v2 documents `base_url` taking precedence over `host`;
  `nightgauge config validate` flags conflicting top-level vs.
  `forges:` values when both are present.
- **Risk:** a future v3 bump faces the same migration question.
  **Mitigation:** this ADR is the precedent — same pattern applies.

---

## Alternatives Considered

- **Hard break at v2.** Rejected: forces every existing installation to
  hand-edit before the next run.
- **Auto-rewrite the YAML on first load.** Rejected: surprises users with
  unexplained diffs and breaks IaC reproducibility.
- **Require `schema_version: "1"` explicitly to opt into v1.** Rejected:
  same as hard break — old configs without the field would fail.
- **Synthesise the `forges:` block lazily on first access.** Rejected:
  validation needs the synthesised shape at load time so error messages
  point at the right config path.

---

## References

- [Configuration → Forge Configuration (`schema_version: 2`)](../CONFIGURATION.md#forge-configuration-schema_version-2)
- [Multi-Repo Workspace → Multi-Forge Workspaces](../MULTI_REPO_WORKSPACE.md#multi-forge-workspaces)
- [Forge Abstraction design doc](../FORGE_ABSTRACTION.md)
- [ADR-006 — `internal/forge/` interface package](006-forge-abstraction.md)
- [ADR-008 — Skills target the `nightgauge forge` CLI](008-skill-forge-cli.md)
