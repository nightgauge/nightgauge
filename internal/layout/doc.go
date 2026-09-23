// Package layout resolves where Nightgauge keeps its data on disk: the roots
// (STATE, CACHE, RUNTIME and the per-clone and per-checkout roots) and the
// per-clone class directories under them. ADR-024 (docs/decisions/
// 024-data-and-state-layout.md) is the authority for every location.
//
// It is a leaf package that imports only the standard library, so any package
// in the module can resolve a location here without an import cycle, the same
// reason internal/configpath is a leaf. A caller never hand-joins a location
// this package owns.
package layout
