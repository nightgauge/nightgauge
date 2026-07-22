---
paths:
  - "packages/nightgauge-sdk/**"
---

# SDK Rules

## Export Patterns

All public API surface is exported from `src/index.ts`. When adding new modules:

1. Create the module in the appropriate directory (`pipeline/`, `context/`,
   `tracking/`)
2. Export public types and classes from `src/index.ts`
3. **No backwards compatibility (pre-customer).** Remove or rename exports
   freely — there are no external consumers to protect. When you rename or
   delete an export, update every call site and the `src/index.ts` barrel in
   the same change; never leave a deprecated alias, re-export shim, or
   compatibility wrapper behind. Consolidate overlapping APIs to one and delete
   the rest. This mirrors the workspace-wide mandate in the root `CLAUDE.md` /
   `AGENTS.md` ("Delete old paths; never add deprecation shims").

## Pipeline Orchestration

The SDK's `PipelineOrchestrator` follows the same single-path principle as the
VSCode extension: all pipeline execution flows through `orchestrator.run()`.
Never create a second execution path.

## Routing Architecture

Pipeline routing uses pure functions for testability:

- `changeAnalyzer.ts` — Pure functions for complexity analysis
- `routingDecision.ts` — Pure functions for routing decisions
- Runtime stage skipping handled by the orchestrator

Keep routing logic pure (no side effects) and test with unit tests.

## Test Requirements

- Test behavior, not implementation details
- Service coverage target: >80%
- Use Vitest as the test framework
- Mock external dependencies (GitHub API, file system) at boundaries
- Tests live alongside source in `src/__tests__/`

## References

- [packages/nightgauge-sdk/README.md](../../packages/nightgauge-sdk/README.md) — SDK
  documentation and API reference
- [docs/ARCHITECTURE.md](../../docs/ARCHITECTURE.md) — SDK architecture context
- [docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) — Context
  file schemas used by SDK
