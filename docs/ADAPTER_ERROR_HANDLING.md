# Adapter Error Handling

This document describes the standardized error handling system used by all 8 CLI
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
| `SERVER_UNREACHABLE` | Local HTTP server not responding                | LM Studio, Ollama       |
| `MODEL_NOT_FOUND`    | Model not loaded or not pulled                  | LM Studio, Ollama       |
| `CONFIG_INVALID`     | Required configuration is missing or invalid    | LM Studio, Ollama       |
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

### LM Studio (`lm-studio`)

No auth validation (LM Studio accepts any API key string). Errors occur at query time.

**Error scenarios:**

- `CONFIG_INVALID` — `NIGHTGAUGE_LM_STUDIO_MODEL` not set
- `MODEL_NOT_FOUND` — HTTP 404/400 from server (model not loaded)
- `SERVER_UNREACHABLE` — Server returned unexpected HTTP error

**Fix for model not found:** Open LM Studio → Model tab → search and load the model
**Docs:** https://lmstudio.ai/docs

### Ollama (`ollama`)

No auth validation (Ollama accepts any API key string). Errors occur at query time.

**Error scenarios:**

- `CONFIG_INVALID` — `NIGHTGAUGE_OLLAMA_MODEL` not set
- `MODEL_NOT_FOUND` — HTTP 404/400 from server (model not pulled)
- `SERVER_UNREACHABLE` — Server returned unexpected HTTP error

**Fix for model not found:** `ollama pull <model>`
**Fix for server unreachable:** `ollama serve`
**Docs:** https://ollama.com/library

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
throwModelNotFound("Ollama", "llama3.1", "ollama pull llama3.1", "ollama serve");

// Server not responding
throwServerUnreachable(
  "LM Studio",
  "http://localhost:1234/v1",
  "Start LM Studio and enable the server"
);

// Version too old
throwVersionMismatch("Gemini", "0.20.0", "0.29.0", "npm update @google/gemini-cli");

// Config key missing
throwConfigInvalid(
  "Ollama",
  "NIGHTGAUGE_OLLAMA_MODEL",
  "Set model: export NIGHTGAUGE_OLLAMA_MODEL=llama3.1"
);

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
