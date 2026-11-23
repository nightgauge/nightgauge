package followup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// countingFiler is a GitHubFiler whose forge reads come from issues and count
// per number (#2872 Part E).
type countingFiler struct {
	*GitHubFiler
	issueReads     map[int]int
	placementReads map[int]int
	edits          map[int]int
	trustReads     map[int]int
	// search maps a query to its hits; untrusted names authors the trust gate
	// refuses.
	search    map[string][]types.Issue
	untrusted map[int]bool
	// placementErr and editErr, when set, fail those calls.
	placementErr, editErr error
}

func newCountingFiler(issues map[int]*types.Issue) *countingFiler {
	c := &countingFiler{
		GitHubFiler: &GitHubFiler{
			owner: "o", repo: "r",
			subs:       map[int]*parentSubsystem{},
			issues:     map[int]*types.Issue{},
			placements: map[int]gh.BoardPlacement{},
			trusted:    map[int]bool{},
		},
		issueReads: map[int]int{}, placementReads: map[int]int{}, edits: map[int]int{},
		trustReads: map[int]int{}, search: map[string][]types.Issue{}, untrusted: map[int]bool{},
	}
	c.searchIssues = func(_ context.Context, q string, _ int) ([]types.Issue, error) {
		return c.search[q], nil
	}
	c.candidateTrusted = func(_ context.Context, n int) bool {
		c.trustReads[n]++
		return !c.untrusted[n]
	}
	byNode := map[string]int{}
	for n, is := range issues {
		byNode[is.NodeID] = n
	}
	c.fetchIssue = func(_ context.Context, n int) (*types.Issue, error) {
		c.issueReads[n]++
		is, ok := issues[n]
		if !ok {
			return nil, errors.New("not found")
		}
		cp := *is
		return &cp, nil
	}
	c.fetchPlacement = func(_ context.Context, n int) (gh.BoardPlacement, error) {
		c.placementReads[n]++
		if c.placementErr != nil {
			return gh.BoardPlacement{}, c.placementErr
		}
		return gh.BoardPlacement{OnBoard: true, Status: "Ready", Priority: "P2"}, nil
	}
	c.editIssue = func(_ context.Context, nodeID, body string) error {
		n := byNode[nodeID]
		c.edits[n]++
		if c.editErr != nil {
			return c.editErr
		}
		issues[n].Body = body
		return nil
	}
	return c
}

func partRecord(id string, issue int) discoveredwork.Record {
	return discoveredwork.Record{
		ID: id, IssueNumber: issue, Summary: "cache the parent reads", Evidence: []string{"x.go:1"},
		SourceStage: "feature-dev", SuggestedType: "chore", SuggestedPriority: "low", SuggestedSize: "S",
	}
}

// TestGitHubFiler_ReadsTheSharedParentOnce: two records with one parent look
// up their markers and the parent's body, and the parent is read once.
func TestGitHubFiler_ReadsTheSharedParentOnce(t *testing.T) {
	recs := []discoveredwork.Record{partRecord("dw-a", 7), partRecord("dw-b", 7)}
	body := Marker(recs[0]) + "\n" + Marker(recs[1]) + "\n- [ ] one criterion\n"
	f := newCountingFiler(map[int]*types.Issue{7: {Number: 7, NodeID: "N7", State: issueOpen, Body: body}})
	ctx := context.Background()
	for _, rec := range recs {
		found, err := f.FindByMarker(ctx, Marker(rec))
		if err != nil {
			t.Fatal(err)
		}
		if found.Number != 7 {
			t.Fatalf("want %s found on #7, got %+v", rec.ID, found)
		}
		got, err := f.ParentBody(ctx, 7)
		if err != nil {
			t.Fatal(err)
		}
		if got != body {
			t.Fatalf("want the parent's body, got %q", got)
		}
	}
	if f.issueReads[7] != 1 {
		t.Fatalf("parent read %d times, want 1", f.issueReads[7])
	}
}

// TestGitHubFiler_ReadsACandidatesPlacementOnce: one candidate (an open epic
// sibling) considered for two records has its board placement read once.
func TestGitHubFiler_ReadsACandidatesPlacementOnce(t *testing.T) {
	f := newCountingFiler(map[int]*types.Issue{
		7: {Number: 7, NodeID: "N7", State: issueOpen, ParentIssueNumber: 1},
		1: {Number: 1, NodeID: "N1", State: issueOpen, SubIssues: []types.SubIssueRef{
			{Number: 7, State: issueOpen}, {Number: 9, State: issueOpen},
		}},
		9: {Number: 9, NodeID: "N9", State: issueOpen, URL: "u9"},
	})
	ctx := context.Background()
	// The marker lookup reads the parent first; the subsystem lookup reuses it.
	if _, err := f.ParentBody(ctx, 7); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		n, _, err := f.FindOpenInSubsystem(ctx, 7, nil, "low")
		if err != nil {
			t.Fatal(err)
		}
		if n != 9 {
			t.Fatalf("want the epic sibling #9 picked, got #%d", n)
		}
	}
	if f.placementReads[9] != 1 {
		t.Fatalf("placement read %d times, want 1", f.placementReads[9])
	}
	for _, n := range []int{7, 1, 9} {
		if f.issueReads[n] != 1 {
			t.Fatalf("#%d read %d times, want 1", n, f.issueReads[n])
		}
	}
}

// TestGitHubFiler_AppendPartRefreshesTheMemo: AppendPart edits a fresh body,
// not the memoized one, and a later FindByMarker on that issue sees the new
// marker without reading it again.
func TestGitHubFiler_AppendPartRefreshesTheMemo(t *testing.T) {
	issues := map[int]*types.Issue{7: {Number: 7, NodeID: "N7", State: issueOpen, Body: "# Parent\n"}}
	f := newCountingFiler(issues)
	ctx := context.Background()
	if _, err := f.ParentBody(ctx, 7); err != nil { // memoizes the old body
		t.Fatal(err)
	}
	issues[7].Body = "# Parent\n\nedited by a human\n" // changes after the memo
	rec := partRecord("dw-new", 7)
	label, err := f.AppendPart(ctx, 7, rec)
	if err != nil {
		t.Fatal(err)
	}
	if label == "" {
		t.Fatal("want a part appended, got no label")
	}
	if !strings.Contains(issues[7].Body, "edited by a human") {
		t.Fatalf("AppendPart edited a stale body: %q", issues[7].Body)
	}
	found, err := f.FindByMarker(ctx, Marker(rec))
	if err != nil {
		t.Fatal(err)
	}
	if found.Number != 7 {
		t.Fatalf("want the appended marker found on #7, got %+v", found)
	}
	if f.issueReads[7] != 2 || f.edits[7] != 1 {
		t.Fatalf("reads %d edits %d, want 2 and 1", f.issueReads[7], f.edits[7])
	}
}

// TestGitHubFiler_FailedReadIsNotMemoized: an error is returned again by a
// fresh read, never replayed from the memo as an empty issue.
func TestGitHubFiler_FailedReadIsNotMemoized(t *testing.T) {
	f := newCountingFiler(map[int]*types.Issue{})
	for range 2 {
		if _, err := f.ParentBody(context.Background(), 5); err == nil {
			t.Fatal("want an error")
		}
	}
	if f.issueReads[5] != 2 {
		t.Fatalf("reads %d, want 2", f.issueReads[5])
	}
}

// TestGitHubFiler_FailedPlacementIsNotMemoized: a failed placement read is
// returned again by a fresh read, never replayed as an empty placement.
func TestGitHubFiler_FailedPlacementIsNotMemoized(t *testing.T) {
	f := newCountingFiler(map[int]*types.Issue{})
	f.placementErr = errors.New("rate limited")
	for range 2 {
		if _, err := f.placement(context.Background(), 9); err == nil {
			t.Fatal("want the placement error")
		}
	}
	if f.placementReads[9] != 2 {
		t.Fatalf("placement reads %d, want 2", f.placementReads[9])
	}
}

// TestGitHubFiler_FailedEditKeepsTheFreshBody: when the edit fails, the memo
// holds the body AppendPart read, not the part it failed to write.
func TestGitHubFiler_FailedEditKeepsTheFreshBody(t *testing.T) {
	issues := map[int]*types.Issue{7: {Number: 7, NodeID: "N7", State: issueOpen, Body: "# Parent\n"}}
	f := newCountingFiler(issues)
	f.editErr = errors.New("forbidden")
	rec := partRecord("dw-new", 7)
	if _, err := f.AppendPart(context.Background(), 7, rec); err == nil || !strings.Contains(err.Error(), "#7") {
		t.Fatalf("want an edit error naming #7, got %v", err)
	}
	if strings.Contains(f.issues[7].Body, Marker(rec)) {
		t.Fatal("the memo claims a part the edit never wrote")
	}
}

// TestNewGitHubFiler_WiresTheMemo: the constructor sets every seam and memo
// the methods use, so none is nil in production.
func TestNewGitHubFiler_WiresTheMemo(t *testing.T) {
	g := NewGitHubFiler(nil, "o", "r", 1, gh.OwnerTypeOrg)
	if g.issues == nil || g.placements == nil || g.subs == nil {
		t.Fatal("a memo map is nil")
	}
	if g.trusted == nil {
		t.Fatal("the trust memo is nil")
	}
	if g.fetchIssue == nil || g.fetchPlacement == nil || g.editIssue == nil ||
		g.searchIssues == nil || g.candidateTrusted == nil {
		t.Fatal("a forge seam is nil")
	}
}

// fileMatchFiler is a parent #7 with no labels or epic, and two open issues
// that qualify by naming the record's file: #20 by an untrusted author (the
// lower number, which PickSubsystemIssue prefers among equals) and #30.
func fileMatchFiler(withTrusted bool) *countingFiler {
	issues := map[int]*types.Issue{7: {Number: 7, NodeID: "N7", State: issueOpen}}
	f := newCountingFiler(issues)
	hits := []types.Issue{{Number: 20, URL: "u20", State: issueOpen, Body: "Breaks in internal/x/store.go"}}
	if withTrusted {
		hits = append(hits, types.Issue{Number: 30, URL: "u30", State: issueOpen, Body: "Tidy internal/x/store.go"})
	}
	f.search[`"internal/x/store.go" in:body`] = hits
	f.untrusted[20] = true
	return f
}

// TestGitHubFiler_SubsystemSkipsUntrustedCandidate: a candidate that
// qualifies by file match but whose author fails the trust gate is never
// returned; the next qualifying trusted candidate is (#2872).
func TestGitHubFiler_SubsystemSkipsUntrustedCandidate(t *testing.T) {
	f := fileMatchFiler(true)
	n, url, err := f.FindOpenInSubsystem(context.Background(), 7, []string{"internal/x/store.go"}, "low")
	if err != nil {
		t.Fatal(err)
	}
	if n != 30 || url != "u30" {
		t.Fatalf("picked #%d (%s), want the trusted #30", n, url)
	}
	if f.trustReads[20] != 1 || f.trustReads[30] != 1 {
		t.Fatalf("trust reads = %v, want #20 and #30 once each", f.trustReads)
	}
}

// TestGitHubFiler_SubsystemUntrustedOnlyFilesAlone: when the only qualifying
// candidate is untrusted, nothing is returned, so the record is filed on its
// own.
func TestGitHubFiler_SubsystemUntrustedOnlyFilesAlone(t *testing.T) {
	f := fileMatchFiler(false)
	n, _, err := f.FindOpenInSubsystem(context.Background(), 7, []string{"internal/x/store.go"}, "low")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("picked the untrusted #%d, want 0", n)
	}
}

// TestGitHubFiler_SubsystemTrustDecidedOncePerRun: two records considering
// the same candidates ask the trust gate about each once.
func TestGitHubFiler_SubsystemTrustDecidedOncePerRun(t *testing.T) {
	f := fileMatchFiler(true)
	for range 2 {
		if n, _, err := f.FindOpenInSubsystem(context.Background(), 7, []string{"internal/x/store.go"}, "low"); err != nil || n != 30 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
	if f.trustReads[20] != 1 || f.trustReads[30] != 1 {
		t.Fatalf("trust reads = %v, want one each", f.trustReads)
	}
}

// TestGitHubFiler_SubsystemTrustedPickStillMeetsThePriorityGuard: after the
// untrusted pick is dropped, the trusted one is still refused when the record
// outranks its board Priority, so the record is filed on its own.
func TestGitHubFiler_SubsystemTrustedPickStillMeetsThePriorityGuard(t *testing.T) {
	f := fileMatchFiler(true)
	n, _, err := f.FindOpenInSubsystem(context.Background(), 7, []string{"internal/x/store.go"}, "critical")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("picked #%d, want 0: a critical record outranks P2", n)
	}
}
