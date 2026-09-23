# Data and State Layout — team config committed, everything else outside the working tree

**Date:** 2026-09-23
**Author:** nightgauge
**Status:** Accepted
**Issue:** #2022 (epic #2021)
**Implemented by:** #2023–#2045 (the epic's Phase 0–4 sub-issues; each is mapped to a row below)
**Consistent with:** [ADR-018](018-adapter-usage-quota-model.md) (`usage/` is per machine),
[ADR-020](020-value-adding-features-default-on.md) (knowledge ships on)
**Amends (location only):** ADR-013 (the trace moves with the
pipeline-state class) and [ADR-015](015-decision-requests.md) (the attention store is
per-clone runtime state)

---

## Executive Summary

Nightgauge had a decision for where _settings_ live
([SETTINGS_ARCHITECTURE.md](../SETTINGS_ARCHITECTURE.md)) and none for anything else. Each
subsystem picked its own path, most of them inside the working tree: caches, pipeline state,
plans, retros, logs, worktrees and the daemon socket all sat under `<repo>/.nightgauge/`, kept out
of git only by an ignore file that a CLI-only clone never received. Machine state hard-coded
`~/.nightgauge` while machine config followed platform conventions.

This ADR fixes one location per data class:

- **Committed:** team config and an explicit allowlist under `.nightgauge/`, plus the knowledge
  base. Nothing else a Nightgauge command writes is committable.
- **Per user, outside every repository:** machine config, secrets (OS keychain), caches, machine
  state, the daemon socket and, by default, pipeline worktrees.
- **Per clone, outside the working tree:** pipeline state, run artefacts, plans, retros and logs
  under `$(git rev-parse --git-common-dir)/nightgauge/`.
- **Extension-only:** VS Code's own storage (`SecretStorage`, mementos, `storageUri`,
  `globalStorageUri`, `logUri`).

Migration is a single path: runtime code reads only the new location, and
`nightgauge doctor --fix` moves existing files once and writes a layout-version marker. There is
no dual read and no N-release fallback.

## Context

An audit of the core found, most severe first: a plaintext PAT accepted in the committed
`.nightgauge/config.yaml`; a license key the CLI and daemon lost once the extension had moved it
into VS Code's `SecretStorage`; clones that never ran the extension committing logs, state and
worktrees (no ignore rules); a knowledge base documented as committed but ignored by the
generated rules; worktrees and unbounded logs inside the working tree (one maintainer machine
held 251 MB in 609 files under `.nightgauge/logs/`); a Unix socket inside the working tree that
fails on long paths, in synced folders and in containers; machine config and machine state
following different conventions on Linux; and `nightgauge.backend.binaryPath` settable from a
workspace's `.vscode/settings.json`.

Anything inside the working tree is traversed by search, file watchers, linters, test runners,
Docker build contexts and cloud-sync clients, and is one `git add -A` from a commit. An ignore
rule is a mitigation that every consumer must receive and keep current; a location outside the
tree needs no rule at all.

### Prior art

- **VS Code** gives an extension a store per kind of data: `SecretStorage` for secrets,
  `globalState`/`workspaceState` mementos and `globalStorageUri`/`storageUri` for data, and
  `logUri` for logs it rotates per session
  ([Extension Capabilities › Data Storage](https://code.visualstudio.com/api/extension-capabilities/common-capabilities#data-storage)).
  `contributes.configuration` scopes `machine` and `machine-overridable` keep machine-specific
  values out of workspace settings
  ([contribution points](https://code.visualstudio.com/api/references/contribution-points#contributes.configuration)).
- **The XDG Base Directory specification** separates config, cache, state and runtime
  directories, and requires the runtime directory to be owned by the user with mode 0700
  ([basedir-spec](https://specifications.freedesktop.org/basedir-spec/latest/)).
- **Go** exposes `os.UserConfigDir` and `os.UserCacheDir`
  ([package os](https://pkg.go.dev/os#UserCacheDir)); it has no state or runtime counterpart.
- **GitHub CLI** stores its token in the system credential store and falls back to a plain-text
  file only when no store is available
  ([`gh auth login`](https://cli.github.com/manual/gh_auth_login)).
- **ESLint, Prettier and Renovate** each have one committed team config file; everything they
  cache or log lives elsewhere.

### Already decided and implemented (Phase 0)

These are recorded here as settled; this ADR does not reopen them.

- **Ignore rules from the Go binary** (#2026, with #1090): `nightgauge config init` and
  `nightgauge serve` write `.nightgauge/.gitignore` from `internal/scaffold`'s embedded template
  (version 15). When the committed file is tracked and older, the committed file is left alone
  and the current rules go to the common dir's `info/exclude` in a marked block. Version 15 still
  ignores `/knowledge/`, pending this ADR and #2042.
- **Plaintext credentials refused in repository tiers** (#2023): the loader rejects a plaintext
  `github_auth.token`, `github_auth.tokens.*` or `platform.license_key` in
  `.nightgauge/config.yaml` or `.nightgauge/config.local.yaml`; only `env:` references are
  accepted there. `internal/configpath` is the one resolver of the machine-tier file.
  `nightgauge doctor`'s `tracked_secrets` check (#2024) reports a committed credential with the
  value redacted.
- **OS keychain store** (#2025, #2027): `internal/keychain` stores credentials under service
  `nightgauge`, account = the credential's dotted machine-tier path (`platform.license_key`).
  License-key resolution is `NIGHTGAUGE_LICENSE_KEY`, then the keychain entry, then the 0600
  machine-tier file. The keychain entry is the source of truth; the extension's `SecretStorage`
  copy is trusted only while its fingerprint matches the one `nightgauge auth license status`
  reports, and the extension writes the key by piping it to `nightgauge auth license set` on
  stdin.
- **Cache home** (PR #2020): the GitHub conditional-request (ETag) store resolves to
  `$NIGHTGAUGE_CACHE_HOME/github-conditional`, else `os.UserCacheDir()/nightgauge/github-conditional`.

## Decision

### 1. Roots

Every location below is built from one of six roots. Each root has one resolver in Go, an
environment override, and a documented default per OS. An explicitly set `XDG_*` variable is
honoured on every OS, as the machine-config resolver already does for `XDG_CONFIG_HOME`.

| Root      | Linux                                                                 | macOS                                               | Windows                                   | Override                 | Resolver                       |
| --------- | --------------------------------------------------------------------- | --------------------------------------------------- | ----------------------------------------- | ------------------------ | ------------------------------ |
| `CONFIG`  | `$XDG_CONFIG_HOME/nightgauge`, else `~/.config/nightgauge`            | `$XDG_CONFIG_HOME/nightgauge`, else `~/.nightgauge` | `%APPDATA%\nightgauge`                    | `NIGHTGAUGE_CONFIG_HOME` | `internal/configpath` (exists) |
| `CACHE`   | `$XDG_CACHE_HOME/nightgauge`, else `~/.cache/nightgauge`              | `~/Library/Caches/nightgauge`                       | `%LOCALAPPDATA%\nightgauge\cache`         | `NIGHTGAUGE_CACHE_HOME`  | `internal/layout.CacheHome`    |
| `STATE`   | `$XDG_STATE_HOME/nightgauge`, else `~/.local/state/nightgauge`        | `~/.nightgauge/state`                               | `%LOCALAPPDATA%\nightgauge\state`         | `NIGHTGAUGE_STATE_HOME`  | `internal/layout.StateHome`    |
| `RUNTIME` | `$XDG_RUNTIME_DIR/nightgauge`, else `<os.TempDir()>/nightgauge-<uid>` | `<os.TempDir()>/nightgauge-<uid>` (`$TMPDIR`)       | not applicable (no Unix-socket transport) | `NIGHTGAUGE_RUNTIME_DIR` | `internal/layout.RuntimeDir`   |
| `CLONE`   | `<git-common-dir>/nightgauge`                                         | same                                                | same                                      | none (git's `GIT_DIR`)   | `internal/layout.CloneDir`     |
| `WTBASE`  | `pipeline.worktree_base`, else `STATE/worktrees/<repo-key>`           | same                                                | same                                      | the config key           | `internal/layout.WorktreeBase` |

`<git-common-dir>` is `git rev-parse --path-format=absolute --git-common-dir`, so every linked
worktree of a clone resolves to the main clone's directory. `<repo-key>` is the first 12 hex
characters of the SHA-256 of the canonical (symlink-evaluated, absolute) git common dir: linked
worktrees of one clone share it and two clones never collide.

`internal/layout` is a new leaf package, importable from anywhere without a cycle (the reason
`internal/configpath` is a leaf). It owns every root except `CONFIG`, which stays in
`internal/configpath`. The per-clone class resolvers (`PipelineStateDir`, `PlansDir`,
`RetrosDir`, `CloneLogsDir`) live there too.

### 2. Where each data class lives

| Data class                                                                                                          | Location                                                                | Env override                                          | Committed                     | Writer · owner package                                                            | Retention                                                     | Implementing issue                  |
| ------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- | ----------------------------------------------------- | ----------------------------- | --------------------------------------------------------------------------------- | ------------------------------------------------------------- | ----------------------------------- |
| Team config (`config.yaml`, `config.schema.json`, `pattern-mining-config.yaml`)                                     | `<repo>/.nightgauge/`                                                   | none                                                  | yes                           | humans, by PR; runtime never writes it · `internal/config`                        | git history                                                   | settled; #2023                      |
| Committed allowlist (`.gitignore`, `audit/`, `skill-smoke/`, `skill-evals/baseline.jsonl`, `model-evals/evidence/`) | `<repo>/.nightgauge/`                                                   | none                                                  | yes                           | humans and the stages that author them · `internal/scaffold` (the template)       | git history                                                   | #2043                               |
| Knowledge base                                                                                                      | `<repo>/.nightgauge/knowledge/`                                         | none                                                  | yes, by default; team opt-out | pipeline stages, in the issue worktree · `internal/knowledge`                     | git history                                                   | #2042                               |
| Per-user config (machine tier)                                                                                      | `CONFIG/config.yaml`, mode 0600                                         | `NIGHTGAUGE_CONFIG_HOME`, `XDG_CONFIG_HOME`           | no                            | the user; settings UI for machine keys · `internal/configpath`, `internal/config` | kept                                                          | settled; #2041 (Linux legacy)       |
| Per-clone config                                                                                                    | `<repo>/.nightgauge/config.local.yaml`                                  | none                                                  | no (ignored)                  | the user; default UI write target · `internal/config`                             | kept                                                          | settled; #2023                      |
| Secrets                                                                                                             | OS keychain, service `nightgauge`; fallback `CONFIG/config.yaml` (0600) | `NIGHTGAUGE_LICENSE_KEY`; `env:` refs; `GITHUB_TOKEN` | never                         | `nightgauge auth license set` (stdin) · `internal/keychain`                       | until cleared                                                 | #2023, #2024, #2025, #2027          |
| Extension-only secrets (webhooks, chat, Gemini, platform tokens)                                                    | VS Code `SecretStorage`                                                 | none                                                  | never                         | the extension                                                                     | until cleared                                                 | settled                             |
| Caches (GitHub ETag store, recall index, any derived index)                                                         | `CACHE/<name>/` (for example `CACHE/recall/<repo-key>/`)                | `NIGHTGAUGE_CACHE_HOME`, `XDG_CACHE_HOME`             | never                         | the component that derives it · `internal/layout.CacheHome`                       | disposable; deleting it costs one rebuild                     | PR #2020 (ETag); #2028              |
| Per-clone state and run artefacts (`pipeline/`, including history and the ADR-013 trace)                            | `CLONE/pipeline/`                                                       | none                                                  | never                         | Go orchestrator and CLI; extension via the binary · `internal/layout`             | history pruned by `pipeline.logs.history_retention_days` (90) | #2033–#2036, #2037; migration #2040 |
| Plans and retros                                                                                                    | `CLONE/plans/`, `CLONE/retros/`                                         | none                                                  | never                         | stages, through a `nightgauge` command · `internal/layout`                        | kept with the clone                                           | #2033–#2037; migration #2040        |
| Other per-clone runtime files (`attention/`, `autonomous/`, `health/`, `graph/`, `focus.yaml`, …)                   | `CLONE/<name>` (target); ignored in `.nightgauge/` until moved          | none                                                  | never                         | their current writers                                                             | per writer                                                    | none yet (see § 13)                 |
| Per-clone logs (Go-consumed: `go-backend.log`, `*_session.log`, `github-api.jsonl`)                                 | `CLONE/logs/`                                                           | none                                                  | never                         | Go binary; extension Go-consumed writers via one helper · `internal/layout`       | 200 MB and 30 days per directory                              | #2029, #2030, #2037                 |
| Extension-only logs                                                                                                 | VS Code `ExtensionContext.logUri`                                       | none (VS Code's)                                      | never                         | the extension                                                                     | VS Code's session rotation                                    | #2030                               |
| Extension-only data                                                                                                 | mementos, `storageUri`, `globalStorageUri`                              | none                                                  | never                         | the extension                                                                     | VS Code-managed                                               | settled                             |
| Worktrees                                                                                                           | `WTBASE/<repo>-issue-<N>`                                               | `pipeline.worktree_base` (machine/local tier)         | never                         | `internal/execution` · `internal/layout.WorktreeBase`                             | removed by pipeline cleanup and reclaim                       | #2038; migration #2040              |
| Daemon socket                                                                                                       | `RUNTIME/<hash>.sock`                                                   | `NIGHTGAUGE_RUNTIME_DIR`, `XDG_RUNTIME_DIR`           | never                         | `nightgauge serve` · `internal/ipc.DaemonSocketPath` over `internal/layout`       | daemon lifetime; recreated on start                           | #2039                               |
| Machine state (serve sidecar, `rate-limit.json`, `machine-id`)                                                      | `STATE/`                                                                | `NIGHTGAUGE_STATE_HOME`, `XDG_STATE_HOME`             | never                         | Go binary · `internal/layout.StateHome`                                           | kept; `rate-limit.json` is a cold-start hint                  | #2031; migration #2041              |
| Machine state (`usage/`, OpenCode run state, machine logs)                                                          | `STATE/usage/`, `STATE/opencode/`, `STATE/logs/`                        | `NIGHTGAUGE_STATE_HOME`, `XDG_STATE_HOME`             | never                         | Go binary; extension reads `usage/` via the binary · `internal/layout`            | logs: 200 MB and 30 days; others per writer                   | #2029, #2032; migration #2041       |
| Operator-installed tools (OpenCode pin)                                                                             | `~/.nightgauge/tools/` (unchanged)                                      | the absolute `opencode.binary` value                  | never                         | the operator (`npm i --prefix`)                                                   | operator-managed                                              | none (exception, § 9)               |
| Layout-version marker                                                                                               | `CLONE/layout-version`, `STATE/layout-version`                          | follows its root                                      | never                         | `nightgauge doctor --fix` · `internal/doctor`                                     | kept                                                          | #2040, #2041                        |

### 3. Team config (committed)

`.nightgauge/config.yaml` is the one team config file, in the ESLint/Prettier/Renovate shape: a
reviewed file, changed by pull request, identical for everyone. Runtime code never writes it
(`internal/config/writer.go` already routes runtime writes elsewhere). It holds no credential in
plaintext; the loader refuses one (#2023).

Rejected: a team tier outside the repository (a shared network location or a platform-held
config). It loses review through pull requests and makes the configuration of a checkout depend
on something the checkout does not contain.

### 4. Per-user config tiers

The precedence chain in [SETTINGS_ARCHITECTURE.md](../SETTINGS_ARCHITECTURE.md) and
[CONFIGURATION.md](../CONFIGURATION.md) stands: project (team) overrides machine, and the local
tier overrides both.

- **Machine tier:** `CONFIG/config.yaml`, mode 0600, resolved only by `internal/configpath`
  (`NIGHTGAUGE_CONFIG_HOME`, then `XDG_CONFIG_HOME/nightgauge`, then the platform default). It is
  the only file that may hold a credential in plaintext.
- **Local tier:** `.nightgauge/config.local.yaml` stays in the working tree and ignored. It is
  hand-edited like `.env.local`, and moving it under the git dir would hide it from the people
  who edit it. Like the project tier, it accepts credentials only as `env:` references, because
  it is one `git add -f` from a commit.

Rejected: moving the macOS machine config from `~/.nightgauge` to
`~/Library/Application Support/nightgauge`. It would move a file every macOS user has, for no
failure the audit found; the macOS state root is placed beside it (§ 8) instead.

### 5. Secrets

- **Store:** the OS keychain (macOS Keychain, Windows Credential Manager, the Secret Service on
  Linux) through `internal/keychain`, service `nightgauge`, account = the dotted machine-tier key.
  It is shared by the extension, the CLI and the daemon; the extension writes through
  `nightgauge auth license set` on stdin and never opens the keychain itself.
- **Fallback:** on a host with no usable keychain (CI runner, container, SSH session), the 0600
  machine-tier file is the only file form, as in `gh`.
- **Resolution:** environment variable, then keychain, then machine file. `env:` references are
  the only form a repository tier accepts.
- **Never in a repository tier, in plaintext:** `github_auth.token`, `github_auth.tokens.*`,
  `platform.license_key`. Any credential added later joins the same list in
  `internal/config/repo_tier_credentials.go`.
- **Never on argv, never in a log.** A command that takes a secret reads it from stdin or the
  environment; argv is visible to other local users through `ps`. Diagnostics carry a source
  label or a fingerprint, never the value.
- Credentials only the extension uses stay in VS Code `SecretStorage`.
- Any other writer of the license key, including the sign-in device-key flow (#1883), stores it
  through the keychain entry, not only in `SecretStorage`.

Rejected: `SecretStorage` as the source of truth (the CLI and daemon cannot read it, which is the
defect #2025 fixed); an encrypted file in the repository (a key still has to live somewhere, and
the ciphertext is permanent in history).

### 6. Caches

A cache is anything that can be deleted at any time and rebuilt from its sources at the cost of
time or API calls. Caches live under `CACHE`, one subdirectory per cache, keyed by `<repo-key>`
when per clone. The resolver is the one PR #2020 introduced, lifted into
`internal/layout.CacheHome` by #2028; no second resolver is added.

Two refinements to PR #2020's resolver, made when #2028 lifts it:

- On Windows `os.UserCacheDir()` is `%LOCALAPPDATA%`, which is also the parent of `STATE`. The
  cache root is `%LOCALAPPDATA%\nightgauge\cache`, so that deleting the cache never deletes state.
- An explicitly set `XDG_CACHE_HOME` is honoured on every OS, as `XDG_CONFIG_HOME` already is.

A cache is never migrated: the old copy is deleted, and the new location starts cold. With no
usable cache directory, the cache is kept in memory for the call and nothing is written into the
working tree.

Rejected: caches in the working tree (the audit's finding); caches under `CLONE` (they would be
backed up and synced with the clone, and they are not per-clone in every case, for example the
ETag store is keyed by token identity).

### 7. Per-clone state, run artefacts, plans, retros and logs

These live under `CLONE = $(git rev-parse --path-format=absolute --git-common-dir)/nightgauge/`:
`pipeline/` (state, run artefacts, history, the ADR-013 trace), `plans/`, `retros/` and `logs/`.

Why there: git never tracks anything under its own directory, so the data cannot be committed
whatever the ignore rules say; every linked worktree of the clone resolves to the same directory,
so a run started in a worktree is visible from the main checkout; and the data is deleted with
the clone.

Rules:

- The resolvers take an absolute repository root and return an error outside a git repository;
  no caller resolves against the process cwd.
- The resolvers are the only path source. The extension, skills and docs obtain the paths from
  the binary (a `nightgauge` subcommand that prints the resolved layout as JSON, added by #2037),
  not by hard-coding `.nightgauge/plans` or its siblings. A TypeScript reimplementation is
  allowed only where the binary cannot be asked, and only with a parity test.
- **Agents do not write under the git directory by path.** Agent harnesses may guard writes
  under `.git/`, and a direct write would bypass the confinement rules in § 13. A stage that
  produces a plan or a retro hands it to a `nightgauge` command, which writes it through the
  resolver.

Rejected:

- `.nightgauge/` in the working tree with ignore rules (the status quo; every consumer must
  receive and maintain the rules, and a missing rule commits transcripts).
- `STATE/clones/<repo-key>/` (outside every repository, but it outlives the clone and needs its
  own garbage collection).
- An environment override for `CLONE`. Git's own `GIT_DIR` and `GIT_COMMON_DIR` already relocate
  it, and a Nightgauge-specific override would break the sharing across linked worktrees.

### 8. Machine state on every OS

One resolver, `internal/layout.StateHome`, replaces every hard-coded `~/.nightgauge` state path:
`NIGHTGAUGE_STATE_HOME`, then `XDG_STATE_HOME/nightgauge`, then the platform default in § 1.

- **Linux** follows XDG, like its machine config.
- **macOS** keeps state beside its machine config, in `~/.nightgauge/state`, so config and state
  share one convention on each OS; that split was the audit's Linux finding.
- **Windows** uses `%LOCALAPPDATA%` (not roaming), because `machine-id` identifies this device
  and must never roam to another one through `%APPDATA%`.

`rate-limit.json` stays a cold-start hint: a missing file is a cold start, not an error.
`machine-id` keeps mode 0600 and is moved byte for byte, never regenerated: a regenerated id is a
new device to the platform and counts against the account's machine limit.

Rejected: `~/Library/Application Support/nightgauge` on macOS (splits config and state across two
roots on one OS, and puts a space in every path the skills and docs print); `os.UserConfigDir()`
on Windows (roaming).

### 9. Worktrees

Pipeline worktrees are created at `WTBASE/<repo>-issue-<N>`, outside the working tree.

- Unset `pipeline.worktree_base` means `STATE/worktrees/<repo-key>/`.
- `pipeline.worktree_base` becomes a machine- or local-tier key: it is an absolute path on one
  machine, which a committed team file cannot express for everyone. `~` is expanded; a relative
  value, or one that resolves inside the working tree, is refused. The TypeScript schema's
  `.worktrees` default goes.
- Sweep and reclaim find worktrees through `git worktree list`, not by scanning a directory.
- Containment is checked after symlink evaluation: a crafted base or issue slug cannot place a
  worktree outside the base.
- `doctor` reports worktrees whose clone no longer exists (their git metadata is gone), and
  `doctor --fix` removes them.

Rejected: `CLONE/worktrees/` (agents and editors work inside the worktree, and tools and harnesses
treat anything under `.git/` as off limits); a sibling directory of the clone (writes into a parent
directory the user did not give Nightgauge, which may be cloud-synced or not writable).

`~/.nightgauge/tools/` (the OpenCode pin, [ADR-022](022-opencode-multi-provider-adapter.md)
§ 20) is operator-installed software referenced by an absolute path in machine config. It is not
state and is not moved.

### 10. The daemon socket

`RUNTIME/<hash>.sock`, where `<hash>` is the first 12 hex characters of the SHA-256 of the
canonical workspace root. `RUNTIME` is `NIGHTGAUGE_RUNTIME_DIR`, else
`$XDG_RUNTIME_DIR/nightgauge`, else `<os.TempDir()>/nightgauge-<uid>`.

- `sun_path` is 104 bytes on macOS and 108 on Linux. The resolver returns an error, never a
  truncated path, when the result exceeds 100 bytes (possible only through a long override).
- The directory is created 0700 and, before binding, verified to be a real directory (not a
  symlink) owned by the current uid with no group or other bits; otherwise the daemon refuses to
  start. A shared `/tmp` lets another user pre-create the directory.
- The extension and CLI clients obtain the path from the binary or the same algorithm, pinned by
  a parity test. The socket needs no migration: it is recreated on each daemon start.

Rejected: the socket in the working tree (length, synced folders, container bind mounts); under
`CLONE` (the path length is still the clone's depth).

### 11. Log retention

One Go pruning function enforces, per log directory (`CLONE/logs/`, `STATE/logs/`):

- a total size cap, default **200 MB**, and a maximum file age, default **30 days**, set by
  machine-tier keys `pipeline.logs.max_size_mb` and `pipeline.logs.max_age_days`;
- oldest files first, never the file currently open for writing, regular files only, confined to
  the resolved directory;
- at `serve` start and at most daily while it runs, and at CLI start when the last prune is over
  a day old.

Pruning deletes whole files, so an append-only log that would otherwise grow as one file
(`github-api.jsonl`) is written in dated segments. Readers report a pruned range as absent, not as
an error. Pipeline history under `CLONE/pipeline/history/` keeps its own existing retention
(`pipeline.logs.history_retention_days`, default 90).

Rejected: size cap only (old files of no value survive on a quiet machine); rotation by an
external tool such as logrotate (not present on macOS or Windows, and no CLI-only clone would
configure it).

### 12. Knowledge

The knowledge base is authored content and is **committed by default**. The generated template
stops ignoring `/knowledge/`; a team that does not want it committed adds `.nightgauge/knowledge/`
to its root `.gitignore`, which template upgrades never touch. This is the resolution recorded on
#1090, consistent with [ADR-020](020-value-adding-features-default-on.md) (value ships on;
opting out is for repository footprint).

The only derived data under `knowledge/` was the recall index; it moves to `CACHE/recall/`
(#2028), after which no knowledge path needs an ignore rule. #2042 changes the Go template and
the `config init` and `knowledge.go` comments to state this policy in one sentence each; it keeps
`/knowledge/.recall-cache/` ignored only if #2028 has not landed.

### 13. The committed `.nightgauge/` allowlist

Once per-clone data has moved, the template becomes deny-by-default (#2043): `/*` followed by `!`
rules for exactly these paths:

- `config.yaml`, `config.schema.json`, `pattern-mining-config.yaml` (team config);
- `.gitignore` (the template itself);
- `audit/` (authored audit files), except `audit/scope-drift-stats.json`, a per-machine counter;
- `skill-smoke/`;
- `skill-evals/baseline.jsonl` and `model-evals/evidence/` (tracked evaluation references);
- `knowledge/`.

`config.local.yaml` stays ignored. The per-directory rules and `.gitkeep` anchors for `pipeline/`,
`plans/`, `logs/`, `worktrees/`, `daemon.sock` and the caches are deleted. Any other file a
Nightgauge component still writes under `.nightgauge/` is ignored by `/*` until it moves to
`CLONE`; the per-clone runtime files the current template names one by one (`attention/`,
`autonomous/`, `health/`, `graph/`, `containment/`, `notifications/`, `skills/`, `triage/`,
`focus.yaml`, `performance-mode.yaml` and the rest) belong in `CLONE`, and no sub-issue of the
epic moves them yet. A consumer's committed copy of the template is upgraded through a pull
request (#1877), never by an in-place edit of a tracked file.

### 14. VS Code setting scopes

A setting whose value is an absolute path or an executable on the local machine has scope
`machine`, so a committed `.vscode/settings.json` (or a malicious repository) cannot redirect it.
Today that is `nightgauge.backend.binaryPath`. `machine-overridable` is for a path a team may
share and a user may override; none exists today. A test fails when a key ending in `Path`,
`binary`, `Binary` or `Dir` has neither scope (#2044).

### 15. Migration: one path, no fallback

The repository operating contract (`AGENTS.md`) forbids compatibility shims and migration
fallbacks. The original proposal's "read the new location first, then the old one" and "keep a
legacy fallback for N releases" are **not adopted**; the epic body already records that. The rule:

1. **Runtime code reads and writes only the new location.** It never opens a legacy path.
2. **`nightgauge doctor`** reports every file found at a legacy location, with its target path,
   as a warning (exit status unchanged).
3. **`nightgauge doctor --fix`** moves each class once (rename on the same filesystem, else copy
   then delete), never overwrites a file present at both locations (it reports both paths),
   moves idle worktrees with `git worktree move` and skips busy ones, deletes legacy caches
   instead of moving them, and writes `layout-version` (an integer, `1` for this layout) in
   `CLONE` and in `STATE`. A second run is a no-op that reports the version.
4. **Fail closed where a cold start does harm.** Two classes may not silently start empty:
   `machine-id` and in-flight pipeline state. For those, runtime code may test whether the legacy
   path exists, solely to refuse with an error naming `nightgauge doctor --fix`. That is a guard,
   not a read: the legacy content is never used.
5. **A relocation and its migration ship in the same release.** `main` may carry the gap between
   the two merges; a release tag may not.
6. The pre-existing Linux fallback that reads `~/.nightgauge/config.yaml` when
   `~/.config/nightgauge/config.yaml` is absent (`configpath.LegacyForGOOS`) is the one dual read
   left in the loader. It is removed, and `doctor --fix` moves that file (#2041).

### 16. Environment overrides

| Location       | Override (highest first)                                     |
| -------------- | ------------------------------------------------------------ |
| Machine config | `NIGHTGAUGE_CONFIG_HOME`, `XDG_CONFIG_HOME`                  |
| Caches         | `NIGHTGAUGE_CACHE_HOME`, `XDG_CACHE_HOME`                    |
| Machine state  | `NIGHTGAUGE_STATE_HOME`, `XDG_STATE_HOME`                    |
| Daemon socket  | `NIGHTGAUGE_RUNTIME_DIR`, `XDG_RUNTIME_DIR`                  |
| Worktrees      | `pipeline.worktree_base` (config key), else follows `STATE`  |
| Per-clone data | none; git's `GIT_DIR` and `GIT_COMMON_DIR`                   |
| License key    | `NIGHTGAUGE_LICENSE_KEY`                                     |
| GitHub token   | `GITHUB_TOKEN` (`GH_TOKEN` for `gh`), or an `env:` reference |

A child process that must see the operator's machine tier while its own `XDG_*` variables are
redirected (the OpenCode per-run root, ADR-022 § 8) is given the explicit `NIGHTGAUGE_*`
override, as `config.MachineConfigDir` already does for config.

### 17. Security constraints

- Every directory this ADR places outside the working tree (`CACHE`, `STATE`, `RUNTIME`, `CLONE`,
  the default `WTBASE`) is created with mode 0700.
- Writers refuse to follow a symlink out of their root: targets are checked after symlink
  evaluation, and a symlinked root or file is refused rather than followed. Deletion (pruning,
  migration, cleanup) is confined to regular files inside the resolved root.
- Secrets are never passed on argv and never written to a log; files holding a secret or the
  device identity are mode 0600.

## Consequences

- `git status` on a clone stays clean after any Nightgauge command, apart from the committed
  files in § 13 and knowledge the pipeline adds through its pull requests.
- Nothing Nightgauge writes is traversed by search, watchers, linters, test runners or Docker
  build contexts, except committed content.
- `.nightgauge/` becomes small and reviewable, and the ignore file stops growing with each new
  runtime file.
- Every path moves behind a resolver first (#2033–#2036, no behaviour change), so the location
  flip (#2037) is a small diff.
- Skills and docs that print `.nightgauge/plans/...` or `~/.nightgauge/...` as current paths must
  change; #2045 writes the reference page and brings the docs in line.

## Conflicts with sub-issue bodies

Where an issue body and this ADR differ, the ADR governs, and the issue should be corrected:

- **#2028:** keys the recall cache on the canonical repository _root_; the key is the canonical
  git _common dir_ (the root differs per linked worktree, which defeats the issue's own sharing
  goal). The lift also adds the Windows `cache` segment and `XDG_CACHE_HOME` on every OS (§ 6).
- **#2029:** does not name the defaults or keys (200 MB, 30 days, `pipeline.logs.max_size_mb`,
  `pipeline.logs.max_age_days`), and whole-file pruning requires `github-api.jsonl` to be
  written in segments (§ 11).
- **#2031:** its verification names a package `internal/machinepaths`; the package is
  `internal/layout` (§ 1).
- **#2037:** must also cover skills and docs that hard-code per-clone paths (the sweeps
  #2033–#2036 cover Go and the extension only), add the layout subcommand, route agent-authored
  plans and retros through a `nightgauge` command, add the fail-closed guard for in-flight
  pipeline state, and ship in the same release as #2040 (§ 7, § 15).
- **#2038:** does not move `pipeline.worktree_base` out of the team tier, refuse relative values
  or drop the `.worktrees` default; the unset default is `STATE/worktrees/<repo-key>/` (§ 9).
- **#2039:** lists no environment override; `NIGHTGAUGE_RUNTIME_DIR` is added, with the length
  refusal (§ 10).
- **#2040:** lists the recall cache as a class to move; caches are deleted, never moved (§ 6).
- **#2041:** says machine config is not moved; on Linux the legacy `~/.nightgauge/config.yaml`
  read is removed and `doctor --fix` moves that file (§ 15, item 6).
- **#2043:** its allowlist omits `config.schema.json`, `pattern-mining-config.yaml`,
  `skill-evals/baseline.jsonl` and `model-evals/evidence/`, and must exclude
  `audit/scope-drift-stats.json` (§ 13).
- **#1883:** says to store the device key in `SecretStorage` as `startTrial` does; it must store
  it through the keychain entry (§ 5).
- **#2022 (this issue):** asks for machine state "under the XDG state directory on every OS";
  `XDG_STATE_HOME` is honoured on every OS when set, and the defaults are per platform (§ 8).
- **Gap:** no sub-issue moves the remaining per-clone runtime files listed in § 13 to `CLONE`;
  they need a follow-up issue.
