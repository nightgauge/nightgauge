# Deprecations

This document tracks all deprecated features, their timelines, and migration
paths.

---

## Deprecation Timeline

| Item                                                                                                                              | Deprecated In  | Removal Target          | Status                                                         | Migration Guide                                                               |
| --------------------------------------------------------------------------------------------------------------------------------- | -------------- | ----------------------- | -------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `nightgauge.yaml` config file                                                                                                     | v0.8.0 (#433)  | v1.0.0                  | Active — both filenames supported                              | [MIGRATION.md](MIGRATION.md#config-file-migration-nightgaugeyaml--configyaml) |
| `sync-project-iteration.sh` script                                                                                                | v1.0.0 (#1205) | v2.0.0                  | Replaced by `ProjectIterationService`                          | [MIGRATION.md](MIGRATION.md)                                                  |
| Codex wrapper-only flow                                                                                                           | v1.0.0         | v2.0.0                  | Entering deprecation                                           | See SDK docs                                                                  |
| `Supercharge` toggle, `pipeline.supercharge` config, `is_supercharge` history field, `pipeline_mode: "supercharge"` outcome value | #3009          | one release after #3009 | Replaced by `pipeline.performance_mode` and `mode: maximum`    | [PERFORMANCE_MODES.md](PERFORMANCE_MODES.md)                                  |
| `autonomous.max_concurrent` config key                                                                                            | #3195          | one release after #3195 | Replaced by `pipeline.max_concurrent` (single source of truth) | See section below                                                             |
| `github_user` config key                                                                                                          | #3338          | two minors after #3646  | Migrated to machine tier (`~/.nightgauge/config.yaml`)         | See section below                                                             |
| `lm_studio` config key                                                                                                            | #3338          | two minors after #3646  | Migrated to machine tier (`~/.nightgauge/config.yaml`)         | See section below                                                             |
| `autonomous.enabled_repos` config key                                                                                             | #3643          | two minors after #3646  | Reclassified to Machine tier; runtime tier owns the value      | See section below                                                             |
| `autonomous.repositories.<repo>.sequential` config key                                                                            | #3643          | two minors after #3646  | Reclassified to Machine tier; use machine tier per-repo config | See section below                                                             |
| `autonomous.repositories.<repo>.max_concurrent` config key                                                                        | #3643          | two minors after #3646  | Reclassified to Machine tier; use machine tier per-repo config | See section below                                                             |

---

## `nightgauge.yaml` → `config.yaml`

**Deprecated**: v0.8.0 (Issue #433) | **Removal**: v1.0.0

The project configuration file was renamed from
`.nightgauge/nightgauge.yaml` to `.nightgauge/config.yaml` as
part of the 6-tier configuration system (Issue #436).

**Current behavior**: Both filenames are supported. If both exist, `config.yaml`
takes precedence. A deprecation warning is shown when the legacy filename is
detected.

**Migration**: Run `Nightgauge: Migrate Config File` from the VSCode
Command Palette, or manually rename the file. See
[MIGRATION.md](MIGRATION.md#config-file-migration-nightgaugeyaml--configyaml)
for full instructions.

---

## `sync-project-iteration.sh` → `ProjectIterationService`

**Deprecated**: v1.0.0 (Issue #1205) | **Removal**: v2.0.0

The shell script `sync-project-iteration.sh` was replaced by the TypeScript
`ProjectIterationService` class, which provides the same functionality with
better error handling and integration with the VSCode extension.

---

## Codex Wrapper-Only Flow

**Deprecated**: v1.0.0 | **Removal**: v2.0.0

The Codex integration previously operated as a thin CLI wrapper. This is being
replaced by native SDK integration through the `PipelineOrchestrator`.

---

## `Supercharge` toggle → `performance_mode` selector

**Deprecated**: Issue #3009 | **Removal target**: one release after #3009

The binary `Supercharge` toggle was replaced with the explicit three-mode
`performance_mode` selector (Efficiency / Elevated / Maximum). See
[PERFORMANCE_MODES.md](PERFORMANCE_MODES.md) for the full design.

The following surfaces are deprecated additively — the new field is the
field of record but the legacy field continues to be written/read for one
release so external consumers (Discord embed, dashboard cost-trend filter,
historical outcome records) keep working:

| Legacy surface                                                    | Replacement                                                 | Read-side behavior                                                                                                                             |
| ----------------------------------------------------------------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `.nightgauge/supercharge.yaml`                                    | `.nightgauge/performance-mode.yaml`                         | One-time migration at activation (active=true → maximum, active=false → elevated). Legacy file renamed to `supercharge.yaml.migrated`.         |
| `NIGHTGAUGE_SUPERCHARGE` env var                                  | `NIGHTGAUGE_PERFORMANCE_MODE`                               | Legacy var no longer read by default; mode resolution honors the new var only.                                                                 |
| `pipeline.supercharge.*` config block                             | `pipeline.performance_mode.*`                               | Legacy block still parsed by `getSuperchargeModel` / `getSuperchargeCodexModel` for the Maximum profile.                                       |
| `is_supercharge: boolean` (history JSONL)                         | `performance_mode: "efficiency" \| "elevated" \| "maximum"` | Both fields written for one release; consumers should prefer `performance_mode` and fall back to `is_supercharge` only on legacy records.      |
| `is_supercharge: boolean` (pipeline state meta)                   | `performance_mode: …`                                       | Same as above — additive write.                                                                                                                |
| `pipeline_mode: "supercharge"` (outcome record)                   | `pipeline_mode: "maximum"`                                  | Enum extended additively. `OutcomeRecorder` excludes both legacy `supercharge` and new `efficiency`/`maximum` from calibration.                |
| Commands `nightgauge.{toggle,activate,deactivate}SuperchargeMode` | `nightgauge.selectPerformanceMode`                          | Legacy command IDs removed. Legacy context key `nightgauge.superchargeModeActive` is still set to mirror `mode === "maximum"` for one release. |

**Removal milestone**: When the next migration window opens, drop the
additive `is_supercharge` writes, the legacy `supercharge` config block
parser, the `"normal"` and `"supercharge"` enum values, and the legacy
context key. See `// TODO(deprecated): remove …` markers in the code.

---

## `autonomous.max_concurrent` → `pipeline.max_concurrent`

**Deprecated**: Issue #3195 | **Removal target**: one release after #3195

`autonomous.max_concurrent` and `pipeline.max_concurrent` were two parallel
keys carrying the same semantics. The drag-to-pipeline drop path read only
`pipeline.max_concurrent`; the autonomous Go scheduler read only
`autonomous.max_concurrent`. With both keys at different values, dragging two
issues in could start two pipelines even when the user had set "1" via the
autonomous key. PR #3187 introduced `pipeline.max_concurrent` as the unified
ceiling but kept `autonomous.max_concurrent` non-deprecated, so the divergence
persisted.

**Current behavior** (#3195):

- `pipeline.max_concurrent` is the single source of truth for **all** slot
  accounting (drag-to-pipeline, queue auto-start, autonomous-mode dispatch,
  IPC `pipeline.getMaxConcurrent`).
- `autonomous.max_concurrent` is honored only as a fallback when
  `pipeline.max_concurrent` is unset. The first time the fallback fires per
  process, the Go binary and the TS extension each emit a one-time
  deprecation warning to their logs.
- On extension activation, a one-time migration consolidates the two keys:
  - **Both unset** — no-op.
  - **Only `autonomous.max_concurrent` set** — silently promoted to
    `pipeline.max_concurrent` with an info toast.
  - **Both set, agreeing** — `autonomous.max_concurrent` is dropped silently.
  - **Both set, disagreeing** — modal prompt asks the user which value to
    keep; the chosen value is written to `pipeline.max_concurrent` and the
    legacy key is removed.

**Migration**: no manual action required — let the extension handle it on
next activation. To migrate manually, replace any `autonomous.max_concurrent`
entry in `.nightgauge/config.yaml` (or `config.local.yaml`) with
`pipeline.max_concurrent`.

**Affected surfaces**:

- `internal/config/config.go` — `ResolvedMaxConcurrent(cfg *Config) int`
  encapsulates the precedence and emits the Go-side deprecation log.
- `cmd/nightgauge/main.go` — autonomous startup and `run` commands now
  call `ResolvedMaxConcurrent` instead of fanning out the inline fallback
  ladder.
- `internal/ipc/server.go` — `pipeline.getMaxConcurrent` returns the resolved
  value; `persistMaxConcurrent` now targets the `pipeline:` block specifically
  (previously it would silently update whichever `max_concurrent` line came
  first in the file).
- `packages/nightgauge-vscode/src/utils/resolvers/otherResolver.ts` —
  `getConcurrentPipelineConfig()` parses both blocks and falls back to
  `autonomous.max_concurrent` with a one-time warning.
- `packages/nightgauge-vscode/src/commands/setConcurrentSlots.ts` —
  no longer dual-writes; only updates `pipeline.max_concurrent`.
- `packages/nightgauge-vscode/src/utils/maxConcurrentMigration.ts` —
  new activation-time migration described above.

**Removal milestone**: When the next deprecation window closes, drop the
fallback in `ResolvedMaxConcurrent` and `getConcurrentPipelineConfig`, remove
the `MaxConcurrent` field from `AutonomousConfig`, and delete the migration
module.

## Machine-Tier Keys (`github_user`, `lm_studio`, `autonomous.enabled_repos`, `autonomous.repositories.*`)

**Deprecated**: Phase 5 (#3338) for `github_user` / `lm_studio`; #3643 for autonomous keys | **Removal target**: two minor versions after #3646

These keys were reclassified as Machine-tier values in Phase 5 and PR #3643. Machine-tier
config lives in `~/.nightgauge/config.yaml` (or the platform runtime tier) and is
never committed to source control.

As of #3646, `validateConfig()` emits a runtime `validationWarnings` entry for each of
these keys when they are present in a project or local config file — no error is thrown
and the config is still loaded normally.

**Affected keys**:

- `github_user` — GitHub username for commit authorship. Migrate to `~/.nightgauge/config.yaml`.
- `lm_studio` — LM Studio local inference config. Migrate to `~/.nightgauge/config.yaml`.
- `autonomous.enabled_repos` — Allowlist of repos the autonomous scheduler may scan. Now
  owned by the runtime tier (`nightgauge.runtime.autonomous.enabled_repos`); toggling
  repos in the Repositories tree writes through that tier automatically.
- `autonomous.repositories.<repo>.sequential` — Per-repo sequential dispatch flag. Move to
  the machine tier config at `~/.nightgauge/config.yaml`.
- `autonomous.repositories.<repo>.max_concurrent` — Per-repo concurrency cap. Move to the
  machine tier config at `~/.nightgauge/config.yaml`.

**Migration**: Remove the above keys from `.nightgauge/config.yaml` and
`.nightgauge/config.local.yaml`. If you need per-machine overrides, add them to
`~/.nightgauge/config.yaml` instead. See `docs/SETTINGS_ARCHITECTURE.md` for the
full tier hierarchy and machine-tier path resolution.

**Removal milestone**: Two minor releases after #3646 ships, the Zod schema will be
updated to strip these keys (or hard-error in strict mode), and the JSDoc `@deprecated`
comments will be removed along with the corresponding schema fields.
