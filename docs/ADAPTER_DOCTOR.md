# Adapter Doctor

The **Adapter Doctor** answers "is my adapter set up correctly?" on demand,
instead of letting misconfiguration (CLI not installed, not logged in, stale
version, MCP not provisioned) surface as a mid-run pipeline failure.

It exists in two complementary layers, each the authority for the part it owns —
there is no duplicated probing logic.

| Layer                             | Consumer                    | Reports                                                                 |
| --------------------------------- | --------------------------- | ----------------------------------------------------------------------- |
| Go `nightgauge doctor --adapters` | Skills (Phase 0), CLI users | Deterministic binary presence + version (vs floor) + Codex MCP config   |
| VSCode **Adapter Doctor** command | IDE users                   | The Go facts **plus** auth status, per-stage routing, remediation, a UI |

## VSCode command

Run **"Nightgauge: Adapter Doctor"** from the command palette
(`nightgauge.adapterDoctor`). It opens a webview reporting, for every
adapter the pipeline resolves to (per stage + the global default):

- **Install / Version** — CLI binary on PATH and its version vs the known floor
  (CLI adapters); API-key configured (SDK adapters); local-model env set (HTTP
  adapters). Sourced from the Go binary.
- **Auth** — authenticated or not, with the failure reason. Sourced from the SDK
  `runAdapterAuthPreflight` (`codex login status`, `claude auth status`, the
  Gemini key/ADC cascade, the Copilot token/CLI cascade).
- **Codex MCP** — whether `$CODEX_HOME/config.toml` exists and carries the
  nightgauge managed MCP block.
- **How to fix** — a concrete remediation per failure (e.g. ``Run `codex
login`.``), merged from the Go binary's install/version hints and the SDK's
  per-adapter `suggestedFix`.

A second table shows **per-stage resolution**: which adapter + model each of the
six executable stages resolves to (`issue-pickup` … `pr-merge`), the resolution
source (env / stage-config / global-config / default), and — for Codex — the
concrete model the tier maps to (e.g. `opus → gpt-5.5`). Each stage is flagged
`ok` / `warn` / `error` based on the resolved adapter's health.

"Re-run checks" recomputes the report in place.

### How the data is merged

```
resolveStageAdapter / getStageModel   → per-stage adapter + model (TS resolvers)
        │
        ├── distinct adapters ──► Go `doctor --adapters … --json`  → binary/version/MCP
        │                     └─► SDK runAdapterAuthPreflight       → auth + suggestedFix
        ▼
   merged AdapterDoctorReport → webview (per-adapter + per-stage tables)
```

When the Go binary cannot be resolved, the panel shows a warning and falls back
to auth-only readiness (the SDK auth probe itself surfaces a missing CLI as a
`BINARY_NOT_FOUND` failure), so the command degrades gracefully.

## Go CLI (`doctor --adapters`)

For skill preflight and headless use, the deterministic half is available
directly:

```bash
nightgauge doctor --adapters codex,claude --json
nightgauge doctor --adapters all
```

See [GO_BINARY.md → Doctor → Per-adapter health](GO_BINARY.md#per-adapter-health---adapters)
for the `adapters[]` schema and the per-kind semantics.

### Local-server reachability (`http` kind, #57)

`ollama` / `lm-studio` health includes a bounded (2 s) HTTP probe of the
local server's `/models` endpoint (`server_url` / `server_reachable` in the
JSON). The base URL comes from `NIGHTGAUGE_OLLAMA_BASE_URL` /
`NIGHTGAUGE_LM_STUDIO_BASE_URL`, defaulting to the servers' standard local
ports. An adapter with its model env set but no listening server now reports
`ok: false` with remediation — previously it could report healthy with no
server running at all.

### Binary self-check cascade (#277)

The `binary` check in the default (non-adapter) `nightgauge doctor` output
reports whether the `nightgauge` binary itself is resolvable, via a
six-step cascade (mirrors `claude-plugins/nightgauge/hooks/lib/guard.sh`):

1. `$NIGHTGAUGE_BIN` (only when it points at an executable file)
2. `PATH` (`exec.LookPath("nightgauge")`)
3. `<repo-root>/bin/nightgauge` (`git rev-parse --show-toplevel`)
4. `<canonical-repo-root>/bin/nightgauge` (`git rev-parse --git-common-dir`,
   for worktree checkouts)
5. `~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`
   — the bundle VSCode **records** as installed (#356), never the first glob
   match and never the biggest version number
6. `~/go/bin/nightgauge`

This is implemented once, in Go, at `internal/doctor/binary_resolve.go`
(`ResolveBinary`), and is the canonical implementation the cascade is
specified against; `internal/doctor/binary_resolve_test.go` pins
`guard.sh`'s resolution order against it for the five filesystem-based
steps, so the two cannot silently drift. Since #356 it also pins the
**order** itself: `TestResolveBinary_Precedence` and
`TestResolveBinary_GuardShParity_Precedence` satisfy each adjacent pair of
steps simultaneously and assert the earlier one wins, in both
implementations. The per-step tests alone could not catch a reorder — each
isolates the cascade so exactly one step is satisfiable.

### What the `binary` check reports (#356)

The cascade is **cwd-dependent**: steps 3 and 4 shell out to `git rev-parse`
in the caller's directory, so the same hook script legitimately runs
different binaries in different repos. Inside a nightgauge checkout the
hooks use `bin/nightgauge`; two directories over they use the extension
bundle. Before #356 nothing reported that, which let a merged hook fix sit
inert everywhere except the repo it was built in.

`checks.binary.detail` therefore reads as _what the hooks resolve from
here_, and carries:

- the resolved path and the resolving step, e.g.
  `hooks resolve /…/dist/bin/nightgauge from this directory (via vscode_extension)`
- the binary's own version, from one bounded `<path> version` exec — note
  the verb is `version`; there is no `--version` flag. The exec lives in
  `doctor` (on demand) and deliberately **not** in `guard.sh`, which runs on
  every tool call.
- the extension-bundle inventory — how many bundle directories are on disk,
  which one VSCode **records** as installed, and whether that recorded bundle
  is the one in use — **even when an earlier step wins**, so running `doctor`
  from inside a checkout still tells you what other repos will use. The
  inventory is also carried on the not-found path, where "2 bundle dir(s) on
  disk, none runnable" is a different problem from "nothing installed".

The `binary` check is **warning-only** — it contributes to `warnings` but
never sets `exit_code` to `2`, and never appears in `failed_checks`. A
missing binary can only be observed by running `doctor` in the first place,
so it cannot be a hard-required check. An extension-only install (binary
resolvable via step 5 but never on `PATH`) reports
`checks.binary.ok == true`, not a false "not found".

Two distinct warning-level outcomes exist and they are not interchangeable:

| Outcome          | `ok`    | `install_instructions` | Meaning                                                                                        |
| ---------------- | ------- | ---------------------- | ---------------------------------------------------------------------------------------------- |
| unresolved       | `false` | populated              | no step matched — installing something is the fix                                              |
| diverging bundle | `false` | **empty**              | step 5 resolved a bundle VSCode's install record does not confirm — installing changes nothing |
| resolved         | `true`  | empty                  | normal                                                                                         |

A diverging bundle names the **recorded** version, the **resolved** version and
the resolved path in `checks.binary.error`, matching the `[stale-binary]` line
`guard.sh` writes to its side-channel log — and it keeps its full `detail`
alongside that error. This is the outcome an operator is actually
investigating, so the resolving step and the resolved binary's own version (the
two facts that answer "is this really an old build?") are reported here, not
only on healthy machines.

#### The install record is the selection authority (#356)

Step 5 does not rank version numbers, and neither does this check. VSCode
records exactly one installed directory per extension in
`~/.vscode/extensions/extensions.json` (`relativeLocation`), and that record —
not the biggest-parsing directory name — decides which bundle the hooks run.

Ranking versions is wrong in three ways this project actually reaches:
maintainer dev-installs are permanently `0.1.<epoch>` and lose to any leftover
`0.2.x` release directory; RC bundles (`…-0.2.0-rc.22`, `…-0.2.0-rc.23`) tie
under every dotted-numeric comparator and fall back to first-glob-match; and a
`….vsctmp` partial-install orphan can out-parse the real install. Consequently
a **recorded downgrade is healthy** — `doctor` reports `ok: true` and says
nothing about the higher-numbered directory next to it.

Divergence is reported only when the record cannot be honored (recorded bundle
absent or not executable) or when there is no usable record and several bundles
are on disk. See
[docs/GO_BINARY.md](GO_BINARY.md#which-bundle-the-hooks-run--vscodes-record-not-the-biggest-number)
for the full rule and for the parsing-parity contract between `guard.sh` and
`internal/doctor/binary_resolve.go`.

`DoctorResult.FailedChecks` (`failed_checks` in JSON) lists every check
name that contributed a required failure (`exit_code == 2`), in the order
added — e.g. `["github_auth"]`. `skills/_shared/PREFLIGHT.md`'s exit-2
branch reads this field to print the failing check name(s) alongside
`.errors[]`, instead of leaving the stage agent to guess which check broke.

## Agentic capability gate (#57)

Every adapter declares whether it drives a real agentic tool loop
(`agentic` on the SDK `ICliAdapter`; `Agentic()` on the Go `SkillRunner`).
Chat-completion-only adapters — `ollama`, `lm-studio`, and the TypeScript
`gemini-sdk` — cannot edit files, run shell commands, or call `gh`, so
**pipeline dispatch rejects them** with remediation at every entry point:
the SDK CLI preflight (`runAdapterPreflightChecks`), the VSCode prerequisite
check (primary, fallback walker, and auto-router enumeration), and the Go
`Manager.RunStage`. They remain first-class for the eval harness / judge /
summarization surfaces, which do not run these gates. (The Go `gemini-sdk`
adapter is agentic — it spawns the gemini CLI — unlike its chat-only
TypeScript namesake.)

## Extending to a new adapter

1. Add the adapter to the Go `adapterSpecs` table (`internal/doctor/adapters.go`)
   with its `kind`, binary/env requirements, and (mirrored) min-version floor.
2. Ensure the SDK adapter implements `validateAuth()` (the auth layer is
   automatic via `runAdapterAuthPreflight`).
3. Declare its agentic truth: `agentic` on the SDK adapter class and
   `Agentic()` on the Go adapter (#57) — `false` bars it from pipeline
   dispatch while keeping it available to eval surfaces.
4. Add a display name to `SDK_ADAPTER_DISPLAY` in
   `packages/nightgauge-vscode/src/commands/adapterDoctor.ts`.

## Related

- [ADAPTER_GUIDE.md](ADAPTER_GUIDE.md) — adapter selection, auth, troubleshooting
- [ADAPTER_MATRIX.md](ADAPTER_MATRIX.md) — verified per-adapter capability matrix
- [MCP_INTEGRATION.md](MCP_INTEGRATION.md) — Codex MCP provisioning
- [GO_BINARY.md](GO_BINARY.md) — `doctor` CLI reference
