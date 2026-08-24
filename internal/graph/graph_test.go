package graph

import (
	"fmt"
	"strings"
	"testing"
)

func prov() Provenance {
	return Provenance{Extractor: "issues", Source: "docs/x.md", SourceLine: 12}
}

func mustNode(t *testing.T, kind NodeKind, ref string) Node {
	t.Helper()
	n, err := NewNode(kind, ref, prov())
	if err != nil {
		t.Fatalf("NewNode(%s, %s): %v", kind, ref, err)
	}
	return n
}

// TestNodeKindsAreExactlyTheADRSet pins ADR-005 Decision 3's closed set. A kind
// added in code without an ADR amendment fails here, and so does one silently
// dropped — the list is compared whole, not merely for membership.
func TestNodeKindsAreExactlyTheADRSet(t *testing.T) {
	want := []NodeKind{
		"adr", "capability", "contract", "doc", "epic", "file", "issue",
		"model", "outcome", "package", "provider", "repo", "run", "stage",
		"symbol",
	}
	got := NodeKinds()
	if len(got) != len(want) {
		t.Fatalf("node kind count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node kind[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEdgeKindsAreExactlyTheADRSet(t *testing.T) {
	want := []EdgeKind{
		"blocks", "consumes", "discovered-in", "documents", "implements",
		"owns-file", "part-of", "produces", "runs-on", "serves-band",
		"supersedes", "tests", "violates",
	}
	got := EdgeKinds()
	if len(got) != len(want) {
		t.Fatalf("edge kind count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("edge kind[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestConstructorsRejectIncompleteProvenance is the AC's test guard. NewNode
// and NewEdge take provenance as a required ARGUMENT, so forgetting it does not
// compile; this covers the remaining case of supplying an empty one.
func TestConstructorsRejectIncompleteProvenance(t *testing.T) {
	bad := map[string]Provenance{
		"zero value":      {},
		"no extractor":    {Source: "docs/x.md"},
		"blank extractor": {Extractor: "   ", Source: "docs/x.md"},
		"no source":       {Extractor: "issues"},
		"blank source":    {Extractor: "issues", Source: "  "},
		"negative line":   {Extractor: "issues", Source: "docs/x.md", SourceLine: -1},
	}
	for name, p := range bad {
		t.Run("node/"+name, func(t *testing.T) {
			if _, err := NewNode(NodeIssue, "nightgauge#1", p); err == nil {
				t.Fatal("NewNode accepted incomplete provenance; it must not")
			}
		})
		t.Run("edge/"+name, func(t *testing.T) {
			if _, err := NewEdge(EdgeBlocks, "issue:a", "issue:b", p); err == nil {
				t.Fatal("NewEdge accepted incomplete provenance; it must not")
			}
		})
	}
	// SourceLine 0 is legitimate: a whole-file or API source has no line.
	if _, err := NewNode(NodeRepo, "nightgauge/nightgauge", Provenance{Extractor: "repos", Source: "config.yaml"}); err != nil {
		t.Fatalf("SourceLine 0 must be allowed for line-less sources: %v", err)
	}
}

// TestAddRejectsUnprovenancedLiterals closes the in-package hole the
// constructors cannot: a Node or Edge built by struct literal. This is the
// guard that catches a future extractor written inside this package.
func TestAddRejectsUnprovenancedLiterals(t *testing.T) {
	g := New()
	if err := g.AddNode(Node{ID: "issue:1", Kind: NodeIssue}); err == nil {
		t.Error("AddNode accepted a node with no provenance; it must not")
	}
	if err := g.AddEdge(Edge{Kind: EdgeBlocks, From: "issue:1", To: "issue:2"}); err == nil {
		t.Error("AddEdge accepted an edge with no provenance; it must not")
	}
	if g.NodeCount() != 0 || g.EdgeCount() != 0 {
		t.Errorf("rejected elements were still stored: %d nodes, %d edges", g.NodeCount(), g.EdgeCount())
	}
}

func TestUnknownKindsAreRejected(t *testing.T) {
	if _, err := NewNode(NodeKind("widget"), "x", prov()); err == nil {
		t.Error("NewNode accepted an unknown node kind")
	}
	if _, err := NewEdge(EdgeKind("relates-to"), "a", "b", prov()); err == nil {
		t.Error("NewEdge accepted an unknown edge kind")
	}
	g := New()
	if err := g.AddNode(Node{ID: "widget:1", Kind: "widget", Prov: prov()}); err == nil {
		t.Error("AddNode accepted an unknown node kind")
	}
}

func TestMakeNodeIDNamespacesByKind(t *testing.T) {
	issue := MakeNodeID(NodeIssue, "499")
	file := MakeNodeID(NodeFile, "499")
	if issue == file {
		t.Fatal("issue #499 and a file named 499 must not share an ID")
	}
	if issue.Kind() != NodeIssue {
		t.Errorf("NodeID.Kind() = %q, want %q", issue.Kind(), NodeIssue)
	}
	if NodeID("no-colon").Kind() != "" {
		t.Error("a non-canonical ID must report an empty kind, not a guess")
	}
}

// TestDanglingEdgesAreReportedNotDropped is ADR-005 Decision 1's third
// corollary. The edge count must be unchanged: a dangling edge is a finding,
// and a finding you deleted is not a finding.
func TestDanglingEdgesAreReportedNotDropped(t *testing.T) {
	g := New()
	a := mustNode(t, NodeIssue, "nightgauge#1")
	if err := g.AddNode(a); err != nil {
		t.Fatal(err)
	}
	e, err := NewEdge(EdgeBlocks, a.ID, MakeNodeID(NodeIssue, "nightgauge#404"), prov())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(e); err != nil {
		t.Fatal(err)
	}

	if got := g.EdgeCount(); got != 1 {
		t.Fatalf("edge count = %d, want 1 — the dangling edge must be RETAINED", got)
	}
	d := g.Dangling()
	if len(d) != 1 {
		t.Fatalf("Dangling() returned %d, want 1", len(d))
	}
	if !d[0].FromExists || d[0].ToExists {
		t.Errorf("wrong end reported: from=%v to=%v, want from=true to=false", d[0].FromExists, d[0].ToExists)
	}
	// The finding must name its extractor and source line: the #126 lesson.
	s := d[0].String()
	for _, want := range []string{"issues", "docs/x.md", "12", "issue:nightgauge#404"} {
		if !strings.Contains(s, want) {
			t.Errorf("dangling report %q omits %q", s, want)
		}
	}
	// And a fully resolved edge must not be reported.
	b := mustNode(t, NodeIssue, "nightgauge#2")
	if err := g.AddNode(b); err != nil {
		t.Fatal(err)
	}
	e2, _ := NewEdge(EdgeBlocks, a.ID, b.ID, prov())
	if err := g.AddEdge(e2); err != nil {
		t.Fatal(err)
	}
	if len(g.Dangling()) != 1 {
		t.Errorf("a resolvable edge was reported as dangling: %v", g.Dangling())
	}
}

func TestAddNodeIsFirstWriterWins(t *testing.T) {
	g := New()
	first, _ := NewNode(NodeFile, "a.go", Provenance{Extractor: "files", Source: "git"})
	second, _ := NewNode(NodeFile, "a.go", Provenance{Extractor: "contracts", Source: "api/generated"})
	if err := g.AddNode(first); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode(second); err != nil {
		t.Fatalf("re-adding a node two extractors both see must not error: %v", err)
	}
	if g.NodeCount() != 1 {
		t.Fatalf("node count = %d, want 1", g.NodeCount())
	}
	got, _ := g.Node(first.ID)
	if got.Prov.Extractor != "files" {
		t.Errorf("provenance = %q, want the DISCOVERING extractor %q", got.Prov.Extractor, "files")
	}
}

func TestEdgesFromAndTo(t *testing.T) {
	g := New()
	a := mustNode(t, NodeIssue, "1")
	b := mustNode(t, NodeIssue, "2")
	for _, n := range []Node{a, b} {
		if err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := NewEdge(EdgeBlocks, a.ID, b.ID, prov())
	if err := g.AddEdge(e); err != nil {
		t.Fatal(err)
	}
	if len(g.EdgesFrom(a.ID)) != 1 || len(g.EdgesTo(b.ID)) != 1 {
		t.Error("edge not indexed in both directions")
	}
	if len(g.EdgesFrom(b.ID)) != 0 {
		t.Error("edges are directed; EdgesFrom must not match the target")
	}
}

func TestMergeKeepsReceiversProvenance(t *testing.T) {
	g := New()
	other := New()
	mine, _ := NewNode(NodeFile, "a.go", Provenance{Extractor: "files", Source: "git"})
	theirs, _ := NewNode(NodeFile, "a.go", Provenance{Extractor: "docs", Source: "README.md"})
	extra := mustNode(t, NodeDoc, "README.md")
	if err := g.AddNode(mine); err != nil {
		t.Fatal(err)
	}
	if err := other.AddNode(theirs); err != nil {
		t.Fatal(err)
	}
	if err := other.AddNode(extra); err != nil {
		t.Fatal(err)
	}
	if err := g.Merge(other); err != nil {
		t.Fatal(err)
	}
	if g.NodeCount() != 2 {
		t.Fatalf("node count after merge = %d, want 2", g.NodeCount())
	}
	got, _ := g.Node(mine.ID)
	if got.Prov.Extractor != "files" {
		t.Errorf("merge overwrote the receiver's provenance: %q", got.Prov.Extractor)
	}
	if err := g.Merge(nil); err != nil {
		t.Errorf("merging nil must be a no-op, got %v", err)
	}
}

func TestNodesAreOrderedByID(t *testing.T) {
	g := New()
	for _, ref := range []string{"c", "a", "b"} {
		if err := g.AddNode(mustNode(t, NodeFile, ref)); err != nil {
			t.Fatal(err)
		}
	}
	got := g.Nodes()
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatalf("Nodes() is not sorted: %v", got)
		}
	}
}

func TestProvenanceString(t *testing.T) {
	withLine := Provenance{Extractor: "issues", Source: "a.md", SourceLine: 7}
	if got, want := withLine.String(), "issues@a.md:7"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	noLine := Provenance{Extractor: "issues", Source: "a.md"}
	if got, want := noLine.String(), "issues@a.md"; got != want {
		t.Errorf("String() = %q, want %q — a line-less source must not print :0", got, want)
	}
}

func TestEmptyEndpointsRejected(t *testing.T) {
	for _, tc := range []struct{ from, to NodeID }{{"", "b"}, {"a", ""}, {" ", "b"}} {
		if _, err := NewEdge(EdgeBlocks, tc.from, tc.to, prov()); err == nil {
			t.Errorf("NewEdge(%q,%q) was accepted; both endpoints are required", tc.from, tc.to)
		}
	}
	if _, err := NewNode(NodeIssue, "  ", prov()); err == nil {
		t.Error("NewNode accepted a blank ref")
	}
}

func TestWithHelpersDoNotMutate(t *testing.T) {
	n := mustNode(t, NodeIssue, "1")
	labelled := n.WithLabel("issue one").WithAttrs(Attrs{"state": "open"})
	if n.Label != "" || n.Attrs != nil {
		t.Error("WithLabel/WithAttrs mutated the receiver; they must return copies")
	}
	if labelled.Label != "issue one" || labelled.Attrs["state"] != "open" {
		t.Errorf("copy did not carry the values: %+v", labelled)
	}
	e, _ := NewEdge(EdgeBlocks, "a", "b", prov())
	e2 := e.WithAttrs(Attrs{"via": "prose"})
	if e.Attrs != nil {
		t.Error("Edge.WithAttrs mutated the receiver")
	}
	if e2.Attrs["via"] != "prose" {
		t.Errorf("edge copy did not carry attrs: %+v", e2)
	}
}

func BenchmarkDangling(b *testing.B) {
	g := New()
	for i := 0; i < 1000; i++ {
		n, _ := NewNode(NodeFile, fmt.Sprintf("f%d.go", i), prov())
		_ = g.AddNode(n)
		e, _ := NewEdge(EdgeTests, n.ID, MakeNodeID(NodeFile, fmt.Sprintf("f%d_test.go", i)), prov())
		_ = g.AddEdge(e)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.Dangling()
	}
}
