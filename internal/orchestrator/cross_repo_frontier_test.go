package orchestrator

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestEvaluateDeps_CrossRepoFrontier pins the workspace-wide dispatch
// frontier (#2573): an issue in o/b is blocked by o/a#7, and o/b has an
// unrelated #7. The blocker's state in ITS OWN repository decides dispatch,
// whether or not o/a#7 is on a board, and an unknown state fails closed.
func TestEvaluateDeps_CrossRepoFrontier(t *testing.T) {
	const dep = "o/a#7"
	sameNumberOtherRepo := &depgraph.Node{Repo: "o/b", Number: 7, State: "CLOSED", BoardStatus: "Done"}

	tests := []struct {
		name        string
		nodes       []*depgraph.Node
		resolved    map[string]string
		validation  map[string]ValidationState
		wantBlocked bool
	}{
		{
			name:        "off-board blocker CLOSED in its repository dispatches",
			resolved:    map[string]string{dep: "CLOSED"},
			validation:  map[string]ValidationState{dep: {}},
			wantBlocked: false,
		},
		{
			name:        "off-board blocker OPEN in its repository is blocked",
			resolved:    map[string]string{dep: "OPEN"},
			wantBlocked: true,
		},
		{
			name:        "on-board blocker CLOSED dispatches",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "CLOSED", BoardStatus: "Done"}},
			wantBlocked: false,
		},
		{
			name:        "on-board blocker OPEN is blocked",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "OPEN", BoardStatus: "In progress"}},
			wantBlocked: true,
		},
		{
			name:        "unresolved off-board blocker fails closed",
			wantBlocked: true,
		},
		{
			name:        "a closed same-numbered issue in the dependent's repository does not satisfy it",
			nodes:       []*depgraph.Node{sameNumberOtherRepo},
			wantBlocked: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := buildTestGraph(tc.nodes, nil)
			res := evaluateDeps([]string{dep}, g, tc.resolved, tc.validation)
			if res.blocked != tc.wantBlocked {
				t.Fatalf("evaluateDeps = %+v, want blocked=%v", res, tc.wantBlocked)
			}
			if res.blocked && res.blocker != dep {
				t.Errorf("blocker = %q, want %q", res.blocker, dep)
			}
		})
	}
}

// TestEvaluateDeps_ProgramValidationGate pins #2574: a closed program blocker
// releases its dependents only with the post-merge validation marker; a closed
// blocker outside any program keeps closure-based release; an unknown standing
// fails closed.
func TestEvaluateDeps_ProgramValidationGate(t *testing.T) {
	const dep = "o/a#7"
	program := []string{"program:billing"}
	tests := []struct {
		name        string
		nodes       []*depgraph.Node
		resolved    map[string]string
		validation  map[string]ValidationState
		wantBlocked bool
		wantMarker  bool // the reason names the missing marker
	}{
		{
			name:        "closed program blocker without the marker keeps its dependents",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "CLOSED", BoardStatus: "Done", Labels: program}},
			validation:  map[string]ValidationState{dep: {Program: true}},
			wantBlocked: true, wantMarker: true,
		},
		{
			name:        "closed program blocker with the marker releases them",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "CLOSED", BoardStatus: "Done", Labels: program}},
			validation:  map[string]ValidationState{dep: {Program: true, Validated: true}},
			wantBlocked: false,
		},
		{
			name:        "closed program blocker whose comments could not be read fails closed",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "CLOSED", BoardStatus: "Done", Labels: program}},
			wantBlocked: true,
		},
		{
			name:        "closed blocker outside any program releases on closure",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "CLOSED", BoardStatus: "Done", Labels: []string{"type:feature"}}},
			wantBlocked: false,
		},
		{
			name:        "open program blocker in review does not release",
			nodes:       []*depgraph.Node{{Repo: "o/a", Number: 7, State: "OPEN", BoardStatus: "In review", Labels: program}},
			wantBlocked: true,
		},
		{
			name:        "off-board closed program blocker without the marker keeps its dependents",
			resolved:    map[string]string{dep: "CLOSED"},
			validation:  map[string]ValidationState{dep: {Program: true}},
			wantBlocked: true, wantMarker: true,
		},
		{
			name:        "off-board closed blocker whose labels could not be read fails closed",
			resolved:    map[string]string{dep: "CLOSED"},
			wantBlocked: true,
		},
		{
			name:        "off-board closed blocker outside any program releases on closure",
			resolved:    map[string]string{dep: "CLOSED"},
			validation:  map[string]ValidationState{dep: {}},
			wantBlocked: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := buildTestGraph(tc.nodes, nil)
			res := evaluateDeps([]string{dep}, g, tc.resolved, tc.validation)
			if res.blocked != tc.wantBlocked {
				t.Fatalf("evaluateDeps = %+v, want blocked=%v", res, tc.wantBlocked)
			}
			if tc.wantMarker && !strings.Contains(res.status, "without validation marker") {
				t.Errorf("status = %q, want it to name the missing validation marker", res.status)
			}
		})
	}
}

// markerComments answers TrustedComments from a fixed map.
type markerComments struct {
	byKey map[string][]trust.Comment
	err   error
	reads []string
}

func (m *markerComments) TrustedComments(_ context.Context, owner, repo string, number int) ([]trust.Comment, error) {
	key := owner + "/" + repo + "#" + strconv.Itoa(number)
	m.reads = append(m.reads, key)
	return m.byKey[key], m.err
}

// ResolveValidationByKey reads trusted comments only for program members,
// reads labels for off-board keys, and leaves undecidable keys out.
func TestResolveValidationByKey(t *testing.T) {
	mock := newMockIssueSvc()
	mock.issues["o/b#5"] = &types.Issue{Number: 5, State: "CLOSED", Labels: []string{"program:x"}}
	mock.issues["o/b#6"] = &types.Issue{Number: 6, State: "CLOSED", Labels: []string{"type:bug"}}
	comments := &markerComments{byKey: map[string][]trust.Comment{
		"o/a#1": {{Body: forge.ValidationMarkerComment("main", "abc")}},
	}}
	got := ResolveValidationByKey(context.Background(), mock, comments, &closingMerges{},
		map[string][]string{"o/a#1": {"program:x"}, "o/a#2": {"program:x"}, "o/a#3": {"type:feature"}},
		[]string{"o/b#5", "o/b#6"})

	want := map[string]ValidationState{
		"o/a#1": {Program: true, Validated: true},
		"o/a#2": {Program: true},
		"o/a#3": {},
		"o/b#5": {Program: true},
		"o/b#6": {},
	}
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			t.Errorf("%s = %+v (present=%v), want %+v", k, g, ok, w)
		}
	}
	for _, r := range comments.reads {
		if r == "o/a#3" || r == "o/b#6" {
			t.Errorf("read comments of non-program issue %s", r)
		}
	}

	failing := &markerComments{err: errors.New("REST 502")}
	got = ResolveValidationByKey(context.Background(), mock, failing, &closingMerges{}, map[string][]string{"o/a#1": {"program:x"}}, nil)
	if _, ok := got["o/a#1"]; ok {
		t.Errorf("a failed comment read resolved o/a#1 = %+v; it must be left out (fail closed)", got["o/a#1"])
	}
}
