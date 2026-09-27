# Workspace Knowledge Graph and the Continuous Alignment Contract

**Date:** 2026-08-23
**Author:** nightgauge
**Status:** Decided
**Issue:** #1475 (written after the fact; implemented by #828 and the capability registry)

---

## Executive Summary

The code in `internal/graph`, `internal/graph/extract`, `internal/capabilities`,
`cmd/nightgauge/capabilities.go` and `capabilities.yaml` cites "ADR-005" at the
precision of individual decisions. This record states those decisions so the
citations resolve. It describes the contract the shipped code implements; it
does not propose new behaviour.

The workspace knowledge graph is a derived index of what exists in a workspace
(capabilities, repositories, packages, files, docs, decisions, issues, runs,
models) and how those things relate. Its purpose is **continuous alignment**:
a mechanical check that the code, docs, issues and the capability registry
still describe the same system, so drift is a finding rather than a surprise.

Decision numbers are kept stable because code cites them. Decisions 4–8 and 11
concern workspace process rather than the code in this repository and are not
restated here; no code in this repository cites them.

## Decisions

### Decision 1 — The graph is derived, never authored

Every node and edge is produced by an extractor from a source that already
exists: the tree, git's file list, the ADR directory, the model registry, the
dependency graph, or the forge. Nothing reads a hand-written node list.

- Provenance is mandatory. `graph.NewNode` and `graph.NewEdge` take it as a
  required argument, so an extractor cannot omit which source and line produced
  an element.
- **Dangling edges are reported and counted, never dropped.** An edge whose
  endpoint does not resolve stays in the graph and is returned by
  `Dangling()`. Reference rot is exactly what alignment exists to detect, so a
  store that discards unresolved edges would hide the defect it is meant to
  surface.
- Because the graph is derived, discarding it costs a rebuild and never data.
  The store therefore has a `SchemaVersion` and no migration path: a version
  mismatch rebuilds.

### Decision 2 — `capabilities.yaml` is the one hand-authored layer

The capability registry (`capabilities.yaml`, loaded by `internal/capabilities`)
is the only authored input. Every other node kind attaches to a capability
declared there. `nightgauge capabilities validate` is its gate: it refuses an
unknown status, disposition or surface, and a doc path or `owns` glob that does
not match the tree. `docs/CAPABILITIES_MAP.md` is generated from it.

A wrong edge from any other extractor is a parsing bug; a wrong edge from this
extractor means the registry itself is wrong, which is why it is kept small and
validated.

### Decision 3 — Closed kind sets and the store location

The node and edge kinds are closed sets. An unknown kind is rejected, not
tolerated; extending either set is an amendment to this record, not a code
change.

- **Node kinds:** `capability`, `repo`, `package`, `file`, `symbol`,
  `contract`, `doc`, `adr`, `issue`, `epic`, `run`, `outcome`, `provider`,
  `model`, `stage`.
- **Edge kinds:** `part-of`, `owns-file`, `implements`, `documents`, `tests`,
  `consumes`, `produces`, `blocks`, `supersedes`, `discovered-in`, `violates`,
  `serves-band`, `runs-on`.
- **Store:** `StoreDir` is `.nightgauge/graph`, workspace-relative and ignored
  by git.
- Extractors reuse existing parsers rather than reimplementing them: issue
  edges wrap `internal/depgraph`; the doc extractor reuses the knowledge index's
  shape (H1 as title, wiki-links as backlinks) over the whole tree. Each
  extractor lists files for itself so each can be verified independently.

### Decision 9 — A band is a node, not an attribute

Provider and model neutrality is a dimension of the graph, not a rename. A
model's routing band is emitted as its own node (on the `stage` axis, with
`kind: band`) joined by `serves-band`, so a band no model serves is a visible
orphan instead of a string nobody queries.

### Decision 10 — No LLM produces a node, an edge or a verdict

Extraction and every alignment verdict are deterministic. A model may read the
graph; it never writes to it and never decides whether a finding is real. This
keeps the graph reproducible and its findings falsifiable.

### Decision 12 — Every capability has exactly one home

Each capability declares one disposition: `core` (the Apache-2.0 local core),
`hosted` (the closed-source platform) or `both` (a core half and a hosted half
across a documented contract, the platform client being the canonical case).
There is no unassigned or multi-valued home.

## Consequences

- A citation of "ADR-005 Decision N" in the packages above resolves here.
- Adding a node or edge kind requires amending Decision 3 in the same pull
  request as the code.
- Findings from the graph (dangling edges, orphaned bands, registry paths that
  match nothing) are defects to fix in the tree or the registry, never entries
  to suppress.
