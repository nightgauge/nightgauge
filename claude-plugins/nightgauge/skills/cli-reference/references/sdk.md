# `@nightgauge/sdk` reference

TypeScript orchestration library used by the VSCode extension and CI. Source of
truth for the import surface: `packages/nightgauge-sdk/src/index.ts`.

## CLI entry points (CI/CD)

```bash
npx @nightgauge/sdk run 42                      # full pipeline
npx @nightgauge/sdk run 42 --auto-approve --format json
npx @nightgauge/sdk stage feature-planning 42   # single stage
npx @nightgauge/sdk status 42
```

## Headline programmatic exports

```ts
import {
  PipelineOrchestrator,
  DEFAULT_STAGES,
  APPROVAL_STAGES,
  type PipelineConfig,
  type PipelineResult,
  type StageResult,
  type ExecutorSelection,
} from "@nightgauge/sdk";

import {
  StageExecutor,
  buildStagePrompt,
  loadStageSkill,
  StageTimeoutError,
} from "@nightgauge/sdk";

import {
  ContextManager,
  ContextNotFoundError,
  ContextValidationError,
  atomicWriteJSON,
} from "@nightgauge/sdk";

import { RunStateManager, uuidV7, type ResumeDetection } from "@nightgauge/sdk";

import { EventBus } from "@nightgauge/sdk"; // pipeline event stream
import { TemplateRegistry, defaultRegistry } from "@nightgauge/sdk";
```

## Source layout (`src/`)

`orchestrator/` (PipelineOrchestrator, StageExecutor), `context/` (ContextManager,
RunStateManager), `events/` (EventBus, phase inference), `stages/`, `templates/`,
`cli/` (adapters + commands), `schemas/`, `eval/`, `tracking/`, `analysis/`,
`audit/`, `preflight/`, `query/`, `tools/`, `types/`, `utils/`.

## Gotchas

- All pipeline execution in the VSCode extension flows through
  `HeadlessOrchestrator.runPipeline()` — don't call the SDK orchestrator from the
  extension directly.
- Stages hand off via JSON context files (`.nightgauge/pipeline/*.json`), not
  conversation history. Use `ContextManager` + `atomicWriteJSON`, never inline
  context into prompts.
- The package is published to the **nightgauge GitHub Packages** registry (private).
  Export a PAT with `read:packages` (`NODE_AUTH_TOKEN`) before `npm install`.
