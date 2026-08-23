package extract

import (
	"strconv"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/graph"
)

// DepGraphSource is the slice of *depgraph.Graph this extractor needs.
//
// It is an interface so a test can supply a built graph without a forge round
// trip — depgraph.BuildGraph needs a live GitHub client, which would make the
// golden test a network test.
type DepGraphSource interface {
	Adjacency() map[string][]string
}

// Issues converts a built *depgraph.Graph into graph nodes and edges.
//
// ADR-005 Decision 3 says to WRAP internal/depgraph, not reimplement it, and
// this is the wrap: depgraph does the fetching, the body parsing and the
// cross-repo alias resolution, and this function only translates its output
// into the graph's vocabulary.
//
// The translation is NOT field-for-field, and assuming it is would silently
// fabricate provenance:
//
//   - depgraph's Edge.Source is the MECHANISM ("graphql", "body_text",
//     "structured_section", "depends_on") — not a location. It becomes an
//     attribute, and graph.Provenance.Source gets the issue reference the edge
//     was found on.
//   - depgraph's Edge.SourceLine is the line TEXT as a string, not a number. It
//     becomes an attribute too; graph.Provenance.SourceLine stays 0 because a
//     real line number is not available, and a fabricated one would defeat the
//     field's whole purpose.
//   - depgraph's Edge.Resolvable is already the dangling signal. A non-
//     resolvable edge is still EMITTED, so graph.Dangling() reports it.
func IssuesFromGraph(dg *depgraph.Graph) Result {
	res := Result{Extractor: "issues", Graph: graph.New()}
	if dg == nil {
		res.Skipped = "no dependency graph supplied"
		return res
	}

	for key, node := range dg.Nodes {
		if node == nil {
			continue
		}
		prov := graph.Provenance{Extractor: res.Extractor, Source: key}
		kind := graph.NodeIssue
		if node.EpicNumber == 0 && hasLabel(node.Labels, "type:epic") {
			kind = graph.NodeEpic
		}
		id, err := addNode(res.Graph, kind, key, prov, node.Title, graph.Attrs{
			"repo":         node.Repo,
			"number":       strconv.Itoa(node.Number),
			"state":        node.State,
			"board_status": node.BoardStatus,
			"size":         node.Size,
			"priority":     node.Priority,
		})
		if err != nil {
			res.Skipped = err.Error()
			return res
		}
		if node.EpicNumber > 0 {
			epic := node.Repo + "#" + strconv.Itoa(node.EpicNumber)
			if err := addEdge(res.Graph, graph.EdgePartOf, id,
				graph.MakeNodeID(graph.NodeEpic, epic), prov, nil); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
	}

	for _, e := range dg.Edges {
		prov := graph.Provenance{Extractor: res.Extractor, Source: e.From.String()}
		attrs := graph.Attrs{
			"mechanism":  e.Source,
			"type":       e.Type,
			"resolvable": strconv.FormatBool(e.Resolvable),
		}
		if e.SourceLine != "" {
			// The prose line that created this edge — #126's lesson. Kept as
			// text because that is what depgraph has; it is NOT a line number.
			attrs["line_text"] = e.SourceLine
		}
		if err := addEdge(res.Graph, graph.EdgeBlocks,
			graph.MakeNodeID(graph.NodeIssue, e.From.String()),
			graph.MakeNodeID(graph.NodeIssue, e.To.String()), prov, attrs); err != nil {
			res.Skipped = err.Error()
			return res
		}
	}
	return res
}

// Issues satisfies All's DepGraphSource parameter.
func Issues(dg DepGraphSource) Result {
	if g, ok := dg.(*depgraph.Graph); ok {
		return IssuesFromGraph(g)
	}
	return Result{Extractor: "issues", Graph: graph.New(), Skipped: "unsupported dependency graph source"}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
