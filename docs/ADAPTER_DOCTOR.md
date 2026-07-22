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
