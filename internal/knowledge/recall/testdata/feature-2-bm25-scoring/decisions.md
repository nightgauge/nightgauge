---
tags: [bm25, scoring, search, retrieval]
status: stable
---

# Decisions: #2 — BM25 Scoring Engine

## ADR-001: BM25 Over Vector Search

**Status**: Stable
**Context**: The recall API needs to score and rank knowledge documents by relevance. Two approaches were evaluated: BM25 term-frequency scoring and embedding-based vector search.
**Decision**: Use BM25 with k1=1.5 and b=0.75 defaults. BM25 is deterministic, requires no external API, and runs in-process with sub-millisecond latency per query. Vector search would require an embedding model or external API call, adding latency and non-determinism.
**Consequences**: BM25 requires stemming to handle morphological variants. Tag and path boosts compensate for vocabulary gaps. The scoring algorithm is testable with fixed inputs and expected outputs.
