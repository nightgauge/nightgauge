# Adapter Error Handling

This document describes the standardized error handling system used by the CLI
adapters in Nightgauge.

## Overview

All adapter errors are instances of `AdapterError`, exported from
`packages/nightgauge-sdk`. Each error carries:

- A **machine-readable category** (`AdapterErrorCategory`)
- The **adapter name** that threw it (e.g., `"Claude Headless"`)
- A **human-readable message** with a specific reason and a `Fix:` action hint
- An optional **docs URL** linking to setup documentation

## Error Format

All errors follow this template when displayed:

```
[Adapter Name] CATEGORY: specific reason
Fix: actionable command or instruction
Docs: https://...  (when available)
```

Example:

```
[Claude Headless] BINARY_NOT_FOUND: claude CLI is not installed or not in PATH.
Fix: brew install claude  # or: npm install -g @anthropic-ai/claude-code
Docs: https://docs.anthropic.com/en/docs/claude-code
```

## Error Categories

| Category             | Meaning                                         | Typical Adapters        |
| -------------------- | ----------------------------------------------- | ----------------------- |
| `AUTH_MISSING`       | No authentication configured                    | All                     |
| `AUTH_EXPIRED`       | Authentication has expired                      | OAuth-based adapters    |
| `BINARY_NOT_FOUND`   | CLI binary not installed or not in PATH         | CLI-based adapters      |
| `VERSION_MISMATCH`   | CLI version is too old                          | Codex, Gemini           |
| `SERVER_UNREACHABLE` | Local HTTP server not responding                | OpenAI-compatible       |
| `MODEL_NOT_FOUND`    | Model not loaded or not pulled                  | OpenAI-compatible       |
| `CONFIG_INVALID`     | Required configuration is missing or invalid    | OpenAI-compatible       |
| `TIMEOUT`            | Auth check timed out in non-interactive context | Claude Headless, Gemini |

## Per-Adapter Auth Validation

### Claude Headless (`claude-headless`)

1. Verify `claude` binary is installed (`--version` check)
2. Run `claude auth status` (with 10s timeout)

**Error scenarios:**

- `BINARY_NOT_FOUND` — `claude` not in PATH
- `TIMEOUT` — `claude auth status` timed out (common in non-interactive contexts)
- `AUTH_MISSING` — Not logged in

**Fix:** `claude auth login`
**Docs:** https://docs.anthropic.com/en/docs/claude-code

### Claude SDK (`claude-sdk`)

Validates presence of `ANTHROPIC_API_KEY` environment variable.

**Error scenarios:**

- `AUTH_MISSING` — `ANTHROPIC_API_KEY` not set

**Fix:** Set `ANTHROPIC_API_KEY=your_key` (get a key at console.anthropic.com/settings/keys)
**Docs:** https://docs.anthropic.com/en/api/getting-started

### Codex (`codex`)

1. Verify `codex` binary is installed
2. Check version against minimum known compatible version (warning only)
3. Run `codex login status`

**Error scenarios:**

- `BINARY_NOT_FOUND` — `codex` not in PATH
- `AUTH_MISSING` — Not logged in

**Fix:** `codex login`
**Docs:** https://docs.openai.com/codex

### Grok Build (`grok`)

1. Verify `grok` binary is installed (`grok --version`)
2. Prefer SuperGrok / grok.com session: `grok login`
3. Or set `XAI_API_KEY` (fallback when no session is active)

**Error scenarios:**

- `BINARY_NOT_FOUND` — `grok` not in PATH
- `AUTH_MISSING` — no `~/.grok/auth.json` and no `XAI_API_KEY`

**Fix:** `grok login` or `export XAI_API_KEY=<your-key>`
**Docs:** https://docs.x.ai/build/overview

### Gemini CLI (`gemini`)

1. Verify `gemini` binary is installed
2. Auth cascade (first match wins):
   - `GEMINI_API_KEY` environment variable
   - `GOOGLE_API_KEY` + `GOOGLE_GENAI_USE_VERTEXAI=true` (Vertex AI)
   - `gcloud auth print-access-token` (Google OAuth)

**Error scenarios:**

- `BINARY_NOT_FOUND` — `gemini` not in PATH
- `TIMEOUT` — `gcloud auth print-access-token` timed out (10s limit)
- `AUTH_MISSING` — All three auth methods failed

**Fix options:**

1. `export GEMINI_API_KEY=your_key` (get key at aistudio.google.com/apikey)
2. `export GOOGLE_API_KEY=your_key && export GOOGLE_GENAI_USE_VERTEXAI=true`
3. `gcloud auth login`

**Docs:** https://ai.google.dev/gemini-api/docs

### Gemini SDK (`gemini-sdk`)

Validates presence of `GEMINI_API_KEY` or `GOOGLE_API_KEY`.

**Error scenarios:**

- `AUTH_MISSING` — Neither `GEMINI_API_KEY` nor `GOOGLE_API_KEY` is set

**Fix:** `export GEMINI_API_KEY=your_key` (get key at aistudio.google.com/apikey)
**Docs:** https://ai.google.dev/gemini-api/docs

### OpenAI-compatible (`openai-compatible`)

No auth validation: the key is optional and sent only when the variable
`NIGHTGAUGE_OPENAI_COMPATIBLE_API_KEY_ENV` names is set. Errors occur at query
time.

**Error scenarios:**

- `CONFIG_INVALID` — `NIGHTGAUGE_OPENAI_COMPATIBLE_BASE_URL` or
  `NIGHTGAUGE_OPENAI_COMPATIBLE_MODEL` not set
- `MODEL_NOT_FOUND` — HTTP 404/400 from server (model not loaded or pulled)
- `SERVER_UNREACHABLE` — Server returned unexpected HTTP error

**Fix for model not found:** load or pull the model on the server, then name it
exactly as `GET /v1/models` lists it.

### GitHub Copilot (`copilot`)

1. Verify `copilot` binary is installed
2. Auth cascade (first match wins):
   - `GH_TOKEN` environment variable
   - `GITHUB_TOKEN` environment variable
   - `COPILOT_GITHUB_TOKEN` environment variable
   - `copilot auth status` CLI subcommand

**Error scenarios:**

- `BINARY_NOT_FOUND` — `copilot` not in PATH
- `AUTH_MISSING` — No token set and CLI auth fails

**Fix:** `gh auth login`
**Docs:** https://docs.github.com/en/copilot/using-github-copilot/using-github-copilot-in-the-command-line

### OpenCode (`opencode`)

**Experimental** — every dispatch is refused before spawn unless
`NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` is set. Full design record:
[ADR-022](decisions/022-opencode-multi-provider-adapter.md).

1. Verify `opencode` binary is installed and at or above the compat
   manifest's floor version
2. Per the dispatched model's provider (`<provider>/<model>` on `-m`): a
   local endpoint needs no credential; `anthropic/*` needs
   `ANTHROPIC_API_KEY`, checked before spawn

**Error scenarios:**

- `BINARY_NOT_FOUND` — `opencode` not in PATH and no `opencode.binary` pin
  resolves
- `VERSION_MISMATCH` — installed version below the compat manifest's floor,
  or a version that could not be read
- `AUTH_MISSING` — `anthropic/*` dispatched with `ANTHROPIC_API_KEY` unset. A
  subscription or OAuth login OpenCode may hold is never used as a
  substitute (ADR-022 § 17).

**Fix:** `npm install -g opencode-ai`, or pin `opencode.binary` to an
absolute path; for `anthropic/*`, `export ANTHROPIC_API_KEY=<key>`
**Docs:** https://opencode.ai/docs/cli/

**Terminal failure kinds (#1631).** Once a dispatch is running, a stage's
failure is classified into one of the kinds
[FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md) documents. These are the kinds
OpenCode's own observed failure text produces (observed on opencode 1.18.30); the
`[adapter-permission-rejected]` and `adapter_incompatible:` markers are
Nightgauge's own (`internal/terminalkind/table.json`,
`internal/terminalkind/testdata/opencode/`):

| Kind                          | What OpenCode reported                                                                                                                                      | Remediation                                                                                                                                                                                                                       |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `context_window_exceeded`     | `AI_APICallError` wrapping a server's own overflow message (the OpenAI-compatible `context_length_exceeded`, LM Studio's, Ollama's, or llama.cpp's wording) | The prompt outgrew the model's loaded context window; shorten the stage's input or dispatch to a model with a larger loaded window                                                                                                |
| `adapter_permission_rejected` | `[adapter-permission-rejected] tool=<permission>` — OpenCode auto-rejected a permission the stage's allowed tools grant                                     | Nightgauge's generated permission map (#1815) holds only `allow` and `deny`, so the `ask` came from OpenCode config outside that map; this parks rather than retries, because the same dispatch is rejected identically next time |
| `adapter_incompatible`        | `adapter_incompatible: <reason>` — below the compat manifest's floor, or a version that could not be read; a newer version is never refused                 | Pin `opencode.binary` to a version at or above the compat manifest's floor                                                                                                                                                        |
| `model_unavailable`           | `not_found_error` / `model not found` / `invalid model`, or a usage-limit phrase naming the model                                                           | Fix `opencode.model` or the dispatched model id, or pull/load the model on the endpoint                                                                                                                                           |
| `network_unavailable`         | `AI_APICallError: Cannot connect to API: Unable to connect. Is the computer able to access the url?`                                                        | The dispatched endpoint's model server is not listening; verify it is running and reachable                                                                                                                                       |
| `adapter_auth_failed`         | `AI_APICallError: Unauthorized` — the model server rejected OpenCode's credentials with a 401                                                               | For `anthropic/*`, verify `ANTHROPIC_API_KEY`; for a declared endpoint, verify the endpoint's configured key                                                                                                                      |

## Usage in Code

### Catching AdapterErrors

```typescript
import { AdapterError } from "@nightgauge/sdk";

try {
  await adapter.validateAuth({ runner, cwd });
} catch (error) {
  if (error instanceof AdapterError) {
    console.error(error.format()); // [Adapter Name] CATEGORY: message
    console.error("Category:", error.category);
    console.error("Adapter:", error.adapterName);
    if (error.actionUrl) {
      console.error("Docs:", error.actionUrl);
    }
  }
}
```

### Throwing AdapterErrors (in adapters)

Use the helper functions from `errors.ts` for consistent formatting:

```typescript
import {
  throwAuthError,
  throwBinaryNotFound,
  throwModelNotFound,
  throwServerUnreachable,
  throwVersionMismatch,
  throwConfigInvalid,
  throwTimeoutError,
} from "./errors.js";

// Auth missing
throwAuthError("My Adapter", "No API key found", "export MY_KEY=xxx", "https://docs.example.com");

// Binary not installed
throwBinaryNotFound("My Adapter", "mytool", "npm install -g mytool", "https://docs.example.com");

// Model not available
throwModelNotFound("My Server", "my-model", "load my-model on the server", "start the server");

// Server not responding
throwServerUnreachable("My Server", "http://127.0.0.1:8080/v1", "Start the server");

// Version too old
throwVersionMismatch("Gemini", "0.20.0", "0.29.0", "npm update @google/gemini-cli");

// Config key missing
throwConfigInvalid("My Adapter", "MY_ADAPTER_MODEL", "Set model: export MY_ADAPTER_MODEL=my-model");

// Command timed out
throwTimeoutError(
  "Claude Headless",
  "`claude auth status`",
  10_000,
  "Verify the command works manually."
);
```

## Adding Errors to a New Adapter

When creating a new adapter, follow this pattern:

1. Define constants at the top of the adapter file:

   ```typescript
   const ADAPTER_NAME = "My Adapter"; // matches displayName
   const MY_ADAPTER_DOCS_URL = "https://docs.my-adapter.com";
   const MY_ADAPTER_INSTALL_CMD = "npm install -g my-adapter-cli";
   ```

2. In `validateAuth()`, use helpers from `./errors.js` instead of throwing `Error` or `CodexPreflightError` directly.

3. In `createQueryFunction()`, use helpers for config or model errors.

4. Add error scenarios to `tests/cli/adapterErrors.integration.test.ts`.

## Backward Compatibility

`CodexPreflightError` is retained for branch state and docs precondition checks
in `codexPreflight.ts`. Adapter-specific errors now throw `AdapterError` instead.
Both extend `Error`, so catch clauses that catch `Error` continue to work.
