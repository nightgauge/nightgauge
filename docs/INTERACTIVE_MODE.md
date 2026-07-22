# Interactive Mode

This document describes the architecture for interactive vs headless execution
modes in the Nightgauge VSCode extension.

## Scope

This document covers **how** the VSCode extension executes pipeline stages —
headless (automated, closed stdin) vs interactive (conversational, open stdin).
It describes process lifecycles, output formats, and mode selection UX within
the extension.

For **where** to run the pipeline (CLI vs VSCode Extension) and the differences
between manual and automated execution, see
[PIPELINE_EXECUTION.md](./PIPELINE_EXECUTION.md).

## Overview

Nightgauge supports two distinct execution modes:

| Mode            | Description                                            |
| --------------- | ------------------------------------------------------ |
| **Headless**    | Automated pipeline execution with closed stdin         |
| **Interactive** | Conversational sessions with open stdin for user input |

Each mode has different process lifecycles, output formats, and capabilities,
designed for different use cases.

### Adapter support

Interactive mode is available for the **Claude** and **Codex** adapters; every
other adapter (Gemini, Gemini SDK, Copilot, LM Studio) is **headless-only** and
`selectExecutionMode` forces headless for them.

| Adapter    | Interactive surface                      | Mechanism                                                                 |
| ---------- | ---------------------------------------- | ------------------------------------------------------------------------- |
| **Claude** | Streamed into the Output panel           | Spawns `claude` (no `-p`) with piped stdio; prompt + follow-ups via stdin |
| **Codex**  | Full-screen **TUI in a VSCode terminal** | Launches the `codex` TUI seeded with the stage prompt (#4024)             |

Codex's interactive mode is a full-screen terminal UI, so it cannot be streamed
through the Output panel the way Claude is. Instead the pipeline opens a VSCode
integrated terminal and launches the Codex TUI seeded with the stage prompt; the
user drives the conversation directly in that terminal. Specifics:

- **Prompt seeding** is quote-safe: the assembled prompt is base64-encoded to a
  temp file and decoded in-shell (`codex --model <m> "$(openssl base64 -d -A -in <file>)"`),
  so a markdown prompt with backticks/`$`/quotes can't break the shell argument.
  (`openssl` is present on macOS and Linux; this path targets those platforms.)
- **Model** is resolved + validated with the same `validateModelForAdapter`
  (#4021) preflight as headless; an invalid configured model falls back to
  Codex's own default rather than blocking the launch.
- **Steering + MCP**: AGENTS.md (#4028) and `~/.codex/config.toml` MCP servers
  (#4025) are provisioned before launch, so the interactive session has the same
  context a headless Codex stage gets. The AGENTS.md managed block is stripped
  when the terminal closes.
- **Token tracking is unavailable** (as with all interactive runs) — the TUI is
  user-driven. The status bar shows the stage running in interactive mode; the
  stage is marked complete/failed when the terminal closes.
- **Opt-out**: set `NIGHTGAUGE_CODEX_MCP_DISABLED=true` to skip MCP
  provisioning for the interactive session.

**Manual verification:** with the Codex adapter active, run
"Nightgauge: Run Stage in Interactive Mode" → a "Codex: …" terminal opens
running the TUI seeded with the stage prompt; the status bar shows the stage
running; closing the terminal marks the stage complete.

---

## Mode Comparison

| Aspect               | Headless Mode                           | Interactive Mode             |
| -------------------- | --------------------------------------- | ---------------------------- |
| CLI Flags            | `claude -p --output-format stream-json` | `claude` (no -p flag)        |
| stdin Handling       | Write prompt, `stdin.end()` immediately | Keep stdin open for messages |
| stdout Format        | stream-json (structured)                | Raw text (conversational)    |
| Token Tracking       | Parse stream-json `usage` blocks        | N/A (not reliably parseable) |
| Session Persistence  | `--no-session-persistence`              | Default (session persists)   |
| Process Termination  | Automatic after response                | User-initiated or timeout    |
| Multi-stage Pipeline | Supported (context isolation)           | Single stage only            |
| Batch Processing     | Supported                               | **NOT SUPPORTED**            |
| Pause/Resume         | Supported (state persisted)             | Not supported (v1)           |

### Headless Mode CLI Flags (Issue #626)

The following flags are conditionally passed to the Claude CLI in headless mode
based on configuration:

| Flag                | Config Source                        | When Passed                         |
| ------------------- | ------------------------------------ | ----------------------------------- |
| `--model`           | `ui.core.default_model`              | When non-default (not `sonnet`)     |
| `--fallback-model`  | `ui.core.fallback_model`             | When configured                     |
| `--max-turns`       | `pipeline.max_turns`                 | When configured (default: no limit) |
| `--max-budget-usd`  | `batch.resource_limits.cost_budget`  | When > 0                            |
| `--permission-mode` | `NIGHTGAUGE_AUTO_ACCEPT_PERMISSIONS` | When auto-accept is enabled         |
| `--bedrock`         | `ui.core.auth_provider`              | When `bedrock`                      |
| `--vertex`          | `ui.core.auth_provider`              | When `vertex`                       |

---

## When to Use Each Mode

### Use Headless Mode When

- Running full pipelines (issue-pickup through pr-merge)
- Processing multiple issues in batch
- Need token tracking and cost analytics
- Automated/unattended execution
- Production workflows

### Use Interactive Mode When

- Exploring or debugging a single stage
- Need to ask questions mid-execution
- Want conversational back-and-forth with the agent
- Learning how a stage works
- Need to provide clarifications during execution

---

## Mode Selection UX

Mode selection depends on the command:

| Command                    | Mode Available | Behavior                               |
| -------------------------- | -------------- | -------------------------------------- |
| `Nightgauge: Run Pipeline` | Headless only  | Full pipeline, no mode choice          |
| `Nightgauge: Run Stage`    | User chooses   | QuickPick: "Headless" or "Interactive" |
| Batch mode                 | Headless only  | No mode choice, enforced headless      |
| Status bar click           | Headless only  | Triggers Run Pipeline (headless)       |

### Run Stage Mode Selection

When using `Nightgauge: Run Stage`, a QuickPick dialog appears:

```
┌────────────────────────────────────────────────────────────────┐
│ Select execution mode                                           │
├────────────────────────────────────────────────────────────────┤
│ ▶ Headless (Recommended)                                        │
│   Automated execution with token tracking                       │
├────────────────────────────────────────────────────────────────┤
│   Interactive                                                   │
│   Conversational - send messages mid-execution                  │
└────────────────────────────────────────────────────────────────┘
```

---

## Architecture

### Class Structure

Interactive and headless modes are implemented in separate orchestrator classes:

```
┌─────────────────────────────────────────────────────────────────┐
│                      ORCHESTRATION LAYER                         │
├────────────────────────────┬────────────────────────────────────┤
│     HeadlessOrchestrator   │     InteractiveOrchestrator        │
│     (existing)             │     (new)                          │
├────────────────────────────┼────────────────────────────────────┤
│  • Multi-stage pipelines   │  • Single-stage only               │
│  • Batch processing        │  • No batch support                │
│  • Context isolation       │  • Session continuity              │
│  • Token tracking          │  • Token tracking N/A              │
│  • stream-json parsing     │  • Raw text streaming              │
│  • stdin.end() immediately │  • stdin open for user input       │
│  • Auto-termination        │  • User/timeout termination        │
└────────────────────────────┴────────────────────────────────────┘
                              │
                              ▼
              ┌───────────────────────────────┐
              │        skillRunner.ts         │
              │   (shared process spawning)   │
              ├───────────────────────────────┤
              │ runStageSkillHeadless()       │
              │ runStageSkillInteractive()    │
              └───────────────────────────────┘
```

**Design Rationale**: Separate classes because:

- Fundamentally different I/O contracts (stdin open vs closed)
- Different process lifecycles (user-terminated vs auto-complete)
- Different output parsing (raw text vs stream-json)
- Prevents complexity creep in HeadlessOrchestrator (~2,040 lines already)
- Clear separation of concerns

---

## Process Lifecycles

### Headless Mode Lifecycle

```
User                HeadlessOrch       skillRunner        Claude CLI
  │                      │                  │                  │
  │─── Run Stage ────────▶│                  │                  │
  │                      │                  │                  │
  │                      │─ runStageSkill ─▶│                  │
  │                      │    Headless()    │                  │
  │                      │                  │                  │
  │                      │                  │─ spawn('claude', │
  │                      │                  │   ['-p',         │
  │                      │                  │    '--output-    │
  │                      │                  │    format',      │
  │                      │                  │    'stream-json'])
  │                      │                  │        │         │
  │                      │                  │─ stdin.write ────▶│
  │                      │                  │   (prompt)       │
  │                      │                  │                  │
  │                      │                  │─ stdin.end() ────▶│ ← CRITICAL
  │                      │                  │                  │
  │                      │                  │◀─ stdout (JSON) ─│
  │                      │                  │   parse tokens   │
  │                      │                  │                  │
  │                      │◀─ onTokenUsage ─│                  │
  │                      │                  │                  │
  │                      │                  │◀─ exit(0) ───────│
  │                      │                  │                  │
  │                      │◀─ onComplete ───│                  │
  │                      │                  │                  │
  │◀─ Stage Complete ────│                  │                  │
```

Key characteristics:

- stdin is closed immediately after writing prompt
- stream-json output enables token parsing
- Process exits automatically when complete
- Token usage extracted from `result` messages

### Interactive Mode Lifecycle

```
User                InteractiveOrch    skillRunner        Claude CLI
  │                      │                  │                  │
  │─── Run Interactive ──▶│                  │                  │
  │                      │                  │                  │
  │                      │─ runStageSkill ─▶│                  │
  │                      │   Interactive()  │                  │
  │                      │                  │                  │
  │                      │                  │─ spawn('claude') │
  │                      │                  │   (no -p flag)   │
  │                      │                  │        │         │
  │                      │                  │─ stdin.write ────▶│
  │                      │                  │   (initial       │
  │                      │                  │    prompt)       │
  │                      │                  │                  │
  │                      │                  │   [NO stdin.end] │ ← stdin stays open
  │                      │                  │                  │
  │                      │                  │◀─ stdout (text) ─│
  │◀─ Stream Output ─────│◀─ onStdout ─────│   (raw output)   │
  │                      │                  │                  │
  │                      │                  │     ... agent    │
  │                      │                  │     working ...  │
  │                      │                  │                  │
  │─── Send Message ─────▶│                  │                  │
  │                      │─ writeToStdin ──▶│                  │
  │                      │                  │─ stdin.write ────▶│
  │                      │                  │   (user msg)     │
  │                      │                  │                  │
  │                      │                  │◀─ stdout (text) ─│
  │◀─ Stream Output ─────│◀─ onStdout ─────│                  │
  │                      │                  │                  │
  │─── End Session ──────▶│                  │                  │
  │                      │─ terminateProc ─▶│                  │
  │                      │                  │─ stdin.end() ────▶│
  │                      │                  │   or SIGTERM     │
  │                      │                  │                  │
  │                      │                  │◀─ exit ──────────│
  │                      │                  │                  │
  │◀─ Session End ───────│◀─ onComplete ───│                  │
```

Key characteristics:

- stdin remains open for user messages
- Raw text output (not parseable for tokens)
- Process terminated by user action or timeout
- Session persists across messages

---

## Output Formats

### Headless Output (stream-json)

```json
{"type":"message_start","message":{"id":"msg_01...","model":"claude-sonnet-4-6"}}
{"type":"content_block_delta","delta":{"text":"I'll start by..."}}
{"type":"tool_use","id":"toolu_01...","name":"Read","input":{"file_path":"..."}}
{"type":"result","session_id":"abc123","usage":{"input_tokens":1234,"output_tokens":567}}
```

- Parsed by `tokenParser.ts`
- Structured, machine-readable
- Token usage extractable from `result` messages

### Interactive Output (raw text)

```
I'll start by reading the issue context...

<Read file_path="/Users/.../.nightgauge/pipeline/issue-494.json">
{
  "schema_version": "1.3",
  ...
}
</Read>

Based on the requirements, I'll...
```

- Rendered directly in Output Window
- Not machine-parseable for tokens
- May include ANSI color codes

---

## Token Tracking

### Headless Mode

Token tracking is fully supported:

- Real-time display in dashboard and sidebar
- Per-stage token counts stored in `state.json`
- Historical tracking for last 50 runs
- Cost analysis and efficiency metrics

### Interactive Mode

Token tracking is **not available**:

- stream-json format requires `-p` flag which closes stdin
- Parsing raw output for token counts is unreliable
- Interactive mode is exploratory - cost tracking less critical

**UI Indicator:**

```
┌─────────────────────────────────────────────────────────────────┐
│  INTERACTIVE SESSION                                             │
│  Stage: feature-dev  │  Issue: #494  │  Tokens: N/A             │
│                                                                  │
│  [Session active - token tracking not available in this mode]   │
└─────────────────────────────────────────────────────────────────┘
```

---

## Feature Compatibility

### Batch Processing

**Interactive mode is INCOMPATIBLE with batch processing.**

- Batch mode processes multiple issues sequentially
- Requires headless automation (no human in loop)
- `InteractiveOrchestrator` simply doesn't implement batch methods

### Pause/Resume

| Feature        | Headless                        | Interactive         |
| -------------- | ------------------------------- | ------------------- |
| Pause command  | Sets `paused` flag in state     | Not supported (v1)  |
| Resume command | Resumes from next pending stage | Not supported (v1)  |
| Cross-session  | State persists in JSON          | Session may timeout |

Interactive pause/resume may be added in future versions if demand exists.

### Agent Teams

Agent teams (`agent_teams.display_mode`) apply to **headless mode only**.
Interactive mode is single-stage, single-process — agent teams do not apply.

| Mode        | Agent Teams Behavior                                 |
| ----------- | ---------------------------------------------------- |
| Headless    | Supported — `display_mode` controls teammate output  |
| Interactive | Not applicable — single-stage, no parallel teammates |

In headless mode, `display_mode: auto` prefers tmux split panes if tmux is
available, falling back to `in-process`. The `in-process` mode keeps all
teammate output in the VSCode Output Window.

### Routing (Stage Skipping)

- Routing only applies to multi-stage pipelines
- Interactive mode is single-stage only
- No interaction with routing logic

### AskUserQuestion

| Mode        | AskUserQuestion Behavior                         |
| ----------- | ------------------------------------------------ |
| Headless    | Filtered from allowedTools, loop detection kills |
| Interactive | Works naturally - user can respond via stdin     |

**Interactive mode advantage**: AskUserQuestion becomes functional since stdin
is open. Skills can use AskUserQuestion when running interactively.

---

## Configuration

Optional configuration in `.nightgauge/config.yaml`:

```yaml
execution:
  default_mode: headless # or 'interactive' - affects Run Stage default
  interactive:
    timeout_minutes: 30 # Auto-terminate after 30 min inactivity
```

| Config Key                              | Default    | Description                        |
| --------------------------------------- | ---------- | ---------------------------------- |
| `execution.default_mode`                | `headless` | Default mode for Run Stage         |
| `execution.interactive.timeout_minutes` | `30`       | Inactivity timeout for interactive |

---

## Implementation Sub-Issues

This architecture is implemented across multiple sub-issues:

| Sub-Issue | Implementation                                 |
| --------- | ---------------------------------------------- |
| #495      | `runStageSkillInteractive()` in skillRunner.ts |
| #496      | Dual-mode Output Window rendering              |
| #497      | Mid-execution message input UI                 |
| #498      | Token tracking N/A indicator                   |
| #499      | Mode selection QuickPick UX                    |
| #500      | Documentation (this file)                      |

---

## Related Documentation

- [PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md) - Manual vs Automated modes
- [ARCHITECTURE.md](ARCHITECTURE.md) - Overall system architecture
- [CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md) - Context file schemas
- [CONFIGURATION.md](CONFIGURATION.md) - Configuration reference

---

## Author

nightgauge
