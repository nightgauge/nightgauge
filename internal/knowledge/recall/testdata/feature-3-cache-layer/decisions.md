---
tags: [cache, jsonl, performance, incremental]
status: stable
---

# Decisions: #3 — Index Cache Layer

## ADR-001: JSONL Cache With Mtime Invalidation

**Status**: Stable
**Context**: The BM25 index must be rebuilt from disk on every cold start. For a knowledge base with 100+ ADRs this takes up to 2 seconds per query. A cache layer reduces warm query latency to under 100ms.
**Decision**: Store the BM25 index as JSONL at .nightgauge/knowledge/.recall-cache/index.jsonl. The first line is a JSON header with version, built_at, k1, and b parameters. Subsequent lines are CacheEntry records with path, mtime, tokens, and term_freq.
**Consequences**: Cache invalidation is mtime-based per file. Any file modification triggers a full rebuild. Version mismatch (e.g. after parameter change) also triggers rebuild. This is simple and correct at the cost of whole-cache rebuilds on any change.
