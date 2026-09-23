# Data and State Layout — team config committed, everything else outside the working tree

**Date:** 2026-09-23
**Author:** nightgauge
**Status:** Decided
**Issue:** #2022 (epic #2021)
**Implemented by:** #2023–#2045 (the epic's Phase 0–4 sub-issues; each is mapped to a row below)
**Consistent with:** [ADR-018](018-adapter-usage-quota-model.md) (`usage/` is per machine),
[ADR-020](020-value-adding-features-default-on.md) (§ 12 records the footprint reason for the
knowledge default)
**Amends (location only):** ADR-013 (the run trace moves with the pipeline-state class) and
[ADR-015](015-decision-requests.md) (the attention store is per-checkout runtime state)

---

## Executive Summary

Nightgauge had a decision for where _settings_ live
([SETTINGS_ARCHITECTURE.md](../SETTINGS_ARCHITECTURE.md)) and none for anything else. Each
subsystem picked its own path, most of them inside the working tree: caches, pipeline state,
plans, retros, logs, worktrees and the daemon socket all sat under `<repo>/.nightgauge/`, kept out
of git only by an ignore file that a CLI-only clone never received. Machine state hard-coded
`~/.nightgauge` while machine config followed platform conventions.

This ADR fixes one location per data class:

- **Committed:** team config and an explicit allowlist under `.nightgauge/`. Nothing else a
  Nightgauge command writes is committed by default; the knowledge base stays in the tree but is
  ignored unless a team opts in.
- **Per user, outside every repository:** machine config, secrets (OS keychain), caches, machine
  state, the daemon socket and, by default, pipeline worktrees.
- **Per clone, outside the working tree:** issue- and run-keyed pipeline data, plans, retros and
  logs under `$(git rev-parse --git-common-dir)/nightgauge/`; per-checkout run control under
  `$(git rev-parse --absolute-git-dir)/nightgauge-worktree/`.
- **Extension-only:** VS Code's own storage (`SecretStorage`, mementos, `storageUri`,
  `globalStorageUri`, `logUri`).

Migration is a one-time move, never a runtime fallback: the binary performs it automatically,
under a lock, the first time a command needs the new layout, with the same code as
`nightgauge doctor --fix`; when it cannot, it fails closed with an error naming that command.

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

### Implemented in Phase 0

These decisions were taken and implemented in the Phase 0 issues, and are on `main`. This ADR
records them as settled and does not re-decide them.

- **Ignore rules from the Go binary** (#1090, #2026; PR #2047): `nightgauge config init` and
  `nightgauge serve` write `.nightgauge/.gitignore` from `internal/scaffold`'s embedded template
  (version 15). When the committed file is tracked and older, it is left alone and the current
  rules go to the common dir's `info/exclude` in a marked block. Version 15 ignores
  `/knowledge/`; § 12 keeps that default.
- **Plaintext credentials refused in repository tiers** (#2023, #2024; PR #2049): the loader
  rejects a plaintext `github_auth.token`, `github_auth.tokens.*` or `platform.license_key` in
  `.nightgauge/config.yaml` or `.nightgauge/config.local.yaml`, and accepts only `env:` references
  there. `internal/configpath` is the one Go resolver of the machine-tier file, and
  `internal/credshape` is the one list of credential shapes that both the loader and `nightgauge
doctor`'s `tracked_secrets` check read; that check reports a committed credential with the value
  redacted.
- **OS keychain store** (#2025, #2027; PR #2048): `internal/keychain` stores credentials under
  service `nightgauge`, account = the credential's dotted machine-tier path
  (`platform.license_key`). License-key resolution is `NIGHTGAUGE_LICENSE_KEY`, then the keychain
  entry, then the 0600 machine-tier file. The keychain entry is the source of truth; the
  extension's `SecretStorage` copy is trusted only while its fingerprint (the first 12 hex
  characters of scrypt over the key with the salt `nightgauge/license-fingerprint/v1`) matches the
  one `nightgauge auth license status` reports, and the extension writes the key by piping it to
  `nightgauge auth license set` on stdin. The extension's startup migration of an existing key
  reads only the machine tier: a key in the committed project config is never imported.
- **Cache home** (PR #2020): the GitHub conditional-request (ETag) store resolves to
  `$NIGHTGAUGE_CACHE_HOME/github-conditional`, else
  `os.UserCacheDir()/nightgauge/github-conditional`.

## Decision

### 1. Roots

Every location below is built from one of seven roots. Each root has one resolver in Go, an
override, and a documented default per OS.

**XDG rule, every OS:** when `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME` or
`XDG_RUNTIME_DIR` is set, the matching root is `$XDG_…/nightgauge` on Linux, macOS and Windows
alike, as the machine-config resolver already does for `XDG_CONFIG_HOME`. The table gives the
default when the variable is unset. A `NIGHTGAUGE_*` override beats the XDG variable.

| Root       | Default on Linux                  | Default on macOS                  | Default on Windows                | Override                                         | Resolver                       |
| ---------- | --------------------------------- | --------------------------------- | --------------------------------- | ------------------------------------------------ | ------------------------------ |
| `CONFIG`   | `~/.config/nightgauge`            | `~/.nightgauge`                   | `%APPDATA%\nightgauge`            | `NIGHTGAUGE_CONFIG_HOME`                         | `internal/configpath` (#2023)  |
| `CACHE`    | `~/.cache/nightgauge`             | `~/Library/Caches/nightgauge`     | `%LOCALAPPDATA%\nightgauge\cache` | `NIGHTGAUGE_CACHE_HOME`                          | `internal/layout.CacheHome`    |
| `STATE`    | `~/.local/state/nightgauge`       | `~/.nightgauge/state`             | `%LOCALAPPDATA%\nightgauge\state` | `NIGHTGAUGE_STATE_HOME`                          | `internal/layout.StateHome`    |
| `RUNTIME`  | `<os.TempDir()>/nightgauge-<uid>` | `<os.TempDir()>/nightgauge-<uid>` | not applicable (no Unix socket)   | `NIGHTGAUGE_RUNTIME_DIR`                         | `internal/layout.RuntimeDir`   |
| `CLONE`    | `<git-common-dir>/nightgauge`     | same                              | same                              | none (§ 7)                                       | `internal/layout.CloneDir`     |
| `CHECKOUT` | `<git-dir>/nightgauge-worktree`   | same                              | same                              | none (§ 7)                                       | `internal/layout.CheckoutDir`  |
| `WTBASE`   | `STATE/worktrees/<repo-key>`      | same                              | same                              | `pipeline.worktree_base` (machine or local tier) | `internal/layout.WorktreeBase` |

- `<git-common-dir>` is `git rev-parse --path-format=absolute --git-common-dir`: every linked
  worktree of a clone resolves to the main clone's directory. `<git-dir>` is
  `git rev-parse --absolute-git-dir`: the main checkout's `.git`, or `.git/worktrees/<name>` for a
  linked worktree, which git deletes when the worktree is removed.
- `<repo-key>` is the first 12 hex characters of the SHA-256 of the canonical git common dir.
- `internal/layout` is a new leaf package, importable from anywhere without a cycle (the reason
  `internal/configpath` is a leaf). It owns every root except `CONFIG`, and the class resolvers
  (`PipelineStateDir`, `PlansDir`, `RetrosDir`, `CloneLogsDir`, `CheckoutDir`).
- The TypeScript side has one resolver per root as well, and obtains the values from the binary
  (§ 7). The existing TS `globalConfigResolver` is the only TS reader of the machine-config path.

### 2. Where each data class lives

| Data class                                                                                                                                        | Location                                                                | Override                                      | Committed                  | Writer · owner package                                                        | Retention                                            | Implementing issue                      |
| ------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- | --------------------------------------------- | -------------------------- | ----------------------------------------------------------------------------- | ---------------------------------------------------- | --------------------------------------- |
| Team config (`config.yaml`, `config.schema.json`, `pattern-mining-config.yaml`)                                                                   | `<repo>/.nightgauge/`                                                   | none                                          | yes                        | humans, by PR; runtime never writes it · `internal/config`                    | git history                                          | #2023                                   |
| Committed allowlist (`.gitignore`, `audit/`, `skill-smoke/`, `skill-evals/baseline.jsonl`, `model-evals/evidence/`)                               | `<repo>/.nightgauge/`                                                   | none                                          | yes                        | humans and the stages that author them · `internal/scaffold` (template)       | git history                                          | #2043                                   |
| Knowledge base                                                                                                                                    | `<repo>/.nightgauge/knowledge/`                                         | none                                          | no by default; team opt-in | pipeline stages, in the issue worktree · `internal/knowledge`                 | kept in the checkout; history when opted in          | #2042                                   |
| Per-user config (machine tier)                                                                                                                    | `CONFIG/config.yaml`, mode 0600                                         | `NIGHTGAUGE_CONFIG_HOME`, `XDG_CONFIG_HOME`   | no                         | the user; settings UI for machine keys · `internal/configpath`                | kept                                                 | #2023; #2041 (Linux legacy, TS readers) |
| Per-clone config                                                                                                                                  | `<repo>/.nightgauge/config.local.yaml`                                  | none                                          | no (ignored)               | the user; default UI write target · `internal/config`                         | kept                                                 | #2023                                   |
| Credentials Nightgauge issues or receives (license key, device key)                                                                               | OS keychain, service `nightgauge`; fallback `CONFIG/config.yaml` (0600) | `NIGHTGAUGE_LICENSE_KEY`                      | never                      | `nightgauge auth license set` (stdin) · `internal/keychain`                   | until cleared                                        | #2025, #2027                            |
| GitHub tokens                                                                                                                                     | gh's credential store; fallback `CONFIG/config.yaml` (0600)             | `GITHUB_TOKEN`, `GH_TOKEN`; `env:` refs       | never                      | `nightgauge forge auth login` / `refresh` (stdin to `gh`) · `internal/github` | until cleared                                        | #2023, #2024                            |
| Every other credential (§ 5 list)                                                                                                                 | environment only                                                        | the named variable                            | never                      | the operator or CI                                                            | process lifetime                                     | #2031 (argv flag, CI)                   |
| Credentials entered in the extension UI (webhooks, Slack, Mattermost, Gemini, platform tokens)                                                    | VS Code `SecretStorage`; handed to Go children in their environment     | none                                          | never                      | the extension                                                                 | until cleared                                        | settled                                 |
| Caches (GitHub ETag store, recall index, any derived index)                                                                                       | `CACHE/<name>/`; recall at `CACHE/recall/<root-key>/`                   | `NIGHTGAUGE_CACHE_HOME`, `XDG_CACHE_HOME`     | never                      | the component that derives it · `internal/layout.CacheHome`                   | disposable; deleting it costs one rebuild            | PR #2020 (ETag); #2028                  |
| Issue- and run-keyed pipeline data (`history/`, contexts, results, ADR-013 traces, `runtime-{issue}-{runId}.json`)                                | `CLONE/pipeline/`                                                       | none                                          | never                      | Go orchestrator and CLI · `internal/layout`                                   | history: `pipeline.logs.history_retention_days` (90) | #2033–#2037                             |
| Per-checkout run control (`current-run.json`, `run-state.json`, `batch-state.json`, `queue-state.json`, `PLAN.md`, serve lease, `go-backend.log`) | `CHECKOUT/`                                                             | none                                          | never                      | the checkout's orchestrator and daemon · `internal/layout`                    | deleted with the checkout                            | #2033–#2037                             |
| Plans and retros                                                                                                                                  | `CLONE/plans/`, `CLONE/retros/`                                         | none                                          | never                      | stages, through a `nightgauge` command · `internal/layout`                    | kept with the clone                                  | #2033–#2037                             |
| Other per-checkout runtime files (`attention/`, `autonomous/`, `health/`, `graph/`, `focus.yaml`, …)                                              | `CHECKOUT/<name>`                                                       | none                                          | never                      | their current writers                                                         | per writer                                           | #2037                                   |
| Per-clone logs (Go-consumed: `*_session.log`, `github-api.jsonl`, `sanitization.log`)                                                             | `CLONE/logs/`                                                           | none                                          | never                      | Go binary; extension's Go-consumed writers via one helper                     | 200 MB and 30 days, never an unfinished run's logs   | #2029, #2030, #2035, #2037              |
| Extension-only logs (including `automations.log`)                                                                                                 | VS Code `ExtensionContext.logUri`                                       | none (VS Code's)                              | never                      | the extension                                                                 | VS Code's session rotation                           | #2030                                   |
| Extension-only data                                                                                                                               | mementos, `storageUri`, `globalStorageUri`                              | none                                          | never                      | the extension                                                                 | VS Code-managed                                      | settled                                 |
| Worktrees                                                                                                                                         | `WTBASE/<repo>-issue-<N>`                                               | `pipeline.worktree_base` (machine/local tier) | never                      | `internal/execution` · `internal/layout.WorktreeBase`                         | removed by pipeline cleanup and reclaim              | #2038                                   |
| Daemon socket                                                                                                                                     | `RUNTIME/<key>.sock`                                                    | `NIGHTGAUGE_RUNTIME_DIR`, `XDG_RUNTIME_DIR`   | never                      | `nightgauge serve` · `internal/ipc` over `internal/layout`                    | daemon lifetime                                      | #2039                                   |
| Machine state (serve sidecar, `rate-limit.json`, `ratelimit-gitlab-<host>.json`, `machine-id`, `telemetry-notice-v1`)                             | `STATE/`                                                                | `NIGHTGAUGE_STATE_HOME`, `XDG_STATE_HOME`     | never                      | Go binary · `internal/layout.StateHome`                                       | kept; rate-limit files are cold-start hints          | #2031                                   |
| Machine state (`usage/`, `opencode/runs`, `opencode/self-test`, `opencode/last-dispatch.json`, machine logs)                                      | `STATE/usage/`, `STATE/opencode/`, `STATE/logs/`                        | `NIGHTGAUGE_STATE_HOME`, `XDG_STATE_HOME`     | never                      | Go binary; extension reads `usage/` via the binary · `internal/layout`        | logs: 200 MB and 30 days; others per writer          | #2029, #2032                            |
| Operator-installed tools (OpenCode pin)                                                                                                           | `~/.nightgauge/tools/` (unchanged)                                      | the absolute `opencode.binary` value          | never                      | the operator (`npm i --prefix`)                                               | operator-managed                                     | none (exception, § 9)                   |
| Layout-version marker and migration lock                                                                                                          | `CLONE/`, `CHECKOUT/`, `STATE/` (`layout-version`, `.migrate.lock`)     | follows its root                              | never                      | the migration · `internal/doctor`                                             | kept                                                 | #2040, #2041                            |

### 3. Team config (committed)

`.nightgauge/config.yaml` is the one team config file, in the ESLint/Prettier/Renovate shape: a
reviewed file, changed by pull request, identical for everyone. Runtime code never writes it
(`internal/config/writer.go` already routes runtime writes elsewhere). It holds no credential in
plaintext; the loader refuses one (#2023).

Rejected: a team tier outside the repository (a shared network location or a platform-held
config). It loses review through pull requests and makes a checkout's configuration depend on
something the checkout does not contain.

### 4. Per-user config tiers

The precedence chain in [SETTINGS_ARCHITECTURE.md](../SETTINGS_ARCHITECTURE.md) and
[CONFIGURATION.md](../CONFIGURATION.md) stands: project (team) overrides machine, and the local
tier overrides both.

- **Machine tier:** `CONFIG/config.yaml`, mode 0600, resolved only by `internal/configpath` in Go
  and `globalConfigResolver` in TypeScript. It is the only file that may hold a credential in
  plaintext. The TS readers that hard-code `~/.nightgauge/config.yaml`
  (`authResolver.ts`, three sites; `IpcClientBase.ts`, three sites) read the wrong file on Linux
  today; #2041 routes them through `globalConfigResolver` in the same change that removes the
  Linux legacy read, so both languages switch together.
- **Local tier:** `.nightgauge/config.local.yaml` stays in the working tree and ignored. It is
  hand-edited like `.env.local`, and moving it under the git dir would hide it from the people
  who edit it. Like the project tier, it accepts credentials only as `env:` references, because
  it is one `git add -f` from a commit.

Rejected: moving the macOS machine config from `~/.nightgauge` to
`~/Library/Application Support/nightgauge`. It would move a file every macOS user has, for no
failure the audit found; the macOS state root is placed beside it (§ 8) instead.

### 5. Secrets

**Rule by credential.** Nightgauge keeps a credential on disk only when Nightgauge itself issues
or receives it. Everything else is the owning tool's or the environment's.

| Credential                                                                                                                                                                    | Where it lives                                                                                                                                                                                                                     |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `platform.license_key`, the per-device key (#1883)                                                                                                                            | env `NIGHTGAUGE_LICENSE_KEY`, else the keychain entry, else the 0600 machine file                                                                                                                                                  |
| GitHub tokens (`github_auth.token`, `github_auth.tokens.*`)                                                                                                                   | env (`GITHUB_TOKEN`, `GH_TOKEN`) or `env:` refs, gh's credential store, or the 0600 machine file. `nightgauge forge auth login` and `refresh` store through `gh auth login --with-token` on stdin; Nightgauge keeps no second copy |
| `NIGHTGAUGE_API_KEY` (platform API key)                                                                                                                                       | env only; the `--api-key` flag, which puts it on argv, is removed                                                                                                                                                                  |
| `COPILOT_GITHUB_TOKEN`, GitLab `CI_JOB_TOKEN`, forge `token_env`                                                                                                              | env only                                                                                                                                                                                                                           |
| `GITLAB_WEBHOOK_SECRET` / `secret_env_var`, channel `token_env`                                                                                                               | env only                                                                                                                                                                                                                           |
| Discord, Slack and Mattermost (`discord_webhook_env`, `bot_token_env`, `SLACK_BOT_TOKEN`, `NIGHTGAUGE_STUCK_EPIC_WEBHOOK`, `NIGHTGAUGE_SHIP_NOTIFY_WEBHOOK`)                  | env only on the Go side; the extension's copies in `SecretStorage`                                                                                                                                                                 |
| Model providers (`ANTHROPIC_API_KEY`, `XAI_API_KEY`, `GEMINI_API_KEY`, `GOOGLE_API_KEY`, `NIGHTGAUGE_OLLAMA_API_KEY`, `NIGHTGAUGE_LM_STUDIO_API_KEY`, OpenCode `api_key_env`) | env only; the Gemini key entered in the extension is held in `SecretStorage` and passed in the child's environment                                                                                                                 |

A config field that _names_ an environment variable (`*_env`, `token_env`, `secret_env_var`,
`api_key_env`) is not a secret and may be committed; the value it names never is.

**Stores.** The OS keychain (macOS Keychain, Windows Credential Manager, the Secret Service on
Linux) through `internal/keychain`, service `nightgauge`, account = the dotted machine-tier key,
shared by extension, CLI and daemon. The extension writes through `nightgauge auth license set`
on stdin and never opens the keychain itself. On a host with no usable keychain the 0600
machine-tier file is the only file form, as in `gh`.

**Never in a repository tier in plaintext:** `github_auth.token`, `github_auth.tokens.*`,
`platform.license_key`; any credential added later joins the list in
`internal/config/repo_tier_credentials.go`. **Never on argv** (visible to other local users
through `ps`) and **never in a log**; diagnostics carry a source label or a fingerprint.

**CI and shared runners.** When `CI=true`, Nightgauge never writes a credential to disk (neither
the keychain nor the machine file) and resolves credentials from the environment first; a
self-hosted runner shared between jobs would otherwise carry one job's key into the next.
`nightgauge doctor` reports a credential in the machine file on a CI host.

**Threat model.** The keychain protects a credential from other OS users, from backups and sync
of plain files, and from commits. It does not protect it from code running as the same user: an
item created through macOS's `security` tool can be read back by any same-user process running
`/usr/bin/security` without a prompt, and pipeline agents are same-user processes. Credentials
reachable by agents are therefore scoped to what an agent may do: the license key is per device
and revocable, and GitHub tokens should be the least-privileged token that runs the pipeline.

Rejected: `SecretStorage` as the source of truth (the CLI and daemon cannot read it, the defect
#2025 fixes); a Nightgauge keychain entry for GitHub tokens (a second copy beside gh's, which can
drift); an encrypted file in the repository (the key still has to live somewhere, and the
ciphertext is permanent in history).

### 6. Caches

A cache is anything that can be deleted at any time and rebuilt from its sources at the cost of
time or API calls. Caches live under `CACHE`, one subdirectory per cache. The resolver is the one
PR #2020 introduced, lifted into `internal/layout.CacheHome` by #2028; no second resolver is
added.

- The recall index is keyed by `<root-key>`, a stable hash of the canonical repository root
  (#2028): each linked worktree indexes its own checkout, whose knowledge files differ, and two
  clones never collide.
- On Windows `os.UserCacheDir()` is `%LOCALAPPDATA%`, which is also the parent of `STATE`; the
  cache root is `%LOCALAPPDATA%\nightgauge\cache`, so deleting the cache never deletes state.
- An explicitly set `XDG_CACHE_HOME` is honoured on every OS (§ 1).
- A cache is never migrated: the migration deletes the legacy copy. With no usable cache
  directory, the cache is kept in memory for the call and nothing is written into the working
  tree.

Rejected: caches in the working tree (the audit's finding); caches under `CLONE` (backed up and
synced with the clone, and not per clone in every case: the ETag store is keyed by token).

### 7. Per-clone and per-checkout data

**Two roots.** `CLONE = <git-common-dir>/nightgauge/` holds what every checkout of the clone
shares; `CHECKOUT = <git-dir>/nightgauge-worktree/` holds what belongs to one checkout (the main
checkout or one linked worktree). Git never tracks anything under its own directory, so neither
can be committed whatever the ignore rules say; `CLONE` is deleted with the clone and `CHECKOUT`
with its worktree.

**Namespacing rule.** A file in `CLONE` must be keyed by issue number or run id, or be an
append-only log written under the clone lock; a single unkeyed file shared by several
orchestrators is a collision. So:

- `CLONE/pipeline/` holds `history/`, per-issue contexts and results, ADR-013 traces
  (`trace/<run_id>.jsonl`) and `runtime-{issue}-{runId}.json`; `CLONE/plans/`, `CLONE/retros/`
  and `CLONE/logs/` are issue-keyed.
- `CHECKOUT/` holds every unkeyed singleton: `current-run.json`, `run-state.json`,
  `batch-state.json`, `queue-state.json`, the hooks' `PLAN.md` fallback, the serve lease,
  `go-backend.log`, and the other per-checkout runtime files the current template ignores one by
  one (`attention/`, `autonomous/`, `health/`, `graph/`, `containment/`, `notifications/`,
  `skills/`, `triage/`, `focus.yaml`, `performance-mode.yaml`, `careful.lock` and the rest).
  #2037 moves them together with the four main classes.

**Locks and ownership.**

- One daemon per checkout: `nightgauge serve` holds an exclusive advisory lock
  (`flock`/`LockFileEx`) on `CHECKOUT/serve.lock` for its lifetime. A second daemon for the same
  checkout (a second window, or the same path spelled with different case on a case-insensitive
  volume) fails to take the lock and runs without the socket, as it does today when the socket is
  in use. The lock is on a file, so path spelling cannot defeat it.
- Daemons of different checkouts of one clone (the main checkout and a linked worktree) are
  separate owners of separate `CHECKOUT` roots and share `CLONE`. Writes to `CLONE` are
  temp-file-plus-rename for keyed files, and appends to shared logs and history take
  `CLONE/.lock` for the duration of the append.

**Resolving git.** The resolvers take an absolute root and run `git -C <root>` with `GIT_DIR`,
`GIT_WORK_TREE`, `GIT_COMMON_DIR` and `GIT_INDEX_FILE` removed from the environment: a git hook
exports `GIT_DIR`, and an inherited value would resolve another repository's directory. Outside a
git repository the resolvers return an error; no caller resolves against the process cwd. There
is no Nightgauge override for `CLONE` or `CHECKOUT`: a relocation would break the sharing rule.

**One path source.** The extension, skills and docs obtain these paths from the binary, through a
`nightgauge` subcommand that prints the resolved layout as JSON (added by #2037), not by
hard-coding `.nightgauge/plans` or its siblings. A TypeScript reimplementation is allowed only
where the binary cannot be asked, and only with a parity test.

**Agents do not write under the git directory by path.** Agent harnesses may guard writes under
`.git/`, and a direct write would bypass § 17. A stage that produces a plan or a retro hands it
to a `nightgauge` command, which writes it through the resolver.

**Group-shared clones.** When a clone sets `core.sharedRepository`, `CLONE` and `CHECKOUT` follow
the group mode git uses for that repository (directories group-writable, setgid), so every member
of the group can run the pipeline in it. Nothing in these roots is a credential (§ 5), so group
access exposes run data only. Every other root stays 0700.

**Read-only git directory.** A command that must write `CLONE` or `CHECKOUT` fails with an error
naming the directory; read-only commands (`status`, `doctor`) report without writing. Nothing is
redirected into the working tree.

Rejected: `.nightgauge/` in the working tree with ignore rules (the status quo); one shared root
for everything (the singleton collisions above); `STATE/clones/<repo-key>/` (outlives the clone
and needs its own garbage collection).

### 8. Machine state on every OS

One resolver, `internal/layout.StateHome`, replaces every hard-coded `~/.nightgauge` state path:
`NIGHTGAUGE_STATE_HOME`, then `XDG_STATE_HOME/nightgauge`, then the platform default in § 1.

- **Linux** follows XDG, like its machine config.
- **macOS** keeps state beside its machine config, in `~/.nightgauge/state`, so config and state
  share one convention on each OS; that split was the audit's Linux finding.
- **Windows** uses `%LOCALAPPDATA%` (not roaming), because `machine-id` identifies this device
  and must never roam to another one through `%APPDATA%`.

The rate-limit files (`rate-limit.json`, `ratelimit-gitlab-<host>.json`) are cold-start hints: a
missing file is a cold start, not an error. `machine-id` keeps mode 0600 and is moved byte for
byte, never regenerated: a regenerated id is a new device to the platform and counts against the
account's machine limit. `telemetry-notice-v1` is moved, so the notice is not shown twice.

**No usable home.** When `HOME` (or `%USERPROFILE%`) is unset or the resolved root is read-only,
as in some CI containers, a command that needs `CONFIG`, `STATE` or `WTBASE` fails with an error
naming the `NIGHTGAUGE_*` override; caches fall back to memory (§ 6) and the rate-limit hints
start cold. Nothing falls back into the working tree.

Rejected: `~/Library/Application Support/nightgauge` on macOS (splits config and state across two
roots on one OS, and puts a space in every path the skills and docs print); `os.UserConfigDir()`
on Windows (roaming).

### 9. Worktrees

Today two writers disagree. The Go binary does not parse `pipeline.worktree_base`
(`internal/config/config.go` leaves it to TypeScript) and `internal/execution/worktree.go`
hard-codes `<repo>/.nightgauge/worktrees/<repo>-issue-<N>`. The extension's `WorktreeManager`
treats `worktree_base` as relative to the repository root, defaulting to `.worktrees`, and names
worktrees `issue-<N>`. Both are inside the working tree.

Decision:

- One resolver, `internal/layout.WorktreeBase`, which the Go loader now parses; the extension
  obtains the path from the binary. Worktrees are named `<repo>-issue-<N>` everywhere.
- Unset `pipeline.worktree_base` means `STATE/worktrees/<repo-key>/`.
- `pipeline.worktree_base` becomes a machine- or local-tier key: an absolute path on one machine,
  which a committed team file cannot express for everyone. `~` is expanded. The TS `.worktrees`
  default and the `team` classification in `internal/config/testdata/tier_classification.yaml`
  go (#2038).
- **Relative or in-tree values are not silently reinterpreted.** A relative value, a value in the
  team tier, or one that resolves inside the working tree makes worktree creation fail with an
  error that names the file and line and the fix (delete the key, or set an absolute path in the
  machine or local tier); `nightgauge doctor` reports it. Existing worktrees under
  `.nightgauge/worktrees/` or `.worktrees/` are moved by the migration (§ 15).
- Sweep and reclaim find worktrees through `git worktree list`, not by scanning a directory.
  Containment is checked after resolving the parents' symlinks, so a crafted base or issue slug
  cannot place a worktree outside the base.
- `doctor` reports worktrees whose clone no longer exists (their git metadata is gone), and
  `doctor --fix` removes them.
- **Windows path length:** the default base is about 75 characters before the repository's own
  paths. `doctor` warns on Windows when `core.longpaths` is not enabled.

Rejected: `CLONE/worktrees/` (agents and editors work inside the worktree, and tools and harnesses
treat anything under `.git/` as off limits); a sibling directory of the clone (writes into a parent
directory the user did not give Nightgauge, which may be cloud-synced or not writable).

`~/.nightgauge/tools/` (the OpenCode pin, [ADR-022](022-opencode-multi-provider-adapter.md)
§ 20) is operator-installed software referenced by an absolute path in machine config. It is not
state and is not moved.

### 10. The daemon socket

`RUNTIME/<key>.sock`, where `RUNTIME` is `NIGHTGAUGE_RUNTIME_DIR`, else
`$XDG_RUNTIME_DIR/nightgauge`, else `<os.TempDir()>/nightgauge-<uid>`, and `<key>` is the first
12 hex characters of the SHA-256 of the checkout's canonical git dir (`CHECKOUT`'s parent).

- **Which daemon a client reaches.** The daemon exports `NIGHTGAUGE_DAEMON_SOCKET` to every
  process it spawns, so a stage running in a pipeline worktree reaches the daemon that started it.
  Any other client computes the key from its own checkout. The `CHECKOUT/serve.lock` (§ 7), not
  the key, guarantees one owner per checkout.
- **Length.** `sun_path` is 104 bytes on macOS and 108 on Linux. The resolver returns an error,
  never a truncated path, when the result exceeds 100 bytes (possible only through a long
  override).
- **Directory.** Created 0700 and, before binding, verified to be a directory, not a symlink,
  owned by the current uid, with no group or other bits; otherwise the daemon refuses to start. A
  shared `/tmp` lets another user pre-create it. Only the final component is checked for a
  symlink: parent symlinks such as macOS `/var` → `/private/var` are resolved normally.
- **Stale socket.** Before binding, the daemon dials the existing path. Connection refused means
  stale: it unlinks the file and binds. A successful connection means a live daemon: it keeps the
  existing `ErrSocketInUse` behaviour. Any other error: it refuses to unlink.
- **Peer check.** On every accepted connection the daemon reads the peer's uid
  (`SO_PEERCRED` on Linux, `getpeereid` on macOS) and closes connections from any other uid.
- The extension and CLI obtain the path from the binary or the same algorithm, pinned by a
  parity test. The socket needs no migration: it is recreated on each daemon start.

Rejected: the socket in the working tree (length, synced folders, container bind mounts); under
`CLONE` (the path length is still the clone's depth).

### 11. Log retention

One Go pruning function enforces, per log directory (`CLONE/logs/`, `STATE/logs/`):

- a total size cap, default **200 MB**, and a maximum file age, default **30 days**, set by
  machine-tier keys `pipeline.logs.max_size_mb` and `pipeline.logs.max_age_days`;
- oldest files first, regular files only, confined to the resolved directory;
- **never a file belonging to a run that is not terminal** (§ 15 defines in-flight): a run paused
  for 40 days keeps its logs. If only such files remain over the cap, pruning stops and `doctor`
  reports the directory as over its cap;
- at `serve` start and at most daily while it runs, and at CLI start when the last prune is over a
  day old.

Pruning deletes whole files, so an append-only log that would otherwise grow as one file
(`github-api.jsonl`) is written in dated segments. `sanitization.log`, which `internal/hooks`
writes today relative to the process cwd with mode 0644, is written to `CLONE/logs/` resolved
from the hook's repository root, with mode 0600 (#2035). Readers report a pruned range as absent,
not as an error. Pipeline history under `CLONE/pipeline/history/` keeps its existing retention
(`pipeline.logs.history_retention_days`, default 90).

Rejected: size cap only (old files of no value survive on a quiet machine); rotation by an
external tool such as logrotate (not present on macOS or Windows, and no CLI-only clone would
configure it).

### 12. Knowledge

The knowledge base stays at `.nightgauge/knowledge/` in the checkout and is **ignored by
default**, as template version 15 has it. A team that wants it committed adds `!/knowledge/`
below the template's "Local additions (kept on upgrade)" line, which upgrades keep.

Reason: the direction for this epic is that only team config is committed by default. Knowledge
files are written on every run, in every consumer repository, and committing them is a
repository-footprint choice, which [ADR-020](020-value-adding-features-default-on.md) names as a
legitimate reason for an opt-out. The feature itself stays on: knowledge is still written, read
and kept in the checkout.

The only derived data under `knowledge/` was the recall index, which moves to `CACHE/recall/`
(#2028). #2042 makes every statement agree with this default: the Go `config init` comment and
the `knowledge.go` comment say "ignored by default; commit it by adding `!/knowledge/` under Local
additions", `docs/KNOWLEDGE_BASE.md` says the same, and a Go test pins that the template ignores
`knowledge/features/1-x/PRD.md` and that the opt-in line un-ignores it.

Rejected: committed by default with a root-`.gitignore` opt-out (the resolution first recorded on
#1090, superseded by the epic's direction); moving knowledge out of the tree (it is authored
content that a team may choose to review and commit).

### 13. The committed `.nightgauge/` allowlist

Once per-clone data has moved, the template becomes deny-by-default (#2043): `/*` followed by `!`
rules for exactly these paths:

- `config.yaml`, `config.schema.json`, `pattern-mining-config.yaml` (team config);
- `.gitignore` (the template itself);
- `audit/` (authored audit files), except `audit/scope-drift-stats.json`, a per-machine counter;
- `skill-smoke/`;
- `skill-evals/baseline.jsonl` and `model-evals/evidence/` (tracked evaluation references).

`knowledge/` and `config.local.yaml` are ignored (the former unless the team opts in, § 12). The
per-directory rules and `.gitkeep` anchors for `pipeline/`, `plans/`, `logs/`, `worktrees/`,
`daemon.sock` and the caches are deleted. Anything a Nightgauge component still writes under
`.nightgauge/` is ignored by `/*`. A consumer's committed copy of the template is upgraded through
a pull request (#1877), never by an in-place edit of a tracked file.

### 14. VS Code setting scopes

A setting whose value is an absolute path or executable on the local machine, or a URL from which
code is fetched or to which credentials are sent, has scope `machine`, so a committed
`.vscode/settings.json` (or a malicious repository) cannot redirect it. Today that is
`nightgauge.backend.binaryPath` and `nightgauge.plugins.marketplaceUrl`; #2044 audits
`nightgauge.dashboardUrl` and `nightgauge.audit.legacyEndpoint` against the same rule.
`machine-overridable` is for a path a team may share and a user may override; none exists today.
A test fails when a key ending in `Path`, `binary`, `Binary`, `Dir` or `Url` has neither scope.

### 15. Migration: a one-time move, never a fallback

The repository operating contract (`AGENTS.md`) forbids compatibility shims and migration
fallbacks. The original proposal's "read the new location first, then the old one" and "keep a
legacy fallback for N releases" are **not adopted**; the epic body records that. A one-time move
is not a fallback: after it, the legacy path does not exist, and runtime code never reads legacy
content as data.

**Automatic, locked, fail-closed.**

1. Runtime code reads and writes only the new location.
2. The first command that resolves a root whose `layout-version` marker is absent or older runs
   the migration for that root, with the same code as `nightgauge doctor --fix`, before doing
   anything else. An upgrade therefore never loses state silently, whether or not anyone runs
   `doctor`.
3. The migration takes an exclusive lock (`.migrate.lock` in the target root), re-reads the
   marker under the lock, and returns if another process finished it.
4. It does not start while any run is in flight or any daemon of the clone is live (a live
   `CHECKOUT/serve.lock` in any worktree `git worktree list` reports), or when a target is not
   writable. In each such case, and on a conflict, the command fails closed: it exits with the
   code below and an error naming the blocker and `nightgauge doctor --fix`. The extension shows
   that error with an action that runs the command.
5. **In flight** means a run whose run state is not terminal: running, queued, paused, or parked
   awaiting a decision. Only completed, failed-terminal and cancelled runs are terminal.

**Mechanics, per file.** Copy to a temporary file in the target directory, `fsync` it, rename it
into place, `fsync` the directory, then delete the source. On one filesystem the copy is a rename.
File modes are preserved; symlinks are recreated as symlinks, never followed.

- **Idempotent after a crash.** A target equal byte for byte to its source means the move had
  finished: the source is deleted. Leftover temporary files are removed. The marker is written
  last, so a crash before it re-runs the migration.
- **Conflicts.** A target that exists and differs is never overwritten: both paths are reported
  and the migration stops for that root. Two exceptions, both by content: `usage/` readings keep
  the target (the store merges per bucket on the next reading, ADR-018), and append-only JSONL
  (`pipeline/history/`, `github-api.jsonl`) is merged as the union of lines ordered by timestamp.
- **Several linked worktrees.** Each checkout's legacy `.nightgauge/`, found through
  `git worktree list`, is migrated: issue- and run-keyed data merges into the one `CLONE` under
  the rules above, and per-checkout files go to that checkout's own `CHECKOUT`.
- **Worktrees** are moved with `git worktree move`, idle ones only.
- **Caches** are deleted, not moved.
- **`machine-id`** is moved byte for byte with mode 0600; a conflict fails closed.
- **Linux legacy machine config.** The loader's pre-existing fallback that reads
  `~/.nightgauge/config.yaml` when `~/.config/nightgauge/config.yaml` is absent is the one dual
  read left, and it goes. The migration moves the file with mode 0600 into a 0700 directory and
  never merges two YAML files: if both exist, that is a conflict. It never prints the file's
  content (it holds credentials) (#2041).

**Exit codes** (`doctor --fix` and any command that triggered the migration): `0` migrated or
nothing to do; `3` conflict, nothing overwritten; `4` blocked (a run in flight, a live daemon, a
held lock, or an unwritable target). Plain `nightgauge doctor` reports legacy files with their
targets as warnings and keeps its existing exit status.

**Releases.** A relocation and its migration ship in the same release. `main` may carry the gap
between the two merges; a release tag may not.

### 16. Overrides

| Location       | Override (highest first)                                                                                     |
| -------------- | ------------------------------------------------------------------------------------------------------------ |
| Machine config | `NIGHTGAUGE_CONFIG_HOME`, `XDG_CONFIG_HOME`                                                                  |
| Caches         | `NIGHTGAUGE_CACHE_HOME`, `XDG_CACHE_HOME`                                                                    |
| Machine state  | `NIGHTGAUGE_STATE_HOME`, `XDG_STATE_HOME`                                                                    |
| Daemon socket  | `NIGHTGAUGE_DAEMON_SOCKET` (set by the daemon for its children), `NIGHTGAUGE_RUNTIME_DIR`, `XDG_RUNTIME_DIR` |
| Worktrees      | `pipeline.worktree_base` (machine or local tier), else follows `STATE`                                       |
| Per-clone data | none; inherited `GIT_*` variables are cleared (§ 7)                                                          |
| License key    | `NIGHTGAUGE_LICENSE_KEY`                                                                                     |
| GitHub token   | `GITHUB_TOKEN`, `GH_TOKEN`, or an `env:` reference                                                           |

A child process that must see the operator's machine tier while its own `XDG_*` variables are
redirected (the OpenCode per-run root, ADR-022 § 8) is given the explicit `NIGHTGAUGE_*`
override, as `config.MachineConfigDir` already does for config.

### 17. Security constraints

- Every directory this ADR places outside the working tree (`CACHE`, `STATE`, `RUNTIME`, the
  default `WTBASE`, and `CLONE` and `CHECKOUT` unless the clone is group-shared, § 7) is created
  with mode 0700.
- Writers refuse a symlink as the final component of a root or a target; parent directories are
  resolved normally, so system symlinks such as macOS `/var` → `/private/var` work. Targets are
  checked after that resolution to lie inside their root. Deletion (pruning, migration, cleanup)
  is confined to regular files inside the resolved root.
- Secrets are never passed on argv and never written to a log; files holding a secret or the
  device identity are mode 0600.

## Consequences

- `git status` on a clone stays clean after any Nightgauge command, apart from the committed
  files in § 13.
- Nothing Nightgauge writes is traversed by search, watchers, linters, test runners or Docker
  build contexts, except committed content and the ignored knowledge tree.
- `.nightgauge/` becomes small and reviewable, and the ignore file stops growing with each new
  runtime file.
- Every path moves behind a resolver first (#2033–#2036, no behaviour change), so the location
  flip (#2037) is a small diff.
- Skills and docs that print `.nightgauge/plans/...` or `~/.nightgauge/...` as current paths must
  change; #2045 writes the reference page and brings the docs in line.
- A team that committed a relative `pipeline.worktree_base` gets an error naming the fix instead
  of worktrees in a new place.

## Conflicts with sub-issue bodies

Where an issue body and this ADR differ, the ADR governs, and the issue is corrected:

- **#2029:** names no defaults or keys (200 MB, 30 days, `pipeline.logs.max_size_mb`,
  `pipeline.logs.max_age_days`); excludes the open file where the rule is "no file of an
  unfinished run"; whole-file pruning requires `github-api.jsonl` in dated segments (§ 11).
- **#2031:** its verification names `internal/machinepaths`; the package is `internal/layout`.
  It also covers `ratelimit-gitlab-<host>.json` and `telemetry-notice-v1` (§ 8).
- **#2035:** must include `internal/hooks`' cwd-relative `sanitization.log` (§ 11).
- **#2037:** splits per-clone data into `CLONE` and `CHECKOUT` with the namespacing and lock
  rules, moves the remaining per-checkout runtime files, covers skills and docs, adds the layout
  subcommand, routes agent-authored plans and retros through a `nightgauge` command, clears
  inherited `GIT_*` variables, and ships in the same release as #2040 (§ 7, § 15).
- **#2038:** says `worktree_base` "is honoured elsewhere"; the Go binary does not parse it and
  the TypeScript side treats it as repo-relative. It must add the Go parsing, the machine/local
  tier, the error for relative or in-tree values, the `tier_classification.yaml` change, and the
  `STATE/worktrees/<repo-key>/` default (§ 9).
- **#2039:** adds `NIGHTGAUGE_RUNTIME_DIR` and `NIGHTGAUGE_DAEMON_SOCKET`, keys the socket on the
  checkout's git dir, and adds the stale-socket and peer-uid rules (§ 10).
- **#2040:** becomes the automatic, locked migration with the exit codes and merge rules of § 15,
  run by any command, not only `doctor --fix`.
- **#2041:** says machine config is not moved; on Linux the legacy file is moved and its read
  removed, and the TS readers are routed through `globalConfigResolver` (§ 4, § 15).
- **#2042:** its "committed-knowledge policy" is inverted: knowledge is ignored by default with a
  Local-additions opt-in (§ 12).
- **#2043:** its allowlist omits `config.schema.json`, `pattern-mining-config.yaml`,
  `skill-evals/baseline.jsonl` and `model-evals/evidence/`, must exclude
  `audit/scope-drift-stats.json`, and no longer allowlists `knowledge/` (§ 12, § 13).
- **#2044:** extends the rule to code-supplying and credential-receiving URL settings (§ 14).
- **#1883:** says to store the device key in `SecretStorage` as `startTrial` does; it must store
  it through the keychain entry (§ 5).
- **#2031:** also carries two § 5 rules that postdate the closed #2023 and #2025, because it owns
  the machine-file fallback: in CI (`CI=true`) no credential is written to disk, and the
  `--api-key` flag that takes `NIGHTGAUGE_API_KEY` on argv is removed.
- **#2022 (this issue):** asks for machine state "under the XDG state directory on every OS";
  `XDG_STATE_HOME` is honoured on every OS when set, and the defaults are per platform (§ 8). It
  also asks for knowledge to follow the #1090 resolution; § 12 records why it does not.
