# MCP Server Integration Guide

This guide explains how to configure MCP (Model Context Protocol) servers for
use with Nightgauge pipeline agents. With MCP, you can extend each pipeline
stage with additional tools — file system access, database queries, browser
automation, and more — without modifying Nightgauge itself.

> **See also:** [Configuration Reference](CONFIGURATION.md) for the full
> `.nightgauge/config.yaml` schema.

---

## Overview

MCP servers expose tools that AI agents can call during a pipeline stage.
Pipeline stages run as CLI subprocesses of whichever provider the stage is
configured to use (Claude, Codex, …), so any MCP server made available to that
provider is reachable from the stage agent.

You declare MCP servers **once**, the Claude-native way (`.mcp.json` at the repo
root, and/or `mcpServers` in `.claude/settings.json`). The pipeline then makes
those same servers visible to **non-Claude** providers automatically — for
Codex, by translating them into `~/.codex/config.toml` `[mcp_servers.*]` before
each Codex stage (see [Per-Provider MCP Configuration](#per-provider-mcp-configuration-claude--codex)).
You do not maintain a second, provider-specific server list.

The `allowed-tools` frontmatter field in each SKILL.md controls which tools that
stage's agent may invoke. Adding an MCP server tool name to `allowed-tools`
grants that stage permission to use it.

### How It Fits Together

```
.claude/settings.json          ← MCP server configuration (credentials, URLs)
       │
       ▼
Claude CLI subprocess           ← Each pipeline stage agent
       │
       ▼
SKILL.md allowed-tools: [...]   ← Per-stage tool permission list
       │
       ▼
MCP tool calls during stage     ← e.g., mcp__filesystem__read_file
```

---

## Per-Provider MCP Configuration (Claude + Codex)

Different CLI providers read MCP configuration from different places. You author
the config once (Claude-native), and the pipeline bridges it to each provider.

| Provider         | Reads MCP servers from                                                       | How it gets there                                                               |
| ---------------- | ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| Claude (CLI/SDK) | `.mcp.json` (repo root) and `.claude/settings.json`                          | Native — Claude reads these directly.                                           |
| Codex (`codex`)  | `$CODEX_HOME/config.toml` (default `~/.codex/config.toml`) `[mcp_servers.*]` | **Provisioned automatically** by the pipeline before every Codex stage (#4025). |

### How Codex provisioning works

Codex does **not** read `.mcp.json` or `.claude/settings.json`. It reads
`[mcp_servers.<name>]` tables from its global config. Before a Codex stage
spawns, the pipeline reads the same servers a Claude stage would see and writes
them into a clearly-delimited **managed block** in `$CODEX_HOME/config.toml`.
This happens on **both** execution paths, byte-for-byte identically: the
VSCode/IPC path via the TypeScript `CodexMcpProvisioner`
(`packages/nightgauge-sdk`), and the Go-direct (auto/CLI) spawn path via
the Go `codexprovision` package (`internal/execution/codexprovision`, #4041).
The Go-direct path also writes the provider-neutral AGENTS.md steering block
(#4028) into the worktree at the same point.

```toml
# >>> BEGIN NIGHTGAUGE MANAGED MCP >>>
# Managed by the Nightgauge pipeline (issue #4025). Servers inside these
# markers are regenerated from the project's .mcp.json on every Codex stage —
# edits here are overwritten. Define your own [mcp_servers.*] OUTSIDE the block.

[mcp_servers.github]
url = "https://api.githubcopilot.com/mcp/"
# <<< END NIGHTGAUGE MANAGED MCP <<<
```

Translation rules (mirrors the [Codex MCP docs](https://developers.openai.com/codex/mcp)):

- **http / sse servers** (`{ "type": "http", "url": "…" }`) → `url`. An
  `Authorization: Bearer ${VAR}` header maps to `bearer_token_env_var = "VAR"`;
  any other headers map to a `http_headers` inline table.
- **stdio servers** (`{ "command": "…", "args": [...], "env": {...} }`) →
  `command` / `args` / `env` / `cwd`.

Properties of the provisioning:

- **Auth-safe.** Codex credentials live in a separate file (`auth.json`); only
  `config.toml` is touched.
- **Idempotent & persisted.** Re-running produces byte-identical output, so the
  block is left in place (not stripped on cleanup) — exactly what `codex mcp add`
  would persist. Removing a server from `.mcp.json` removes it from the block on
  the next stage. Output is **deterministic regardless of JSON key order**:
  server tables and inline-table keys (`env`, `http_headers`) are both sorted, so
  the TypeScript and Go provisioners emit the same bytes and re-runs never thrash.
- **Non-destructive.** Everything outside the markers is preserved byte-for-byte.
  If you define your own `[mcp_servers.<name>]` **outside** the block, the
  pipeline detects the name collision and **skips** its own entry — your
  definition wins.
- **Opt-out.** Set `NIGHTGAUGE_CODEX_MCP_DISABLED=true` in the pipeline
  environment to disable Codex MCP provisioning entirely.

> **Known limitation (global config is shared).** `$CODEX_HOME/config.toml` is a
> machine-global file. The autonomous orchestrator runs Codex stages serially, so
> the managed block reflects the active repo's servers. True parallel Codex runs
> across **different** repos that share one `$CODEX_HOME` would have the
> last-writer's servers in the block. Point `CODEX_HOME` at a per-worktree
> directory if you need fully isolated parallel Codex runs.

> **Verifying end-to-end.** After a Codex stage runs, confirm the servers landed:
> `grep -A3 'BEGIN NIGHTGAUGE MANAGED MCP' "${CODEX_HOME:-$HOME/.codex}/config.toml"`.
> Codex's own `codex mcp list` will then include the provisioned servers.

---

### Prerequisites

- Claude CLI installed and authenticated
- MCP server(s) installed locally or accessible via network
- `.claude/settings.json` (or `~/.claude/settings.json`) configured with server
  definitions

---

## Quick Setup

### Step 1: Configure the MCP Server

MCP servers are defined in `.claude/settings.json` at the repository root (or in
`~/.claude/settings.json` for user-wide availability). Each server entry
specifies how to launch the server process or connect to it.

**Example: Filesystem server**

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/project"]
    }
  }
}
```

**Example: Remote SSE server**

```json
{
  "mcpServers": {
    "my-api-server": {
      "type": "sse",
      "url": "http://localhost:3001/mcp"
    }
  }
}
```

### Step 2: Verify the Server is Reachable

```bash
# List available MCP tools from Claude's perspective
claude mcp list
```

You should see your server and its tools listed. Tool names follow the pattern
`mcp__<server-name>__<tool-name>`.

### Step 3: Grant Stage Permissions

Edit the SKILL.md for the stage(s) you want to extend. Add the MCP tool name(s)
to the `allowed-tools` frontmatter:

```yaml
---
name: nightgauge-feature-dev
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion mcp__filesystem__read_file
  mcp__filesystem__write_file
---
```

> **Note:** Until issues #1725 and #1726 ship (per-stage MCP tool config via
> `.nightgauge/config.yaml`), editing SKILL.md directly is how you grant
> stage-level MCP permissions. This already works today.

---

## Per-Stage Control

Each pipeline stage runs as an isolated subagent with its own `allowed-tools`
list. This means you can grant MCP tools selectively — for example, allowing
browser automation only during `feature-validate`, or database access only
during `feature-dev`.

### Stage-by-Stage Reference

| Stage              | SKILL.md location                             | Common MCP additions          |
| ------------------ | --------------------------------------------- | ----------------------------- |
| `issue-pickup`     | `skills/nightgauge-issue-pickup/SKILL.md`     | project management tools      |
| `feature-planning` | `skills/nightgauge-feature-planning/SKILL.md` | filesystem, docs search       |
| `feature-dev`      | `skills/nightgauge-feature-dev/SKILL.md`      | filesystem, database, browser |
| `feature-validate` | `skills/nightgauge-feature-validate/SKILL.md` | browser, test runners         |
| `pr-create`        | `skills/nightgauge-pr-create/SKILL.md`        | GitHub, Slack notifications   |
| `pr-merge`         | `skills/nightgauge-pr-merge/SKILL.md`         | GitHub, deployment hooks      |

### Example: Browser Automation in Validate Only

In `skills/nightgauge-feature-validate/SKILL.md`:

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
  mcp__playwright__browser_navigate mcp__playwright__browser_snapshot
  mcp__playwright__browser_click
```

All other stages keep their original `allowed-tools` list unchanged. The
Playwright MCP server is started automatically by Claude CLI but only invocable
by stages that list its tools.

---

## Recipes for Popular Servers

### Filesystem (`@modelcontextprotocol/server-filesystem`)

Provides structured file operations — useful when you want the agent to read
files outside the project root or perform safe, audited writes.

**Install**

```bash
npm install -g @modelcontextprotocol/server-filesystem
```

**Configure in `.claude/settings.json`**

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/allowed/directory"]
    }
  }
}
```

**Add to SKILL.md**

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task mcp__filesystem__read_file
  mcp__filesystem__write_file mcp__filesystem__list_directory
```

---

### GitHub (`@modelcontextprotocol/server-github`)

Read and write GitHub resources — issues, PRs, file contents — without shelling
out to `gh`.

**Install**

```bash
npm install -g @modelcontextprotocol/server-github
```

**Configure in `.claude/settings.json`**

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "<your-token>"
      }
    }
  }
}
```

> **Security**: Never hardcode tokens in committed config files. Use environment
> variable references (e.g. `"GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"`)
> or inject via CI environment.

**Add to SKILL.md**

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task mcp__github__create_issue
  mcp__github__get_pull_request mcp__github__create_pull_request
```

---

### Playwright (`@playwright/mcp`)

Browser automation for E2E validation — navigate pages, take screenshots, click
elements.

**Install**

```bash
npm install -g @playwright/mcp
npx playwright install chromium
```

**Configure in `.claude/settings.json`**

```json
{
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": ["-y", "@playwright/mcp", "--headless"]
    }
  }
}
```

**Add to `feature-validate` SKILL.md only**

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
  mcp__playwright__browser_navigate mcp__playwright__browser_snapshot
  mcp__playwright__browser_click mcp__playwright__browser_fill_form
  mcp__playwright__browser_take_screenshot
```

> **In this repo**, `feature-validate` does not carry Playwright tools
> directly — it chains into `nightgauge-verify-ui` (Phase 2.45, #4193),
> which owns the full `browser_*` tool set in its own `allowed-tools`. Add
> Playwright tools straight to a stage's frontmatter only if you are wiring a
> new, standalone browser-driven gate that isn't going through that skill.

#### Console errors and Core Web Vitals (#4193)

Two more Playwright MCP tools matter for gating on runtime behavior, not just
DOM state:

- `mcp__playwright__browser_console_messages` — returns console messages at a
  given severity (`error`/`warning`/`info`/`debug`), scoped to **since the
  last navigation** by default (`all: true` widens to the whole session).
  Calling it twice with no navigation in between returns the _same_
  accumulating list both times — to detect "a new error just from this step,"
  diff the returned list against what you already captured, don't assume the
  tool resets on each call.
- `mcp__playwright__browser_evaluate` — runs arbitrary JS in the page and
  returns the result (Promises are awaited). Verified empirically (#4193):
  calling `performance.getEntriesByType('largest-contentful-paint')` or
  `('layout-shift')` directly logs a `Deprecated API for given entry type`
  console **warning** on every call — noise that would itself corrupt a
  console-error diff. Use a buffered `PerformanceObserver` instead:
  ```js
  new Promise((resolve) => {
    const obs = new PerformanceObserver((list) => resolve(list.getEntries()));
    obs.observe({ type: "largest-contentful-paint", buffered: true });
  });
  ```
  This returns the same entries with zero console noise — confirmed against a
  live page during this issue's implementation. See
  [skills/nightgauge-verify-ui/SKILL.md](../skills/nightgauge-verify-ui/SKILL.md)
  Phase 2.5 for the full LCP/CLS measurement snippet.

---

### PostgreSQL (`@modelcontextprotocol/server-postgres`)

Read-only database introspection — useful for feature-dev stages that need to
understand schema before writing migrations.

**Install**

```bash
npm install -g @modelcontextprotocol/server-postgres
```

**Configure in `.claude/settings.json`**

```json
{
  "mcpServers": {
    "postgres": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-postgres", "${DATABASE_URL}"]
    }
  }
}
```

> **Security**: Use an environment variable reference for the connection string.
> Never commit credentials.

**Add to `feature-dev` SKILL.md only**

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion mcp__postgres__query
  mcp__postgres__describe_table
```

---

### Slack (`@modelcontextprotocol/server-slack`)

Post pipeline status updates to Slack channels.

**Install**

```bash
npm install -g @modelcontextprotocol/server-slack
```

**Configure in `.claude/settings.json`**

```json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-slack"],
      "env": {
        "SLACK_BOT_TOKEN": "${SLACK_BOT_TOKEN}",
        "SLACK_TEAM_ID": "${SLACK_TEAM_ID}"
      }
    }
  }
}
```

**Add to `pr-create` SKILL.md**

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task mcp__slack__post_message
  mcp__slack__list_channels
```

---

### Memory / Knowledge Graph (`@modelcontextprotocol/server-memory`)

Persist context across pipeline runs — useful when the pipeline spans multiple
days or needs to recall decisions from earlier stages.

**Install**

```bash
npm install -g @modelcontextprotocol/server-memory
```

**Configure in `.claude/settings.json`**

```json
{
  "mcpServers": {
    "memory": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-memory"]
    }
  }
}
```

**Add to any stage needing recall**

```yaml
allowed-tools: Read Write Edit Glob Grep Bash Task mcp__memory__create_entities
  mcp__memory__search_nodes mcp__memory__open_nodes
```

---

## Tool Name Conventions

MCP tool names follow a consistent pattern:

```
mcp__<server-name>__<tool-name>
```

Where `<server-name>` is the key from your `mcpServers` config (e.g.,
`filesystem`, `playwright`, `github`) and `<tool-name>` is the tool exposed by
that server (e.g., `read_file`, `browser_navigate`, `create_pull_request`).

To discover exact tool names for any server:

```bash
# List all MCP tools available in the current Claude context
claude mcp list
```

---

## Security Considerations

- **Principle of least privilege**: Only add the specific MCP tools each stage
  needs. Do not add all tools to all stages.
- **Never commit credentials**: Use environment variable references in
  `.claude/settings.json` (`"${ENV_VAR}"`), not literal values.
- **Scope filesystem access**: When using the filesystem server, restrict the
  allowed path to the minimum necessary directory.
- **Read-only for planning stages**: Grant only read operations to
  `issue-pickup` and `feature-planning`; reserve write operations for
  `feature-dev` and later stages.
- **`.claude/settings.json` in `.gitignore`**: If your settings file contains
  server URLs or other environment-specific values, add it to `.gitignore`. Use
  `.claude/settings.json.example` as a committed template.

---

## Troubleshooting

### MCP server not found

```
Error: Unknown tool: mcp__filesystem__read_file
```

The server is not running or not configured. Check:

1. `claude mcp list` shows the server
2. The server name in `mcpServers` matches what appears in the tool name
3. The server process started without errors

### Tool not in `allowed-tools`

```
Tool mcp__playwright__browser_navigate is not in the allowed-tools list
```

Add the tool name to the `allowed-tools` frontmatter in the relevant SKILL.md.

### Credentials not injected

If `${ENV_VAR}` is not substituted, ensure the variable is exported in your
shell before launching the Claude CLI:

```bash
export GITHUB_TOKEN=ghp_...
claude ...
```

For CI, set the variable in your pipeline environment (GitHub Actions secrets,
etc.).

### Server crashes mid-stage

If an MCP server exits unexpectedly, the stage agent will receive tool-not-found
errors for subsequent calls. Check the server's stderr output. Most servers
write logs to stderr; redirect with:

```bash
npx @modelcontextprotocol/server-filesystem /path 2> /tmp/mcp-fs.log
```

---

## GitHub MCP Server (Native)

The GitHub MCP server provides native tool access for Claude's ad-hoc GitHub
interactions. It complements (does not replace) the Go binary deterministic
layer.

### Configuration

Project-scoped via `.mcp.json` at the repository root (committed to git so all
team members get the same setup):

```json
{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/"
    }
  }
}
```

This uses the HTTP transport type rather than spawning a local process. No
personal access token is required in the config file.

### Authentication

Run `/mcp` in Claude Code and select GitHub to authenticate via OAuth.

### Responsibility Matrix

The GitHub MCP server handles exploratory, read-heavy operations. The Go binary
handles deterministic mutations that must succeed reliably.

| Operation                  | Tool      | Rationale                               |
| -------------------------- | --------- | --------------------------------------- |
| Read issue details         | MCP       | Ad-hoc, Claude-driven exploration       |
| Search code across repos   | MCP       | Exploratory, benefits from native tools |
| Review PR diffs            | MCP       | Requires reasoning about changes        |
| List PRs / issues          | MCP       | Quick lookups                           |
| Add issue to project board | Go binary | Deterministic, must succeed reliably    |
| Set board field values     | Go binary | Requires exact field IDs                |
| Epic completion check      | Go binary | Deterministic business logic            |
| blockedBy mutations        | Go binary | Deterministic relationship management   |
| Pipeline state transitions | Go binary | Deterministic state machine             |
| Sub-issue linking          | Go binary | Deterministic parent-child management   |

### Usage in Skills

Skills that interact with GitHub should include MCP tools in their
`allowed-tools`:

```yaml
allowed-tools: Read Grep Glob Bash(gh *) mcp__github__*
```

The `mcp__github__*` pattern allows all tools from the GitHub MCP server.

### MCP Output Limits

Set `MAX_MCP_OUTPUT_TOKENS` if working with large repositories:

```bash
export MAX_MCP_OUTPUT_TOKENS=50000
```

Default limit is 25,000 tokens. Warning threshold is 10,000 tokens.

---

## Related Documentation

- [Configuration Reference](CONFIGURATION.md) — Full
  `.nightgauge/config.yaml` schema
- [Skills README](../skills/README.md) — SKILL.md format and `allowed-tools`
  field reference
- [Contributing Guide](../CONTRIBUTING.md) — How to modify skills
- [Pipeline Execution](PIPELINE_EXECUTION.md) — How stages run as subagents
- [Security Standards](../standards/security.md) — Credential and input
  validation rules
