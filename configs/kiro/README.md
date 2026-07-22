# Kiro IDE Configuration

This directory contains configuration files for [Kiro IDE](https://kiro.dev/),
AWS's AI-powered autonomous coding environment.

## Overview

Kiro IDE uses a **steering system** to provide intelligent, context-aware AI
guidance. Steering files are automatically loaded based on file context and can
reference other project files.

## Directory Structure

```text
configs/kiro/
├── README.md                    # This file
└── steering/                    # Steering file templates
    └── nightgauge-standards.md     # Nightgauge coding standards
```

## Using Kiro Skills

Kiro supports Agent Skills from the `skills/` directory. To use Nightgauge
skills with Kiro:

```bash
# Copy skills to your project
mkdir -p .kiro/skills
cp -r skills/smart-setup/ .kiro/skills/smart-setup/
cp -r skills/update-docs/ .kiro/skills/update-docs/
```

## Setting Up Kiro for Your Project

### 1. Create Steering Directory

```bash
mkdir -p .kiro/steering
```

### 2. Add Steering Files

Copy the templates from this directory to your project:

```bash
cp configs/kiro/steering/*.md .kiro/steering/
```

### 3. Configure MCP Servers (Optional)

Create `.kiro/settings/mcp.json` for MCP server integration:

```json
{
  "mcpServers": {
    "context7": {
      "command": "npx",
      "args": ["-y", "@upstash/context7-mcp"]
    }
  }
}
```

## Steering File Format

Steering files use Markdown with optional frontmatter for conditional inclusion:

```markdown
---
description: When this steering file applies
globs:
  - "src/**/*.ts"
  - "lib/**/*.ts"
alwaysApply: false
---

# Steering Content

Your AI guidance here...
```

### Frontmatter Options

| Option        | Description                                 |
| ------------- | ------------------------------------------- |
| `description` | Human-readable description of when to apply |
| `globs`       | File patterns that trigger this steering    |
| `alwaysApply` | If `true`, always include this steering     |

## Referencing Project Files

Kiro supports referencing other files in steering:

```markdown
Follow the coding standards in #[[file:docs/CODE_STANDARDS.md]]
```

## Example Steering Files

### Always-Applied Standards

```markdown
---
description: Core Nightgauge development standards
alwaysApply: true
---

# Nightgauge Development Standards

- Follow coding standards in docs/CODE_STANDARDS.md
- Use conventional commits for all changes
- Never commit secrets or credentials
- Write tests for all new functionality
```

### Context-Specific Steering

```markdown
---
description: React component guidelines
globs:
  - "src/components/**/*.tsx"
  - "src/components/**/*.jsx"
---

# React Component Standards

- Use functional components with hooks
- Implement proper TypeScript types for props
- Include accessibility attributes (aria-\*, role)
- Add unit tests using React Testing Library
```

## Kiro vs Other AI Tools

| Feature         | Kiro              | Claude Code | Copilot                           |
| --------------- | ----------------- | ----------- | --------------------------------- |
| Steering files  | `.kiro/steering/` | `CLAUDE.md` | `.github/copilot-instructions.md` |
| Skills location | `.kiro/skills/`   | Via plugin  | `.github/skills/`                 |
| MCP support     | Yes               | Yes         | Limited                           |
| Autonomous mode | Yes (Autopilot)   | Yes         | No                                |

## Related Resources

- [Kiro IDE Documentation](https://kiro.dev/docs)
- [Kiro Setup Guide](https://github.com/nightgauge/nightgauge/blob/main/guides/agent-setup/KIRO_SETUP_GUIDE.md)
- [Universal Skills](../../skills/README.md)
- [Agent Skills Specification](https://agentskills.io/specification)

## Author

nightgauge
