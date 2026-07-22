# BM25 Parameters Reference

<!-- graduated-from: .nightgauge/knowledge/features/feature-6-graduated/decisions.md#ADR-001 -->

This document contains the authoritative BM25 scoring parameter decisions.

Use BM25 with k1=1.5 and b=0.75 as defaults. These parameters were validated
against the ADR knowledge corpus and produce well-calibrated scores for
technical English documents.

## Parameters

- **k1** (default 1.5): Controls term frequency saturation.
- **b** (default 0.75): Controls document length normalization.
