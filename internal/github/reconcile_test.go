package github

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestAllSubsClosed(t *testing.T) {
	tests := []struct {
		name string
		subs []types.SubIssueRef
		want bool
	}{
		{"empty is not complete", nil, false},
		{"all closed", []types.SubIssueRef{{State: "CLOSED"}, {State: "closed"}}, true},
		{"one open", []types.SubIssueRef{{State: "CLOSED"}, {State: "OPEN"}}, false},
		{"all open", []types.SubIssueRef{{State: "OPEN"}}, false},
	}
	for _, tt := range tests {
		if got := allSubsClosed(tt.subs); got != tt.want {
			t.Errorf("%s: allSubsClosed = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAnySubOpen(t *testing.T) {
	tests := []struct {
		name string
		subs []types.SubIssueRef
		want bool
	}{
		{"empty has none open", nil, false},
		{"all closed", []types.SubIssueRef{{State: "CLOSED"}}, false},
		{"one open", []types.SubIssueRef{{State: "CLOSED"}, {State: "OPEN"}}, true},
	}
	for _, tt := range tests {
		if got := anySubOpen(tt.subs); got != tt.want {
			t.Errorf("%s: anySubOpen = %v, want %v", tt.name, got, tt.want)
		}
	}
}

type subFixture struct {
	nodeID string
	number int
	state  string
}

// reconcileEpicJSON builds a GetIssue GraphQL response for an epic (labeled
// `type:epic`) with the given state, stateReason, and sub-issues.
func reconcileEpicJSON(epicNumber int, state, stateReason string, subs []subFixture) string {
	return reconcileEpicJSONWithLabels(epicNumber, state, stateReason, []string{"type:epic"}, subs)
}

// reconcileEpicJSONWithLabels builds a GetIssue GraphQL response for a parent
// issue with an arbitrary label set, state, stateReason, and sub-issues — used
// to exercise the `type:spike` guard (#4197) and unlabeled epics alike.
func reconcileEpicJSONWithLabels(epicNumber int, state, stateReason string, labels []string, subs []subFixture) string {
	var nodes []string
	for _, s := range subs {
		nodes = append(nodes, fmt.Sprintf(
			`{"id":%q,"number":%d,"title":"Sub %d","state":%q,"repository":{"nameWithOwner":"o/r"},"labels":{"nodes":[]}}`,
			s.nodeID, s.number, s.number, s.state))
	}
	var labelNodes []string
	for _, l := range labels {
		labelNodes = append(labelNodes, fmt.Sprintf(`{"name":%q}`, l))
	}
	return fmt.Sprintf(`{"data":{"repository":{"issue":{
		"id":"I_%d","number":%d,"title":"Epic %d","body":"","state":%q,"stateReason":%q,"url":"",
		"parent":{"id":"","number":0,"title":""},
		"labels":{"nodes":[%s]},
		"assignees":{"nodes":[]},
		"subIssues":{"nodes":[%s]},
		"blockedBy":{"nodes":[]},
		"blocking":{"nodes":[]}
	}}}}`, epicNumber, epicNumber, epicNumber, state, stateReason, strings.Join(labelNodes, ","), strings.Join(nodes, ","))
}

func TestCloseOrphanSubs_EpicStillOpen_Skips(t *testing.T) {
	resp := reconcileEpicJSON(100, "OPEN", "", []subFixture{{"SUB_1", 200, "OPEN"}})
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()

	res, err := NewEpicService(client).CloseOrphanSubs(context.Background(), "o", "r", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Guard != "epic_open" {
		t.Errorf("Guard = %q, want epic_open", res.Guard)
	}
	if res.Closed != 0 {
		t.Errorf("Closed = %d, want 0 — an open epic's subs must not be touched", res.Closed)
	}
}

func TestCloseOrphanSubs_NotPlanned_LeavesSubsUntouched(t *testing.T) {
	// Epic CLOSED but as NOT_PLANNED (cancelled) with an open sub. The guard must
	// refuse to close the sub — it may be genuine, unstarted work.
	resp := reconcileEpicJSON(100, "CLOSED", "NOT_PLANNED", []subFixture{{"SUB_1", 200, "OPEN"}})
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()

	res, err := NewEpicService(client).CloseOrphanSubs(context.Background(), "o", "r", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Guard != "not_completed" {
		t.Errorf("Guard = %q, want not_completed", res.Guard)
	}
	if res.Closed != 0 {
		t.Errorf("Closed = %d, want 0 — a cancelled epic must not auto-close its subs", res.Closed)
	}
}

func TestCloseOrphanSubs_Completed_ClosesOnlyOpenSubs(t *testing.T) {
	// Epic CLOSED as COMPLETED with one open and one already-closed sub.
	// Only the open one should be closed. projectNumber=0 skips board sync.
	epicResp := reconcileEpicJSON(100, "CLOSED", "COMPLETED", []subFixture{
		{"SUB_OPEN", 200, "OPEN"},
		{"SUB_DONE", 201, "CLOSED"},
	})
	// epicResp first; every following mutation (CloseIssue, AddComment) gets {}.
	client, cleanup := mockGraphQLServer(t, epicResp, `{"data":{}}`)
	defer cleanup()

	res, err := NewEpicService(client).CloseOrphanSubs(context.Background(), "o", "r", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Guard != "completed" {
		t.Errorf("Guard = %q, want completed", res.Guard)
	}
	if res.Closed != 1 {
		t.Fatalf("Closed = %d, want 1 (only the open sub)", res.Closed)
	}
	if len(res.Subs) != 1 || res.Subs[0].SubNumber != 200 || res.Subs[0].Action != "closed" {
		t.Errorf("Subs = %+v, want one closed action for sub #200", res.Subs)
	}
}

func TestCloseOrphanSubs_Completed_NoOrphans(t *testing.T) {
	resp := reconcileEpicJSON(100, "CLOSED", "COMPLETED", []subFixture{{"SUB_DONE", 201, "CLOSED"}})
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()

	res, err := NewEpicService(client).CloseOrphanSubs(context.Background(), "o", "r", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Guard != "no_orphans" {
		t.Errorf("Guard = %q, want no_orphans", res.Guard)
	}
	if res.Closed != 0 {
		t.Errorf("Closed = %d, want 0", res.Closed)
	}
}

// TestCloseOrphanSubs_Spike_LeavesSubsUntouched is the regression test for
// #4197: a CLOSED, StateReason=COMPLETED parent labeled `type:spike` must NOT
// have its open native sub-issues closed. `spike materialize` links adopted
// follow-up recommendations as sub-issues purely for traceability — they are
// meant to be implemented independently, later, and are not "done" just
// because the spike's own design-decision PR merged (real incident: spike
// #4134 auto-closing unimplemented follow-ups #4152 and #4153).
func TestCloseOrphanSubs_Spike_LeavesSubsUntouched(t *testing.T) {
	resp := reconcileEpicJSONWithLabels(100, "CLOSED", "COMPLETED", []string{"type:spike"},
		[]subFixture{{"SUB_1", 200, "OPEN"}})
	client, cleanup := mockGraphQLServer(t, resp)
	defer cleanup()

	res, err := NewEpicService(client).CloseOrphanSubs(context.Background(), "o", "r", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Guard != "spike" {
		t.Errorf("Guard = %q, want spike", res.Guard)
	}
	if res.Closed != 0 {
		t.Errorf("Closed = %d, want 0 — a type:spike parent must not auto-close its adopted follow-up sub-issues", res.Closed)
	}
	if len(res.Subs) != 0 {
		t.Errorf("Subs = %+v, want none — the spike guard must return before touching any sub-issue", res.Subs)
	}
}

// TestCloseOrphanSubs_UnlabeledEpic_StillClosesOrphans is a regression guard
// alongside the #4197 spike fix: a closed, completed parent with native
// sub-issues but no `type:epic` label (structural-only epic detection) must
// still close its orphaned open subs — the spike guard must not accidentally
// widen into a general "unlabeled parent" skip.
func TestCloseOrphanSubs_UnlabeledEpic_StillClosesOrphans(t *testing.T) {
	epicResp := reconcileEpicJSONWithLabels(100, "CLOSED", "COMPLETED", nil,
		[]subFixture{{"SUB_OPEN", 200, "OPEN"}})
	client, cleanup := mockGraphQLServer(t, epicResp, `{"data":{}}`)
	defer cleanup()

	res, err := NewEpicService(client).CloseOrphanSubs(context.Background(), "o", "r", 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Guard != "completed" {
		t.Errorf("Guard = %q, want completed", res.Guard)
	}
	if res.Closed != 1 {
		t.Fatalf("Closed = %d, want 1 — an unlabeled (structural) epic must still auto-close orphaned open subs", res.Closed)
	}
}
