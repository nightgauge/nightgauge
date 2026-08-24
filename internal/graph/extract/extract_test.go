package extract

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/graph"
)

// fixtureRepo builds a tiny tracked repo so the extractors that read `git
// ls-files` have something real to read. gittest is used rather than a bare
// exec so ambient git config cannot decide whether this suite passes (#542).
func fixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-m", "fixture")
	return dir
}

// golden renders a graph as stable, diffable text: every node and every edge
// with its full provenance. Provenance is IN the golden output on purpose —
// an extractor that keeps emitting the right edges from the wrong line is
// exactly the bug Phase 1.5 must not have to guess at.
func golden(g *graph.Graph) string {
	var b strings.Builder
	for _, n := range g.Nodes() {
		b.WriteString("NODE " + string(n.ID) + " [" + n.Prov.String() + "]")
		if n.Label != "" {
			b.WriteString(" label=" + n.Label)
		}
		if len(n.Attrs) > 0 {
			keys := make([]string, 0, len(n.Attrs))
			for k := range n.Attrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if n.Attrs[k] != "" {
					b.WriteString(" " + k + "=" + n.Attrs[k])
				}
			}
		}
		b.WriteString("\n")
	}
	edges := g.Edges()
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Kind < edges[j].Kind
	})
	for _, e := range edges {
		b.WriteString("EDGE " + string(e.Kind) + " " + string(e.From) + " -> " + string(e.To) +
			" [" + e.Prov.String() + "]\n")
	}
	return b.String()
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with UPDATE_GOLDEN=1): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

const fixtureCapabilities = `schema_version: 1
capabilities:
  - id: alpha
    title: Alpha capability
    status: ga
    disposition: core
    surfaces: [cli]
    docs:
      - docs/ALPHA.md
      - docs/DELETED.md
    owns:
      - internal/alpha/**
    depends_on:
      - beta
  - id: beta
    title: Beta capability
    status: ga
    disposition: core
    surfaces: [cli]
    docs:
      - docs/BETA.md
    owns:
      - internal/nothing-here/**
    depends_on: []
`

func TestCapabilitiesGolden(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"capabilities.yaml":   fixtureCapabilities,
		"docs/ALPHA.md":       "# Alpha\n",
		"docs/BETA.md":        "# Beta\n",
		"internal/alpha/a.go": "package alpha\n",
		"internal/alpha/b.go": "package alpha\n",
	})
	res := Capabilities(dir)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	checkGolden(t, "capabilities", golden(res.Graph))

	// The two findings this fixture deliberately plants must both survive as
	// dangling edges rather than being dropped or silently repaired.
	g := res.Graph
	for _, f := range []string{"internal/alpha/a.go", "internal/alpha/b.go"} {
		if err := g.AddNode(mustFileNode(t, f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.AddNode(mustDocNode(t, "docs/ALPHA.md")); err != nil {
		t.Fatal(err)
	}
	if err := g.AddNode(mustDocNode(t, "docs/BETA.md")); err != nil {
		t.Fatal(err)
	}
	var deletedDoc, emptyGlob bool
	for _, d := range g.Dangling() {
		if strings.Contains(string(d.Edge.From), "DELETED") {
			deletedDoc = true
		}
		if d.Edge.Attrs["matched"] == "0" {
			emptyGlob = true
		}
	}
	if !deletedDoc {
		t.Error("a capability documented by a deleted file must produce a dangling edge")
	}
	if !emptyGlob {
		t.Error("an owns glob matching nothing must produce a dangling edge, not silence")
	}
}

// TestCapabilityGlobsResolveToRealFiles is the regression test for the
// extractor bug this issue found in itself: an earlier cut pointed owns-file
// edges at the GLOB STRING, which can never match a file node, and produced
// 100+ dangling findings that were extractor artifacts rather than defects.
//
// MUTATION: point the edge at graph.MakeNodeID(graph.NodeFile, glob) instead of
// at each match and this fails — it compiles fine, which is the point.
func TestCapabilityGlobsResolveToRealFiles(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"capabilities.yaml":   fixtureCapabilities,
		"docs/ALPHA.md":       "# Alpha\n",
		"docs/BETA.md":        "# Beta\n",
		"internal/alpha/a.go": "package alpha\n",
		"internal/alpha/b.go": "package alpha\n",
	})
	res := Capabilities(dir)
	var owns []graph.Edge
	for _, e := range res.Graph.Edges() {
		if e.Kind == graph.EdgeOwnsFile && e.Attrs["matched"] != "0" {
			owns = append(owns, e)
		}
	}
	if len(owns) != 2 {
		t.Fatalf("owns-file edges = %d, want 2 (one per matched file), got %v", len(owns), owns)
	}
	for _, e := range owns {
		if strings.Contains(string(e.To), "*") {
			t.Errorf("owns-file edge points at a glob (%s) — it can never resolve to a file node", e.To)
		}
	}
}

func TestFilesGolden(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"README.md":           "# Root\n",
		"internal/alpha/a.go": "package alpha\n",
		"docs/ALPHA.md":       "# Alpha\n",
	})
	res := Files(dir)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	checkGolden(t, "files", golden(res.Graph))
}

func TestDocsGolden(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"docs/A.md": "# A\n\nSee [B](B.md) and [[C]] and [the makefile](../Makefile).\n" +
			"An [external](https://example.com) link and an [anchor](#section).\n" +
			"A [gone](GONE.md) link.\n\n```\n[fenced](NOT_A_LINK.md)\n```\n",
		"docs/B.md": "# B\n",
		"Makefile":  "all:\n",
	})
	res := Docs(dir)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	checkGolden(t, "docs", golden(res.Graph))
}

// TestDocsLinkClassification pins the three decisions that produced fake
// findings before they were fixed: an external link is not an edge, a link to a
// non-markdown file targets a FILE node, and a fenced code block is an
// illustration rather than a reference.
func TestDocsLinkClassification(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"docs/A.md": "# A\n\n[md](B.md) [file](../Makefile) [ext](https://example.com)\n" +
			"```\n[fenced](NOPE.md)\n```\n",
		"docs/B.md": "# B\n",
		"Makefile":  "all:\n",
	})
	res := Docs(dir)
	var toDoc, toFile int
	for _, e := range res.Graph.Edges() {
		switch e.To.Kind() {
		case graph.NodeDoc:
			toDoc++
		case graph.NodeFile:
			toFile++
		}
		if strings.Contains(string(e.To), "example.com") {
			t.Error("an external link became an edge; it is outside the graph's universe")
		}
		if strings.Contains(string(e.To), "NOPE") {
			t.Error("a link inside a fenced code block became an edge; it is an illustration")
		}
	}
	if toDoc != 1 {
		t.Errorf("doc->doc edges = %d, want 1", toDoc)
	}
	if toFile != 1 {
		t.Errorf("doc->file edges = %d, want 1 (a link to Makefile is a FILE reference)", toFile)
	}
}

func TestADRsGolden(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"docs/decisions/001-first.md":  "# ADR-001 — First\n\nOriginal.\n",
		"docs/decisions/002-second.md": "# ADR-002 — Second\n\nThis supersedes ADR-001 entirely.\n",
		"docs/decisions/003-third.md":  "# ADR-003 — Third\n\nSupersedes ADR-099, which never existed.\n",
	})
	res := ADRs(dir)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	checkGolden(t, "adrs", golden(res.Graph))

	// The prose-derived edge must be marked as such (#126), and the one whose
	// target does not exist must still be emitted.
	var proseMarked, missingTarget bool
	for _, e := range res.Graph.Edges() {
		if e.Kind != graph.EdgeSupersedes {
			continue
		}
		if e.Attrs["derived_from"] == "prose" {
			proseMarked = true
		}
		if strings.Contains(string(e.To), "099") {
			missingTarget = true
		}
	}
	if !proseMarked {
		t.Error("a supersedes edge parsed from prose must be marked derived_from=prose")
	}
	if !missingTarget {
		t.Error("a supersedes edge naming a nonexistent ADR must still be emitted as a finding")
	}
}

const fixtureModels = `{
  "version": "test-1",
  "models": [
    {"id": "claude-opus-5", "name": "Opus 5", "provider": "anthropic", "band": "frontier", "status": "ga"},
    {"id": "claude-haiku-4-5", "name": "Haiku 4.5", "provider": "anthropic", "band": "efficiency", "status": "ga"}
  ]
}`

func TestModelsGolden(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{modelRegistryPath: fixtureModels})
	res := Models(dir)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	checkGolden(t, "models", golden(res.Graph))
}

// TestIssuesWrapsDepgraphRatherThanReimplementingIt is the AC that depgraph is
// CALLED, not copied, plus the field-mapping trap: depgraph's SourceLine is the
// line TEXT and its Source is the MECHANISM, so neither may be poured into
// graph.Provenance's same-named fields.
func TestIssuesWrapsDepgraphRatherThanReimplementingIt(t *testing.T) {
	dg := depgraph.NewGraph()
	dg.AddNode(&depgraph.Node{Repo: "acme/repo", Number: 1, Title: "child", State: "OPEN", EpicNumber: 9})
	dg.AddNode(&depgraph.Node{Repo: "acme/repo", Number: 2, Title: "blocker", State: "OPEN"})
	dg.AddEdge(depgraph.Edge{
		From:       depgraph.NodeID{Repo: "acme/repo", Number: 1},
		To:         depgraph.NodeID{Repo: "acme/repo", Number: 2},
		Type:       "blockedBy",
		Source:     "body_text",
		Resolvable: true,
		SourceLine: "Blocked by #2 per the plan",
	})

	res := IssuesFromGraph(dg)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}

	var blocks *graph.Edge
	for _, e := range res.Graph.Edges() {
		if e.Kind == graph.EdgeBlocks {
			ee := e
			blocks = &ee
		}
	}
	if blocks == nil {
		t.Fatal("no blocks edge produced")
	}
	if blocks.Prov.SourceLine != 0 {
		t.Errorf("Provenance.SourceLine = %d, want 0 — depgraph's SourceLine is the line TEXT, "+
			"not a number, and fabricating one defeats the field", blocks.Prov.SourceLine)
	}
	if got := blocks.Attrs["line_text"]; got != "Blocked by #2 per the plan" {
		t.Errorf("line_text attr = %q, want the prose line depgraph parsed (#126)", got)
	}
	if got := blocks.Attrs["mechanism"]; got != "body_text" {
		t.Errorf("mechanism attr = %q, want depgraph's Source verbatim", got)
	}
	if blocks.Prov.Source == "body_text" {
		t.Error("Provenance.Source must be a LOCATION (the issue reference), not depgraph's mechanism")
	}

	// The epic edge points at an epic that is not in this graph — a finding.
	if len(res.Graph.Dangling()) == 0 {
		t.Error("a part-of edge to an absent epic must be reported as dangling")
	}
}

func TestAllMergesAndDeduplicates(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"capabilities.yaml":       fixtureCapabilities,
		"docs/ALPHA.md":           "# Alpha\n",
		"docs/BETA.md":            "# Beta\n",
		"internal/alpha/a.go":     "package alpha\n",
		modelRegistryPath:         fixtureModels,
		"docs/decisions/001-x.md": "# ADR-001 — X\n",
	})
	g, results, err := All(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 wave-A extractors without a depgraph source, got %d", len(results))
	}
	// docs/ALPHA.md is seen by BOTH the docs extractor and the files
	// extractor. Node identity must collapse them, and first-writer-wins must
	// keep the discovering extractor's provenance.
	doc, ok := g.Node(graph.MakeNodeID(graph.NodeDoc, "docs/ALPHA.md"))
	if !ok {
		t.Fatal("docs/ALPHA.md missing from the merged graph")
	}
	if doc.Prov.Extractor != "docs" {
		t.Errorf("doc node provenance = %q, want the discovering extractor", doc.Prov.Extractor)
	}
	file, ok := g.Node(graph.MakeNodeID(graph.NodeFile, "docs/ALPHA.md"))
	if !ok {
		t.Fatal("the same path must also exist as a file node — kinds namespace identity")
	}
	if file.Prov.Extractor != "files" {
		t.Errorf("file node provenance = %q, want %q", file.Prov.Extractor, "files")
	}
	if g.NodeCount() == 0 || g.EdgeCount() == 0 {
		t.Fatal("merged graph is empty")
	}
}

func TestExtractorsSkipRatherThanFailOnAbsentSources(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{"README.md": "# only\n"})
	for _, r := range []Result{Capabilities(dir), Models(dir), ADRs(dir)} {
		if r.Skipped == "" {
			t.Errorf("%s: an absent source must SKIP with a reason, not silently succeed", r.Extractor)
		}
		if r.Graph == nil {
			t.Errorf("%s: a skipped extractor must still return an empty graph", r.Extractor)
		}
	}
}

func TestAllRejectsEmptyRoot(t *testing.T) {
	if _, _, err := All("", nil); err == nil {
		t.Error("All accepted an empty root")
	}
}

func TestMatchGlob(t *testing.T) {
	files := []string{"internal/a/x.go", "internal/a/b/y.go", "internal/c/z.go", "top.go"}
	for _, tc := range []struct {
		pattern string
		want    int
	}{
		{"internal/a/**", 2},
		{"internal/**", 3},
		{"internal/*/*.go", 2}, // * does not cross a separator
		{"*.go", 1},
		{"nothing/**", 0},
	} {
		if got := len(matchGlob(files, tc.pattern)); got != tc.want {
			t.Errorf("matchGlob(%q) = %d, want %d", tc.pattern, got, tc.want)
		}
	}
}

func TestResolveDocLink(t *testing.T) {
	for _, tc := range []struct{ from, target, want string }{
		{"docs/A.md", "B.md", "docs/B.md"},
		{"docs/A.md", "../Makefile", "Makefile"},
		{"docs/A.md", "/root.md", "root.md"},
		{"docs/A.md", "B.md#frag", "docs/B.md"},
		{"docs/A.md", "#anchor", ""},
		{"docs/A.md", "https://x.example", ""},
		{"docs/A.md", "mailto:a@b.c", ""},
		{"docs/A.md", "../../escape.md", ""},
	} {
		if got := resolveDocLink(tc.from, tc.target); got != tc.want {
			t.Errorf("resolveDocLink(%q, %q) = %q, want %q", tc.from, tc.target, got, tc.want)
		}
	}
}

func mustFileNode(t *testing.T, p string) graph.Node {
	t.Helper()
	n, err := graph.NewNode(graph.NodeFile, p, graph.Provenance{Extractor: "test", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustDocNode(t *testing.T, p string) graph.Node {
	t.Helper()
	n, err := graph.NewNode(graph.NodeDoc, p, graph.Provenance{Extractor: "test", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var _ = exec.Command // keep os/exec referenced if a future test drops its use
