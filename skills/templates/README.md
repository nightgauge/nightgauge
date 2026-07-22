# Prompt Template Catalog

Reusable Handlebars prompt templates for all Nightgauge pipeline stages.

See [docs/PROMPT_TEMPLATES.md](../../docs/PROMPT_TEMPLATES.md) for full
documentation on the template system, format specification, and usage guide.

## Directory Structure

```
skills/templates/
├── README.md                              # This file
├── system-prompts/                        # System role messages for pipeline stages
│   ├── issue-pickup.handlebars
│   ├── feature-planning.handlebars
│   ├── feature-dev.handlebars
│   ├── feature-validate.handlebars
│   ├── pr-create.handlebars
│   └── pr-merge.handlebars
└── dialog-prompts/                        # User-facing dialog and confirmation prompts
    ├── complexity-assessment.handlebars
    └── approval-prompt.handlebars
```

## Template Catalog

### System Prompts

| Template name             | Version | Stage            | Description                            |
| ------------------------- | ------- | ---------------- | -------------------------------------- |
| `issue-pickup-system`     | 1.0.0   | issue-pickup     | System role for issue pickup agent     |
| `feature-planning-system` | 1.0.0   | feature-planning | System role for feature planning agent |
| `feature-dev-system`      | 1.0.0   | feature-dev      | System role for feature dev agent      |
| `feature-validate-system` | 1.0.0   | feature-validate | System role for validation agent       |
| `pr-create-system`        | 1.0.0   | pr-create        | System role for PR creation agent      |
| `pr-merge-system`         | 1.0.0   | pr-merge         | System role for PR merge agent         |

### Dialog Prompts

| Template name                  | Version | Layer     | Description                    |
| ------------------------------ | ------- | --------- | ------------------------------ |
| `complexity-assessment-dialog` | 1.0.0   | extension | Complexity confirmation dialog |
| `approval-prompt-dialog`       | 1.0.0   | extension | Generic stage approval dialog  |

## Quick Start

### Loading templates via SDK

```typescript
import { TemplateRegistry, PromptRenderer } from "@nightgauge/sdk";

const registry = new TemplateRegistry();
await registry.loadTemplates("skills/templates");

const renderer = new PromptRenderer();
const template = registry.getTemplate("feature-planning-system");
if (template) {
  const rendered = renderer.render(template, {
    issueNumber: 42,
    issueTitle: "Add dark mode",
    docScopePath: "docs/ARCHITECTURE.md",
  });
}
```

### Using PromptTemplateService (VSCode extension)

```typescript
import { PromptTemplateService } from "./services/PromptTemplateService";

const service = new PromptTemplateService(context.extensionPath);
await service.initialize();

const prompt = service.renderSystemPrompt("feature-planning", {
  issueNumber: 42,
});
```

## Adding a New Template

1. Create a `.handlebars` file in the appropriate subdirectory
2. Add YAML frontmatter with required fields: `name`, `version`, `layer`, `description`
3. Use `{{variableName}}` for variable substitution
4. Add the template to the catalog table in this README
5. Add parameter documentation to the `params` list in frontmatter

### Required frontmatter fields

```yaml
---
name: "my-template-name" # Unique name used for registry lookup
version: "1.0.0" # Semantic version
layer: "skill" # One of: skill | sdk | extension | platform
description: "Short description of what this template does"
params: # Optional — documents available variables
  - name: "myVar"
    type: "string"
    description: "What myVar is for"
    required: true
---
```

## Template Syntax

Templates use [Handlebars](https://handlebarsjs.com/) syntax:

| Syntax                                | Description                 |
| ------------------------------------- | --------------------------- |
| `{{variable}}`                        | Variable substitution       |
| `{{{variable}}}`                      | Variable (no HTML escaping) |
| `{{#if condition}}...{{/if}}`         | Conditional block           |
| `{{#each items}}...{{/each}}`         | Array iteration             |
| `{{#unless condition}}...{{/unless}}` | Inverse conditional         |
| `{{variable.property}}`               | Nested property access      |
