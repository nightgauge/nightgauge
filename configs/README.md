# Tool-Specific Configurations

This directory contains tool-specific configurations for various AI coding
assistants.

> **Note:** The primary way to use Nightgauge is through the **VSCode
> Extension** or **Claude Code CLI**. Codex includes adapter wrappers for the
> core issue-to-PR stages.

## Status

| Tool       | Status    | Notes                                  |
| ---------- | --------- | -------------------------------------- |
| `claude/`  | Reference | Team settings example                  |
| `copilot/` | Reference | GitHub Copilot instructions template   |
| `cursor/`  | Reference | Cursor IDE rules template              |
| `codex/`   | Beta      | Stage wrappers + runtime contract docs |
| `kiro/`    | Reference | Amazon Kiro steering files             |

See `codex/README.md` for stage wrapper commands and runtime requirements.

## Recommended Usage

### For Nightgauge Pipeline

Use the VSCode extension or Claude Code CLI:

```bash
# VSCode Extension (primary)
cd packages/nightgauge-vscode && npm run package

# Claude Code CLI (alternative)
claude plugin install nightgauge@nightgauge-plugins
```

### For Standalone Skills

Copy skills directly to your tool's skills directory:

| Tool           | Skills Location    |
| -------------- | ------------------ |
| GitHub Copilot | `.github/skills/`  |
| OpenAI Codex   | `~/.codex/skills/` |
| Cursor IDE     | `.cursor/skills/`  |

Skills are located in the `skills/` directory at the root of this repository.

## When to Use These Configs

These configurations may be useful when:

1. Setting up a new team with a specific AI tool
2. Understanding how to configure a tool for Nightgauge standards
3. Creating custom configurations for tools not yet fully supported

For most use cases, the skills in `skills/` provide better portability and
maintenance than tool-specific configurations.

---

**See [README.md](../README.md) for the full Nightgauge documentation.**
