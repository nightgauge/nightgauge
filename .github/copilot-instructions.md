# GitHub Copilot Instructions

This file provides context for GitHub Copilot when working with the nightgauge repository.

## Project Overview

This repository contains universal AI agent plugins and configurations for Claude Code, OpenAI Codex, GitHub Copilot, Cursor, and other AI coding assistants. It enables company-wide distribution of AI tool configurations with Nightgauge.

## Technology Stack

- **Primary Languages**: Markdown, JSON, YAML
- **Purpose**: Configuration and documentation templates
- **Distribution**: GitHub-based plugin marketplace for Claude Code

## Repository Structure

```text
nightgauge/
├── claude-plugins/             # Claude Code plugins
│   └── smart-setup/            # Make any repo AI-ready
│       ├── .claude-plugin/
│       │   └── plugin.json     # Plugin manifest
│       ├── commands/           # Slash commands
│       └── skills/             # Auto-activating skills
├── configs/                    # Tool-specific configurations
│   ├── claude/                 # Claude Code team settings
│   ├── codex/                  # OpenAI Codex configs
│   ├── copilot/                # GitHub Copilot instructions
│   └── kiro/                   # Kiro IDE steering
├── standards/                  # Universal standards
│   ├── code-standards.md
│   └── security.md
├── skills/                     # Standalone skills
└── docs/                       # Documentation
```

## Key Guidelines

### When Suggesting Changes

1. **File naming**: Use `kebab-case` for directories and files
2. **Template files**: Use `SCREAMING_SNAKE_CASE` (e.g., `AGENTS_TEMPLATE.md`)
3. **Markdown**: Use ATX-style headers, include language in code blocks
4. **JSON**: Use 2-space indentation, no trailing commas

### Content Principles

- **Be concise**: Every word should add value
- **Use examples**: Concrete over abstract
- **Reference, don't duplicate**: Link to existing documentation
- **Mark unknowns**: Use `[TEAM TO DOCUMENT]` for items needing human input

### NON-DESTRUCTIVE Policy

When working with AI configuration files:

- Never overwrite existing AGENTS.md or CLAUDE.md without permission
- Only suggest additions, not replacements
- Ask before modifying user's custom documentation

## Common Tasks

### Adding a New Command

1. Create command file in `claude-plugins/<plugin>/commands/`
2. Update `plugin.json` with command reference
3. Update `marketplace.json` with description
4. Update plugin README

### Adding Tool Configuration

1. Create directory in `configs/<tool>/`
2. Add configuration files
3. Add README explaining usage
4. Update main README compatibility matrix

### Modifying Standards

1. Changes to `standards/` affect all tools
2. Keep standards tool-agnostic
3. Reference external standards (OWASP, etc.) rather than duplicating

## Security (CRITICAL)

MANDATORY: Follow the security guidelines in [standards/security.md](../standards/security.md)

### Key Security Rules for GitHub Copilot

1. **NEVER hardcode secrets** - Use environment variables or secure secret management
2. **ALWAYS validate input** - Sanitize all user input at system boundaries
3. **NEVER expose sensitive data** - Keep credentials, API keys, and PII out of logs and responses
4. **ALWAYS use parameterized queries** - Prevent SQL injection attacks
5. **NEVER commit secrets to git** - Use .gitignore and pre-commit hooks

### Quick Security Checklist

Before submitting code:

- [ ] No hardcoded secrets or credentials
- [ ] All user input is validated and sanitized
- [ ] Parameterized queries used for database access
- [ ] Sensitive data is not logged
- [ ] Error messages don't expose internal details

See [standards/security.md](../standards/security.md) for complete security requirements.

## Related Resources

- [AGENTS.md](../AGENTS.md) - AI agent configuration for this repository
- [CLAUDE.md](../CLAUDE.md) - Claude Code specific instructions
- [docs/](../docs/) - Complete documentation
- [nightgauge/nightgauge](https://github.com/nightgauge/nightgauge) - Source of methodology

## Author

nightgauge
