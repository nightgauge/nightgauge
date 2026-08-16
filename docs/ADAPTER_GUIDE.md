# Adapter Selection Guide

**Version:** 1.2
**Updated:** 2026-08-15
**Issue:** #2599, #584

---

## Which Adapter Should I Use?

Nightgauge includes eight provider adapters, but only five have the agentic tool
loop required for pipeline execution. Pick an agentic adapter for issue-to-PR
work; use chat-only adapters for evaluation, judging, or summarization.

| Priority                     | Recommended Adapter | Category       |
| ---------------------------- | ------------------- | -------------- |
| Primary tested pipeline path | **Claude Headless** | Cloud AI (CLI) |
| Direct SDK integration       | **Claude SDK**      | Cloud AI (SDK) |
| OpenAI models                | **Codex**           | Cloud AI (CLI) |
| Google agentic pipeline      | **Gemini CLI**      | Experimental   |
| GitHub agentic pipeline      | **Copilot**         | Experimental   |
| xAI Grok Build CLI           | **Grok**            | Experimental   |
| Google API evaluation        | **Gemini SDK**      | Chat-only      |
| Privacy / offline evaluation | **Ollama**          | Chat-only      |
| GUI-based local evaluation   | **LM Studio**       | Chat-only      |

### Decision Matrix

| Factor              | Claude SDK | Claude Headless |   Codex    | Gemini SDK | Gemini CLI | Copilot  | Ollama  | LM Studio |
| ------------------- | :--------: | :-------------: | :--------: | :--------: | :--------: | :------: | :-----: | :-------: |
| **Cost**            | Per-token  |  Subscription   | Per-token  | Per-token  | Per-token  | Per-req  |  Free   |   Free    |
| **Privacy**         |   Cloud    |      Cloud      |   Cloud    |   Cloud    |   Cloud    |  Cloud   |  Local  |   Local   |
| **Setup**           |  API key   |   OAuth login   | CLI login  |  API key   |  API key   | GH login | Install |  Install  |
| **Quality**         |  Highest   |      High       |    High    |    High    |    High    |   Good   | Varies  |  Varies   |
| **Session Resume**  |     ✓      |        ✗        | ✓ (opt-in) |     ✗      |     ✗      |    ✗     |    ✗    |     ✗     |
| **Token Tracking**  |     ✓      |        ✗        |     ✓      |     ✓      |     ✓      |    ⚠️    |    ✓    |     ✓     |
| **Offline**         |     ✗      |        ✗        |     ✗      |     ✗      |     ✗      |    ✗     |    ✓    |     ✓     |
| **Pipeline stages** |     ✓      |        ✓        |  ✓ (beta)  |     ✗      |  ✓ (exp.)  | ✓ (exp.) |    ✗    |     ✗     |

---

## Cloud AI Adapters (Managed)

These adapters call hosted AI APIs. They offer the best model quality and
require an API key or subscription.

### Claude SDK

An advanced direct-SDK adapter with multi-turn conversations, session resume,
streaming JSON, and native token tracking. Claude Headless is the primary
tested path for the VS Code extension and normal pipeline use.

This is an opt-in integration for direct `@nightgauge/sdk` consumers. Install
`@anthropic-ai/claude-agent-sdk` separately after reviewing Anthropic's license
and commercial terms. Nightgauge's CLI archives and VS Code extension do not
redistribute it; the VS Code Claude selection uses Claude Headless instead.

**Prerequisites:**

- Anthropic API key ([console.anthropic.com](https://console.anthropic.com))
- Separately installed `@anthropic-ai/claude-agent-sdk` optional peer

**Quick Start:**

```bash
# Set your API key
export ANTHROPIC_API_KEY=sk-ant-...

# The SDK CLI auto-detects this adapter when ANTHROPIC_API_KEY is set.
# The VS Code extension intentionally does not.
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: claude
```

**Verification (does not print the key):**

```bash
test -n "${ANTHROPIC_API_KEY:-}" && echo "ANTHROPIC_API_KEY is set"
```

**Known Limitations:**

- Per-token billing — costs scale with usage
- Requires internet connectivity

**Troubleshooting:**

| Problem                   | Solution                                                             |
| ------------------------- | -------------------------------------------------------------------- |
| "API key not found"       | Set `ANTHROPIC_API_KEY` in your shell profile                        |
| Auth errors at query time | Verify key at [console.anthropic.com](https://console.anthropic.com) |
| High costs                | Monitor token usage in the VSCode extension dashboard                |

---

### Gemini SDK

Direct SDK integration with Google's Gemini models. Supports multi-turn
conversations and native token tracking.

> **Pipeline limitation:** this is a chat-completion adapter, not an agentic
> coding loop. Nightgauge rejects it at pipeline dispatch. Use it for
> evaluation, judging, and summarization, or select Gemini CLI for experimental
> pipeline execution.

**Prerequisites:**

- Google AI API key ([aistudio.google.com](https://aistudio.google.com))

**Quick Start:**

```bash
# Set your API key (either works)
export GEMINI_API_KEY=AI...
# or
export GOOGLE_API_KEY=AI...

# Optional: choose a model (default: gemini-2.5-flash)
export NIGHTGAUGE_GEMINI_MODEL=gemini-2.5-pro
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: gemini-sdk
gemini:
  model: gemini-2.5-pro # or gemini-2.5-flash, gemini-2.0-flash
```

**Verification:**

```bash
echo $GEMINI_API_KEY | head -c 5
# Should print: AI...
```

**Known Limitations:**

- Cost reporting always shows $0.00 (Gemini SDK does not provide cost data)
- No session resume support

**Troubleshooting:**

| Problem             | Solution                                               |
| ------------------- | ------------------------------------------------------ |
| "API key not found" | Set `GEMINI_API_KEY` or `GOOGLE_API_KEY`               |
| Wrong model         | Set `NIGHTGAUGE_GEMINI_MODEL` or config `gemini.model` |

---

## Cloud AI Adapters (CLI)

These adapters spawn an installed CLI binary as a subprocess. They use the
CLI's own authentication (OAuth, login commands) rather than raw API keys.

### Claude Headless

Uses the `claude` CLI in single-shot mode. No API key needed — authenticates
via Claude's built-in OAuth flow.

**Prerequisites:**

- Claude CLI installed ([docs.anthropic.com](https://docs.anthropic.com))
- Logged in via `claude auth login`

**Quick Start:**

```bash
# Install Claude CLI (if not already installed)
# See https://docs.anthropic.com for platform-specific instructions

# Authenticate
claude auth login

# This is the default adapter — no configuration needed
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: claude # default when no API key is set
```

**Verification:**

```bash
claude auth status
# Should show: Authenticated
```

**Known Limitations:**

- Single-shot execution only (no multi-turn conversations)
- No session resume
- No native token tracking (text output format)

**Troubleshooting:**

| Problem                 | Solution                                                  |
| ----------------------- | --------------------------------------------------------- |
| "Not authenticated"     | Run `claude auth login`                                   |
| Timeout (exit code 124) | Check network; retry with longer timeout                  |
| CLI not found           | Install Claude CLI or set `NIGHTGAUGE_CLAUDE_CLI_COMMAND` |

**Environment Variables:**

| Variable                        | Description                    |
| ------------------------------- | ------------------------------ |
| `NIGHTGAUGE_CLAUDE_CLI_COMMAND` | Override CLI binary path       |
| `NIGHTGAUGE_CLAUDE_CLI_ARGS`    | Override default CLI arguments |

### Grok Build

Uses the `grok` CLI in headless mode (`-p` / `--prompt-file`). Authenticates
via SuperGrok / grok.com OAuth (`grok login`) or `XAI_API_KEY`.

**Prerequisites:**

- Grok Build CLI installed (`curl -fsSL https://x.ai/cli/install.sh | bash`)
- `grok login` **or** `XAI_API_KEY`

**Quick Start:**

```bash
grok login
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: grok
```

**Verification:**

```bash
grok --version
test -f ~/.grok/auth.json && echo "session present"
```

**Known Limitations:**

- Headless ignores piped stdin — Nightgauge uses `--prompt-file`
- No `--max-budget-usd`; Nightgauge enforces budget itself
- Subscription usage shares a weekly pool with other Grok products
- Dollar cost is often unstamped on the subscription path; tokens still record
- Experimental until a live six-stage matrix lands (#528)

**Troubleshooting:**

| Problem              | Solution                                                    |
| -------------------- | ----------------------------------------------------------- |
| Not authenticated    | `grok login` or set `XAI_API_KEY`                           |
| CLI not found        | Install Grok Build or set `NIGHTGAUGE_GROK_CLI_COMMAND`     |
| Usage pool exhausted | Wait for the weekly reset; classified as quota, not a crash |

**Environment Variables:**

| Variable                      | Description                                                                                                                                  |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `NIGHTGAUGE_GROK_CLI_COMMAND` | Override CLI binary path                                                                                                                     |
| `NIGHTGAUGE_GROK_CLI_ARGS`    | Override default flags                                                                                                                       |
| `NIGHTGAUGE_GROK_MODEL`       | Concrete model override                                                                                                                      |
| `NIGHTGAUGE_GROK_EFFORT`      | `--effort` operator override — gated against the registry (below); absent, the Go-resolved dispatch envelope's effort is used instead (#606) |
| `XAI_API_KEY`                 | API-key fallback                                                                                                                             |
| `GROK_HOME`                   | Override `~/.grok`                                                                                                                           |

**Effort gate (#569):** `NIGHTGAUGE_GROK_EFFORT` is not a free pass-through to
`grok --effort`. Before spawn it is checked against the `supported_efforts`
ladder the model registry declares for the model this dispatch actually
resolves — the same resolution `BuildCommand` uses (a band name resolves to
the registry's xai model; a concrete id resolves to itself). A rung the
resolved model does not declare fails the stage **closed**, before the CLI is
ever spawned, with an error naming the model, the requested effort, and the
declared ladder — never a silent pass-through, never a silent downgrade to
whatever rung the CLI happens to serve (#75).

- Grok-native `none`/`minimal` collapse to `low` **before** the ladder check
  runs (#523) — they are valid exactly when the resolved model declares
  `low`.
- `supported_efforts: []` is a positive declaration — "no effort axis" — so
  any explicit effort is rejected outright (`grok-build-0.1` declares `[]`
  today) (#336).
- A model with no registry descriptor at all passes through with a logged
  warning, never a hard failure — there is nothing to validate against.
- A value outside the Grok CLI vocabulary
  (`none|minimal|low|medium|high|xhigh|max`) is dropped exactly as before,
  now with a logged warning instead of silence.
- Enforced on every dispatch path: the Go binary's pre-spawn `ValidateModel`
  hook (`internal/execution/adapters/grok_effort.go`), the SDK's
  `grokCliEffortFlag` (`packages/nightgauge-sdk/src/cli/adapters/grokEffort.ts`),
  and the VS Code extension's `checkAdapterEffortSupported` /
  `preflightAdapterEffort` (`stageResolver.ts` / `skillRunner.ts`).

**Effort channel (#606):** the ladder gate above is unchanged, but the value
it gates is no longer sourced from `NIGHTGAUGE_GROK_EFFORT` alone.
`dispatchGrokEffort` (`internal/execution/adapters/grok_effort.go`) demoted
the env var from sole authority (#569) to an **operator override**: set, it
wins outright — identically at the pre-spawn `ValidateEffort` gate and at
`BuildCommand`'s `--effort` emission, so the gated value and the dispatched
value can never diverge. Unset, the CLI now receives the Go-resolved dispatch
envelope's effort instead of no flag at all — the scheduler's `wireEffort`
(`internal/orchestrator/scheduler.go`, `resolveWireEffort` in
`dispatch_envelope.go`), threaded through `RunOptions.Effort`. That resolved
value is itself overridden, last, by a same-model effort descent the
`RetryEngine` records (`StickyEffort`) after an API rejection — never over
the operator env override. In short: env override > sticky same-model
descent > Go-resolved wire effort > no flag.

**Worked example — the #532 signature:** `grok-4.5` declares
`supported_efforts: [low, medium, high]`. Dispatching to it with
`NIGHTGAUGE_GROK_EFFORT=xhigh` now fails **before** spawn:

```
effort "xhigh" is not supported by model "grok-4.5" (supports: low, medium,
high); choose a supported level or route to a model that accepts "xhigh"
```

Before #569 this env var passed the static CLI-vocabulary filter unchecked
and reached `grok --effort xhigh`, which died inside the CLI as
`unknown effort level 'xhigh'` — exit 1 in seconds, no work done, nothing
classified.

---

### Codex

OpenAI's Codex CLI for GPT-powered pipeline execution. Supports session resume
and streaming JSON output.

**Prerequisites:**

- Codex CLI v0.111.0+ installed
- Logged in via `codex login`

**Quick Start:**

```bash
# Install Codex CLI
npm install -g @openai/codex

# Authenticate
codex login

# Configure adapter
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: codex
    codex:
      model: gpt-5.4
      cli_command: codex
      # Optional: enable session continuity across resumable stages
      resume_enabled: false

pipeline:
  stage_models:
    issue-pickup: haiku
    feature-planning: sonnet
    feature-dev: sonnet
    feature-validate: sonnet
    pr-create: haiku
    pr-merge: haiku
```

For Codex, shared pipeline tiers are translated to OpenAI-native models before
invocation:

- `haiku` → `gpt-5.4-mini`
- `sonnet` → `ui.core.codex.model` (default: `gpt-5.4`)
- `opus` → `gpt-5.5`
- `fable` → `gpt-5.5`

This mapping is owned by the SDK's canonical `codexModelRegistry` (#4018) — the
single source of truth consumed by the adapter, pricing table, and catalog.

**Verification:**

```bash
codex login status
# Should show: Authenticated
codex --version
# Should be >= 0.111.0
```

**Known Limitations:**

- No native token tracking
- Session resume is opt-in (set `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`)
- Some stages run as ephemeral by default (issue-pickup, feature-validate,
  pr-create, pr-merge)

**Troubleshooting:**

| Problem                    | Solution                                      |
| -------------------------- | --------------------------------------------- |
| "Not authenticated"        | Run `codex login`                             |
| Version warning            | Update: `npm install -g @openai/codex@latest` |
| Session resume not working | Set `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`    |

**Environment Variables:**

| Variable                            | Description                                                         |
| ----------------------------------- | ------------------------------------------------------------------- |
| `NIGHTGAUGE_CODEX_CLI_COMMAND`      | Override CLI binary path                                            |
| `NIGHTGAUGE_CODEX_CLI_ARGS`         | Override default CLI arguments                                      |
| `NIGHTGAUGE_CODEX_MODEL`            | Model selection                                                     |
| `NIGHTGAUGE_CODEX_REASONING_EFFORT` | `model_reasoning_effort` value — gated against the registry (below) |
| `NIGHTGAUGE_CODEX_RESUME_ENABLED`   | Enable session resume (`true`/`false`)                              |
| `NIGHTGAUGE_CODEX_EPHEMERAL`        | Make all stages ephemeral (`true`)                                  |
| `NIGHTGAUGE_CODEX_EPHEMERAL_STAGES` | Comma-separated list of ephemeral stages                            |

**Effort gate (#569):** `NIGHTGAUGE_CODEX_REASONING_EFFORT` is checked against
the resolved model's `supported_efforts` ladder before dispatch, mirroring the
Grok rule above — on the SDK and VS Code extension paths. (The Go binary's
Codex adapter does not forward a reasoning-effort flag at all today; deriving
a codex-specific ladder for the Go dispatch path is #435, out of scope for
#569.)

- A value outside the Codex CLI vocabulary (`none|low|medium|high|xhigh|max`)
  is rejected immediately — the CLI would reject it at spawn anyway, so this
  fails fast with the vocabulary named.
- Codex's sub-`low` rung `none` normalizes to `low` for the ladder check
  (mirrors the Grok `none`/`minimal` rule, #523); the value forwarded to the
  CLI stays `none`.
- `supported_efforts: []` rejects any explicit effort (#336); a model with no
  registry descriptor passes through with a logged warning, never a hard
  failure.
- A rung the resolved model does not declare throws a classified
  `AdapterError` (`CONFIG_INVALID`) naming the model, the requested effort,
  and the declared ladder — before spawn, never downgraded (#75).
- Enforced by the SDK's `codexReasoningEffortFlag`
  (`packages/nightgauge-sdk/src/cli/adapters/codexEffort.ts`) and the VS Code
  extension's `checkAdapterEffortSupported` / `preflightAdapterEffort`
  (`stageResolver.ts` / `skillRunner.ts`).

**Worked example:** the default Codex model `gpt-5.4` declares
`supported_efforts: [low, medium, high]`. Dispatching to it with
`NIGHTGAUGE_CODEX_REASONING_EFFORT=xhigh` fails closed before spawn with the
same #532-signature shape as the Grok example above — the model, the
requested effort, and the declared ladder named in the error.

---

### Gemini CLI

Google's Gemini CLI for headless pipeline execution. Supports three
authentication methods.

**Prerequisites:**

- Gemini CLI v0.29.0+ installed

**Quick Start:**

```bash
# Option 1: API key (simplest)
export GEMINI_API_KEY=AI...

# Option 2: Vertex AI
export GOOGLE_API_KEY=AI...
export GOOGLE_GENAI_USE_VERTEXAI=true

# Option 3: gcloud auth (no env vars needed)
gcloud auth print-access-token  # verify this works
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: gemini
gemini:
  auth_method: api-key # or google-login, vertex-ai
  model: gemini-2.5-pro # or gemini-2.5-flash, gemini-2.0-flash
```

**Verification:**

```bash
gemini --version
# Should be >= 0.29.0
```

**Known Limitations:**

- Single-shot execution (no multi-turn)
- No session resume

**Troubleshooting:**

| Problem              | Solution                                                  |
| -------------------- | --------------------------------------------------------- |
| Auth cascade failing | Try each method: API key → Vertex AI → gcloud             |
| CLI not found        | Install Gemini CLI or set `NIGHTGAUGE_GEMINI_CLI_COMMAND` |

**Environment Variables:**

| Variable                        | Description                    |
| ------------------------------- | ------------------------------ |
| `GEMINI_API_KEY`                | Primary API key                |
| `GOOGLE_API_KEY`                | Vertex AI key                  |
| `GOOGLE_GENAI_USE_VERTEXAI`     | Set `true` for Vertex AI       |
| `NIGHTGAUGE_GEMINI_CLI_COMMAND` | Override CLI binary path       |
| `NIGHTGAUGE_GEMINI_CLI_ARGS`    | Override default CLI arguments |

---

### Copilot

Experimental GitHub Copilot CLI adapter for teams already in the GitHub
ecosystem. Its agentic contract is implemented, but the live six-stage provider
matrix is still pending.

**Prerequisites:**

- GitHub Copilot CLI installed
- GitHub authentication

**Quick Start:**

```bash
# Install Copilot CLI
npm install -g @github/copilot-cli

# Authenticate (any of these)
export GH_TOKEN=ghp_...
# or
gh auth login
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: copilot
copilot:
  model: gpt-4o # optional — uses CLI default if omitted
```

**Verification:**

```bash
copilot --version
gh auth status
```

**Known Limitations:**

- No multi-turn conversations
- No session resume
- No streaming JSON output
- Token tracking reliability is uncertain
- Cost model is per-request (~$0.04/request), not per-token

**Troubleshooting:**

| Problem       | Solution                                                  |
| ------------- | --------------------------------------------------------- |
| Auth errors   | Set `GH_TOKEN`, `GITHUB_TOKEN`, or `COPILOT_GITHUB_TOKEN` |
| CLI not found | `npm install -g @github/copilot-cli`                      |

**Environment Variables:**

| Variable                         | Description                     |
| -------------------------------- | ------------------------------- |
| `GH_TOKEN`                       | GitHub token (highest priority) |
| `GITHUB_TOKEN`                   | GitHub token (fallback)         |
| `COPILOT_GITHUB_TOKEN`           | Copilot-specific token          |
| `NIGHTGAUGE_COPILOT_CLI_COMMAND` | Override CLI binary path        |
| `NIGHTGAUGE_COPILOT_CLI_ARGS`    | Override default CLI arguments  |

---

## Local AI Adapters

These adapters connect to locally running model servers and do not require a
model-provider cloud API. Review Nightgauge platform, telemetry, forge, and
notification settings separately before describing an entire run as offline or
private. Quality depends on the model you choose.

### Ollama

> **Pipeline limitation:** Ollama is chat-completion-only. It is available for
> evaluation, judging, and summarization, but cannot edit files or run pipeline
> stages.

Open-source local model runner. Best for quick setup with popular open models.

**Prerequisites:**

- Ollama installed ([ollama.com](https://ollama.com))
- A model pulled locally

**Quick Start:**

```bash
# Install Ollama
# macOS: brew install ollama
# Linux: curl -fsSL https://ollama.com/install.sh | sh

# Pull a model
ollama pull llama3.1

# Start Ollama server (if not already running)
ollama serve

# Configure
export NIGHTGAUGE_OLLAMA_MODEL=llama3.1
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: ollama
ollama:
  model: llama3.1
  # base_url: http://localhost:11434/v1   # default
  # timeout_ms: 300000                     # default: 5 minutes
```

**Verification:**

```bash
# Check Ollama is running
curl -s http://localhost:11434/v1/models | head -5

# Check model is available
ollama list
```

**Known Limitations:**

- Quality depends entirely on the chosen model
- No multi-turn conversations
- No session resume
- Slower than cloud adapters (depends on hardware)
- Default timeout is 5 minutes (local models can be slow to load)

**Troubleshooting:**

| Problem            | Solution                                                 |
| ------------------ | -------------------------------------------------------- |
| Connection refused | Run `ollama serve` to start the server                   |
| Model not found    | Run `ollama pull <model-name>`                           |
| Slow responses     | Use a smaller model or increase `timeout_ms`             |
| Out of memory      | Use a quantized model variant (e.g., `llama3.1:8b-q4_0`) |

**Environment Variables:**

| Variable                       | Description                                       |
| ------------------------------ | ------------------------------------------------- |
| `NIGHTGAUGE_OLLAMA_MODEL`      | Model name (required, no default)                 |
| `NIGHTGAUGE_OLLAMA_BASE_URL`   | Server URL (default: `http://localhost:11434/v1`) |
| `NIGHTGAUGE_OLLAMA_API_KEY`    | Auth string (default: `ollama`)                   |
| `NIGHTGAUGE_OLLAMA_TIMEOUT_MS` | Timeout in ms (default: 300000)                   |

---

### LM Studio

> **Pipeline limitation:** LM Studio is chat-completion-only. It is available
> for evaluation, judging, and summarization, but cannot edit files or run
> pipeline stages.

Desktop application with a GUI for managing and running local models. Best for
users who prefer a visual interface for model management.

**Prerequisites:**

- LM Studio installed ([lmstudio.ai](https://lmstudio.ai))
- A model downloaded and loaded in LM Studio
- Local server started in LM Studio

**Quick Start:**

```bash
# 1. Download and install LM Studio from lmstudio.ai
# 2. Open LM Studio, download a model from the Discover tab
# 3. Load the model and start the local server (Developer tab)

# Configure
export NIGHTGAUGE_LM_STUDIO_MODEL=your-model-name
```

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: lm-studio
lm-studio:
  model: your-model-name # required — matches loaded model in LM Studio
  # base_url: http://localhost:1234/v1   # default
  # timeout_ms: 180000                    # default: 3 minutes
```

**Verification:**

```bash
# Check LM Studio server is running
curl -s http://localhost:1234/v1/models
```

**Known Limitations:**

- Quality depends on the chosen model
- No multi-turn conversations
- No session resume
- Must manually start the server in LM Studio's UI
- Default timeout is 3 minutes

**Troubleshooting:**

| Problem            | Solution                                             |
| ------------------ | ---------------------------------------------------- |
| Connection refused | Start the local server in LM Studio's Developer tab  |
| Model not found    | Ensure the model is loaded (not just downloaded)     |
| Slow responses     | Use a smaller/quantized model; increase `timeout_ms` |

**Environment Variables:**

| Variable                          | Description                                      |
| --------------------------------- | ------------------------------------------------ |
| `NIGHTGAUGE_LM_STUDIO_MODEL`      | Model name (required, no default)                |
| `NIGHTGAUGE_LM_STUDIO_BASE_URL`   | Server URL (default: `http://localhost:1234/v1`) |
| `NIGHTGAUGE_LM_STUDIO_API_KEY`    | Auth string (default: `lm-studio`)               |
| `NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS` | Timeout in ms (default: 180000)                  |

---

## Switching Adapters

### Via VSCode Settings

1. Open Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`)
2. Search "Nightgauge: Select Adapter"
3. Choose an agentic adapter: Claude, Codex (beta), Gemini CLI
   (experimental), or Copilot (experimental)

### Via Configuration File

```yaml
# .nightgauge/config.yaml
ui:
  core:
    adapter: claude # claude | codex | gemini | copilot
```

### Via Environment Variable

```bash
export NIGHTGAUGE_ADAPTER=codex
```

### Auto-Detection

When no adapter is explicitly configured, Nightgauge auto-detects based on
available credentials (checked in order):

1. `ANTHROPIC_API_KEY` set → **Claude SDK**
2. `GEMINI_API_KEY` or `GOOGLE_API_KEY` set → **Gemini SDK**
3. `NIGHTGAUGE_OLLAMA_MODEL` set → **Ollama**
4. `NIGHTGAUGE_LM_STUDIO_MODEL` set → **LM Studio**
5. `COPILOT_GITHUB_TOKEN` set → **Copilot**
6. Default fallback → **Claude Headless**

This auto-detection sequence applies to the standalone SDK CLI. The VS Code
extension always maps its Claude choice to Claude Headless so the Marketplace
artifact never depends on or redistributes the optional Agent SDK.

For pipeline execution, the agentic truth gate still applies after detection:
Gemini SDK, Ollama, and LM Studio are rejected and must be replaced with an
agentic adapter. Their auto-detection remains useful for evaluation, judging,
and summarization surfaces.

### Mid-Pipeline Switching

Agentic adapters use the same context file format, so you can switch adapters
between pipeline stages on the same branch:

```bash
# Start with Codex
scripts/run-stage.sh codex issue-pickup 42
scripts/run-stage.sh codex feature-planning 42

# Continue with Claude (same issue + branch)
/nightgauge:feature-dev
/nightgauge:pr-create
```

---

## Pipeline Feature Compatibility

Not all adapters support all pipeline features. This table shows which pipeline
capabilities are available per adapter:

| Feature                 | Claude SDK | Claude HL |   Codex   | Gemini SDK | Gemini CLI | Copilot | Ollama | LM Studio |
| ----------------------- | :--------: | :-------: | :-------: | :--------: | :--------: | :-----: | :----: | :-------: |
| All 6 pipeline stages   |     ✓      |     ✓     |  ✓ beta   |     ✗      |   ✓ exp.   | ✓ exp.  |   ✗    |     ✗     |
| Multi-turn conversation |     ✓      |     ✗     |     ✗     |     ✓      |     ✗      |    ✗    |   ✗    |     ✗     |
| Session resume          |     ✓      |     ✗     |    ✓\*    |     ✗      |     ✗      |    ✗    |   ✗    |     ✗     |
| Token usage tracking    |     ✓‡     |     ✗     |    ✓‡     |     ✓      |     ✓      |   ⚠️    |   ✓    |     ✓     |
| Streaming JSON output   |     ✓      |     ✗     |     ✓     |     ✓      |     ✓      |    ✗    |   ✗    |     ✗     |
| Cost reporting          |     ✓      |     ✗     |    ✗†     |     ✗†     |     ✗†     |    ✗    |  N/A   |    N/A    |
| System steering         |   preset   |  preset   | AGENTS.md | GEMINI.md  | GEMINI.md  | prompt  | prompt |  prompt   |

\* Codex session resume is opt-in via `NIGHTGAUGE_CODEX_RESUME_ENABLED=true`
† Codex and the Gemini adapters report real token counts but no provider USD cost
(always $0.00 from the adapter); the platform derives cost as pricing × tokens.
‡ Codex reports token usage via the `turn.completed.usage` event since
#4027 (superseding
spike #2587's "no usage" finding).

### System steering (provider-aware)

Each adapter receives baseline system-level guidance through the mechanism its
runtime actually understands — the shared executor carries no Claude-only
assumption (#4028):

- **Claude** (`claude-sdk` / `claude-headless`): the `claude_code` SDK
  system-prompt preset.
- **Codex**: an `AGENTS.md` managed block provisioned before the stage and
  stripped after (`CodexContextGenerator`). It is **non-destructive** — a
  user-authored `AGENTS.md` is preserved; only the delimited
  `NIGHTGAUGE MANAGED STEERING` block is written/removed, so nothing leaks
  into commits.
- **Gemini** (`gemini` / `gemini-sdk`): a generated `GEMINI.md`
  (`GeminiContextGenerator`, gitignored).
- **lm-studio / ollama / copilot**: no preset; guidance arrives via the prompt.

`systemPromptPresetForAdapter()` resolves the preset (Claude only); the per-adapter
context generators self-guard by adapter name.

---

## How auto-routing works

`AutoProviderRouter` (Issue #3230) is the SDK service that picks
`(adapter, model)` per stage when no explicit override is configured. It runs
as Step 2.5 of `resolveStageAdapter` — between the typed `pipeline.stage_adapters`
override and the global `ui.core.adapter` fallback.

### Decision tree

1. If `pipeline.auto_router.enabled: false` → router is bypassed entirely.
2. If no adapters pass auth pre-flight → router abstains.
3. If `model_routing.mode: manual` → router abstains (user steers explicitly).
4. If exactly one adapter passes auth → that adapter wins with confidence 1.0.
5. Otherwise score every candidate as
   `cost × w.cost + capability × w.capability + context_window × w.context_window`.
   Sub-scores live in `[0, 1]`; weights default to `0.4 / 0.4 / 0.2` and the
   router normalises them to sum to 1.0 internally.
6. In `hybrid` mode the top must beat the second by ≥ 0.15. Otherwise abstain.
7. Confidence = `topScore − secondScore` clipped to `[0, 1]`. When confidence
   falls below the threshold (default 0.7) the router abstains so the resolver
   falls through to the global / default step.

### Sub-scores

- **Cost** — driven by recent execution history. Adapters with lower mean
  per-stage cost score higher; adapters absent from history get a neutral 0.5.
  When `remaining_budget_usd` and `stage_estimated_cost_usd` indicate
  comfortable headroom (ratio ≥ 10), the cost dimension is suppressed so
  capability dominates.
- **Capability** — a static per-`(stage_category, adapter)` matrix that encodes
  prior knowledge: Claude is the canonical pick for classification (issue
  pickup) and dev work; Codex shines on dev; Gemini's giant context window
  helps with planning; local models score lower so they aren't picked when
  paid adapters are available.
- **Context window** — saturates at 1.0 once the adapter's window covers the
  expected active context for the stage; degrades linearly below 1×.

### Determinism

The router is pure: no clock, no `Math.random()`, no map-iteration-order
dependencies. Adapters are scored in lexicographic order so tie-breaking is
stable. Identical inputs always produce identical outputs — the unit test
suite asserts this directly.

### Observability

- Confident picks log `[skillRunner] Auto-router: adapter=X rationale="…"` at
  info level. The rationale string includes the per-sub-score breakdown and
  the second-best score margin.
- The extension's adapter decision reports `source: "auto-router"` alongside
  the resolved adapter for diagnostics. Persisted history records the adapter,
  while model attribution remains a separate axis — see
  `model_selection.source` / `MODEL_SELECTION_SOURCES`.

### When the router abstains

When the router returns `null`, the resolver falls through to the existing
precedence chain — never producing a low-quality auto pick. Common abstain
reasons:

- No adapters passed auth pre-flight.
- Multiple candidates tied to within the confidence threshold (margin too
  thin).
- `model_routing.mode: manual` — the user is in explicit control.
- `pipeline.auto_router.enabled: false` — administratively disabled.

### Tuning

Adjust `pipeline.auto_router.weights` to match your team's priorities:

```yaml
pipeline:
  auto_router:
    enabled: true
    weights:
      cost: 0.7 # cheapest acceptable
      capability: 0.2
      context_window: 0.1
```

```yaml
pipeline:
  auto_router:
    enabled: true
    weights:
      cost: 0.1
      capability: 0.8 # best capability regardless of cost
      context_window: 0.1
```

Disable entirely when you want full manual control without uninstalling
the rest of the routing chain:

```yaml
pipeline:
  auto_router:
    enabled: false
```

---

## Further Reading

- [Adapter Capability Matrix](ADAPTER_MATRIX.md) — Technical audit of all
  adapter implementations with code-level verification
- [Configuration Reference](CONFIGURATION.md) — Full configuration options for
  all adapters
- [Multi-Backend Setup](MULTI_BACKEND_SETUP.md) — Advanced multi-adapter
  configuration
