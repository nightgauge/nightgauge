# Architecture Decision Records (ADRs)

This directory contains strategic decision documents for Nightgauge.

## Purpose

Unlike the operational documentation in `docs/` (ARCHITECTURE.md,
GIT_WORKFLOW.md, etc.), these documents record **strategic decisions** and the
analysis that led to them.

## Format

Each decision document follows the pattern:

- `NNN-title-of-decision.md` (e.g., `001-jira-vs-github-projects.md`)
- Date, author, status (Discovery, Decided, Implemented)
- Options analyzed with pros/cons
- Recommendation and rationale
- Implementation tracking (issues, milestones)

## Active Decisions

| ID  | Title                                     | Status    | Date       | Decision                                                                                                                                                                                                                                                                                                            |
| --- | ----------------------------------------- | --------- | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 001 | Jira vs GitHub Projects Integration       | Discovery | 2026-02-04 | TBD - Leaning toward Option 1 (GitHub Projects maturity)                                                                                                                                                                                                                                                            |
| 002 | API Technology Selection                  | Decided   | 2026-02-21 | tRPC v11 with Hono — zero-codegen type safety for TypeScript client/server                                                                                                                                                                                                                                          |
| 004 | Adaptive Stall Recovery                   | Decided   | 2026-04-25 | Rewind to feature-planning once on first stall-kill (Issue #3005)                                                                                                                                                                                                                                                   |
| 007 | Slash-Command → Skill Invocation Contract | Decided   | 2026-05-09 | Canonical enforcement banner that instructs agents to invoke the Skill tool before reading command file content (Issue #3343, Epic #3342)                                                                                                                                                                           |
| 008 | Skills target the `nightgauge forge` CLI  | Decided   | 2026-05-11 | Skills migrate from direct `gh` calls to `nightgauge forge` (15 skills in Wave 4); deprecation linter gates regressions; carve-outs route through `forge graphql` (Issue #3363, Epic #3349)                                                                                                                         |
| 012 | Performance Modes → Policy Envelopes      | Proposed  | 2026-07-10 | Modes become floor/ceiling envelopes (router runs in every mode; frontier ceiling = Fable-on-L/XL only) + a Custom per-stage selector over existing `stage_models` config                                                                                                                                           |
| 013 | Run Lifecycle Trace Schema                | Decided   | 2026-07-17 | Per-run append-only decision trace at `.nightgauge/pipeline/trace/<run_id>.jsonl`; envelope + closed kind taxonomy with structured rationale; `(run_id, producer, seq)` idempotent upload; dedicated platform trace-events table (Issue #179, Epic #178)                                                            |
| 014 | Live Trace Transport (SSE run stream)     | Decided   | 2026-07-17 | Reuse the existing per-run SSE stream `GET /v1/pipelines/{runId}/stream`; push each ADR-013 trace envelope as a `trace.event` PipelineProgressEvent, idempotent by (run_id,producer,seq) (#234, Epic #226)                                                                                                          |
| 015 | DecisionRequests (Action Center)          | Decided   | 2026-07-19 | First-class `DecisionRequest` primitive: closed `kind`/verb-registry schema, local-first `.nightgauge/attention/` store with a single authoritative writer, a `list`/`subscribe`/`resolve` Surface contract (VSCode/dashboard/future Discord), 8 producers, steer-as-context, trace+history audit (#323, Epic #322) |

## Reference

Decision documents are referenced from implementation issues but are **not**
included in CLAUDE.md or AGENTS.md. They are historical records, not operational
guidance.

For operational guidance, see:

- [docs/ARCHITECTURE.md](../ARCHITECTURE.md) - How the system works
- [docs/GIT_WORKFLOW.md](../GIT_WORKFLOW.md) - How to use git
- [CLAUDE.md](../../CLAUDE.md) - Project instructions for AI agents
