package workorder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

const programsYAML = `
# A richer registry's extra keys are ignored.
constraints: [{ id: x, rule: y }]
programs:
  - id: onboarding
    rank: 2
    title: Users can sign up
  - id: billing
    rank: 1
    title: Customers can pay
    repos: [acme-api]
    goal: ignored
  - id: docs
    label: area:docs
`

func node(repo string, n int, status, prio string, labels ...string) *depgraph.Node {
	return &depgraph.Node{Repo: repo, Number: n, Title: "t", State: "OPEN", BoardStatus: status, Priority: prio, Labels: labels}
}

func graphOf(nodes []*depgraph.Node, edges []depgraph.Edge) *depgraph.Graph {
	g := depgraph.NewGraph()
	for _, n := range nodes {
		g.AddNode(n)
	}
	for _, e := range edges {
		g.AddEdge(e)
	}
	g.Waves, g.Cycles = depgraph.ComputeWaves(g)
	return g
}

func refs(items []Item) string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Ref)
	}
	return strings.Join(out, " ")
}

func TestParse_DefaultsAndOrder(t *testing.T) {
	f, err := Parse([]byte(programsYAML))
	if err != nil {
		t.Fatal(err)
	}
	if f.LabelPrefix != "program:" || f.ReadyStatus != "Ready" || strings.Join(f.ExcludeLabels, ",") != "blocked" {
		t.Fatalf("defaults: %+v", f)
	}
	var ids []string
	for _, p := range f.Ordered() {
		ids = append(ids, p.ID+"="+f.Selector(p))
	}
	if got := strings.Join(ids, " "); got != "billing=program:billing onboarding=program:onboarding docs=area:docs" {
		t.Fatalf("order = %s", got)
	}
	if strings.Join(f.Repos(), ",") != "acme-api" {
		t.Fatalf("repos = %v", f.Repos())
	}
}

func TestParse_Rejects(t *testing.T) {
	for name, src := range map[string]string{
		"no id":     "programs: [{ title: x }]",
		"duplicate": "programs: [{ id: a }, { id: a }]",
		"not yaml":  "programs: [",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestRank_Populated(t *testing.T) {
	f, err := Parse([]byte(programsYAML))
	if err != nil {
		t.Fatal(err)
	}
	api, web := "acme/acme-api", "acme/acme-web"
	nodes := []*depgraph.Node{
		node(api, 10, "Ready", "P2", "program:billing"),
		node(api, 11, "Ready", "P0", "program:billing"),
		node(api, 12, "Ready", "P1", "program:billing"),            // blocked by #20, open
		node(api, 13, "Ready", "P1", "program:billing", "blocked"), // label
		node(api, 14, "Backlog", "P0", "program:billing"),          // not Ready
		node(web, 15, "Ready", "P0", "program:billing"),            // repo outside the program
		node(web, 16, "Ready", "", "program:onboarding"),
		node(web, 17, "Ready", "P3", "area:docs"),
		node(web, 18, "Ready", "P1"),            // unprogrammed
		node(web, 19, "Ready", "P1", "blocked"), // unprogrammed and blocked: omitted
		node(api, 20, "In progress", "P1"),
	}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: api, Number: 12}, To: depgraph.NodeID{Repo: api, Number: 20}, Type: "blockedBy", Resolvable: true},
		// A closed blocker is not a node: it does not block.
		{From: depgraph.NodeID{Repo: api, Number: 10}, To: depgraph.NodeID{Repo: api, Number: 1}, Type: "blockedBy", Resolvable: true},
		// A dependency outside the read set is reported, not blocking.
		{From: depgraph.NodeID{Repo: web, Number: 16}, To: depgraph.NodeID{Repo: "acme/other", Number: 5}, Type: "crossRepo", Resolvable: false},
	}
	res := Rank(f, graphOf(nodes, edges))

	if len(res.Programs) != 3 {
		t.Fatalf("programs = %d", len(res.Programs))
	}
	billing := res.Programs[0]
	if billing.ID != "billing" || refs(billing.Ready) != "acme/acme-api#11 acme/acme-api#10" {
		t.Fatalf("billing ready = %s", refs(billing.Ready))
	}
	if refs(billing.Blocked) != "acme/acme-api#12 acme/acme-api#13" {
		t.Fatalf("billing blocked = %s", refs(billing.Blocked))
	}
	if got := strings.Join(billing.Blocked[0].BlockedBy, ","); got != "acme/acme-api#20" {
		t.Fatalf("#12 blocked_by = %s", got)
	}
	if got := strings.Join(billing.Blocked[1].BlockedBy, ","); got != "label `blocked`" {
		t.Fatalf("#13 blocked_by = %s", got)
	}
	onboarding := res.Programs[1]
	if refs(onboarding.Ready) != "acme/acme-web#16" || strings.Join(onboarding.Ready[0].Unresolved, ",") != "acme/other#5" {
		t.Fatalf("onboarding = %+v", onboarding.Ready)
	}
	if refs(res.Programs[2].Ready) != "acme/acme-web#17" {
		t.Fatalf("docs = %s", refs(res.Programs[2].Ready))
	}
	if refs(res.UnprogrammedReady) != "acme/acme-web#15 acme/acme-web#18" {
		t.Fatalf("unprogrammed = %s", refs(res.UnprogrammedReady))
	}
	if res.Totals != (Totals{Ready: 4, Blocked: 2, UnprogrammedReady: 2}) {
		t.Fatalf("totals = %+v", res.Totals)
	}
}

func TestRank_TruncatedLabelsAndCyclesFailClosed(t *testing.T) {
	f, _ := Parse([]byte("programs: [{ id: a }]"))
	api := "acme/acme-api"
	truncated := node(api, 1, "Ready", "", "program:a")
	truncated.LabelsTruncated = true
	nodes := []*depgraph.Node{truncated, node(api, 2, "Ready", "", "program:a"), node(api, 3, "Ready", "", "program:a")}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: api, Number: 2}, To: depgraph.NodeID{Repo: api, Number: 3}, Resolvable: true},
		{From: depgraph.NodeID{Repo: api, Number: 3}, To: depgraph.NodeID{Repo: api, Number: 2}, Resolvable: true},
	}
	res := Rank(f, graphOf(nodes, edges))
	if len(res.Programs[0].Ready) != 0 || len(res.Programs[0].Blocked) != 3 {
		t.Fatalf("result = %+v", res.Programs[0])
	}
	if !strings.Contains(strings.Join(res.Programs[0].Blocked[0].BlockedBy, ","), "labels truncated") {
		t.Fatalf("#1 = %v", res.Programs[0].Blocked[0].BlockedBy)
	}
}

func TestRank_Empty(t *testing.T) {
	f, _ := Parse([]byte("programs: []"))
	for _, g := range []*depgraph.Graph{nil, depgraph.NewGraph()} {
		res := Rank(f, g)
		b, _ := json.Marshal(res)
		for _, key := range []string{`"programs":[]`, `"unprogrammed_ready":[]`, `"totals":{"ready":0,"blocked":0,"unprogrammed_ready":0}`} {
			if !strings.Contains(string(b), key) {
				t.Fatalf("%s missing from %s", key, b)
			}
		}
	}
	// A program with no matching issues is listed with empty arrays.
	f, _ = Parse([]byte("programs: [{ id: a }]"))
	b, _ := json.Marshal(Rank(f, depgraph.NewGraph()))
	if !strings.Contains(string(b), `"ready":[],"blocked":[]`) {
		t.Fatalf("empty program: %s", b)
	}
}
