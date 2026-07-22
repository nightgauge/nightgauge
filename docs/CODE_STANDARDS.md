# Code Standards

This document defines the standards for contributing to the nightgauge repository.

## File Types

This repository primarily contains:

- **Markdown (.md)** - Documentation and command definitions
- **JSON (.json)** - Plugin manifests and configurations
- **YAML (.yaml)** - Configuration files

## Naming Conventions

### Directories

- Use `kebab-case` for directory names
- Examples: `smart-setup`, `code-standards`

### Files

| File Type     | Convention                        | Example                   |
| ------------- | --------------------------------- | ------------------------- |
| Commands      | `kebab-case.md`                   | `smart-setup.md`          |
| Templates     | `SCREAMING_SNAKE_CASE.md`         | `AGENTS_TEMPLATE.md`      |
| Manifests     | `plugin.json`, `marketplace.json` | Always these names        |
| Documentation | `SCREAMING_SNAKE_CASE.md`         | `GETTING_STARTED.md`      |
| Config files  | `kebab-case.ext`                  | `copilot-instructions.md` |

## Markdown Standards

### Headers

- Use ATX-style headers (`#`, `##`, etc.)
- Only one H1 (`#`) per file - the document title
- Use H2 (`##`) for major sections
- Use H3 (`###`) for subsections

### Code Blocks

Always include language identifiers:

````markdown
```json
{
  "name": "example"
}
```
````

### Tables

Use tables for structured comparisons:

```markdown
| Column 1 | Column 2 |
| -------- | -------- |
| Value 1  | Value 2  |
```

### Line Length

- Keep lines under 100 characters where practical
- Exceptions: URLs, code blocks, tables

### Lists

- Use `-` for unordered lists
- Use `1.` for ordered lists
- Add blank lines between list items if they contain multiple paragraphs

## JSON Standards

### Formatting

- Use 2-space indentation
- Include trailing newline
- No trailing commas

### Schema References

Include `$schema` when available:

```json
{
  "$schema": "https://anthropic.com/claude-code/plugin.schema.json",
  "name": "plugin-name"
}
```

## Content Guidelines

### Be Concise

❌ **Too verbose:**

> "In order to accomplish the task of making your repository AI-ready, you will need to follow the steps outlined below which will guide you through the process..."

✅ **Concise:**

> "To make your repository AI-ready:"

### Use Concrete Examples

❌ **Abstract:**

> "Use appropriate naming conventions for your files."

✅ **Concrete:**

> "Use kebab-case for command files: `smart-setup.md`, `update-docs.md`"

### Reference, Don't Duplicate

When information exists elsewhere, link to it:

```markdown
See [standards/code-standards.md](../standards/code-standards.md) for coding standards.
```

### Mark Unknowns

Use `[TEAM TO DOCUMENT]` markers for content requiring human input:

```markdown
## Deployment Process

[TEAM TO DOCUMENT: Describe the deployment pipeline for plugins]
```

## Plugin-Specific Standards

### Plugin Manifest (plugin.json)

Required fields:

```json
{
  "name": "plugin-name",
  "description": "Brief description of what the plugin does",
  "version": "1.0.0",
  "author": {
    "name": "Author Name",
    "email": "author@example.com"
  },
  "commands": []
}
```

### Skill Files (slash commands)

Each `/nightgauge:<name>` slash command is a `skills/<name>/SKILL.md`
(ADR-007, revised #3876) — there are no command-wrapper files. Required
frontmatter and structure follow the
[Agent Skills specification](https://agentskills.io/specification),
`CONTRIBUTING.md`, and `.claude/rules/skills.md`. `disable-model-invocation` is
injected into the generated plugin copy by `scripts/install-agent-skills.sh` —
not authored in the canonical skill.

### README Files

Each plugin directory must have a README.md with:

1. Plugin name and description
2. Installation instructions
3. Available commands
4. Usage examples
5. Related resources

## Quality Checklist

Before submitting changes:

- [ ] Markdown passes linting (no MD errors)
- [ ] JSON is valid (use `jq .` to verify)
- [ ] All links are valid
- [ ] Code blocks have language identifiers
- [ ] File names follow conventions
- [ ] No duplicate content (reference instead)
- [ ] Examples are realistic and tested

## SKILL.md Frontmatter Schema

Every `skills/*/SKILL.md` file must include YAML frontmatter between `---`
markers. CI validates this via `scripts/validate-skill-metadata.sh`.

### Required Fields (in order)

| Field              | Type   | Description                                      |
| ------------------ | ------ | ------------------------------------------------ |
| `name`             | string | Kebab-case identifier, must match directory name |
| `description`      | string | 1-2 sentence use case                            |
| `license`          | string | `"Apache-2.0"`                                   |
| `metadata.author`  | string | `"nightgauge"`                                   |
| `metadata.version` | string | Semver, quoted (e.g., `"1.0.0"`)                 |
| `metadata.source`  | string | `"https://github.com/nightgauge/nightgauge"`     |
| `allowed-tools`    | string | Space-separated tool names                       |

Canonical skills must **not** set `disable-model-invocation` themselves —
`scripts/install-agent-skills.sh` injects it into the generated plugin copy at
build time (`scripts/validate-skill-metadata.sh` rejects it if authored
directly). By default every skill gets it (side-effecting, user-triggered
workflow); a skill opts out via `metadata.chainable: true` (see below).

### Optional Fields (after required, in order)

| Field                | Type    | Description                                                                                                                                                                                                                                                                                                                                                                                   |
| -------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `programmatic-tools` | string  | Space-separated SDK tool names                                                                                                                                                                                                                                                                                                                                                                |
| `context`            | string  | `"fork"` when present                                                                                                                                                                                                                                                                                                                                                                         |
| `agent`              | string  | Subagent type                                                                                                                                                                                                                                                                                                                                                                                 |
| `model`              | string  | `"haiku"` or `"sonnet"`                                                                                                                                                                                                                                                                                                                                                                       |
| `hooks`              | object  | Hook definitions                                                                                                                                                                                                                                                                                                                                                                              |
| `metadata.chainable` | boolean | Set to `true` only on a read-only/advisory skill (analysis or audit; no mutation without an explicit opt-in flag like `--fix`) to exempt it from `disable-model-invocation` so a parent skill's documented `Skill()` chain into it works when the caller is the model, not a human typing the slash command (#4194). Never set on the six core pipeline stages or other side-effecting flows. |

### Example

```yaml
---
name: my-skill
description: Brief description of what this skill does and when to use it.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash
---
```

A read-only skill meant to be chained into from another skill adds
`chainable: true` under `metadata`:

```yaml
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
```

## Author

nightgauge
