// Package graph is the Workspace Knowledge Graph substrate: the node and edge
// types, the provenance every one of them must carry, and the in-memory graph
// the extractors build.
//
// Contract: ADR-005 (Workspace Knowledge Graph and the Continuous Alignment
// Contract), Decisions 1, 3 and 10.
//
// Three properties of this package are load-bearing and must not be relaxed:
//
//  1. The graph is DERIVED, never authored. Nothing here reads a hand-written
//     node list. Every node and edge is produced by an extractor from a source
//     that already exists in the tree or the forge (Decision 1).
//
//  2. Provenance is MANDATORY, not advisory. NewNode and NewEdge take it as a
//     required argument, so an extractor cannot forget it and reach a compile.
//     internal/depgraph.Edge.SourceLine already carries this for body-derived
//     issue edges, and #126 recorded the reason: "a dependency edge created by
//     a line of prose is otherwise indistinguishable from a real GitHub
//     relation, which made a stalled fleet undiagnosable." Phase 1.5's
//     falsifier has to classify each sampled finding as a real defect or an
//     extractor bug — a question that is unanswerable without knowing which
//     extractor produced the edge and from which line.
//
//  3. Dangling edges are FINDINGS, never silent drops. An edge whose endpoint
//     does not resolve stays in the graph and is reported by Dangling(). A
//     store that quietly discards them cannot detect the reference rot this
//     program exists to gate on.
//
// No LLM participates in producing a node, an edge, or a verdict (Decision 10).
package graph

import (
	"fmt"
	"sort"
	"strings"
)

// NodeKind enumerates the node kinds fixed by ADR-005 Decision 3.
type NodeKind string

const (
	NodeCapability NodeKind = "capability"
	NodeRepo       NodeKind = "repo"
	NodePackage    NodeKind = "package"
	NodeFile       NodeKind = "file"
	NodeSymbol     NodeKind = "symbol"
	NodeContract   NodeKind = "contract"
	NodeDoc        NodeKind = "doc"
	NodeADR        NodeKind = "adr"
	NodeIssue      NodeKind = "issue"
	NodeEpic       NodeKind = "epic"
	NodeRun        NodeKind = "run"
	NodeOutcome    NodeKind = "outcome"
	NodeProvider   NodeKind = "provider"
	NodeModel      NodeKind = "model"
	NodeStage      NodeKind = "stage"
)

// nodeKinds is the closed set. ADR-005 Decision 3 fixes it; extending it is an
// ADR amendment, not a code change, so an unknown kind is rejected rather than
// tolerated. A graph that accepts arbitrary kinds cannot be queried by kind
// with any confidence.
var nodeKinds = map[NodeKind]bool{
	NodeCapability: true, NodeRepo: true, NodePackage: true, NodeFile: true,
	NodeSymbol: true, NodeContract: true, NodeDoc: true, NodeADR: true,
	NodeIssue: true, NodeEpic: true, NodeRun: true, NodeOutcome: true,
	NodeProvider: true, NodeModel: true, NodeStage: true,
}

// Valid reports whether k is one of ADR-005 Decision 3's node kinds.
func (k NodeKind) Valid() bool { return nodeKinds[k] }

// NodeKinds returns every valid node kind in a stable order.
func NodeKinds() []NodeKind { return sortedKeys(nodeKinds) }

// EdgeKind enumerates the edge kinds fixed by ADR-005 Decision 3.
type EdgeKind string

const (
	EdgePartOf       EdgeKind = "part-of"
	EdgeOwnsFile     EdgeKind = "owns-file"
	EdgeImplements   EdgeKind = "implements"
	EdgeDocuments    EdgeKind = "documents"
	EdgeTests        EdgeKind = "tests"
	EdgeConsumes     EdgeKind = "consumes"
	EdgeProduces     EdgeKind = "produces"
	EdgeBlocks       EdgeKind = "blocks"
	EdgeSupersedes   EdgeKind = "supersedes"
	EdgeDiscoveredIn EdgeKind = "discovered-in"
	EdgeViolates     EdgeKind = "violates"
	EdgeServesBand   EdgeKind = "serves-band"
	EdgeRunsOn       EdgeKind = "runs-on"
)

var edgeKinds = map[EdgeKind]bool{
	EdgePartOf: true, EdgeOwnsFile: true, EdgeImplements: true,
	EdgeDocuments: true, EdgeTests: true, EdgeConsumes: true,
	EdgeProduces: true, EdgeBlocks: true, EdgeSupersedes: true,
	EdgeDiscoveredIn: true, EdgeViolates: true, EdgeServesBand: true,
	EdgeRunsOn: true,
}

// Valid reports whether k is one of ADR-005 Decision 3's edge kinds.
func (k EdgeKind) Valid() bool { return edgeKinds[k] }

// EdgeKinds returns every valid edge kind in a stable order.
func EdgeKinds() []EdgeKind { return sortedKeys(edgeKinds) }

// Provenance records who produced a node or edge and from where.
//
// Every field except SourceLine is required. SourceLine is 0 for a source that
// has no meaningful line — a whole file, an API response, a directory listing —
// and 1-based otherwise; it is never negative.
type Provenance struct {
	// Extractor names the extractor that produced this element, e.g.
	// "issues", "capabilities", "docs". Required.
	Extractor string `json:"extractor"`
	// Source is where the element came from: a workspace-relative path, a
	// forge reference, or a registry file. Required.
	Source string `json:"source"`
	// SourceLine is the 1-based line within Source, or 0 when the source has
	// no line granularity. Never negative.
	SourceLine int `json:"source_line,omitempty"`
}

// Validate reports why the provenance is unusable, or nil when it is complete.
func (p Provenance) Validate() error {
	if strings.TrimSpace(p.Extractor) == "" {
		return fmt.Errorf("provenance: extractor is required")
	}
	if strings.TrimSpace(p.Source) == "" {
		return fmt.Errorf("provenance: source is required")
	}
	if p.SourceLine < 0 {
		return fmt.Errorf("provenance: source_line must not be negative, got %d", p.SourceLine)
	}
	return nil
}

// String renders provenance for `graph explain` and for error messages.
func (p Provenance) String() string {
	if p.SourceLine > 0 {
		return fmt.Sprintf("%s@%s:%d", p.Extractor, p.Source, p.SourceLine)
	}
	return fmt.Sprintf("%s@%s", p.Extractor, p.Source)
}

// Node is one vertex of the derived graph.
//
// Construct with NewNode. The zero value is not a valid node, and AddNode
// rejects it — the fields are exported so the JSONL store stays a plain
// encoding rather than a bespoke one, not as an invitation to build a node by
// literal.
type Node struct {
	ID    NodeID     `json:"id"`
	Kind  NodeKind   `json:"kind"`
	Label string     `json:"label,omitempty"`
	Attrs Attrs      `json:"attrs,omitempty"`
	Prov  Provenance `json:"provenance"`
}

// Edge is one directed relation between two nodes.
//
// Construct with NewEdge. From and To are node IDs that need not exist yet:
// an extractor emits what its source says, and an endpoint that never resolves
// becomes a Dangling finding rather than a dropped edge.
type Edge struct {
	Kind  EdgeKind   `json:"kind"`
	From  NodeID     `json:"from"`
	To    NodeID     `json:"to"`
	Attrs Attrs      `json:"attrs,omitempty"`
	Prov  Provenance `json:"provenance"`
}

// Attrs is extractor-specific detail. It is deliberately untyped: the schema
// that matters is the kind sets and the provenance, and forcing every
// extractor's incidental fields through this package would make each new
// extractor a change here.
type Attrs map[string]string

// NodeID is a node's canonical identity, "<kind>:<ref>" — see MakeNodeID.
type NodeID string

// MakeNodeID builds the canonical ID for a node. Namespacing by kind is what
// keeps a file named "499" and issue #499 from colliding, and it makes an ID
// self-describing in a `graph explain` trace.
func MakeNodeID(kind NodeKind, ref string) NodeID {
	return NodeID(string(kind) + ":" + ref)
}

// Kind returns the node kind encoded in the ID, or "" when the ID is not in
// canonical form.
func (id NodeID) Kind() NodeKind {
	k, _, ok := strings.Cut(string(id), ":")
	if !ok {
		return ""
	}
	return NodeKind(k)
}

// NewNode constructs a node, requiring provenance as an argument so that
// omitting it does not compile.
func NewNode(kind NodeKind, ref string, prov Provenance) (Node, error) {
	if !kind.Valid() {
		return Node{}, fmt.Errorf("node: unknown kind %q", kind)
	}
	if strings.TrimSpace(ref) == "" {
		return Node{}, fmt.Errorf("node: ref is required for kind %q", kind)
	}
	if err := prov.Validate(); err != nil {
		return Node{}, fmt.Errorf("node %s:%s: %w", kind, ref, err)
	}
	return Node{ID: MakeNodeID(kind, ref), Kind: kind, Prov: prov}, nil
}

// WithLabel returns a copy of n carrying a human-readable label.
func (n Node) WithLabel(label string) Node { n.Label = label; return n }

// WithAttrs returns a copy of n carrying the given attributes.
func (n Node) WithAttrs(a Attrs) Node { n.Attrs = a; return n }

// NewEdge constructs an edge, requiring provenance as an argument so that
// omitting it does not compile.
func NewEdge(kind EdgeKind, from, to NodeID, prov Provenance) (Edge, error) {
	if !kind.Valid() {
		return Edge{}, fmt.Errorf("edge: unknown kind %q", kind)
	}
	if strings.TrimSpace(string(from)) == "" || strings.TrimSpace(string(to)) == "" {
		return Edge{}, fmt.Errorf("edge %s: both endpoints are required (from=%q to=%q)", kind, from, to)
	}
	if err := prov.Validate(); err != nil {
		return Edge{}, fmt.Errorf("edge %s %s->%s: %w", kind, from, to, err)
	}
	return Edge{Kind: kind, From: from, To: to, Prov: prov}, nil
}

// WithAttrs returns a copy of e carrying the given attributes.
func (e Edge) WithAttrs(a Attrs) Edge { e.Attrs = a; return e }

// Graph is the in-memory derived graph. It is not safe for concurrent writes;
// extractors build their own and are merged by the caller.
type Graph struct {
	nodes map[NodeID]Node
	edges []Edge
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{nodes: make(map[NodeID]Node)}
}

// AddNode inserts a node, re-validating it. The constructor already enforces
// this, so a failure here means a Node literal was built inside the package —
// which is exactly the case worth failing on rather than trusting.
//
// Adding the same ID twice is not an error: two extractors legitimately see the
// same file or issue. The first insertion wins, so provenance names the
// extractor that discovered the node rather than the last one to mention it.
func (g *Graph) AddNode(n Node) error {
	if !n.Kind.Valid() {
		return fmt.Errorf("add node %s: unknown kind %q", n.ID, n.Kind)
	}
	if strings.TrimSpace(string(n.ID)) == "" {
		return fmt.Errorf("add node: id is required")
	}
	if err := n.Prov.Validate(); err != nil {
		return fmt.Errorf("add node %s: %w", n.ID, err)
	}
	if _, exists := g.nodes[n.ID]; exists {
		return nil
	}
	g.nodes[n.ID] = n
	return nil
}

// AddEdge appends an edge, re-validating it. Endpoints are NOT required to
// exist: see Dangling.
func (g *Graph) AddEdge(e Edge) error {
	if !e.Kind.Valid() {
		return fmt.Errorf("add edge: unknown kind %q", e.Kind)
	}
	if strings.TrimSpace(string(e.From)) == "" || strings.TrimSpace(string(e.To)) == "" {
		return fmt.Errorf("add edge %s: both endpoints are required", e.Kind)
	}
	if err := e.Prov.Validate(); err != nil {
		return fmt.Errorf("add edge %s %s->%s: %w", e.Kind, e.From, e.To, err)
	}
	g.edges = append(g.edges, e)
	return nil
}

// Node returns the node with the given ID.
func (g *Graph) Node(id NodeID) (Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// Nodes returns every node, ordered by ID so callers and golden files can rely
// on it.
func (g *Graph) Nodes() []Node {
	out := make([]Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Edges returns every edge in insertion order, dangling ones included.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, len(g.edges))
	copy(out, g.edges)
	return out
}

// NodeCount and EdgeCount report sizes without copying.
func (g *Graph) NodeCount() int { return len(g.nodes) }
func (g *Graph) EdgeCount() int { return len(g.edges) }

// DanglingEdge is an edge with at least one unresolvable endpoint, together
// with which end failed. ADR-005 Decision 1: these are reported and counted,
// never silently dropped.
type DanglingEdge struct {
	Edge       Edge `json:"edge"`
	FromExists bool `json:"from_exists"`
	ToExists   bool `json:"to_exists"`
}

// String renders a dangling edge as a finding, provenance first, because the
// first question about any finding is which extractor asserted it.
func (d DanglingEdge) String() string {
	var missing []string
	if !d.FromExists {
		missing = append(missing, "from="+string(d.Edge.From))
	}
	if !d.ToExists {
		missing = append(missing, "to="+string(d.Edge.To))
	}
	return fmt.Sprintf("%s: %s %s->%s unresolved (%s)",
		d.Edge.Prov, d.Edge.Kind, d.Edge.From, d.Edge.To, strings.Join(missing, " "))
}

// Dangling returns every edge with an endpoint that is not a node in this
// graph, in edge order. An empty result means every edge resolves.
func (g *Graph) Dangling() []DanglingEdge {
	var out []DanglingEdge
	for _, e := range g.edges {
		_, from := g.nodes[e.From]
		_, to := g.nodes[e.To]
		if from && to {
			continue
		}
		out = append(out, DanglingEdge{Edge: e, FromExists: from, ToExists: to})
	}
	return out
}

// EdgesFrom returns every edge originating at id, dangling ones included.
func (g *Graph) EdgesFrom(id NodeID) []Edge {
	var out []Edge
	for _, e := range g.edges {
		if e.From == id {
			out = append(out, e)
		}
	}
	return out
}

// EdgesTo returns every edge terminating at id, dangling ones included.
func (g *Graph) EdgesTo(id NodeID) []Edge {
	var out []Edge
	for _, e := range g.edges {
		if e.To == id {
			out = append(out, e)
		}
	}
	return out
}

// Merge folds other into g. Node conflicts keep g's copy — and therefore g's
// provenance — matching AddNode's first-writer-wins rule.
func (g *Graph) Merge(other *Graph) error {
	if other == nil {
		return nil
	}
	for _, n := range other.Nodes() {
		if err := g.AddNode(n); err != nil {
			return fmt.Errorf("merge node: %w", err)
		}
	}
	for _, e := range other.edges {
		if err := g.AddEdge(e); err != nil {
			return fmt.Errorf("merge edge: %w", err)
		}
	}
	return nil
}

func sortedKeys[K ~string](m map[K]bool) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
