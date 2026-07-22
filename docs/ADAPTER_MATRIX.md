# Adapter Capability Matrix

**Version:** 1.0
**Date:** 2026-04-09
**Issue:** #2595
**Author:** nightgauge

---

## Overview

This document is the canonical reference for what each Nightgauge AI CLI adapter
actually supports. It was produced by a systematic audit of all 8 adapter implementations
in both the TypeScript SDK layer and the Go binary layer, with code-level verification of
every capability claim.

The audit methodology followed the pattern established by
[Decision #003 (Codex adapter parity)](decisions/003-codex-adapter-feature-parity.md):
read source, verify claims against implementation, document gaps with specific evidence,
and assign an adoption decision to each gap.

---

## Full Capability Matrix

### Capability Definitions

| Capability              | Meaning                                                             |
| ----------------------- | ------------------------------------------------------------------- |
| **interactive**         | Supports multi-turn conversational execution (not just single-shot) |
| **sessionResume**       | Can resume a prior session/thread by ID across pipeline stages      |
| **streamJson**          | CLI/SDK produces structured NDJSON events (not plain text)          |
| **nativeTokenTracking** | Token usage counts are reliably available in the adapter's output   |

### Summary Table (TypeScript SDK Adapters)

| Adapter         | Interactive | Session Resume | Stream JSON | Token Tracking | Auth Method                                    | Min Version     |
| --------------- | :---------: | :------------: | :---------: | :------------: | ---------------------------------------------- | --------------- |
| claude-headless |      ✗      |       ✗        |      ✗      |       ✗        | OAuth (`claude auth status`)                   | None documented |
| claude-sdk      |      ✓      |       ✓        |      ✓      |       ✓        | API key (`ANTHROPIC_API_KEY`)                  | N/A (SDK)       |
| codex           |      ✗      |   ✓ (opt-in)   |      ✓      |       ✓        | `codex login status`                           | 0.111.0         |
| gemini          |      ✗      |       ✗        |      ✓      |       ✓        | Cascade (GEMINI_API_KEY / Vertex / gcloud)     | 0.29.0          |
| gemini-sdk      |      ✓      |       ✗        |      ✓      |       ✓        | API key (`GEMINI_API_KEY` or `GOOGLE_API_KEY`) | N/A (SDK)       |
| lm-studio       |      ✗      |       ✗        |     ✗†      |       ✓        | None (local HTTP server)                       | N/A (HTTP)      |
| ollama          |      ✗      |       ✗        |     ✗†      |       ✓        | None (local HTTP server)                       | N/A (HTTP)      |
| copilot         |      ✗      |       ✗        |      ✗      |       ⚠️       | Token env vars / `copilot auth status`         | Unknown         |

† HTTP-based adapters (lm-studio, ollama) stream internally via SSE but produce SDKMessage objects,
not raw NDJSON CLI output. See [Gap #3](#gap-3-lm-studio--ollama-streamjson-capability-semantics).

### Go Binary Adapter Coverage

The Go binary (`cmd/nightgauge`) has its own adapter layer (`internal/execution/adapters/`).
The Go adapters are the **scheduler-driven execution path** (not the VSCode IPC path).

| Adapter         | Go Binary Support | TypeScript Support | Gap?                                                                                    |
| --------------- | :---------------: | :----------------: | --------------------------------------------------------------------------------------- |
| claude-headless |         ✓         |         ✓          | Stream-JSON parity (see [Gap #4](#gap-4-claude-headless-streamjson-and-token-tracking)) |
| claude-sdk      |         ✓         |         ✓          | Different implementation (see note)                                                     |
| codex           |         ✓         |         ✓          | Session resume, ephemeral, sandbox (see #2589)                                          |
| gemini          |         ✓         |         ✓          | Parity (#4032): positional prompt + `--output-format stream-json`                       |
| gemini-sdk      |         ✓         |         ✓          | Stream-JSON flag in Go (uses `--output-format stream-json`)                             |
| lm-studio       |         ✗         |         ✓          | Not in Go registry                                                                      |
| ollama          |    ✓ (bridge)     |         ✓          | Go uses claude CLI as SDK bridge                                                        |
| copilot         |         ✗         |         ✓          | Not in Go registry                                                                      |

**Note on claude-sdk Go adapter:** The Go `ClaudeSdkAdapter` spawns `claude -p --output-format stream-json`
using `ANTHROPIC_API_KEY`. This is NOT the same as the TypeScript `ClaudeSdkAdapter` which imports
`@anthropic-ai/claude-agent-sdk` directly. They achieve similar results via different mechanisms.

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

**Capabilities (TypeScript):**

| Capability          | Declared | Actual  | Status                                      |
| ------------------- | -------- | ------- | ------------------------------------------- |
| interactive         | `false`  | `false` | ✓ Correct — `--print` is single-shot        |
| sessionResume       | `false`  | `false` | ✓ Correct — no resume support               |
| streamJson          | `false`  | `false` | ✓ Correct — uses `text` format              |
| nativeTokenTracking | `false`  | `false` | ✓ Correct — text output has no token fields |

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

**Gaps identified:** [Gap #1](#gap-1-claude-headless-typescript-vs-go-parity), [Gap #4](#gap-4-claude-headless-streamjson-and-token-tracking)

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

**Capabilities (TypeScript):**

| Capability          | Declared | Actual | Status                                               |
| ------------------- | -------- | ------ | ---------------------------------------------------- |
| interactive         | `true`   | `true` | ✓ Correct — SDK enables multi-turn                   |
| sessionResume       | `true`   | `true` | ✓ Correct — SDK passes session context               |
| streamJson          | `true`   | `true` | ✓ Correct — yields typed SDKMessage objects          |
| nativeTokenTracking | `true`   | `true` | ✓ Correct — SDK returns `usage` in `result` messages |

**Auth validation quality:** Does NOT validate at `validateAuth()` time — always returns "passed".
Validation is deferred to `createQueryFunction()` which throws if no API key. This means the
`validateAuth()` step does not surface missing-key errors early in the pipeline.
**Decision:** DEFER — early auth validation could be added in a follow-up.

**Environment variables:**

- `ANTHROPIC_API_KEY` — Required

**No gaps in capability declarations.** This is the gold-standard adapter.

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

**Capabilities (TypeScript):**

| Capability          | Declared | Actual  | Status                                                                                                                                      |
| ------------------- | -------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| interactive         | `false`  | `false` | ✓ Correct — headless `exec` mode                                                                                                            |
| sessionResume       | `true`   | `true`  | ✓ Correct — `exec resume <threadId>` via `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`                                                             |
| streamJson          | `true`   | `true`  | ✓ Correct — `--json` produces JSONL (`thread.started`, `item.completed`, `turn.completed`)                                                  |
| nativeTokenTracking | `true`   | `true`  | ✓ Correct — `turn.completed.usage` (`input_tokens`/`cached_input_tokens`/`output_tokens`) parsed since Issue #4027 (supersedes spike #2587) |

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

**Capabilities (TypeScript):**

| Capability          | Declared | Actual  | Status                                                                 |
| ------------------- | -------- | ------- | ---------------------------------------------------------------------- |
| interactive         | `false`  | `false` | ✓ Correct — single-shot positional delivery                            |
| sessionResume       | `false`  | `false` | ✓ Correct — no session support                                         |
| streamJson          | `true`   | `true`  | ✓ Correct — `--output-format stream-json` produces NDJSON events       |
| nativeTokenTracking | `true`   | `true`  | ✓ Correct — `result` event contains `stats.input_tokens/output_tokens` |

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

| Property               | Value                                                                   |
| ---------------------- | ----------------------------------------------------------------------- |
| CLI command            | `gemini` (declared but not used — imports `@google/genai` SDK directly) |
| Auth method            | Always returns "passed"; throws in `createQueryFunction` if no key      |
| Prompt delivery        | SDK-native (no CLI spawn)                                               |
| Default args           | `[]` (SDK-based)                                                        |
| Min version            | N/A (SDK, not CLI)                                                      |
| `requiresDirectApiKey` | `true`                                                                  |

**Capabilities (TypeScript):**

| Capability          | Declared | Actual  | Status                                                                        |
| ------------------- | -------- | ------- | ----------------------------------------------------------------------------- |
| interactive         | `true`   | `true`  | ✓ Correct — SDK-based, multi-turn capable                                     |
| sessionResume       | `false`  | `false` | ✓ Correct — no session/thread tracking                                        |
| streamJson          | `true`   | `true`  | ✓ Correct — `generateContentStream` yields typed chunks                       |
| nativeTokenTracking | `true`   | `true`  | ✓ Correct — `usageMetadata.promptTokenCount/candidatesTokenCount` from stream |

**Auth validation quality:** Same gap as `claude-sdk` — `validateAuth()` always returns "passed";
real validation (key presence check) deferred to `createQueryFunction()`.

**Model resolution:** `NIGHTGAUGE_GEMINI_MODEL` → `NIGHTGAUGE_MODEL` → `gemini-2.5-flash` (default)

**Environment variables:**

- `GEMINI_API_KEY` or `GOOGLE_API_KEY` — Required
- `NIGHTGAUGE_GEMINI_MODEL` — Model override
- `NIGHTGAUGE_MODEL` — Global model override

**Note:** `cost_usd` is always reported as 0 — Gemini SDK does not provide cost information.

---

### 6. lm-studio

**File:** `packages/nightgauge-sdk/src/cli/adapters/LmStudioAdapter.ts`

| Property               | Value                                                          |
| ---------------------- | -------------------------------------------------------------- |
| CLI command            | `lm-studio` (declared but not used — uses fetch API)           |
| Auth method            | Always returns "passed" (LM Studio accepts any API key string) |
| Prompt delivery        | HTTP POST to `/v1/chat/completions` (OpenAI-compatible)        |
| Default args           | `[]` (HTTP-based)                                              |
| Min version            | N/A (HTTP server)                                              |
| `requiresDirectApiKey` | `false`                                                        |

**Capabilities (TypeScript):**

| Capability          | Declared | Actual               | Status                                                                                                        |
| ------------------- | -------- | -------------------- | ------------------------------------------------------------------------------------------------------------- |
| interactive         | `false`  | `false`              | ✓ Correct — single-turn HTTP request                                                                          |
| sessionResume       | `false`  | `false`              | ✓ Correct — no session support                                                                                |
| streamJson          | `false`  | ⚠️ Partially correct | See [Gap #3](#gap-3-lm-studio--ollama-streamjson-capability-semantics)                                        |
| nativeTokenTracking | `true`   | `true`               | ✓ Correct — `stream_options: {include_usage: true}` in request; reads `usage.prompt_tokens/completion_tokens` |

**Auth validation quality:** Minimal — `validateAuth()` always passes. Real validation happens at
request time: HTTP 404/400 → actionable "model not loaded" error; other HTTP errors → status code
reported. Server connectivity errors surface at request time.

**LM Studio-specific error messages:**

- Model not loaded: `LM Studio model '${model}' is not loaded. Load the model in LM Studio before running the pipeline.`
- Connection refused: `LM Studio server returned HTTP ${status}: ${statusText}`

**Environment variables:**

- `NIGHTGAUGE_LM_STUDIO_BASE_URL` — Server URL (default: `http://localhost:1234/v1`)
- `NIGHTGAUGE_LM_STUDIO_MODEL` — **Required** — model name
- `NIGHTGAUGE_LM_STUDIO_API_KEY` — API key (default: `lm-studio`)
- `NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS` — Request timeout (default: 180000ms / 3 minutes)

**Not in Go binary registry.** Only TypeScript SDK path supports LM Studio.

---

### 7. ollama

**File:** `packages/nightgauge-sdk/src/cli/adapters/OllamaAdapter.ts`

| Property               | Value                                                       |
| ---------------------- | ----------------------------------------------------------- |
| CLI command            | `ollama` (declared but not used — uses fetch API)           |
| Auth method            | Always returns "passed" (Ollama accepts any API key string) |
| Prompt delivery        | HTTP POST to `/v1/chat/completions` (OpenAI-compatible)     |
| Default args           | `[]` (HTTP-based)                                           |
| Min version            | N/A (HTTP server)                                           |
| `requiresDirectApiKey` | `false`                                                     |

**Capabilities (TypeScript):**

| Capability          | Declared | Actual               | Status                                                                                             |
| ------------------- | -------- | -------------------- | -------------------------------------------------------------------------------------------------- |
| interactive         | `false`  | `false`              | ✓ Correct — single-turn HTTP request                                                               |
| sessionResume       | `false`  | `false`              | ✓ Correct — no session support                                                                     |
| streamJson          | `false`  | ⚠️ Partially correct | See [Gap #3](#gap-3-lm-studio--ollama-streamjson-capability-semantics)                             |
| nativeTokenTracking | `true`   | `true`               | ✓ Correct — `stream_options: {include_usage: true}`; reads `usage.prompt_tokens/completion_tokens` |

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

| Property               | Value                                                                                      |
| ---------------------- | ------------------------------------------------------------------------------------------ |
| CLI command            | `copilot`                                                                                  |
| Auth method            | Cascade: `GH_TOKEN`/`GITHUB_TOKEN`/`COPILOT_GITHUB_TOKEN` env vars → `copilot auth status` |
| Prompt delivery        | stdin                                                                                      |
| Default args           | `--allow-all`                                                                              |
| Min version            | Not documented                                                                             |
| `requiresDirectApiKey` | `false`                                                                                    |

**Capabilities (TypeScript):**

| Capability          | Declared | Actual        | Status                                                 |
| ------------------- | -------- | ------------- | ------------------------------------------------------ |
| interactive         | `false`  | `false`       | ✓ Correct — headless stdin mode                        |
| sessionResume       | `false`  | `false`       | ✓ Correct — no session support                         |
| streamJson          | `false`  | `false`       | ✓ Correct — Copilot CLI produces plain text            |
| nativeTokenTracking | `true`   | ⚠️ Unverified | See [Gap #2](#gap-2-copilot-nativetokentracking-claim) |

**Auth validation quality:** Good design — checks cheapest methods first:

1. `GH_TOKEN` env var (instant, no subprocess)
2. `GITHUB_TOKEN` env var (instant, no subprocess)
3. `COPILOT_GITHUB_TOKEN` env var (instant, no subprocess)
4. `copilot auth status` CLI (subprocess fallback)

Error messages from `validateCLIAuth`: `copilot CLI is not authenticated. Run 'gh auth login' to authenticate.`

**Token parsing (best-effort):** `summarizeCopilotOutput()` uses three defensive strategies:

1. JSON block at end of output matching `{"usage": {...}}`
2. Text regex: `Input tokens: N` / `Output tokens: N`
3. Text regex: `Tokens used: N input, N output`
4. Text regex: `Usage: N input tokens, N output tokens`

Falls back to zero tokens if no pattern matches. **This is not guaranteed token extraction** — it
depends on Copilot CLI actually emitting one of these patterns.

**Not in Go binary registry.** Only TypeScript SDK path supports Copilot.

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

### Gap #3: lm-studio / ollama `streamJson` Capability Semantics

| Attribute  | Value                                                                             |
| ---------- | --------------------------------------------------------------------------------- |
| Adapters   | `lm-studio`, `ollama`                                                             |
| Capability | `streamJson`                                                                      |
| Declared   | `false`                                                                           |
| Reality    | Internally streams via SSE/OpenAI-compatible streaming; yields SDKMessage objects |
| Severity   | LOW (documentation gap only)                                                      |
| Decision   | **SKIP** — document the nuance; no code change needed                             |

**Evidence:**

- Both adapters use `stream: true` with `stream_options: {include_usage: true}` in their HTTP requests
- They parse SSE `data:` lines into SDKMessage objects yielded from async generators
- The `streamJson` capability was designed for CLI adapters that produce NDJSON to stdout
- HTTP-based adapters produce streaming at the HTTP transport layer, not NDJSON CLI output
- Declaring `streamJson: false` is correct under the original definition; the adapters stream differently

**Resolution:** The `streamJson` capability field definition should be clarified in `ICliAdapter.ts`
to explicitly state it refers to CLI NDJSON output, not internal streaming. This is a documentation
gap, not a capability gap.

---

### Gap #4: claude-headless Stream-JSON and Token Tracking

| Attribute  | Value                                                                 |
| ---------- | --------------------------------------------------------------------- |
| Adapter    | `claude-headless` (TypeScript)                                        |
| Capability | `streamJson`, `nativeTokenTracking`                                   |
| Declared   | Both `false`                                                          |
| Go adapter | Uses `--output-format stream-json --verbose` (enables token tracking) |
| Severity   | MEDIUM                                                                |
| Decision   | **DEFER** — upgrade requires updating default args and output parser  |

**Evidence:**

- TypeScript `ClaudeHeadlessAdapter.getDefaultArgs()` returns `["--print", "--output-format", "text"]`
- Go `ClaudeAdapter.BuildCommand()` uses `-p --output-format stream-json --verbose`
- Claude CLI `--output-format stream-json` produces NDJSON with `system.start` events containing token counts
- If TypeScript `ClaudeHeadlessAdapter` switched to `--output-format stream-json`, it could report tokens natively

**Rationale for DEFER:** Requires:

1. Updating `ClaudeHeadlessAdapter.createQueryFunction()` to use `stream-json` args
2. Adding stream-json parsing in `cliQueryHelper.ts` for the `claude-headless` adapter (currently only `codex`, `gemini`, `copilot` have parsers)
3. Updating `getCapabilities()` to reflect new `streamJson: true` and `nativeTokenTracking: true`

This is non-trivial but high-value. Tracked as part of the broader adapter parity work.

---

### Gap #5: Missing Minimum Version in claude-headless

| Attribute       | Value                                                          |
| --------------- | -------------------------------------------------------------- |
| Adapter         | `claude-headless`                                              |
| Capability      | Version detection                                              |
| Codex pattern   | `MIN_KNOWN_VERSION = "0.111.0"` with warning in `validateAuth` |
| Gemini pattern  | `MIN_KNOWN_VERSION = "0.29.0"` with warning in `validateAuth`  |
| Claude headless | No minimum version defined; no version check in `validateAuth` |
| Severity        | LOW                                                            |
| Decision        | **DEFER**                                                      |

**Evidence:** `ClaudeHeadlessAdapter.validateAuth()` verifies the CLI is installed and authenticated
but does not call `verifyCLIInstalled()` or check the version. Codex and Gemini adapters both check
version and emit a non-blocking warning when below the known minimum.

**Recommended fix:** Add `MIN_KNOWN_VERSION = "3.0.0"` (or appropriate value) and call
`verifyCLIInstalled()` at the start of `validateAuth()` when a runner is provided.

---

### Gap #6: claude-sdk and gemini-sdk `validateAuth` Deferred to Query Time

| Attribute | Value                                                                                           |
| --------- | ----------------------------------------------------------------------------------------------- |
| Adapters  | `claude-sdk`, `gemini-sdk`                                                                      |
| Issue     | `validateAuth()` always returns "passed"; API key validation happens in `createQueryFunction()` |
| Impact    | Pipeline fails later (at query time) instead of at the preflight check stage                    |
| Severity  | LOW                                                                                             |
| Decision  | **DEFER**                                                                                       |

**Evidence:**

- `ClaudeSdkAdapter.validateAuth()`: `return "passed"` with no key check
- `GeminiSdkAdapter.validateAuth()`: `return "passed"` with no key check
- Actual key check is in `createQueryFunction()` which throws on missing key
- Pipeline preflight runs `validateAuth()` — SDK adapters don't surface missing-key errors there

**Recommended fix:** Add API key presence check in `validateAuth()` for both SDK adapters, mirroring
how `GeminiSdkAdapter.createQueryFunction()` already does it.

---

### Gap #7: Go Registry Missing lm-studio and copilot

| Attribute           | Value                                                       |
| ------------------- | ----------------------------------------------------------- |
| Adapters            | `lm-studio`, `copilot`                                      |
| Go Registry         | Not registered in `internal/execution/adapters/registry.go` |
| TypeScript Registry | Fully registered in `AdapterRegistry.ts`                    |
| Severity            | MEDIUM                                                      |
| Decision            | **DEFER**                                                   |

**Evidence:** `NewRegistry()` in `registry.go` registers 6 adapters: `claude-headless`, `claude-sdk`,
`codex`, `gemini`, `gemini-sdk`, `ollama`. Neither `lm-studio` nor `copilot` is present.

**Impact:** Users cannot select `lm-studio` or `copilot` adapters when running the pipeline via the
Go binary CLI (`nightgauge run --adapter lm-studio`). Only the VSCode extension / TypeScript path
supports these adapters.

**Rationale for DEFER:**

- LM Studio: Go adapter would need to implement an HTTP bridge (similar to Ollama's Claude-bridge approach)
- Copilot: Requires `copilot` CLI binary handling in Go + output parsing

---

## Follow-Up Issues

| Priority | Issue | Title                                                                  | Adapter                | Gap                                                                            |
| -------- | ----- | ---------------------------------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------ |
| HIGH     | #2589 | Sync Go Codex Adapter with TypeScript Adapter                          | codex                  | Session resume, ephemeral, sandbox                                             |
| MEDIUM   | —     | Verify Copilot CLI token output format                                 | copilot                | [Gap #2](#gap-2-copilot-nativetokentracking-claim)                             |
| MEDIUM   | —     | Upgrade claude-headless TypeScript to stream-json                      | claude-headless        | [Gap #4](#gap-4-claude-headless-streamjson-and-token-tracking)                 |
| MEDIUM   | —     | Add lm-studio and copilot to Go binary registry                        | lm-studio, copilot     | [Gap #7](#gap-7-go-registry-missing-lm-studio-and-copilot)                     |
| LOW      | —     | Add version check to claude-headless validateAuth                      | claude-headless        | [Gap #5](#gap-5-missing-minimum-version-in-claude-headless)                    |
| LOW      | —     | Add early API key validation to claude-sdk and gemini-sdk validateAuth | claude-sdk, gemini-sdk | [Gap #6](#gap-6-claude-sdk-and-gemini-sdk-validateauth-deferred-to-query-time) |
| LOW      | —     | Clarify streamJson definition in ICliAdapter.ts for HTTP adapters      | lm-studio, ollama      | [Gap #3](#gap-3-lm-studio--ollama-streamjson-capability-semantics)             |

---

## Capability Definitions (Reference)

### interactive

An adapter is `interactive: true` when it supports multi-turn conversational execution where
the model can ask clarifying questions and the pipeline responds. This is different from
"streaming" (receiving tokens incrementally).

Currently `true`: `claude-sdk` (via Agent SDK), `gemini-sdk` (via `generateContentStream`)

### sessionResume

An adapter supports `sessionResume: true` when a prior conversation thread can be resumed
by ID across separate pipeline stage invocations. The session ID must be extracted from one
stage's output and passed to the next stage's query function.

Currently `true`: `claude-sdk` (native), `codex` (opt-in via `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`)

### streamJson

An adapter declares `streamJson: true` when its **CLI binary** produces structured NDJSON output
to stdout. This is distinct from internal streaming (SSE, SDK generators).

- `codex`: `--json` flag → JSONL events (`thread.started`, `item.completed`, `turn.completed`)
- `gemini`: `--output-format stream-json` → NDJSON events (`init`, `message`, `tool_use`, etc.)
- `claude-sdk`: yields typed `SDKMessage` objects (SDK-native, not NDJSON CLI)
- `gemini-sdk`: yields typed chunks from `generateContentStream` (SDK-native, not NDJSON CLI)
- `lm-studio`, `ollama`: stream internally via SSE but produce `SDKMessage` objects (not NDJSON CLI)

### nativeTokenTracking

An adapter has `nativeTokenTracking: true` when its output **reliably** contains token usage counts
that can be extracted programmatically without estimation.

| Adapter         | Source                               | Fields                                                                |
| --------------- | ------------------------------------ | --------------------------------------------------------------------- |
| claude-sdk      | `result` message `usage` field       | `input_tokens`, `output_tokens`, `cache_read_input_tokens`            |
| gemini          | `result` NDJSON event `stats` field  | `input_tokens`, `output_tokens`, `cached`                             |
| gemini-sdk      | `usageMetadata` in stream chunks     | `promptTokenCount`, `candidatesTokenCount`, `cachedContentTokenCount` |
| lm-studio       | Final SSE chunk `usage` field        | `prompt_tokens`, `completion_tokens`                                  |
| ollama          | Final SSE chunk `usage` field        | `prompt_tokens`, `completion_tokens`                                  |
| codex           | `turn.completed` event `usage` field | `input_tokens`, `cached_input_tokens`, `output_tokens` (#4027)        |
| claude-headless | Not available                        | Requires external estimation                                          |
| copilot         | Best-effort regex parsing            | Unverified reliability (see Gap #2)                                   |

---

## Maintenance

This document should be updated when:

1. A new adapter is added to `AdapterRegistry.ts`
2. An adapter's `getCapabilities()` return value changes
3. A capability gap is resolved (update Decision column to "Adopted" with issue reference)
4. A new capability dimension is added to `AdapterCapabilities` interface

**Last verified:** 2026-04-09 via full code review of all 8 TypeScript adapters and 6 Go adapters.
