package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sampleGraph(t *testing.T) *Graph {
	t.Helper()
	g := New()
	issue := mustNode(t, NodeIssue, "nightgauge#828")
	cap := mustNode(t, NodeCapability, "workspace-graph")
	if err := g.AddNode(issue.WithLabel("graph substrate")); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode(cap.WithAttrs(Attrs{"status": "planned"})); err != nil {
		t.Fatal(err)
	}
	e, err := NewEdge(EdgeImplements, issue.ID, cap.ID, Provenance{
		Extractor: "capabilities", Source: "capabilities.yaml", SourceLine: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AddEdge(e); err != nil {
		t.Fatal(err)
	}
	// A deliberately unresolvable edge, so the round trip has to preserve one.
	d, _ := NewEdge(EdgeBlocks, issue.ID, MakeNodeID(NodeIssue, "nightgauge#499"), prov())
	if err := g.AddEdge(d); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	want := sampleGraph(t)

	meta, err := Save(root, want)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if meta.SchemaVersion != SchemaVersion {
		t.Errorf("meta schema version = %d, want %d", meta.SchemaVersion, SchemaVersion)
	}
	if meta.Nodes != 2 || meta.Edges != 2 || meta.Dangling != 1 {
		t.Errorf("meta = %+v, want 2 nodes / 2 edges / 1 dangling", meta)
	}
	if strings.Join(meta.Extractors, ",") != "capabilities,issues" {
		t.Errorf("meta.Extractors = %v, want [capabilities issues]", meta.Extractors)
	}

	got, gotMeta, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || gotMeta == nil {
		t.Fatal("Load returned no graph for a store that was just written")
	}
	if got.NodeCount() != want.NodeCount() || got.EdgeCount() != want.EdgeCount() {
		t.Fatalf("round trip lost data: %d/%d nodes, %d/%d edges",
			got.NodeCount(), want.NodeCount(), got.EdgeCount(), want.EdgeCount())
	}
	// Provenance must survive the round trip — without it the reloaded graph
	// cannot answer 1.5's "real defect or extractor bug?" question at all.
	for _, n := range got.Nodes() {
		if err := n.Prov.Validate(); err != nil {
			t.Errorf("node %s lost provenance across the round trip: %v", n.ID, err)
		}
	}
	for _, e := range got.Edges() {
		if err := e.Prov.Validate(); err != nil {
			t.Errorf("edge %s %s->%s lost provenance: %v", e.Kind, e.From, e.To, err)
		}
	}
	if len(got.Dangling()) != 1 {
		t.Errorf("dangling count after reload = %d, want 1", len(got.Dangling()))
	}
	// Attrs and labels survive too.
	c, ok := got.Node(MakeNodeID(NodeCapability, "workspace-graph"))
	if !ok || c.Attrs["status"] != "planned" {
		t.Errorf("attrs lost across round trip: %+v", c)
	}
	i, _ := got.Node(MakeNodeID(NodeIssue, "nightgauge#828"))
	if i.Label != "graph substrate" {
		t.Errorf("label lost across round trip: %q", i.Label)
	}
	// Exact provenance, not merely "some".
	edges := got.Edges()
	var impl *Edge
	for k := range edges {
		if edges[k].Kind == EdgeImplements {
			impl = &edges[k]
		}
	}
	if impl == nil {
		t.Fatal("implements edge missing after reload")
	}
	if impl.Prov.Extractor != "capabilities" || impl.Prov.Source != "capabilities.yaml" || impl.Prov.SourceLine != 4 {
		t.Errorf("provenance mangled: %+v", impl.Prov)
	}
}

func TestLoadMissingStoreIsNoGraph(t *testing.T) {
	g, meta, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a missing store must not be an error: %v", err)
	}
	if g != nil || meta != nil {
		t.Error("a missing store must read as no graph")
	}
}

// TestSchemaVersionMismatchIsNoGraph is the AC: a mismatch means "no graph",
// triggering a rebuild — never a migration and never a partial read.
func TestSchemaVersionMismatchIsNoGraph(t *testing.T) {
	root := t.TempDir()
	if _, err := Save(root, sampleGraph(t)); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(root, StoreDir, metaFile)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["schema_version"] = SchemaVersion + 1
	bumped, _ := json.Marshal(m)
	if err := os.WriteFile(metaPath, bumped, 0o644); err != nil {
		t.Fatal(err)
	}

	g, meta, err := Load(root)
	if err != nil {
		t.Fatalf("a stale schema must not error: %v", err)
	}
	if g != nil || meta != nil {
		t.Error("a schema mismatch must read as no graph so the caller rebuilds")
	}
}

func TestCorruptMetaIsNoGraphButCorruptJSONLIsAnError(t *testing.T) {
	root := t.TempDir()
	if _, err := Save(root, sampleGraph(t)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, StoreDir)

	// A corrupt JSONL line must NOT be silently swallowed as "no graph":
	// rebuilding past corruption makes a store that is wrong every time look
	// like one that is merely cold.
	nodes := filepath.Join(dir, nodesFile)
	raw, _ := os.ReadFile(nodes)
	if err := os.WriteFile(nodes, append(raw, []byte("{not json\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root); err == nil {
		t.Error("a corrupt JSONL line must return an error, not a silent rebuild")
	}

	// Corrupt meta, by contrast, IS "no graph" — nothing has been read yet.
	if err := os.WriteFile(filepath.Join(dir, metaFile), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, meta, err := Load(root)
	if err != nil || g != nil || meta != nil {
		t.Errorf("corrupt meta must read as no graph, got g=%v meta=%v err=%v", g != nil, meta != nil, err)
	}
}

// TestLoadRejectsUnprovenancedRecords closes the hand-edited-store hole. A
// record on disk with no provenance must not enter the graph, or the mandatory
// guard stops at the process boundary.
func TestLoadRejectsUnprovenancedRecords(t *testing.T) {
	root := t.TempDir()
	if _, err := Save(root, sampleGraph(t)); err != nil {
		t.Fatal(err)
	}
	nodes := filepath.Join(root, StoreDir, nodesFile)
	raw, _ := os.ReadFile(nodes)
	line := `{"id":"issue:smuggled","kind":"issue"}` + "\n"
	if err := os.WriteFile(nodes, append(raw, []byte(line)...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root)
	if err == nil {
		t.Fatal("a node with no provenance was loaded from disk; it must be rejected")
	}
	if !strings.Contains(err.Error(), "provenance") {
		t.Errorf("error should name provenance, got %v", err)
	}
}

// TestConcurrentSavesDoNotRace is the #786 proof. Every writer must succeed and
// the store must end readable.
//
// MUTATION: change uniqueTempPath to return path + ".tmp" and this test fails —
// the O_EXCL create loses with EEXIST, or the loser's rename hits ENOENT. That
// is the fixed-temp-path race #777 fixed in TelemetryStore.writeIndex, and it
// is invisible to any test with a single writer.
func TestConcurrentSavesDoNotRace(t *testing.T) {
	root := t.TempDir()
	g := sampleGraph(t)

	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Save(root, g); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Save failed: %v", err)
	}

	got, meta, err := Load(root)
	if err != nil {
		t.Fatalf("store unreadable after concurrent writes: %v", err)
	}
	if got == nil || meta == nil {
		t.Fatal("store empty after concurrent writes")
	}
	if got.NodeCount() != g.NodeCount() || got.EdgeCount() != g.EdgeCount() {
		t.Errorf("store corrupted by concurrent writes: %d nodes / %d edges", got.NodeCount(), got.EdgeCount())
	}
}

// TestNoTempFilesSurvive guards the other half of the temp-path contract: each
// writer cleans up after itself, so a store directory never accretes debris.
func TestNoTempFilesSurvive(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if _, err := Save(root, sampleGraph(t)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, StoreDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(names) != 3 {
		t.Errorf("store contains %v, want exactly nodes.jsonl, edges.jsonl, meta.json", names)
	}
}

func TestUniqueTempPathIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := uniqueTempPath("/tmp/x/nodes.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		if seen[p] {
			t.Fatalf("uniqueTempPath repeated %q — the #786 race is reachable", p)
		}
		seen[p] = true
		if filepath.Dir(p) != "/tmp/x" {
			t.Fatalf("temp path %q left the destination directory; rename would stop being atomic", p)
		}
	}
}

func TestSaveEmptyGraph(t *testing.T) {
	root := t.TempDir()
	meta, err := Save(root, New())
	if err != nil {
		t.Fatalf("saving an empty graph must work: %v", err)
	}
	if meta.Nodes != 0 || meta.Edges != 0 {
		t.Errorf("meta = %+v, want zeros", meta)
	}
	g, m, err := Load(root)
	if err != nil {
		t.Fatalf("Load after empty save: %v", err)
	}
	if g == nil || m == nil {
		t.Fatal("an empty graph is still a graph; Load must not report it missing")
	}
	if g.NodeCount() != 0 {
		t.Errorf("node count = %d, want 0", g.NodeCount())
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	if _, err := Save("", New()); err == nil {
		t.Error("Save accepted an empty root")
	}
	if _, err := Save(t.TempDir(), nil); err == nil {
		t.Error("Save accepted a nil graph")
	}
	if _, _, err := Load(""); err == nil {
		t.Error("Load accepted an empty root")
	}
}

func TestStorePathIsTheADRPath(t *testing.T) {
	if StoreDir != ".nightgauge/graph" {
		t.Errorf("StoreDir = %q, want .nightgauge/graph per ADR-005 Decision 3", StoreDir)
	}
	root := t.TempDir()
	if _, err := Save(root, sampleGraph(t)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{nodesFile, edgesFile, metaFile} {
		if _, err := os.Stat(filepath.Join(root, StoreDir, f)); err != nil {
			t.Errorf("expected %s in the store: %v", f, err)
		}
	}
}

// TestNodesFileIsOneJSONObjectPerLine keeps the format greppable — the reason
// JSONL was chosen over one big JSON document.
func TestNodesFileIsOneJSONObjectPerLine(t *testing.T) {
	root := t.TempDir()
	if _, err := Save(root, sampleGraph(t)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, StoreDir, nodesFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("nodes.jsonl has %d lines, want 2", len(lines))
	}
	for i, l := range lines {
		var n Node
		if err := json.Unmarshal([]byte(l), &n); err != nil {
			t.Errorf("line %d is not a standalone JSON object: %v", i+1, err)
		}
	}
}
