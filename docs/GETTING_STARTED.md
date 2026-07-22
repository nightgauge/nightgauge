# Getting Started

This guide explains how to install and use the nightgauge for your AI
coding assistant.

## Prerequisites

- Git access to this repository
- One of the supported AI coding assistants:
  - Claude Code
  - GitHub Copilot
  - OpenAI Codex
  - Cursor IDE
  - Kiro IDE

## Installation

### Claude Code (Recommended)

#### Option 1: Add Plugin Marketplace

Add to your `~/.claude/settings.json`:

```json
{
  "extraKnownMarketplaces": ["https://github.com/nightgauge/nightgauge"]
}
```

Then install plugins via Claude Code:

```bash
/plugins install smart-setup   # One-time AI-readiness setup
/plugins install docs          # Ongoing documentation maintenance
```

#### Option 2: Direct Plugin Reference

Add to your `~/.claude/settings.json`:

```json
{
  "plugins": ["https://github.com/nightgauge/nightgauge/tree/main/claude-plugins/nightgauge"]
}
```

### GitHub Copilot

Copy the AGENTS.md template to your repository:

```bash
curl -o AGENTS.md https://raw.githubusercontent.com/nightgauge/nightgauge/main/standards/AGENTS_TEMPLATE.md
```

Or copy the Copilot-specific instructions:

```bash
mkdir -p .github
curl -o .github/copilot-instructions.md https://raw.githubusercontent.com/nightgauge/nightgauge/main/configs/copilot/copilot-instructions.md
```

### OpenAI Codex

Copy the AGENTS.md template (Codex reads AGENTS.md automatically):

```bash
curl -o AGENTS.md https://raw.githubusercontent.com/nightgauge/nightgauge/main/standards/AGENTS_TEMPLATE.md
```

#### Codex CLI Quickstart (Beta Adapter)

Codex currently supports the core Nightgauge pipeline via adapter wrappers.

Prerequisites:

- Node.js 18+
- `gh` authenticated (`gh auth status`)
- Built Nightgauge SDK CLI output

From the repository root:

```bash
# Build the SDK once
npm run -w @nightgauge/sdk build

# Run the full issue-to-PR path
scripts/run-stage.sh codex issue-pickup 42
scripts/run-stage.sh codex feature-planning 42
scripts/run-stage.sh codex feature-dev 42
scripts/run-stage.sh codex feature-validate 42
scripts/run-stage.sh codex pr-create 42
scripts/run-stage.sh codex pr-merge 42
```

Known beta limitations:

- Queue/project-sync and some plugin-level automations remain Claude-first
- Wrappers require built SDK output:
  `npm run -w @nightgauge/sdk build`

### Cursor IDE

Copy the Cursor rules:

```bash
mkdir -p .cursor/rules
curl -o .cursor/rules/nightgauge-standards.mdc https://raw.githubusercontent.com/nightgauge/nightgauge/main/configs/cursor/nightgauge-standards.mdc
```

## Usage

### Unified Stage Workflow (All Tools)

| Stage              | Claude Code                    | Codex CLI (Beta Adapter)                         | GitHub Copilot                                         | Cursor IDE                                             |
| ------------------ | ------------------------------ | ------------------------------------------------ | ------------------------------------------------------ | ------------------------------------------------------ |
| `issue-pickup`     | `/nightgauge-issue-pickup 42`  | `scripts/run-stage.sh codex issue-pickup 42`     | `Use skill nightgauge-issue-pickup for issue #42.`     | `Use skill nightgauge-issue-pickup for issue #42.`     |
| `feature-planning` | `/nightgauge-feature-planning` | `scripts/run-stage.sh codex feature-planning 42` | `Use skill nightgauge-feature-planning for issue #42.` | `Use skill nightgauge-feature-planning for issue #42.` |
| `feature-dev`      | `/nightgauge-feature-dev`      | `scripts/run-stage.sh codex feature-dev 42`      | `Use skill nightgauge-feature-dev for issue #42.`      | `Use skill nightgauge-feature-dev for issue #42.`      |
| `feature-validate` | `/nightgauge-feature-validate` | `scripts/run-stage.sh codex feature-validate 42` | `Use skill nightgauge-feature-validate for issue #42.` | `Use skill nightgauge-feature-validate for issue #42.` |
| `pr-create`        | `/nightgauge-pr-create`        | `scripts/run-stage.sh codex pr-create 42`        | `Use skill nightgauge-pr-create for issue #42.`        | `Use skill nightgauge-pr-create for issue #42.`        |
| `pr-merge`         | `/nightgauge-pr-merge`         | `scripts/run-stage.sh codex pr-merge 42`         | `Use skill nightgauge-pr-merge for issue #42.`         | `Use skill nightgauge-pr-merge for issue #42.`         |

Cross-reference:

- Adapter capability matrix: [ADAPTER_MATRIX.md](./ADAPTER_MATRIX.md)
- Skills catalog: [../skills/README.md](../skills/README.md)
- Codex runtime details:
  [../configs/codex/README.md](../configs/codex/README.md)

### Smart Setup Command (Claude Code)

Make any repository AI-ready:

```bash
/smart-setup
```

This is an interactive command. You can request specific behaviors in natural
language:

- "Just audit what's missing" - Only report what exists and what's missing
- "Skip questions and use placeholders" - Generate docs without asking
  clarifying questions
- "Only create the docs directory" - Skip AGENTS.md and CLAUDE.md

### Update Docs Command (Claude Code)

_Requires the `docs` plugin: `/plugins install docs`_

Keep documentation in sync with your codebase:

```bash
/update-docs
```

This is an interactive command. You can request specific behaviors in natural
language:

- "Just show me what's stale" - Report without making changes
- "Focus on the API docs" - Limit scope to specific documentation
- "Auto-fix simple issues" - Automatically fix obvious problems

### Using with Other AI Assistants

For AI assistants without native plugin support:

1. Copy
   [AI_SMART_SETUP.md](https://github.com/nightgauge/nightgauge/blob/main/AI_SMART_SETUP.md)
   to your repository
2. Ask your AI assistant: "Read AI_SMART_SETUP.md and follow the instructions"

## Verification

After installation, verify the setup:

### Claude Code

```text
/smart-setup - just show me what's missing
```

You should see a report of your repository's AI-readiness status.

### Other Tools

Check that the configuration files are being read by your AI assistant. Most
assistants will acknowledge the AGENTS.md file when you start a conversation.

### Codex CLI

Run:

```bash
scripts/run-stage.sh codex issue-pickup 42
```

You should see preflight checks and stage execution output for issue `#42`.

## Next Steps

- Review [ARCHITECTURE.md](./ARCHITECTURE.md) for repository structure
- See [CODE_STANDARDS.md](./CODE_STANDARDS.md) for contribution guidelines
- Check [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) if you encounter issues

## Author

nightgauge
