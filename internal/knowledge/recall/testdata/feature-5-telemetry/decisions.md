---
tags: [telemetry, events, jsonl, observability]
status: stable
---

# Decisions: #5 — Telemetry Event Schema

## ADR-001: JSONL Event Stream for Knowledge Operations

**Status**: Stable
**Context**: The knowledge base needs an audit trail of operations for graduation scoring and stale ADR detection. Several storage options were evaluated: relational DB, structured logs, and append-only JSONL.
**Decision**: Append one JSONL line per knowledge operation to .nightgauge/pipeline/history/knowledge-events.jsonl. Each line is a telemetry.Event struct. The file is never truncated — it grows indefinitely and is read by graduation candidates and stale ADR scanners.
**Consequences**: Simple append-only write. No transactions required. File can grow large over time; consumers must stream-read rather than load into memory. The JSONL format is forward-compatible: new fields are ignored by old readers.
