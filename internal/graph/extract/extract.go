// Package extract holds the Workspace Knowledge Graph's wave-A extractors:
// the ones that need no new parsing because their source is already a
// structured artifact this repository maintains.
//
// Contract: ADR-005 Decision 3's extractor table, and Decision 1 — the graph is
// DERIVED. Every function here reads something that already exists (the
// capability registry, the model registry, the ADR directory, git's own file
// list, the dependency graph) and emits nodes and edges from it. Nothing here
// accepts a hand-authored node list, and nothing here asks a model anything
// (Decision 10).
//
// Two rules bind every extractor in this package:
//
//  1. **Provenance must be ACCURATE, not merely present.** internal/graph makes
//     it impossible to construct a node without provenance, so "it compiles"
//     proves nothing. The Source must be the file the fact actually came from
//     and the SourceLine must point at the line that carries it, because Phase
//     1.5 classifies findings as real-defect vs extractor-bug by reading
//     exactly those two fields.
//
//  2. **Emit the edge even when the endpoint does not resolve.** A capability
//     naming a doc that was deleted, an ADR superseding one that never existed
//     — those are the findings. graph.Dangling() reports them. An extractor
//     that checks first and stays silent has destroyed the signal.
package extract

import (
	"fmt"

	"github.com/nightgauge/nightgauge/internal/graph"
)

// Result is one extractor's output plus what it could not do. Errors are
// collected rather than returned fatally: an extractor whose source file is
// absent should contribute nothing and say so, not abort the whole build.
type Result struct {
	Graph *graph.Graph
	// Extractor is the name that appears in every Provenance this Result
	// carries.
	Extractor string
	// Skipped explains why an extractor produced nothing, when that is a
	// legitimate state (no capabilities.yaml in this repo, no ADR directory).
	// A skipped extractor is not an error, but it must not be silent either —
	// "the graph has no ADRs" and "the ADR extractor never ran" are different
	// facts and the difference matters to 1.5.
	Skipped string
}

// All runs every wave-A extractor against root and merges the results into one
// graph.
//
// Merge order is fixed and alphabetical by extractor name, not arbitrary,
// because graph.AddNode is first-writer-wins: the node's provenance names
// whichever extractor got there first. A stable order makes that attribution
// reproducible rather than dependent on map iteration.
func All(root string, dg DepGraphSource) (*graph.Graph, []Result, error) {
	if root == "" {
		return nil, nil, fmt.Errorf("extract: root is required")
	}

	results := []Result{
		Capabilities(root),
		Docs(root),
		Files(root),
		Models(root),
	}
	results = append(results, ADRs(root))
	if dg != nil {
		results = append(results, Issues(dg))
	}

	merged := graph.New()
	for _, r := range results {
		if r.Graph == nil {
			continue
		}
		if err := merged.Merge(r.Graph); err != nil {
			return nil, results, fmt.Errorf("extract: merge %s: %w", r.Extractor, err)
		}
	}
	return merged, results, nil
}

// addNode is the shared "construct and insert, or record why not" helper. An
// extractor that cannot build a node from a line of its own source has found a
// malformed source, which is a finding about the tree — so the error names the
// provenance rather than swallowing it.
func addNode(g *graph.Graph, kind graph.NodeKind, ref string, prov graph.Provenance, label string, attrs graph.Attrs) (graph.NodeID, error) {
	n, err := graph.NewNode(kind, ref, prov)
	if err != nil {
		return "", fmt.Errorf("%s: %w", prov, err)
	}
	if label != "" {
		n = n.WithLabel(label)
	}
	if len(attrs) > 0 {
		n = n.WithAttrs(attrs)
	}
	if err := g.AddNode(n); err != nil {
		return "", fmt.Errorf("%s: %w", prov, err)
	}
	return n.ID, nil
}

// addEdge mirrors addNode. Note it never checks whether To exists: see the
// package doc's second rule.
func addEdge(g *graph.Graph, kind graph.EdgeKind, from, to graph.NodeID, prov graph.Provenance, attrs graph.Attrs) error {
	e, err := graph.NewEdge(kind, from, to, prov)
	if err != nil {
		return fmt.Errorf("%s: %w", prov, err)
	}
	if len(attrs) > 0 {
		e = e.WithAttrs(attrs)
	}
	return g.AddEdge(e)
}
