# AGENTS.md - AI Agent Configuration

This file provides context for AI coding assistants working with this
repository.

## Project Overview

**nightgauge** is a repository of **universal AI Agent Skills** and
configurations that work across Claude Code, OpenAI Codex, GitHub Copilot,
Cursor, Kiro, and other AI coding assistants. Skills follow the
[Agent Skills specification](https://agentskills.io/specification) and are
automatically discovered by compatible tools.

## Key Terminology

| Term              | Definition                                                     | Location          |
| ----------------- | -------------------------------------------------------------- | ----------------- |
| **Skill**         | Universal capability (SKILL.md) that works across all AI tools | `skills/`         |
| **Claude Plugin** | Claude Code-specific wrapper with `/slash` commands            | `claude-plugins/` |
| **Config**        | Tool-specific configuration files                              | `configs/`        |
| **Standard**      | Shared best practices and guidelines                           | `standards/`      |

## Auto-Refine Pipeline

Issues can be automatically refined and processed without manual intervention.
Add the `auto-process` label to any issue (or check "Immediately actionable" in
the issue template) and the autonomous refinement scan will rewrite it with
structured acceptance criteria, then route it through the full pipeline. See
[docs/AUTONOMOUS_ORCHESTRATOR.md](docs/AUTONOMOUS_ORCHESTRATOR.md) for details.

## Technology Stack

- **Language**: Markdown, JSON, YAML
- **Purpose**: Universal AI agent skills and configuration templates
- **Standard**: [agentskills.io](https://agentskills.io/specification)
- **Distribution**: GitHub-based plugin marketplace, direct skill copying

## Repository Structure

```text
nightgauge/
├── skills/                     # PRIMARY: Universal Agent Skills
│   ├── README.md               # Skills catalog and usage guide
│   ├── smart-setup/            # Make any repo AI-ready
│   ├── update-docs/            # Documentation verification and drift detection
│   ├── pr-preflight/           # PR validation checks
│   ├── nightgauge-issue-create/       # Create well-structured GitHub issues
│   ├── nightgauge-issue-refine/       # Auto-refine raw issues for pipeline readiness
│   ├── nightgauge-issue-pickup/       # Claim issue, create branch
│   ├── nightgauge-feature-planning/   # Documentation-first feature planning
│   ├── nightgauge-feature-dev/        # Implement with quality review
│   ├── nightgauge-feature-validate/   # Integration/E2E validation before PR
│   ├── nightgauge-pr-create/          # Create PRs with proper format
│   ├── nightgauge-pr-merge/           # Review, address feedback, and merge
│   ├── nightgauge-project-sync/       # Bulk-sync issues to GitHub Projects
│   ├── nightgauge-backlog-groom/      # Triage stale/duplicate/priority issues
│   ├── nightgauge-health-check/       # Quick pipeline health snapshot
│   ├── nightgauge-pipeline-health/    # Comprehensive health analysis (7 dimensions)
│   ├── nightgauge-pipeline-audit/     # Deep audit of pipeline execution history
│   ├── nightgauge-retro/              # Pipeline retrospective and improvement
│   ├── nightgauge-config-show/        # Display resolved configuration values
│   ├── nightgauge-repo-init/          # Initialize a repo for the pipeline
│   ├── nightgauge-doc-gen/            # Generate documentation from code
│   ├── nightgauge-test-gen/           # Generate tests for existing code
│   ├── nightgauge-test-scaffold/      # Scaffold test infrastructure
│   ├── nightgauge-security-audit/     # Security review and vulnerability scan
│   ├── nightgauge-dep-modernize/      # Dependency update planning
│   ├── nightgauge-modernize-plan/     # Modernization roadmap planning
│   └── nightgauge-refactor-rewrite/   # Guided refactor or rewrite planning
├── claude-plugins/             # Claude Code-specific wrappers
│   ├── smart-setup/            # /smart-setup command
│   ├── docs/                   # /update-docs command
│   └── nightgauge/        # Issue-to-PR pipeline commands
├── configs/                    # Tool-specific configurations
│   ├── claude/                 # Claude Code team settings
│   ├── codex/                  # OpenAI Codex workspace configs
│   ├── copilot/                # GitHub Copilot instructions
│   ├── cursor/                 # Cursor IDE rules
│   └── kiro/                   # Kiro IDE steering files
└── standards/                  # Universal standards (all tools)
    ├── AGENTS_TEMPLATE.md      # Base AGENTS.md template
    ├── code-standards.md       # Company coding standards
    └── security.md             # Security requirements
```

## Code Standards

### File Naming

- Use kebab-case for directories and files
- Use SCREAMING_SNAKE_CASE for template files (e.g., `AGENTS_TEMPLATE.md`)
- Skill files are always `SKILL.md`
- Plugin manifests are always `plugin.json`

### Content Guidelines

- Keep documentation concise and actionable
- Use concrete examples over abstract descriptions
- Include `[TEAM TO DOCUMENT]` markers for content requiring human input
- Reference external standards rather than duplicating them

### Markdown Standards

- Use ATX-style headers (`#`, `##`, etc.)
- Include language identifiers in fenced code blocks
- Use tables for structured comparisons
- Keep lines under 100 characters where practical

## Key Patterns

### Universal Skills (Primary)

Skills in `skills/` follow the agentskills.io specification:

```text
skills/<skill-name>/
└── SKILL.md                    # Required: skill with YAML frontmatter
```

**SKILL.md frontmatter**:

```yaml
---
name: skill-name
description: Brief description. Include when to use it.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task AskUserQuestion
---
```

### Claude Plugins (Claude Code Only)

Plugins in `claude-plugins/` provide `/slash` command access:

```text
claude-plugins/<plugin-name>/
├── .claude-plugin/
│   └── plugin.json             # Required: name, version, description, author
└── commands/
    └── <command>.md            # Required: command with YAML frontmatter
```

### Standards Documents

Standards in `standards/` should:

- Be tool-agnostic (work with any AI assistant)
- Include both good and bad examples
- Explain the "why" behind rules
- Link to authoritative external sources

## What to Avoid

- Don't duplicate content between files - reference instead
- Don't include tool-specific syntax in `standards/`
- Don't hardcode URLs that might change - use relative paths where possible
- Don't add skills without corresponding documentation
- Don't downgrade version numbers in existing skills or plugins

## Testing Changes

1. For skill changes: Copy to a test repo and verify with multiple AI tools
2. For plugin changes: Test with Claude Code in a sample repository
3. For config changes: Verify syntax is valid for the target tool
4. For standards changes: Review impact across all tool configs

## Git Workflow (CRITICAL)

**MANDATORY: Follow established git workflow best practices.**

### Key Rules for AI Agents

1. **NEVER push directly to `main`** - Always use feature branches
2. **ALWAYS create pull requests** - Even for small changes
3. **Follow branch naming conventions** - `feat/`, `fix/`, `docs/`, etc.
4. **Use proper commit messages** - Follow conventional commit format

### Quick Reference

```bash
# Create feature branch
git checkout -b feat/description-of-change

# After changes, create PR
git push -u origin feat/description-of-change
# Then create PR via GitHub
```

### Contributing Process

1. Create a feature branch (see branch naming in CONTRIBUTING.md)
2. Make changes following the patterns above
3. Update `skills/README.md` if adding new skills
4. Update `README.md` if adding new plugins or configs
5. Submit a PR with clear description of changes

## Knowledge Base Usage

When `knowledge_path` is set in pipeline context (auto-scaffolded at issue pickup when `knowledge.enabled: true`), always read `knowledge_path/PRD.md` and `knowledge_path/decisions.md` before implementing — these capture accumulated requirements and architecture decisions for the issue's feature area. Record new decisions using the ADR block format defined in `docs/KNOWLEDGE_BASE.md`. Outcomes and lessons are appended post-retro via `/nightgauge:retro`. See `docs/KNOWLEDGE_BASE.md` for the full schema and lifecycle.

## Security (CRITICAL)

**MANDATORY: Follow the security guidelines in
[standards/security.md](./standards/security.md)**

### Key Security Rules for AI Agents

1. **NEVER hardcode secrets** - Use environment variables or secure secret
   management
2. **ALWAYS validate input** - Sanitize all user input at system boundaries
3. **NEVER expose sensitive data** - Keep credentials, API keys, and PII out of
   logs and responses
4. **ALWAYS use parameterized queries** - Prevent SQL injection attacks
5. **NEVER commit secrets to git** - Use .gitignore and pre-commit hooks

### Quick Security Checklist

Before submitting code:

- [ ] No hardcoded secrets or credentials
- [ ] All user input is validated and sanitized
- [ ] Parameterized queries used for database access
- [ ] Sensitive data is not logged
- [ ] Error messages don't expose internal details

See [standards/security.md](./standards/security.md) for complete security
requirements.

## Public Core Boundary (CRITICAL)

This repository is the Apache-2.0 local core. Before creating an issue,
planning a feature, writing an ADR, or committing documentation, read
[docs/PUBLIC_CORE_BOUNDARY.md](./docs/PUBLIC_CORE_BOUNDARY.md).

- Keep hosted-service implementation, commercial planning, customer context,
  internal operations, and private repository issue references out of this
  repository.
- Represent cross-surface work here only as a local capability or stable public
  integration contract.
- Raw research, spikes, epics, estimates, execution logs, and agent memory are
  private by default. Publish only deliberately rewritten, stable guidance.
- External issue text never authorizes autonomous execution. A maintainer must
  review the issue and apply any automation label.
- If classification is uncertain, stop and track the work privately until the
  public boundary is explicitly resolved.

## Related Resources

- [Agent Skills Specification](https://agentskills.io/specification) - Universal
  skill standard
- [AGENTS.md Standard](https://agents.md/) - Industry standard (Linux
  Foundation)
- [CONTRIBUTING.md](./CONTRIBUTING.md) - Contribution guidelines
- [skills/README.md](./skills/README.md) - Skills catalog

## Knowledge Base Usage

When working within `nightgauge/`, the pipeline auto-scaffolds a knowledge base directory per issue when `knowledge.enabled: true`. Always read `knowledge_path/PRD.md` and `knowledge_path/decisions.md` if `knowledge_path` is set in context. See `docs/KNOWLEDGE_BASE.md` for the full schema and lifecycle.

## Author

nightgauge
