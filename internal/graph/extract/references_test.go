package extract

import (
	"reflect"
	"testing"

	"github.com/nightgauge/nightgauge/internal/graph"
)

func TestPathTokens(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"Edit `internal/orchestrator/scheduler.go` now", []string{"internal/orchestrator/scheduler.go"}},
		{"see internal/graph/graph.go:104 and docs/X.md.", []string{"internal/graph/graph.go", "docs/X.md"}},
		{"the packages/nightgauge/ tree", []string{"packages/nightgauge/"}},
		{"link [doc](docs/GIT_WORKFLOW.md)", []string{"docs/GIT_WORKFLOW.md"}},
		{"https://github.com/a/b/blob/main/x.go", nil},
		{"nightgauge/nightgauge#12 and #13", nil},
		{"and/or either/neither", nil},
		{"/etc/hosts ~/x/y.go ../up/z.go", nil},
	}
	for _, c := range cases {
		if got := pathTokens(c.line); !reflect.DeepEqual(got, c.want) {
			t.Errorf("pathTokens(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestReferencesEdges(t *testing.T) {
	body := "Context: `a/x.go` matters.\n" +
		"```\n# not a heading\nb/in-fence.go\n```\n" +
		"## Acceptance criteria\n" +
		"- [ ] change `c/y.ts`\n" +
		"- [ ] again a/x.go\n" +
		"## Notes\n" +
		"- dir pkg/sub/\n"
	res := References("nightgauge/nightgauge#139", body)
	if res.Skipped != "" {
		t.Fatalf("skipped: %s", res.Skipped)
	}
	from := graph.MakeNodeID(graph.NodeIssue, "nightgauge/nightgauge#139")
	type want struct {
		line    int
		ac, dir string
	}
	wants := map[graph.NodeID]want{
		graph.MakeNodeID(graph.NodeFile, "a/x.go"):        {1, "false", "false"},
		graph.MakeNodeID(graph.NodeFile, "b/in-fence.go"): {4, "false", "false"},
		graph.MakeNodeID(graph.NodeFile, "c/y.ts"):        {7, "true", "false"},
		graph.MakeNodeID(graph.NodeFile, "pkg/sub"):       {10, "false", "true"},
	}
	edges := res.Graph.EdgesFrom(from)
	if len(edges) != len(wants) {
		t.Fatalf("got %d edges, want %d: %+v", len(edges), len(wants), edges)
	}
	for _, e := range edges {
		w, ok := wants[e.To]
		if !ok {
			t.Errorf("unexpected edge to %s", e.To)
			continue
		}
		if e.Kind != graph.EdgeReferences || e.Prov.SourceLine != w.line ||
			e.Attrs["in_acceptance_criteria"] != w.ac || e.Attrs["dir"] != w.dir {
			t.Errorf("edge %s = %+v, want %+v", e.To, e, w)
		}
	}
	if d := res.Graph.Dangling(); len(d) != len(wants) {
		t.Errorf("Dangling() = %d, want %d (edges are unresolved by design)", len(d), len(wants))
	}
}

func TestReferencesEmptyBody(t *testing.T) {
	if res := References("o/r#1", "  \n"); res.Skipped != "no body" {
		t.Errorf("Skipped = %q, want no body", res.Skipped)
	}
}
