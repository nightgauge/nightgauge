# Adapter Selection Guide

**Version:** 1.3
**Updated:** 2026-09-21
**Issue:** #2599, #584, #1649

---

## Which Adapter Should I Use?

Nightgauge includes ten provider adapters; seven have the agentic tool
loop required for pipeline execution. Pick an agentic adapter for issue-to-PR
work; use chat-only adapters for evaluation, judging, or summarization.

| Priority                                                    | Recommended Adapter   | Category       |
| ----------------------------------------------------------- | --------------------- | -------------- |
| Primary tested pipeline path                                | **Claude Headless**   | Cloud AI (CLI) |
| Direct SDK integration                                      | **Claude SDK**        | Cloud AI (SDK) |
| OpenAI models                                               | **Codex**             | Cloud AI (CLI) |
| Google agentic pipeline                                     | **Gemini CLI**        | Experimental   |
| GitHub agentic pipeline                                     | **Copilot**           | Experimental   |
| xAI Grok Build CLI                                          | **Grok**              | Beta           |
| Local models (agentic), or another CLI to a hosted provider | **OpenCode**          | Experimental   |
| Google API evaluation                                       | **Gemini SDK**        | Chat-only      |
| Evaluation on any OpenAI-compatible server                  | **OpenAI-compatible** | Chat-only      |

### Decision Matrix

| Factor              | Claude SDK | Claude Headless |   Codex    | Gemini SDK | Gemini CLI | Copilot  |   Grok    |
| ------------------- | :--------: | :-------------: | :--------: | :--------: | :--------: | :------: | :-------: |
| **Cost**            | Per-token  |  Subscription   | Per-token  | Per-token  | Per-token  | Per-req  | Per-token |
| **Privacy**         |   Cloud    |      Cloud      |   Cloud    |   Cloud    |   Cloud    |  Cloud   |   Cloud   |
| **Setup**           |  API key   |   OAuth login   | CLI login  |  API key   |  API key   | GH login | CLI login |
| **Quality**         |  Highest   |      High       |    High    |    High    |    High    |   Good   |   High    |
| **Session Resume**  |     ✓      |        ✗        | ✓ (opt-in) |     ✗      |     ✗      |    ✗     |     ✗     |
| **Token Tracking**  |     ✓      |        ✗        |     ✓      |     ✓      |     ✓      |    ⚠️    |     ✓     |
| **Offline**         |     ✗      |        ✗        |     ✗      |     ✗      |     ✗      |    ✗     |     ✗     |
| **Pipeline stages** |     ✓      |        ✓        |  ✓ (beta)  |     ✗      |  ✓ (exp.)  | ✓ (exp.) | ✓ (beta)  |

OpenCode has no column here: it dispatches through whatever provider its
`-m <provider>/<model>` names, so cost, privacy, and offline posture are
properties of the dispatched model, not of the adapter. See
[OpenCode](#opencode) below.

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
- Beta since 2026-08-15 (#528): a live six-stage run met the beta bar; see
  [ADAPTER_MATRIX.md § Grok Live-Run Evidence](ADAPTER_MATRIX.md#grok-live-run-evidence-528)

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
export GH_TOKEN={env:GH_TOKEN}
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

### OpenCode

**Experimental.** One adapter id, `opencode`, reaches both a model server the
operator runs (LM Studio, Ollama, or another OpenAI-compatible server) and a
hosted provider (Anthropic, OpenAI, xAI, Google), because the provider is a
property of the dispatched model id, not of the adapter. Full design record:
[ADR-022](decisions/022-opencode-multi-provider-adapter.md).

`Manager.RunStage` refuses every `opencode` dispatch before spawning anything
unless `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` is set in the process environment,
and an allowed dispatch still prints, on stderr, every control the ADR lists
as not yet enforced. A dispatch to an `anthropic/*` model is refused while
`ANTHROPIC_API_KEY` is unset, whatever the switch says.

**Prerequisites:**

- OpenCode CLI installed (`npm install -g opencode-ai`, or a version pin per
  below)
- `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` set in the environment the pipeline
  runs in
- For a hosted `anthropic/*` model: `ANTHROPIC_API_KEY` set
- For a self-run model: the server already running and reachable on loopback

**Quick Start:**

```bash
export NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1
```

```yaml
# .nightgauge/config.yaml
pipeline:
  stage_adapters:
    feature-dev: opencode
```

A dispatch must name a model OpenCode can take on `-m <provider>/<model>` —
there is no bare-id fallback. The `<provider>` is the first path segment,
split on the first `/`: `lmstudio/qwen/qwen3.8-27b` is provider key
`lmstudio`, model `qwen/qwen3.8-27b`; `anthropic/claude-sonnet-5` is provider
key `anthropic`, model `claude-sonnet-5`. A local model server is declared as
a named endpoint in the machine tier (`~/.nightgauge/config.yaml`, never the
committed project config):

```yaml
opencode:
  binary: /opt/opencode/bin/opencode # an absolute path, never looked up on PATH
  endpoints:
    - id: lmstudio # becomes the OpenCode provider key
      provider: lm-studio # lm-studio | ollama | openai-compatible
      base_url: http://127.0.0.1:1234/v1
      limit:
        context: 131072 # overrides the value discovered from the server
        output: 16384
```

**Isolation from the operator's own OpenCode state.** Every dispatch gets its
own XDG-isolated run root (`~/.nightgauge/opencode/runs/<run_id>/`), never the
operator's real `~/.opencode` or global OpenCode config: `inherit_user_config`
defaults to `false`, and a run's `HOME` structurally keeps `~/.opencode` out
of it. The session database, transcript, and per-run config are deleted with
the run root when the run ends, and every inherited `OPENCODE_*` variable is
stripped before the environment this ADR sets is added — including
`OPENCODE_AUTH_CONTENT`, so a stored subscription or OAuth login the operator
holds is never available to a dispatch. A subscription or OAuth login is
never used for `anthropic/*`; the only credential is `ANTHROPIC_API_KEY`.

**Headless permission semantics.** OpenCode auto-rejects a permission that
resolves to `ask`: the tool call fails, the run ends after that step, and the
process **exits 0** — a silent stop that looks like success (observed on opencode 1.18.30). Nightgauge's
permission maps contain only `allow` and `deny`, never `ask`, and the parser
classifies a rejected-permission tool event as a failure regardless of the
exit code, so a stage that hits this does not report success. `--auto` and
its hidden aliases (observed on opencode 1.18.30) are never emitted; approval is the permission map's job.

**Known Limitations:**

- Experimental: no live six-stage evidence recorded yet (#1659); the switch
  above must be set explicitly, and every allowed dispatch discloses the
  controls still unenforced
- `Subagents (task)` are denied under the current plugin gate (see
  [ADAPTER_MATRIX.md § 10. opencode](ADAPTER_MATRIX.md#10-opencode) for the
  full capability disposition table)
- A model server the operator runs is refused above the compat manifest's
  max-tested version, even though a hosted dispatch continues under a warning
- The machine's own managed OpenCode config, when one exists, refuses every
  dispatch unless `inherit_user_config` is explicitly turned on

**Troubleshooting:**

| Problem                                   | Solution                                                                            |
| ----------------------------------------- | ----------------------------------------------------------------------------------- |
| Dispatch refused, gate not enabled        | Set `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` where the pipeline runs                    |
| `anthropic/*` refused                     | Set `ANTHROPIC_API_KEY`; a subscription or OAuth login is never accepted here       |
| A local endpoint's dispatch refused       | Check the binary's version against the compat manifest's max-tested version         |
| Stage fails on an auto-rejected tool call | Widen the stage's allowed tools so the permission map generates `allow`, not `deny` |
| CLI not found                             | `npm install -g opencode-ai`, or pin `opencode.binary` to an absolute path          |

**Environment Variables:**

| Variable                           | Description                                                                 |
| ---------------------------------- | --------------------------------------------------------------------------- |
| `NIGHTGAUGE_EXPERIMENTAL_OPENCODE` | Must be exactly `1` to allow any dispatch                                   |
| `NIGHTGAUGE_MODEL`                 | The `<provider>/<model>` this dispatch sends on `-m`                        |
| `ANTHROPIC_API_KEY`                | Required for any `anthropic/*` model; no other Anthropic credential is read |

Step-by-step LM Studio and Ollama setup:
[MULTI_BACKEND_SETUP.md § Local models through OpenCode](MULTI_BACKEND_SETUP.md#local-models-through-opencode-agentic).

---

## Local Models

Local models run only through the [OpenCode](#opencode) adapter, which drives
a real tool loop against any OpenAI-compatible server the operator runs: LM
Studio, Ollama, oMLX, MTPLX, llama.cpp, vLLM and the like. Declare the server
as an endpoint under `opencode.endpoints` in the machine tier. A model counts
as local when its endpoint is declared local or its base URL is a loopback or
private address, whatever the provider key is called. Review Nightgauge
platform, telemetry, forge and notification settings separately before
describing an entire run as offline or private.

The `lm-studio` and `ollama` adapters were removed (#2128). A config, flag or
environment variable that still names either fails with an error naming the
setting and the replacement; see
[DEPRECATIONS.md](DEPRECATIONS.md#lm-studio-and-ollama-adapters--opencode-and-openai-compatible).

### OpenAI-compatible (evaluation and judging)

`openai-compatible` is a chat-completion client for any server that speaks the
OpenAI chat-completions API, local or hosted. It has no tool loop, so it is
never agentic: `Manager.RunStage` refuses to dispatch a pipeline stage to it.
It serves evaluation, judging and summarization.

```bash
export NIGHTGAUGE_OPENAI_COMPATIBLE_BASE_URL=http://127.0.0.1:1234/v1 # required, no default
export NIGHTGAUGE_OPENAI_COMPATIBLE_MODEL=your-model-name              # required
```

| Variable                                   | Description                                                                               |
| ------------------------------------------ | ----------------------------------------------------------------------------------------- |
| `NIGHTGAUGE_OPENAI_COMPATIBLE_BASE_URL`    | Server base URL (required, no default)                                                    |
| `NIGHTGAUGE_OPENAI_COMPATIBLE_MODEL`       | Model id exactly as the server's `GET /v1/models` lists it (required)                     |
| `NIGHTGAUGE_OPENAI_COMPATIBLE_API_KEY_ENV` | Name of the variable holding the API key (default `NIGHTGAUGE_OPENAI_COMPATIBLE_API_KEY`) |
| `NIGHTGAUGE_OPENAI_COMPATIBLE_TIMEOUT_MS`  | Request timeout in ms (default: 180000)                                                   |

An `Authorization` header is sent only when the named key variable is set, so
a local server never receives a placeholder key. A judge on a loopback or
private base URL is local and costs $0; one on a hosted base URL reports its
cost as an estimate.

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

1. `GEMINI_API_KEY` or `GOOGLE_API_KEY` set → **Gemini SDK**
2. `ANTHROPIC_API_KEY` set → **Claude SDK**
3. `COPILOT_GITHUB_TOKEN` set → **Copilot**
4. Default fallback → **Claude Headless**

This auto-detection sequence applies to the standalone SDK CLI. The VS Code
extension always maps its Claude choice to Claude Headless so the Marketplace
artifact never depends on or redistributes the optional Agent SDK.

For pipeline execution, the agentic truth gate still applies after detection:
Gemini SDK and OpenAI-compatible are rejected and must be replaced with an
agentic adapter. They remain useful for evaluation, judging, and summarization
surfaces.

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

| Feature                 | Claude SDK | Claude HL |   Codex   | Gemini SDK | Gemini CLI | Copilot |
| ----------------------- | :--------: | :-------: | :-------: | :--------: | :--------: | :-----: |
| All 6 pipeline stages   |     ✓      |     ✓     |  ✓ beta   |     ✗      |   ✓ exp.   | ✓ exp.  |
| Multi-turn conversation |     ✓      |     ✗     |     ✗     |     ✓      |     ✗      |    ✗    |
| Session resume          |     ✓      |     ✗     |    ✓\*    |     ✗      |     ✗      |    ✗    |
| Token usage tracking    |     ✓‡     |     ✗     |    ✓‡     |     ✓      |     ✓      |   ⚠️    |
| Streaming JSON output   |     ✓      |     ✗     |     ✓     |     ✓      |     ✓      |    ✗    |
| Cost reporting          |     ✓      |     ✗     |    ✗†     |     ✗†     |     ✗†     |    ✗    |
| System steering         |   preset   |  preset   | AGENTS.md | GEMINI.md  | GEMINI.md  | prompt  |

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
  stripped after (`CodexContextGenerator` in the SDK and extension,
  `codexprovision` on the Go-direct path). It is **non-destructive** — a
  user-authored `AGENTS.md` is preserved; only the delimited
  `NIGHTGAUGE MANAGED STEERING` block is written/removed. See
  [Generated steering is never committed](#generated-steering-is-never-committed).
- **Gemini** (`gemini` / `gemini-sdk`): a generated `GEMINI.md`
  (`GeminiContextGenerator`, gitignored).
- **openai-compatible / copilot**: no preset; guidance arrives via the prompt.

`systemPromptPresetForAdapter()` resolves the preset (Claude only); the per-adapter
context generators self-guard by adapter name.

#### Where the project description comes from

The `## Project` section of the Codex block and of `GEMINI.md` summarizes the
repository's own contract: the user-authored part of root `AGENTS.md`, with any
managed block removed. `CLAUDE.md` is read only when `AGENTS.md` is absent or
holds nothing but the managed block, and then a leading `@AGENTS.md` import
line is skipped. In the agent-guidance architecture `CLAUDE.md` is a thin
adapter that imports `AGENTS.md`, so reading it first would summarize the
adapter instead of the rules. Go and TypeScript share this precedence and the
same summary rules (headings without body text are dropped, so a title
followed directly by a subsection still yields the subsection's content),
pinned by shared fixtures in
`internal/execution/codexprovision/testdata/extract-summary/`.

#### Generated steering is never committed

The Codex agent runs, and may commit, while the block is in `AGENTS.md`, so
stripping the working tree after the stage is not enough. Every path is
guarded:

- **Pipeline-owned commits** (pr-create, the scheduler's recovery commit, the
  reset checkpoint, the heal commit, the extension's WIP and validate
  backstops) remove the block from the staged `AGENTS.md` before committing;
  the working tree keeps its steering while a stage is live.
- **Commits the agent made itself** are repaired after every stage, on every
  adapter and exit path: when `HEAD`'s `AGENTS.md` carries the block, one
  commit removing exactly the block is added. The deterministic pr-create push
  repairs before pushing, and pr-merge refuses to merge a pull request whose
  head still carries the block.
- **Existing repositories**: `nightgauge preflight managed-steering` reports a
  block committed in any tracked `AGENTS.md` (exit `1`), and `--fix` removes it
  from the working tree for review. Smart Setup's migration and verify mode use
  the same check.

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
