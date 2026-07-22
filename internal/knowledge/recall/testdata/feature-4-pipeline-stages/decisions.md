---
tags: [pipeline, stage, orchestration, sequential]
status: stable
---

# Decisions: #4 — Pipeline Stage Architecture

## ADR-001: Six-Stage Sequential Pipeline

**Status**: Stable
**Context**: The SDLC automation needs a structured sequence of operations from issue pickup through PR merge. Several pipeline topologies were considered: monolithic single-stage, parallel stages, and sequential micro-stages.
**Decision**: Use a six-stage sequential pipeline: issue-pickup, feature-planning, feature-dev, feature-validate, pr-create, pr-merge. Each stage runs as an isolated agent with JSON context handoff files. No shared in-memory state between stages.
**Consequences**: Stage isolation means each stage must re-load context from disk. This enables retry-from-failed-stage without re-running completed work. The JSON handoff format is versioned to support schema evolution.
