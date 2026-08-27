# Adapter Capability Matrix

**Version:** 1.2
**Updated:** 2026-08-26
**Author:** nightgauge

---

## Overview

This document is the canonical reference for what each Nightgauge AI CLI adapter
actually supports. It was produced by a systematic audit of all nine adapter implementations
in both the TypeScript SDK layer and the Go binary layer, with code-level verification of
every claim against source.

The audit methodology followed the pattern established by
[Decision #003 (Codex adapter parity)](decisions/003-codex-adapter-feature-parity.md):
read source, verify claims against implementation, document gaps with specific evidence,
and assign an adoption decision to each gap.

### Pipeline eligibility and release status

Capability flags such as streaming and token tracking do not imply that an
adapter can run a coding pipeline. Pipeline dispatch requires an agentic tool
loop capable of editing files, running commands, and calling `gh`.

| Adapter         | Agentic pipeline eligible | Release status                                  |
| --------------- | :-----------------------: | ----------------------------------------------- |
| claude-headless |             ✓             | Recommended; primary tested path                |
| claude-sdk      |             ✓             | Advanced optional SDK integration               |
| codex           |             ✓             | **Beta**; live six-stage matrix pending         |
| gemini          |             ✓             | **Experimental**; live six-stage matrix pending |
| copilot         |             ✓             | **Experimental**; live six-stage matrix pending |
| grok            |             ✓             | **Beta**; six-stage run + beta bar met (#528)   |
| gemini-sdk      |             ✗             | Chat-completion-only                            |
| ollama          |             ✗             | Chat-completion-only                            |
| lm-studio       |             ✗             | Chat-completion-only                            |

The runtime's `isAgenticAdapter()` check is authoritative. Chat-only adapters
remain supported for evaluation, judging, and summarization but are rejected at
pipeline dispatch.

---

## Full Capability Matrix

### Adapter Surface (declared on `ICliAdapter`)

Every TypeScript adapter implements `ICliAdapter`
(`packages/nightgauge-sdk/src/cli/adapters/ICliAdapter.ts:67-122`). The members
below are the whole declared surface: `agentic` (:87),
`getOrchestrationCapability()` (:105), the optional `runWorkflow?()` (:113), and
`requiresDirectApiKey()` (:121).

- **agentic** — drives a real tool loop (edit files, run shell, call `gh`). A
  hard requirement for pipeline stage dispatch (#57); chat-completion-only
  adapters declare `false` and are rejected there.
- **orchestration** — `native-workflow` adapters may offload a fan-out to their
  own engine via `runWorkflow?()`; everything else runs on the portable
  `SdkFanoutRunner` floor. Orthogonal to `agentic` (codex is agentic yet
  `sdk-fanout`).
- **direct API key** — `requiresDirectApiKey()`; `true` means a raw provider key
  is mandatory rather than a CLI session.

| Adapter         | agentic | orchestration     | direct API key | `runWorkflow?()`       | Auth method                                                                  | Min version    |
| --------------- | :-----: | ----------------- | :------------: | ---------------------- | ---------------------------------------------------------------------------- | -------------- |
| claude-headless |    ✓    | `native-workflow` |       ✗        | ✓ (gated ≥ `v2.1.154`) | `claude auth status` (OAuth)                                                 | none declared  |
| claude-sdk      |    ✓    | `native-workflow` |       ✓        | ✓ (gated ≥ `v2.1.154`) | `ANTHROPIC_API_KEY` (checked in `validateAuth`)                              | N/A (SDK)      |
| codex           |    ✓    | `sdk-fanout`      |       ✗        | —                      | `codex login status`                                                         | `0.111.0` warn |
| gemini          |    ✓    | `sdk-fanout`      |       ✗        | —                      | Cascade: `GEMINI_API_KEY` / Vertex / `gcloud`                                | `0.29.0` warn  |
| gemini-sdk      |    ✗    | `sdk-fanout`      |       ✓        | —                      | `GEMINI_API_KEY` or `GOOGLE_API_KEY` (checked in `validateAuth`)             | N/A (SDK)      |
| grok            |    ✓    | `sdk-fanout`      |       ✗        | —                      | `grok login` session or `XAI_API_KEY`                                        | `1.0.0` warn   |
| copilot         |    ✓    | `sdk-fanout`      |       ✗        | —                      | `GH_TOKEN` / `GITHUB_TOKEN` / `COPILOT_GITHUB_TOKEN` → `copilot auth status` | none declared  |
| lm-studio       |    ✗    | `sdk-fanout`      |       ✗        | —                      | None (local HTTP server)                                                     | N/A (HTTP)     |
| ollama          |    ✗    | `sdk-fanout`      |       ✗        | —                      | None (local HTTP server)                                                     | N/A (HTTP)     |

`grok` has no per-adapter deep dive below by design — its verified behaviour is
recorded in [§ Grok Live-Run Evidence (#528)](#grok-live-run-evidence-528).

### Go Binary Adapter Coverage

The Go binary (`cmd/nightgauge`) has its own adapter layer (`internal/execution/adapters/`).
The Go adapters are the **scheduler-driven execution path** (not the VSCode IPC path).

| Adapter         | Go Binary Support | TypeScript Support | Gap?                                                                                                                   |
| --------------- | :---------------: | :----------------: | ---------------------------------------------------------------------------------------------------------------------- |
| claude-headless |         ✓         |         ✓          | Stream-JSON parity (see [Gap #4](#gap-4-claude-headless-typescript-plain-text-output-and-token-reporting))             |
| claude-sdk      |         ✓         |         ✓          | Different implementation (see note)                                                                                    |
| codex           |         ✓         |         ✓          | Session resume, ephemeral, sandbox (see #2589)                                                                         |
| gemini          |         ✓         |         ✓          | Parity (#4032): positional prompt + `--output-format stream-json`                                                      |
| gemini-sdk      |         ✓         |         ✓          | Stream-JSON flag in Go (uses `--output-format stream-json`)                                                            |
| lm-studio       |    ✓ (bridge)     |         ✓          | Registered at `internal/execution/adapters/registry.go:38` (alias `lmstudio` at :45); Go uses claude CLI as SDK bridge |
| ollama          |    ✓ (bridge)     |         ✓          | Go uses claude CLI as SDK bridge                                                                                       |
| copilot         |         ✓         |         ✓          | CLI contract exists in both layers; live verification remains                                                          |
| grok            |         ✓         |         ✓          | Beta since 2026-08-15 (#528) — see § Grok Live-Run Evidence                                                            |

**Note on claude-sdk Go adapter:** The Go `ClaudeSdkAdapter` spawns `claude -p --output-format stream-json`
using `ANTHROPIC_API_KEY`. This is NOT the same as the TypeScript `ClaudeSdkAdapter` which imports
`@anthropic-ai/claude-agent-sdk` directly. They achieve similar results via different mechanisms.

---

## Grok Live-Run Evidence (#528)

One full six-stage run (issue-pickup → pr-merge) executed 2026-08-15 with
`adapter: grok` on subscription auth against a real M-sized Go fix
(issue #583 → PR #587, merged with all required checks green and the
merge commit verified green on `main`).

| Stage            | Duration | Tokens in / out  | Cache read (hit rate) | Gate                                         |
| ---------------- | -------- | ---------------- | --------------------- | -------------------------------------------- |
| issue-pickup     | 5m 02s   | 99,950 / 27,527  | 1.42M (93%)           | ✅                                           |
| feature-planning | 10m 30s  | 484,709 / 96,317 | 3.49M (88%)           | ✅                                           |
| feature-dev      | 14m 57s  | 184,220 / 80,491 | 6.18M (97%)           | ✅                                           |
| feature-validate | 19m 36s  | 244,486 / 66,297 | 6.45M (96%)           | ✅                                           |
| pr-create        | 3s       | 0 / 0            | —                     | ✅ (deterministic path)                      |
| pr-merge         | 10m 20s  | 99,579 / 32,016  | 0.93M (90%)           | ✅ (LLM path after deterministic punt, #589) |

Every dispatched band resolved to `grok-4.6` — the xai band ladder is a
deliberate cost no-op (see the registry BAND NOTE). Token capture per the
#119 pattern is solid, including per-stage cache buckets and hit rates.

**Status decision (updated same day): promoted to Beta** — the bar below
was met within hours of the run. Original gap list, with dispositions:

- #585 — stage cost stamps priced this run at Anthropic rates
  (~$12.66 recorded vs ≈ $2.26 true) — **fixed and merged** (PR #588:
  adapter-aware pricing with explicit unstamped semantics)
- #591 — the CLI's `Not signed in` auth failure classified as
  `subagent_crash` — **fixed and merged** (PR #595); live re-probe
  recorded `adapter_auth_failed` with no model escalation (2026-08-15,
  probe #590). #560 (402 usage-balance) remains open and separate
- #580 — run records cannot yet express adapter, concrete model id,
  effort, or thinking (epic #567 run-record envelope)
- #586 — CLI-started runs are nearly invisible in the extension while
  executing
- #589 — the pr-merge deterministic path punts in worktree runs

**Beta bar (met 2026-08-15):** #591 and #585 fixed and merged, plus the
re-run auth probe landing `adapter_auth_failed` in the run record.
Remaining non-blocking gaps stay filed: #580 (record envelope, in the
#567 phase train), #586 (CLI-run observability), #589 (pr-merge
deterministic punt), #560 (402 classification).

---

## Per-Adapter Deep Dive

### 1. claude-headless

**File:** `packages/nightgauge-sdk/src/cli/adapters/ClaudeHeadlessAdapter.ts`

| Property               | Value                                    |
| ---------------------- | ---------------------------------------- |
| CLI command            | `claude`                                 |
| Auth method            | `claude auth status` (OAuth, no API key) |
| Prompt delivery        | stdin                                    |
| Default args           | `--print --output-format text`           |
| Min version            | None documented in TypeScript adapter    |
| `requiresDirectApiKey` | `false`                                  |

**Auth validation quality:** Good — specific error with clear recovery action
(`Run 'claude auth login'`). Handles timeout (exit code 124) with a separate, actionable message.

**Go adapter differences:**

- Go `ClaudeAdapter` uses `-p --output-format stream-json --verbose` instead of `--print --output-format text`
- Go adapter supports `--allowedTools`, `--max-tokens`, `--max-turns`, `--max-budget-usd`
- TypeScript adapter supports none of these
- Go adapter has stream-json output (token tracking capable); TypeScript does not

**Environment variables:**

- `NIGHTGAUGE_CLAUDE_CLI_COMMAND` — Override CLI binary path
- `NIGHTGAUGE_CLAUDE_CLI_ARGS` — Override default args

**Gaps identified:** [Gap #1](#gap-1-claude-headless-typescript-vs-go-parity), [Gap #4](#gap-4-claude-headless-typescript-plain-text-output-and-token-reporting)

---

### 2. claude-sdk

**File:** `packages/nightgauge-sdk/src/cli/adapters/ClaudeSdkAdapter.ts`

This adapter requires the consumer to install
`@anthropic-ai/claude-agent-sdk` as an optional peer. It is externalized from
the VS Code bundle, and VS Code routes its Claude selection through
`claude-headless`.

| Property               | Value                                                                     |
| ---------------------- | ------------------------------------------------------------------------- |
| CLI command            | `claude` (declared but not used — imports SDK directly)                   |
| Auth method            | Always returns "passed" (validates at query time via `ANTHROPIC_API_KEY`) |
| Prompt delivery        | SDK-native (no CLI spawn)                                                 |
| Default args           | `[]` (SDK-based)                                                          |
| Min version            | N/A (SDK, not CLI)                                                        |
| `requiresDirectApiKey` | `true`                                                                    |

**Auth validation quality:** Good — `validateAuth()` (`ClaudeSdkAdapter.ts:44-56`)
throws `AUTH_MISSING` when `ANTHROPIC_API_KEY` is absent, so a missing key
surfaces at the preflight check rather than mid-query. It also runs the
native-workflow version preflight, which never hard-fails auth — a stale SDK
simply downgrades orchestration to the `sdk-fanout` floor.

**Environment variables:**

- `ANTHROPIC_API_KEY` — Required

**No open gaps.** This is the gold-standard adapter.

---

### 3. codex

**File:** `packages/nightgauge-sdk/src/cli/adapters/CodexAdapter.ts`

| Property               | Value                                                  |
| ---------------------- | ------------------------------------------------------ |
| CLI command            | `codex`                                                |
| Auth method            | `codex login status` (Codex CLI 0.98+)                 |
| Prompt delivery        | stdin                                                  |
| Default args           | `exec --full-auto --sandbox danger-full-access --json` |
| Min version            | `0.111.0` (warn, not block)                            |
| `requiresDirectApiKey` | `false`                                                |

**Auth validation quality:** Good — specific to Codex CLI 0.98+ API (`codex login status`).
Includes version check with warning (non-blocking) when below 0.111.0.
Error message: `codex CLI is not authenticated. Run 'codex login' to authenticate.`

**Special behaviors:**

- **Ephemeral mode:** Stateless stages (`issue-pickup`, `feature-validate`, `pr-create`, `pr-merge`)
  get `--ephemeral` flag by default. Configurable via `NIGHTGAUGE_CODEX_EPHEMERAL_STAGES`.
- **Session resume:** Opt-in via `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`. Resume uses
  `exec resume <threadId> -` syntax; falls back to `exec resume --last` when no ID available.
- **Sandbox scoping from allowed-tools (#4026):** Codex has no per-tool allowlist
  flag, so the skill's `allowed-tools` are mapped onto Codex's sandbox mode +
  approval policy (`resolveCodexSandboxMode` in `codexSandbox.ts` / `codex_sandbox.go`,
  single source of truth shared by both spawn paths). The mapping only ever
  TIGHTENS with positive evidence — default is full access so autonomous runs are
  never locked out:
  | allowed-tools                                                                | Codex flags                                                |
  | ---------------------------------------------------------------------------- | ---------------------------------------------------------- |
  | absent/empty, or any of `Bash`/`Task`/`WebFetch`/`WebSearch`/`mcp__*`        | `--dangerously-bypass-approvals-and-sandbox` (full access) |
  | `Write`/`Edit`/`MultiEdit`/`NotebookEdit` (no shell/network)                 | `--sandbox workspace-write --ask-for-approval never`       |
  | read-only set (`Read`/`Grep`/`Glob`/…)                                       | `--sandbox read-only --ask-for-approval never`             |
  | `--ask-for-approval never` is always kept (autonomous). `exec resume` cannot |
  | sandbox (the flag is unsupported there), so resumed stages stay full access. |
- **Model routing:** `NIGHTGAUGE_CODEX_MODEL` env var → `--model <value>`

**Go adapter (`nightgauge run --adapter codex`, #4019):**

- Go adapter: `codex exec <sandbox flags> --json [--model <id>]`, prompt piped via
  stdin (`-`) — matches the TypeScript adapter's modern `exec` contract. The
  `<sandbox flags>` are scoped from `RunOptions.AllowedTools` via the same mapping
  as the SDK (#4026): `--dangerously-bypass-approvals-and-sandbox` by default,
  tightening to `--sandbox <mode> --ask-for-approval never` for read-only / edit-only stages.
- `--json` output is parsed by `ParseCodexStreamLine` for token/event tracking
- Session resume and ephemeral stages remain TypeScript-only (the Go path is the local CLI runner)

---

### 4. gemini

**File:** `packages/nightgauge-sdk/src/cli/adapters/GeminiAdapter.ts`

| Property               | Value                                           |
| ---------------------- | ----------------------------------------------- |
| CLI command            | `gemini`                                        |
| Auth method            | Three-method cascade (see below)                |
| Prompt delivery        | Positional argument (`gemini "prompt" --flags`) |
| Default args           | `--output-format stream-json`                   |
| Min version            | `0.29.0` (warn, not block)                      |
| `requiresDirectApiKey` | `false`                                         |

**Auth validation quality:** Excellent — three-method cascade with specific instructions for each:

1. `GEMINI_API_KEY` — instant env var check
2. `GOOGLE_API_KEY` + `GOOGLE_GENAI_USE_VERTEXAI=true` — Vertex AI path
3. `gcloud auth print-access-token` — OAuth fallback with timeout handling

Error message includes all three recovery options with URLs. Best auth UX of all CLI adapters.

**Stream-json events understood:** `init`, `message`, `tool_use`, `tool_result`, `error`, `result`.
The parser extracts `stats.input_tokens`, `stats.output_tokens`, `stats.cached` from the `result` event.

**Environment variables:**

- `GEMINI_API_KEY` — Primary API key
- `GOOGLE_API_KEY` — Vertex AI key
- `GOOGLE_GENAI_USE_VERTEXAI` — Set to `true` for Vertex AI path
- `NIGHTGAUGE_GEMINI_CLI_COMMAND` — Override CLI binary
- `NIGHTGAUGE_GEMINI_CLI_ARGS` — Override default args

**Go adapter (#4032 — now matches the TS contract):**

- Go `GeminiAdapter` emits `gemini "<prompt>" --output-format stream-json [--model <m>]`
  — positional prompt delivery (the built `RunOptions.Prompt`, prepended) +
  structured NDJSON, mirroring the TypeScript `GeminiAdapter`. `UsesStdin()` is
  `false` (the manager does not pipe stdin; the prompt is in the args).
- The current Gemini CLI has **no** `--prompt-file` flag; the pre-#4032 Go
  invocation (`--noinput --prompt-file <path>`) never delivered the prompt and
  is removed.

---

### 5. gemini-sdk

**File:** `packages/nightgauge-sdk/src/cli/adapters/GeminiSdkAdapter.ts`

**Pipeline role:** Chat-completion-only. Despite its conversational and token
capabilities, it has no coding-agent tool loop and is rejected at pipeline
dispatch.

| Property               | Value                                                                   |
| ---------------------- | ----------------------------------------------------------------------- |
| CLI command            | `gemini` (declared but not used — imports `@google/genai` SDK directly) |
| Auth method            | Always returns "passed"; throws in `createQueryFunction` if no key      |
| Prompt delivery        | SDK-native (no CLI spawn)                                               |
| Default args           | `[]` (SDK-based)                                                        |
| Min version            | N/A (SDK, not CLI)                                                      |
| `requiresDirectApiKey` | `true`                                                                  |

**Auth validation quality:** Good — `validateAuth()` (`GeminiSdkAdapter.ts:45-61`)
resolves `GEMINI_API_KEY` then `GOOGLE_API_KEY` and throws `AUTH_MISSING` when
neither is set. `createQueryFunction()` repeats the check before importing
`@google/genai`.

**Model resolution:** `NIGHTGAUGE_GEMINI_MODEL` → `NIGHTGAUGE_MODEL` → `gemini-2.5-flash` (default)

**Environment variables:**

- `GEMINI_API_KEY` or `GOOGLE_API_KEY` — Required
- `NIGHTGAUGE_GEMINI_MODEL` — Model override
- `NIGHTGAUGE_MODEL` — Global model override

**Note:** `cost_usd` is always reported as 0 — Gemini SDK does not provide cost information.

---

### 6. lm-studio

**File:** `packages/nightgauge-sdk/src/cli/adapters/LmStudioAdapter.ts`

**Pipeline role:** Chat-completion-only; supported for evaluation, judging, and
summarization, not repository-changing pipeline stages.

| Property               | Value                                                          |
| ---------------------- | -------------------------------------------------------------- |
| CLI command            | `lm-studio` (declared but not used — uses fetch API)           |
| Auth method            | Always returns "passed" (LM Studio accepts any API key string) |
| Prompt delivery        | HTTP POST to `/v1/chat/completions` (OpenAI-compatible)        |
| Default args           | `[]` (HTTP-based)                                              |
| Min version            | N/A (HTTP server)                                              |
| `requiresDirectApiKey` | `false`                                                        |

**Auth validation quality:** Minimal — `validateAuth()` always passes. Real validation happens at
request time: HTTP 404/400 → actionable "model not loaded" error; other HTTP errors → status code
reported. Server connectivity errors surface at request time.

**LM Studio-specific error messages:**

- Model not loaded: `LM Studio model '${model}' is not loaded. Load the model before retrying.`
- Connection refused: `LM Studio server returned HTTP ${status}: ${statusText}`

**Environment variables:**

- `NIGHTGAUGE_LM_STUDIO_BASE_URL` — Server URL (default: `http://localhost:1234/v1`)
- `NIGHTGAUGE_LM_STUDIO_MODEL` — **Required** — model name
- `NIGHTGAUGE_LM_STUDIO_API_KEY` — API key (default: `lm-studio`)
- `NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS` — Request timeout (default: 180000ms / 3 minutes)

**Go binary adapter (bridge mode):** registered at
`internal/execution/adapters/registry.go:38` with the alias `lmstudio` at
`registry.go:45`. `internal/execution/adapters/lmstudio.go` spawns
`claude -p --output-format stream-json --verbose` and sets
`NIGHTGAUGE_ADAPTER=lm-studio` so the TypeScript `LmStudioAdapter` handles the
HTTP call — the same SDK-bridge shape as the Go Ollama adapter, and it likewise
requires the `claude` CLI to be installed. `Agentic()` returns `false`, so the
Go path is available for eval/judge, not pipeline dispatch.

---

### 7. ollama

**File:** `packages/nightgauge-sdk/src/cli/adapters/OllamaAdapter.ts`

**Pipeline role:** Chat-completion-only; supported for evaluation, judging, and
summarization, not repository-changing pipeline stages.

| Property               | Value                                                       |
| ---------------------- | ----------------------------------------------------------- |
| CLI command            | `ollama` (declared but not used — uses fetch API)           |
| Auth method            | Always returns "passed" (Ollama accepts any API key string) |
| Prompt delivery        | HTTP POST to `/v1/chat/completions` (OpenAI-compatible)     |
| Default args           | `[]` (HTTP-based)                                           |
| Min version            | N/A (HTTP server)                                           |
| `requiresDirectApiKey` | `false`                                                     |

**Auth validation quality:** Same as LM Studio — always passes. Actionable error messages at request time:

- Model not pulled: `Run 'ollama pull ${model}' to download the model, then retry.`
- Server not running: `Make sure Ollama is running: 'ollama serve'`

**Environment variables:**

- `NIGHTGAUGE_OLLAMA_BASE_URL` — Server URL (default: `http://localhost:11434/v1`)
- `NIGHTGAUGE_OLLAMA_MODEL` — **Required** — model name (e.g., `llama3.1`, `codellama`)
- `NIGHTGAUGE_OLLAMA_API_KEY` — API key (default: `ollama`; real key for remote deployments)
- `NIGHTGAUGE_OLLAMA_TIMEOUT_MS` — Request timeout (default: 300000ms / 5 minutes)

**Go binary adapter (bridge mode):**
The Go `OllamaAdapter` uses the Claude CLI (`claude`) as an SDK bridge:

- Spawns `claude -p --output-format stream-json --verbose` (same as Go ClaudeAdapter)
- Sets `NIGHTGAUGE_ADAPTER=ollama` env var so the TypeScript SDK routes to `OllamaAdapter`
- Passes through all `NIGHTGAUGE_OLLAMA_*` env vars
- Supports `--allowedTools`, `--max-tokens`, `--max-turns`, `--max-budget-usd` (same as Go ClaudeAdapter)

This is architecturally elegant but creates a dependency: Go Ollama path requires `claude` CLI installed.

---

### 8. copilot

**File:** `packages/nightgauge-sdk/src/cli/adapters/CopilotCliAdapter.ts`

**Release status:** Experimental. The agentic pipeline contract exists, but a
live representative six-stage run has not yet been recorded.

| Property               | Value                                                                                      |
| ---------------------- | ------------------------------------------------------------------------------------------ |
| CLI command            | `copilot`                                                                                  |
| Auth method            | Cascade: `GH_TOKEN`/`GITHUB_TOKEN`/`COPILOT_GITHUB_TOKEN` env vars → `copilot auth status` |
| Prompt delivery        | stdin                                                                                      |
| Default args           | `--allow-all-tools`                                                                        |
| Min version            | Not documented                                                                             |
| `requiresDirectApiKey` | `false`                                                                                    |

**Auth validation quality:** Good design — checks cheapest methods first:

1. `GH_TOKEN` env var (instant, no subprocess)
2. `GITHUB_TOKEN` env var (instant, no subprocess)
3. `COPILOT_GITHUB_TOKEN` env var (instant, no subprocess)
4. `copilot auth status` CLI (subprocess fallback)

Error messages from `validateCLIAuth`: `copilot CLI is not authenticated. Run 'gh auth login' to authenticate.`

**Usage and cost — settled in #52.** Copilot is subscription-based and the CLI
emits **no token counts at all**, so `input_tokens` / `output_tokens` are honest
zeros rather than an estimate. Billing is per _premium request_, and that is what
the adapter records: `summarizeCopilotOutput()`
(`packages/nightgauge-sdk/src/cli/adapterQuery.ts:488`) parses the plain-text
stats footer the CLI prints by default (the adapter deliberately omits `-s`),
extracting the premium-request count, session id, and served model, and strips
the footer from the displayed text. Cost is
`premium_requests × COPILOT_PREMIUM_REQUEST_COST_USD`. When no footer usage line
is present (early exit, or `-s` output) `usage` is left `undefined` and cost is
`0`, mirroring Codex's "unobserved → undefined" convention rather than
fabricating a count. See [Gap #2](#gap-2-copilot-stream-parsing-model-control-and-cost-accounting--resolved-52)
for what remains: a live confirmation of the footer wording, not the token
question.

**Go support:** `CopilotAdapter` is registered in the Go execution layer and its
plain-text stats stream is parsed there. Live entitlement-backed verification
is still pending.

---

## Gaps and Decisions

### Gap #1: claude-headless TypeScript vs Go Parity

| Attribute        | Value                                                                                |
| ---------------- | ------------------------------------------------------------------------------------ |
| Adapter          | `claude-headless`                                                                    |
| Capability       | Tool calling, budget limits                                                          |
| TypeScript claim | Does not support `--allowedTools`, `--max-tokens`, `--max-turns`, `--max-budget-usd` |
| Go adapter       | Supports all of the above                                                            |
| Severity         | MEDIUM                                                                               |
| Decision         | **DEFER**                                                                            |

**Evidence:**

- Go `ClaudeAdapter.BuildCommand()` appends `--allowedTools`, `--model`, `--max-tokens`, `--max-turns`, `--max-budget-usd` when set in `RunOptions`
- TypeScript `ClaudeHeadlessAdapter.createQueryFunction()` reads only `NIGHTGAUGE_CLAUDE_CLI_COMMAND` and `NIGHTGAUGE_CLAUDE_CLI_ARGS` env vars — no structured options
- Adding these to TypeScript requires extending `QueryFunctionOptions` interface

**Rationale for DEFER:** TypeScript headless adapter is typically used via the VSCode extension IPC path where the orchestrator manages budget; Go adapter is the scheduler-driven path where budget enforcement is more critical. The gap is real but not blocking current usage.

---

### Gap #2: copilot stream parsing, model control, and cost accounting — RESOLVED (#52)

| Attribute     | Value                                                                       |
| ------------- | --------------------------------------------------------------------------- |
| Adapter       | `copilot`                                                                   |
| Capability    | stream parsing / `--model` control / cost accounting                        |
| Prior reality | No stream parser, `--model` never sent, flat $0.04/invocation cost guess    |
| Now           | Stats-footer parser, `--model` forced + resolved, real premium-request cost |
| Severity      | MEDIUM                                                                      |
| Decision      | **RESOLVED** — see below; one live-verification pass remains                |

**What changed (#52):**

- **Stream parser.** The GitHub Copilot CLI does NOT emit NDJSON — it prints the
  agent response as plain text followed by a human-readable stats footer
  (suppressed only by `-s`, which the adapter deliberately omits). Both layers
  now parse that footer: `summarizeCopilotOutput()` (`adapterQuery.ts`) strips
  the footer from the displayed text and extracts the premium-request count,
  session id, and served model; the Go `ParseCopilotStreamLine`
  (`stream.go`, dispatched via `StreamFormatCopilot`) records the premium-request
  count into `TokenAccumulator.PremiumRequests` instead of falling through to the
  Claude parser and recording silent zeros.
- **Model control.** Both `CopilotCliAdapter` (SDK) and the Go `CopilotAdapter`
  now forward `--model`, translating Claude routing tiers/ids to a concrete
  copilot-hosted id via the shared registry (`resolveCopilotModel`). The env var
  is no longer cosmetic. Copilot has no refusal-fallback, so the served model IS
  the requested one and is attributed on the result.
- **Cost accounting.** Copilot is subscription-based and emits no token counts,
  so token totals are honest zeros. Cost is now derived from the ACTUAL
  premium-request count parsed from the footer × the labeled per-request estimate
  (`COPILOT_PREMIUM_REQUEST_COST_USD`), replacing the flat "always 1 per
  invocation" guess. When no footer usage line is present, usage is left
  `undefined` (mirroring Codex's "unobserved → undefined" convention).
- **Correct flag.** The tool-permission flag is `--allow-all-tools` (the prior
  `--allow-all` was not the documented tool-permission flag).

**Remaining live verification (blocked without a Copilot CLI + entitlement):**
Confirm the stats-footer wording against a live run (`copilot -p "…"
--allow-all-tools`) — the parser is built to the documented format and captured
community samples, and the unit tests fixture that format, but the exact
"Total usage est: N Premium requests" phrasing and a possible `Model:` line have
not been observed live here. AllowedTools-scoped `--allow-tool` mapping (instead
of blanket `--allow-all-tools`) is a follow-up.

---

### Gap #4: claude-headless TypeScript Plain-Text Output and Token Reporting

| Attribute  | Value                                                                 |
| ---------- | --------------------------------------------------------------------- |
| Adapter    | `claude-headless` (TypeScript)                                        |
| Behaviour  | Emits plain text; no token counts are recoverable from the output     |
| Go adapter | Uses `--output-format stream-json --verbose` (tokens are recoverable) |
| Severity   | MEDIUM                                                                |
| Decision   | **DEFER** — upgrade requires new default args and an output parser    |

**Evidence:**

- TypeScript `ClaudeHeadlessAdapter.createQueryFunction()` defaults to
  `["--print", "--output-format", "text"]`
- Go `ClaudeAdapter.BuildCommand()` uses `-p --output-format stream-json --verbose`
- Claude CLI `--output-format stream-json` produces NDJSON carrying token counts
- Switching the TypeScript adapter would let it report tokens natively instead of
  relying on external estimation

**Rationale for DEFER:** Requires (1) changing
`ClaudeHeadlessAdapter.createQueryFunction()` to the stream-json args, and
(2) adding a stream-json parser for `claude-headless` in `cliQueryHelper.ts`
(only `codex`, `gemini` and `copilot` have parsers today). Non-trivial but
high-value; tracked as part of the broader adapter parity work.

---

### Gap #5: Missing Minimum Version in claude-headless

| Attribute       | Value                                                             |
| --------------- | ----------------------------------------------------------------- |
| Adapter         | `claude-headless`                                                 |
| Capability      | Version detection                                                 |
| Codex pattern   | `MIN_KNOWN_VERSION = "0.111.0"` with warning in `validateAuth`    |
| Gemini pattern  | `MIN_KNOWN_VERSION = "0.29.0"` with warning in `validateAuth`     |
| Claude headless | Version is detected, but no `MIN_KNOWN_VERSION` floor is compared |
| Severity        | LOW                                                               |
| Decision        | **DEFER**                                                         |

**Evidence:** `ClaudeHeadlessAdapter.validateAuth()` calls `verifyCLIInstalled()`
and `detectClaudeCliVersion()`, but the detected version is used only for the
native-workflow floor (`>= v2.1.154`) — there is no general `MIN_KNOWN_VERSION`
compatibility warning. Codex (`0.111.0`), Gemini (`0.29.0`) and Grok (`1.0.0`)
all emit a non-blocking warning when the CLI is below their known minimum.

**Recommended fix:** Add a `MIN_KNOWN_VERSION` constant and compare the version
already returned by `detectClaudeCliVersion()` against it, warning (not blocking)
when the CLI is below the floor — the same shape as Codex, Gemini and Grok.

---

## Follow-Up Issues

| Priority | Issue | Title                                             | Adapter         | Gap                                                                               |
| -------- | ----- | ------------------------------------------------- | --------------- | --------------------------------------------------------------------------------- |
| HIGH     | #2589 | Sync Go Codex Adapter with TypeScript Adapter     | codex           | Session resume, ephemeral, sandbox                                                |
| MEDIUM   | —     | Upgrade claude-headless TypeScript to stream-json | claude-headless | [Gap #4](#gap-4-claude-headless-typescript-plain-text-output-and-token-reporting) |
| LOW      | —     | Add version check to claude-headless validateAuth | claude-headless | [Gap #5](#gap-5-missing-minimum-version-in-claude-headless)                       |

---

## Maintenance

This document should be updated when:

1. A new adapter is registered in
   `packages/nightgauge-sdk/src/cli/adapters/AdapterRegistry.ts` or
   `internal/execution/adapters/registry.go`
2. An adapter's `agentic` field or `getOrchestrationCapability()` return value
   changes
3. An adapter gains or loses `runWorkflow?()`, or its `requiresDirectApiKey()`
   answer changes
4. The `ICliAdapter` surface itself
   (`packages/nightgauge-sdk/src/cli/adapters/ICliAdapter.ts:67-122`) gains or
   loses a member
5. A gap is resolved (delete the gap section and its Follow-Up row rather than
   leaving a closed entry behind)

**Last verified:** 2026-08-26 — the Adapter Surface table, the Go Binary Adapter
Coverage table, and every gap section were re-read against
`packages/nightgauge-sdk/src/cli/adapters/*.ts`,
`packages/nightgauge-sdk/src/cli/adapterQuery.ts`, and
`internal/execution/adapters/`. The Grok Live-Run Evidence section carries
forward from its 2026-08-15 run and was not re-executed.
