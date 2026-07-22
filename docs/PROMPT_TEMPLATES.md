# Prompt Template System

Unified, parameterized prompt templates for all Nightgauge pipeline stages.

## Overview

Before this system, prompts were hardcoded across three product layers:

- **Skills** — Markdown text embedded directly in SKILL.md files
- **SDK** — Stage roles and instructions built by string concatenation
- **VSCode Extension** — Dialog messages and validation text in TypeScript

This fragmentation caused duplication, inconsistency, and a high maintenance burden. The template system provides a single source of truth for all prompt content.

## Architecture

```
skills/templates/         ← Git-versioned template files (.handlebars)
      │
      ▼
TemplateRegistry          ← Runtime registry (SDK) — loads & caches templates
PromptRenderer            ← Handlebars rendering engine with compile cache
      │
      ├── BaseStage       ← SDK stages can render templates before execution
      └── PromptTemplateService  ← VSCode extension helper service
```

Templates are stored as `.handlebars` files in `skills/templates/`. They are loaded at runtime by `TemplateRegistry`, compiled by `PromptRenderer`, and injected with stage-specific context variables.

## Template File Format

Every template is a `.handlebars` file with YAML frontmatter followed by Handlebars content:

```handlebars
---
name: "feature-planning-system"
version: "1.0.0"
layer: "skill"
description: "System role for the feature-planning stage"
params:
  - name: "issueNumber"
    type: "number"
    description: "GitHub issue number being planned"
    required: true
  - name: "issueTitle"
    type: "string"
    description: "Issue title"
    required: false
---

You are the feature-planning agent for issue #{{issueNumber}}{{#if issueTitle}}:
  {{issueTitle}}{{/if}}. Your role is to produce an approved PLAN.md...
```

### Required frontmatter fields

| Field         | Type   | Description                                     |
| ------------- | ------ | ----------------------------------------------- |
| `name`        | string | Unique template name used for registry lookup   |
| `version`     | string | Semantic version (e.g., `1.0.0`)                |
| `layer`       | string | One of: `skill`, `sdk`, `extension`, `platform` |
| `description` | string | Short human-readable description                |

### Optional frontmatter fields

| Field    | Type  | Description                           |
| -------- | ----- | ------------------------------------- |
| `params` | array | Documentation for available variables |

## Handlebars Syntax

Templates use [Handlebars](https://handlebarsjs.com/) syntax:

| Syntax                                | Description            |
| ------------------------------------- | ---------------------- |
| `{{variable}}`                        | Variable substitution  |
| `{{{variable}}}`                      | No HTML escaping       |
| `{{#if condition}}...{{/if}}`         | Conditional block      |
| `{{#unless condition}}...{{/unless}}` | Inverse conditional    |
| `{{#each items}}...{{/each}}`         | Array iteration        |
| `{{variable.property}}`               | Nested property access |

> **Note**: The renderer uses `noEscape: true` so `{{variable}}` and
> `{{{variable}}}` behave identically — HTML characters are **not** escaped.
> Prompts are plain text, not HTML.

## Template Catalog

See [skills/templates/README.md](../skills/templates/README.md) for the full catalog.

### System Prompts (`skills/templates/system-prompts/`)

| Template                  | Stage            |
| ------------------------- | ---------------- |
| `issue-pickup-system`     | issue-pickup     |
| `feature-planning-system` | feature-planning |
| `feature-dev-system`      | feature-dev      |
| `feature-validate-system` | feature-validate |
| `pr-create-system`        | pr-create        |
| `pr-merge-system`         | pr-merge         |

### Dialog Prompts (`skills/templates/dialog-prompts/`)

| Template                       | Usage                       |
| ------------------------------ | --------------------------- |
| `complexity-assessment-dialog` | Complexity confirmation UI  |
| `approval-prompt-dialog`       | Stage approval confirmation |

## SDK Usage

### TemplateRegistry

```typescript
import { TemplateRegistry, PromptRenderer } from "@nightgauge/sdk";

const registry = new TemplateRegistry();
await registry.loadTemplates("skills/templates");

const template = registry.getTemplate("feature-planning-system");
if (template) {
  const renderer = new PromptRenderer();
  const rendered = renderer.render(template, {
    issueNumber: 42,
    issueTitle: "Add dark mode support",
  });
}
```

### Version pinning

```typescript
// Get latest version (default)
const latest = registry.getTemplate("feature-planning-system");

// Pin to a specific version
const v1 = registry.getTemplate("feature-planning-system", "1.0.0");
```

### Singleton instances

The SDK exports shared singletons for convenience:

```typescript
import { defaultRegistry, defaultRenderer } from "@nightgauge/sdk";
```

## VSCode Extension Usage

`PromptTemplateService` wraps the registry and renderer with extension-specific helpers:

```typescript
import { PromptTemplateService } from "./services/PromptTemplateService";

// Initialized during extension activation (see bootstrap/services.ts)
const service = new PromptTemplateService(context.extensionPath);
await service.initialize();

// Render a pipeline stage system prompt
const systemPrompt = service.renderSystemPrompt("feature-planning", {
  issueNumber: 42,
  issueTitle: "Add dark mode",
});

// Render a dialog prompt
const dialog = service.renderComplexityAssessment({
  issueNumber: 42,
  assessedComplexity: "M",
  rationale: "Single service modification with unit tests",
});
```

The service initializes in the background during extension activation. If the
`skills/templates` directory is missing, it silently treats the registry as
empty (fallback to hardcoded prompts is the caller's responsibility).

## Adding a New Template

1. Create a `.handlebars` file in `skills/templates/system-prompts/` or
   `skills/templates/dialog-prompts/`
2. Add the required YAML frontmatter block
3. Write your Handlebars template content
4. Add an entry to the catalog in `skills/templates/README.md`
5. Add typed helper method to `PromptTemplateService` if it's a common dialog

### Example: new dialog template

```handlebars
---
name: "conflict-resolution-dialog"
version: "1.0.0"
layer: "extension"
description: "Shown when a merge conflict is detected during pipeline execution"
params:
  - name: "issueNumber"
    type: "number"
    description: "Issue being processed"
    required: true
  - name: "conflictingFiles"
    type: "string"
    description: "Comma-separated list of conflicting files"
    required: true
---

Merge conflict detected while processing issue #{{issueNumber}}. Conflicting files:
{{conflictingFiles}}

Resolve the conflicts manually, then resume the pipeline.
```

## Caching Behavior

- `TemplateRegistry.loadTemplates()` scans the directory and parses all
  `.handlebars` files. Results are stored in an in-memory map.
- `PromptRenderer` compiles each template on first use and caches the compiled
  function keyed by `name@version`. Subsequent calls skip compilation.
- Templates can be re-loaded by calling `loadTemplates()` again (union merge).
- The compile cache can be cleared with `renderer.clearCache()` (primarily for tests).

## Error Handling

| Scenario                         | Behavior                                           |
| -------------------------------- | -------------------------------------------------- |
| Template file has invalid YAML   | Warning logged; file skipped; registry continues   |
| Template missing required field  | Warning logged; file skipped; registry continues   |
| Template not found in registry   | `getTemplate()` returns `null`; caller falls back  |
| Handlebars compile error         | `render()` throws `Error` with descriptive message |
| `skills/templates` dir not found | `loadTemplates({ ignore: true })` returns 0        |

## Backward Compatibility

This system is **additive**. Existing code that reads `SKILL.md` files directly
or uses hardcoded prompt strings continues to work unchanged. Template usage
is opt-in at the call site.

Phase 2 work (separate issues) will gradually migrate hardcoded prompts to
templates as they are updated.

## Platform API Integration (Phase 2)

The template design supports a future Platform API serving endpoint:

```
GET /api/templates/{name}/{version}    → PromptTemplate JSON
GET /api/templates/catalog             → List of all available templates
```

The `TemplateRegistry` will gain a `loadFromPlatform(apiUrl, authToken)` method
that fetches templates with ETag-based caching. This is out of scope for Phase 1.

## Testing

### SDK tests

```bash
cd packages/nightgauge-sdk
node_modules/.bin/vitest run src/templates
```

### Extension tests

```bash
npx -w nightgauge-vscode vitest run tests/services/PromptTemplateService.test.ts
```
