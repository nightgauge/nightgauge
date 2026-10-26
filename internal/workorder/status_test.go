package workorder

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

func issue(repo string, n int, state, reason string, labels ...string) pkgtypes.Issue {
	return pkgtypes.Issue{Repo: repo, Number: n, State: state, StateReason: reason, Labels: labels}
}

// Status counts a program's whole issue set by state, counts only issues
// closed as completed as validated, and takes "dispatchable" from Rank so the
// readout and `nightgauge next` agree (#2573).
func TestStatus_CountsAndAgreesWithRank(t *testing.T) {
	f, err := Parse([]byte(`programs:
  - { id: billing, rank: 1, repos: [api] }
  - { id: docs, rank: 2 }
`))
	if err != nil {
		t.Fatal(err)
	}
	api, web := "o/api", "o/web"
	issues := []pkgtypes.Issue{
		issue(api, 1, "OPEN", "", "program:billing"),
		issue(api, 2, "OPEN", "", "program:billing"),
		issue(api, 3, "CLOSED", "COMPLETED", "program:billing"),
		issue(api, 4, "CLOSED", "NOT_PLANNED", "program:billing"),
		issue(api, 3, "CLOSED", "COMPLETED", "program:billing"), // listed twice: counted once
		issue(web, 5, "OPEN", "", "program:billing"),            // outside the program's repos
		issue(web, 6, "CLOSED", "COMPLETED", "program:docs"),
	}
	nodes := []*depgraph.Node{
		node(api, 1, "Ready", "", "program:billing"),
		node(api, 2, "Ready", "", "program:billing"),
	}
	edges := []depgraph.Edge{{From: depgraph.NodeID{Repo: api, Number: 2}, To: depgraph.NodeID{Repo: "o/core", Number: 9}, Type: "crossRepo"}}
	ranked := Rank(f, graphOf(nodes, edges), map[string]string{"o/core#9": "OPEN"})

	got := Status(f, issues, ranked)
	if len(got) != 2 || got[0].ID != "billing" || got[1].ID != "docs" {
		t.Fatalf("programs = %+v", got)
	}
	b := got[0]
	if b.Total != 4 || b.Open != 2 || b.Closed != 2 || b.Validated != 1 {
		t.Fatalf("billing counts = total %d open %d closed %d validated %d, want 4/2/2/1", b.Total, b.Open, b.Closed, b.Validated)
	}
	if b.Dispatchable != 1 || strings.Join(b.DispatchableRefs, ",") != "o/api#1" {
		t.Fatalf("billing dispatchable = %d %v, want o/api#1", b.Dispatchable, b.DispatchableRefs)
	}
	if len(b.Blocked) != 1 || b.Blocked[0].Ref != "o/api#2" || strings.Join(b.Blocked[0].BlockedBy, ",") != "o/core#9" {
		t.Fatalf("billing blocked = %+v", b.Blocked)
	}
	d := got[1]
	if d.Total != 1 || d.Closed != 1 || d.Validated != 1 || d.Dispatchable != 0 {
		t.Fatalf("docs = %+v", d)
	}
}

func TestIsValidated(t *testing.T) {
	for _, tc := range []struct {
		is   pkgtypes.Issue
		want bool
	}{
		{issue("o/a", 1, "CLOSED", "COMPLETED"), true},
		{issue("o/a", 1, "CLOSED", "NOT_PLANNED"), false},
		{issue("o/a", 1, "CLOSED", ""), false},
		{issue("o/a", 1, "OPEN", ""), false},
	} {
		if got := isValidated(tc.is); got != tc.want {
			t.Errorf("isValidated(%s/%s) = %v, want %v", tc.is.State, tc.is.StateReason, got, tc.want)
		}
	}
}

func TestStatus_EmptyProgramHasEmptyArrays(t *testing.T) {
	f, _ := Parse([]byte("programs: [{ id: a }]"))
	got := Status(f, nil, nil)
	if len(got) != 1 || got[0].DispatchableRefs == nil || got[0].Blocked == nil || got[0].Repos == nil {
		t.Fatalf("status = %+v", got)
	}
}
