---
paths:
  - "packages/nightgauge-vscode/**"
---

# VSCode Extension Rules

## Shipping Extension/Binary Changes While Autonomous Is Live

Installing a new VSIX is inert until the window reloads, but a reload **kills
every in-flight pipeline run in that window**. When landing extension or Go
binary changes while autonomous mode is running (operator rule, 2026-07-18):

1. **Stop the autonomous scheduler first** so no new issues are dispatched.
2. **Let in-flight runs drain to completion** (watch `.nightgauge/pipeline/`
   runtime files; the run is over when its runtime-\<issue\>.json is removed).
3. Then rebuild + install (`packages/nightgauge-vscode/scripts/dev-install.sh`)
   and reload the window.

Never reload over a live run — the swept run records no terminal outcome and
recycles as an unlabeled failure (see the #252 zombie-run retro).

## Pipeline Orchestration (Go-Driven, TypeScript Executes)

**All pipeline orchestration decisions** (stage sequencing, retry, backtrack,
model escalation, budget enforcement, RALPH loop) flow through the Go scheduler
(`internal/orchestrator/scheduler.go`). TypeScript `SkillRunner` executes stages
only and reports results back via IPC.

This replaces the previous architecture where
`HeadlessOrchestrator.runPipeline()` was a single 8,874-line god object. See
Issue #1901 for the decomposition.

**Architecture layers:**

- **Go Scheduler** (`scheduler.runPipeline()`) — stage loop, retry engine,
  budget enforcer, RALPH controller, backtrack evaluation
- **StageRunner interface** — abstracts execution mode:
  - `ExecutionManagerRunner` — auto/CLI mode (Go spawns Claude directly)
  - `IpcStageRunner` — VSCode IPC mode (Go → TS → Claude → TS → Go)
- **PipelineBridge** (`PipelineBridge.ts`) — receives `pipeline.runStage`
  events, delegates to `SkillRunner`, sends `pipeline.stageResult` back to Go
- **SkillRunner** (`SkillRunner.ts`) — thin executor: spawns Claude CLI, streams
  output, reports exit code and tokens

**When adding pipeline features:**

- Add orchestration logic to Go (`internal/orchestrator/`)
- Add execution logic to `SkillRunner.ts` (only what the CLI subprocess needs)
- Pre-checks (epic detection, validation) go in Go scheduler
- Reuse the `nightgauge` Go binary (e.g., `epic check-completion`) rather
  than reimplementing their logic differently

**Legacy path:** `HeadlessOrchestrator.ts` retains the old `runPipeline()` for
backward compatibility during transition. New features should NOT be added
there.

## StatusBarManager Usage

- `showRunning(stage: PipelineStage)` — For pipeline stage display

## State File Compatibility

TypeScript Zod schemas in `PipelineStateService` MUST match the output format of
the Go binary `project move-status` command. When adding runtime validation,
always check that binary output passes the Zod schema.

- `null` values from scripts are rejected by `.optional()` — use `undefined`
- Always sanitize script output before Zod validation as defense-in-depth

## Success Calculation

When counting completed stages, include both `completedStages` and
`skippedStages`:

```typescript
completedStages.length + skippedStages.length === STAGE_ORDER.length;
```

Skipped stages (from routing) must be counted, or the pipeline falsely reports
failure.

## GitHub API — Use Server-Side Filtering

`ProjectBoardService` uses GitHub's `ProjectV2.items(query: ...)` parameter for
server-side status filtering. **Never revert to fetching all items and filtering
locally.** The fetch-all pattern caused a progressive-rendering race condition
where partial cache data was served with a fresh timestamp, producing empty
views on refresh.

- `items(query: "status:Ready is:open")` → 15 items, 1 page, no race condition
- `items(first: 100)` (all items) → 677 items, 7 pages, race condition

If adding new project board features that need different field filters, check
the API first:
`gh api graphql -f query='{ __type(name: "ProjectV2") { fields { name args { name } } } }'`

See also: [standards/code-standards.md](../../standards/code-standards.md) §
External API & Dependency Usage

## Epic Grouping in Board Views

When epic grouping is enabled, `groupIssuesByEpic()` groups sub-issues under
their parent epic header. Each sub-issue appears in **exactly one tab** matching
its actual GitHub project board status — no duplication across tabs.

**Key implementation details:**

- `groupIssuesByEpic()` in `EpicGroupTreeItem.ts` groups the status-filtered
  issues by `epicRef`; `allItems` is only used to resolve epic metadata (title)
- Epic detection: `isEpic = subIssues.length > 0` (set in Go `board.go`)
- Parent lookup: derived from epic's `subIssues` array (no `parentIssue` field
  exists in the GitHub GraphQL API)
- Blocking display: `ReadyIssueTreeItem` checks `blockedBy` for open blockers

**When setting up epics with sequential phases:**

1. Link sub-issues via `addSubIssue` GraphQL mutation
2. Add all sub-issues to the project board
3. Set up `blockedBy` relationships for sequential ordering
4. Set **all sub-issues to "Ready"** — the pipeline uses `blockedBy` to enforce
   ordering, NOT board status. Blocked items show lock icons in the Ready tab.
   Only use "Backlog" for issues that are genuinely not ready for work yet.

## References

- [docs/ARCHITECTURE.md](../../docs/ARCHITECTURE.md) — Full extension
  architecture
- [docs/INTERACTIVE_MODE.md](../../docs/INTERACTIVE_MODE.md) — Headless vs
  interactive execution modes
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — Extension contribution guidelines
