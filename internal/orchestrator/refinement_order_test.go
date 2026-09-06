package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// --- helpers ---------------------------------------------------------------

func boardNode(repo string, number int, status, priority string, labels ...string) *depgraph.Node {
	return &depgraph.Node{
		Repo: repo, Number: number, Title: "issue", State: "OPEN",
		BoardStatus: status, Priority: priority, Labels: labels,
		AuthorAssociation: "OWNER",
	}
}

func unrefined(number int, labels ...string) gh.UnrefinedIssue {
	return gh.UnrefinedIssue{
		Number: number, Title: "issue", Labels: labels,
		AuthorAssociation: "OWNER",
	}
}

// viewOver builds a refinementDispatchView the way refinementView() does —
// including the derived tier cap — but from an explicit node set, so a test can
// state the board it means without standing up a graph builder.
func viewOver(backlogEnabled bool, nodes map[string]*depgraph.Node) refinementDispatchView {
	v := refinementDispatchView{
		nodes:          snapshotRefinementNodes(nodes),
		holds:          map[string]string{},
		openPR:         map[string]bool{},
		excludeLabels:  defaultExcludeLabels,
		backlogEnabled: backlogEnabled,
	}
	v.higherTierPending = v.hasHigherTierPending()
	return v
}

func planNumbers(plan []refinementCandidate) []int {
	out := make([]int, 0, len(plan))
	for _, c := range plan {
		out = append(out, c.issue.Number)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- ordering --------------------------------------------------------------

// TestPlanRefinement_OrdersByDispatchTier is the core of #1514: candidates come
// back in the order the DISPATCH scan will consume them — board Ready first,
// then Backlog-with-a-priority — and within a tier by the dispatch scan's own
// priority ordering, not by issue age.
func TestPlanRefinement_OrdersByDispatchTier(t *testing.T) {
	const repo = "o/r"
	nodes := map[string]*depgraph.Node{
		repo + "#10": boardNode(repo, 10, "Backlog", "P1"), // tier 2
		repo + "#20": boardNode(repo, 20, "Ready", "P3"),   // tier 1
		repo + "#30": boardNode(repo, 30, "Ready", "P0"),   // tier 1, higher priority
		repo + "#40": boardNode(repo, 40, "Backlog", "P0"), // tier 2, higher priority
	}
	// Oldest-first is the order the listing hands us; it must not survive.
	issues := []gh.UnrefinedIssue{unrefined(10), unrefined(20), unrefined(30), unrefined(40)}

	plan, skips := viewOver(true, nodes).planRefinement(repo, issues)
	if len(skips) != 0 {
		t.Fatalf("no candidate should be skipped here, got %v", skips)
	}
	want := []int{30, 20, 40, 10}
	if got := planNumbers(plan); !equalInts(got, want) {
		t.Fatalf("dispatch-order plan = %v, want %v", got, want)
	}
}

// TestPlanRefinement_TierThreeIsOldestFirstAndLast proves tier 3 keeps the
// pre-#1514 ordering (oldest first) and never displaces a board issue. The tier
// cap is inert here because the board issue is already refined — the whole
// point being that "higher tier pending" means UNREFINED, not merely present.
func TestPlanRefinement_TierThreeIsOldestFirstAndLast(t *testing.T) {
	const repo = "o/r"
	nodes := map[string]*depgraph.Node{
		repo + "#5": boardNode(repo, 5, "Ready", "P0", gh.LabelRefined),
	}
	issues := []gh.UnrefinedIssue{unrefined(99), unrefined(7)}

	plan, skips := viewOver(true, nodes).planRefinement(repo, issues)
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if got := planNumbers(plan); !equalInts(got, []int{7, 99}) {
		t.Fatalf("tier-3 order = %v, want [7 99] (oldest first)", got)
	}
}

// TestPlanRefinement_CapsPerCycle: the per-repo cap of five survives the
// reordering — what changed is WHICH five, not how many.
func TestPlanRefinement_CapsPerCycle(t *testing.T) {
	const repo = "o/r"
	nodes := map[string]*depgraph.Node{}
	var issues []gh.UnrefinedIssue
	for i := 1; i <= 8; i++ {
		nodes[repo+"#"+strconv.Itoa(i)] = boardNode(repo, i, "Ready", "P2")
		issues = append(issues, unrefined(i))
	}
	plan, _ := viewOver(false, nodes).planRefinement(repo, issues)
	if len(plan) != refinementCandidatesPerCycle {
		t.Fatalf("plan size = %d, want %d", len(plan), refinementCandidatesPerCycle)
	}
}

// --- skip classes ----------------------------------------------------------

// TestPlanRefinement_SkipClasses walks every class of issue that can never
// dispatch, so refining it would spend a model call on a body no pipeline will
// read.
func TestPlanRefinement_SkipClasses(t *testing.T) {
	const repo = "o/r"
	const key = repo + "#1"

	cases := []struct {
		name    string
		node    *depgraph.Node
		labels  []string
		mutate  func(v *refinementDispatchView)
		want    string
		backlog bool
	}{
		{
			name:    "human-only exclude label",
			node:    boardNode(repo, 1, "Ready", "P0", "owner-action"),
			labels:  []string{"owner-action"},
			want:    refinementSkipExcludedLabel,
			backlog: true,
		},
		{
			name:    "blocked label",
			node:    boardNode(repo, 1, "Ready", "P0", "blocked"),
			labels:  []string{"blocked"},
			want:    refinementSkipExcludedLabel,
			backlog: true,
		},
		{
			name:    "board status In progress",
			node:    boardNode(repo, 1, "In progress", "P0"),
			want:    refinementSkipBoardStatus,
			backlog: true,
		},
		{
			name:    "board status In review",
			node:    boardNode(repo, 1, "In review", "P0"),
			want:    refinementSkipBoardStatus,
			backlog: true,
		},
		{
			name:    "board status Done",
			node:    boardNode(repo, 1, "Done", "P0"),
			want:    refinementSkipBoardStatus,
			backlog: true,
		},
		{
			name:    "open PR on the issue",
			node:    boardNode(repo, 1, "Ready", "P0"),
			mutate:  func(v *refinementDispatchView) { v.openPR[key] = true },
			want:    refinementSkipOpenPR,
			backlog: true,
		},
		{
			name:    "held for a human decision",
			node:    boardNode(repo, 1, "Ready", "P0"),
			mutate:  func(v *refinementDispatchView) { v.holds[key] = HoldArchitectureApproval },
			want:    refinementSkipHumanHold,
			backlog: true,
		},
		{
			name:    "tier 3 with refinement_backlog off",
			node:    nil,
			want:    refinementSkipBacklogOff,
			backlog: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := map[string]*depgraph.Node{}
			if tc.node != nil {
				nodes[key] = tc.node
			}
			v := viewOver(tc.backlog, nodes)
			if tc.mutate != nil {
				tc.mutate(&v)
				v.higherTierPending = v.hasHigherTierPending()
			}
			plan, skips := v.planRefinement(repo, []gh.UnrefinedIssue{unrefined(1, tc.labels...)})
			if len(plan) != 0 {
				t.Fatalf("candidate should have been skipped, plan=%v", planNumbers(plan))
			}
			if got := skips[tc.want]; len(got) != 1 || got[0] != 1 {
				t.Fatalf("skip class %q = %v, want [1] (all skips: %v)", tc.want, got, skips)
			}
		})
	}
}

// --- tier cap --------------------------------------------------------------

// TestPlanRefinement_TierCapHoldsBudgetForHigherTiers is #1514 point 3: while a
// tier-1 or tier-2 issue ANYWHERE in the workspace is unrefined, the hourly
// rate rail is not spent on tier 3 — including in a different repo, which is
// why the cap is derived from the whole graph rather than per repo.
func TestPlanRefinement_TierCapHoldsBudgetForHigherTiers(t *testing.T) {
	nodes := map[string]*depgraph.Node{
		"o/other#5": boardNode("o/other", 5, "Ready", "P1"), // unrefined tier 1, another repo
	}
	v := viewOver(true, nodes)
	if !v.higherTierPending {
		t.Fatal("an unrefined Ready issue in another repo must set higherTierPending")
	}

	plan, skips := v.planRefinement("o/r", []gh.UnrefinedIssue{unrefined(1)})
	if len(plan) != 0 {
		t.Fatalf("tier 3 must not be refined while tier 1/2 work is unrefined, got %v", planNumbers(plan))
	}
	if len(skips[refinementSkipTierCap]) != 1 {
		t.Fatalf("expected the tier-cap skip class, got %v", skips)
	}

	// Once that issue is refined the cap lifts and tier 3 is eligible again.
	nodes["o/other#5"].Labels = []string{gh.LabelRefined}
	plan, _ = viewOver(true, nodes).planRefinement("o/r", []gh.UnrefinedIssue{unrefined(1)})
	if len(plan) != 1 {
		t.Fatalf("tier 3 must be refined once nothing higher-tier is unrefined, got %v", planNumbers(plan))
	}
}

// TestPlanRefinement_BacklogOffNeverTouchesTierThree is #1514 point 5: with
// autonomous.refinement_backlog false (the default), tier 3 is never refined —
// not even when nothing higher-tier is pending. The one exception is an issue
// the operator labelled auto-process, which is them asking for this issue by
// name.
func TestPlanRefinement_BacklogOffNeverTouchesTierThree(t *testing.T) {
	const repo = "o/r"
	v := viewOver(false, map[string]*depgraph.Node{})
	if v.higherTierPending {
		t.Fatal("empty board must not report higher-tier work pending")
	}

	plan, skips := v.planRefinement(repo, []gh.UnrefinedIssue{
		unrefined(1),
		unrefined(2, gh.LabelAutoProcess),
	})
	if got := planNumbers(plan); !equalInts(got, []int{2}) {
		t.Fatalf("plan = %v, want only the auto-process issue [2]", got)
	}
	if len(skips[refinementSkipBacklogOff]) != 1 || skips[refinementSkipBacklogOff][0] != 1 {
		t.Fatalf("expected #1 skipped as %s, got %v", refinementSkipBacklogOff, skips)
	}
}

// TestDefaultAutonomousConfig_RefinementBacklogOff pins the default: the cost
// opt-in is opt-IN.
func TestDefaultAutonomousConfig_RefinementBacklogOff(t *testing.T) {
	if DefaultAutonomousConfig().RefinementBacklog {
		t.Fatal("autonomous.refinement_backlog must default to false (#1514)")
	}
}

// --- end-to-end through the cycle -----------------------------------------

// tieredIssuesServer answers ListIssuesExcludingLabels with three unrefined
// issues in GitHub's own (oldest-first) order.
func tieredIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveRepoLabels(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {"repository": {"issues": {
				"pageInfo": {"hasNextPage": false, "endCursor": ""},
				"nodes": [
					{"number": 1, "title": "oldest, off board", "createdAt": "2026-01-01T00:00:00Z", "authorAssociation": "OWNER", "labels": {"nodes": []}},
					{"number": 2, "title": "backlog with priority", "createdAt": "2026-01-02T00:00:00Z", "authorAssociation": "OWNER", "labels": {"nodes": []}},
					{"number": 3, "title": "ready", "createdAt": "2026-01-03T00:00:00Z", "authorAssociation": "OWNER", "labels": {"nodes": []}}
				]
			}}}
		}`))
	}))
}

// TestRunRefinementCycle_RefinesInDispatchOrder is the wiring assertion: the
// cycle itself (not just planRefinement) picks the Ready issue before the
// prioritized Backlog one and leaves the oldest, off-board issue alone under
// the default backlog gate.
//
// The ORDER is asserted from the cycle's own plan line, which is written
// synchronously as the candidates are chosen. The dispatch callbacks are not
// an order oracle: refineIssue runs each candidate in its own goroutine, so
// with more than one slot their callbacks legitimately interleave.
func TestRunRefinementCycle_RefinesInDispatchOrder(t *testing.T) {
	logs := withCapturedLog(t)
	srv := tieredIssuesServer(t)
	defer srv.Close()

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 3
	as := NewAutonomousScheduler(sched, client, []depgraph.RepoConfig{
		{Owner: "o", Name: "r"},
	}, nil, cfg, t.TempDir())
	as.state.Status = "running"
	as.graphCache = &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"o/r#2": boardNode("o/r", 2, "Backlog", "P1"),
		"o/r#3": boardNode("o/r", 3, "Ready", "P2"),
	}}
	as.graphCacheAt = time.Now()

	var mu sync.Mutex
	refined := map[int]bool{}
	as.WithRefinementRunner(func(_ context.Context, _, _ string, n int) error {
		mu.Lock()
		refined[n] = true
		mu.Unlock()
		return nil
	})
	as.markRefinedFn = func(context.Context, string, string, int) error { return nil }

	as.runRefinementCycle(context.Background())
	as.drainBackground()

	if out := logs.String(); !strings.Contains(out, "candidate(s) in dispatch order: [3 2]") {
		t.Fatalf("expected the plan to order Ready #3 before prioritized-Backlog #2, got:\n%s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if !refined[3] || !refined[2] {
		t.Fatalf("both board issues should have been refined, got %v", refined)
	}
	if refined[1] {
		t.Fatal("#1 is off the board (tier 3) and refinement_backlog is off — it must not be refined")
	}
}

// --- the pre-dispatch hook -------------------------------------------------

func hookScheduler(t *testing.T, slots int) *AutonomousScheduler {
	t.Helper()
	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = slots
	as := NewAutonomousScheduler(nil, nil, []depgraph.RepoConfig{{Owner: "o", Name: "r"}},
		nil, cfg, t.TempDir())
	as.state.Status = "running"
	as.markRefinedFn = func(context.Context, string, string, int) error { return nil }
	return as
}

func hookGraph(labels ...string) *depgraph.Graph {
	return &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"o/r#7": boardNode("o/r", 7, "Ready", "P0", labels...),
	}}
}

var hookItem = CandidateItem{Repo: "o/r", Number: 7, Title: "issue", BoardStatus: "Ready", Priority: "P0"}

// TestRefineBeforeDispatch_RefinesThenDispatches is #1514 point 4's happy path:
// the issue about to dispatch is unrefined and a slot is free, so it is refined
// FIRST — synchronously, which is what makes "then dispatched" true rather than
// a race the caller wins by accident.
func TestRefineBeforeDispatch_RefinesThenDispatches(t *testing.T) {
	as := hookScheduler(t, 1)
	refined := make(chan int, 1)
	as.WithRefinementRunner(func(_ context.Context, _, _ string, n int) error { refined <- n; return nil })

	if !as.refineBeforeDispatch(context.Background(), hookGraph(), hookItem) {
		t.Fatal("hook should have refined the unrefined item")
	}
	select {
	case n := <-refined:
		if n != 7 {
			t.Fatalf("refined #%d, want #7", n)
		}
	default:
		t.Fatal("refinement must have completed before the hook returned")
	}
	// The slot it took is released, so the next dispatch is not starved.
	if len(as.refinementSem) != 0 {
		t.Fatalf("refinement slot leaked: %d held", len(as.refinementSem))
	}
}

// TestRefineBeforeDispatch_NoSlotDispatchesUnrefined: a busy refinement slot
// must delay the dispatch by nothing at all — the issue goes out unrefined and
// one line says so.
func TestRefineBeforeDispatch_NoSlotDispatchesUnrefined(t *testing.T) {
	logs := withCapturedLog(t)
	as := hookScheduler(t, 1)
	as.WithRefinementRunner(func(_ context.Context, _, _ string, _ int) error {
		t.Error("no refinement may run while the only slot is held")
		return nil
	})
	as.refinementSem <- struct{}{} // the one slot is occupied

	if as.refineBeforeDispatch(context.Background(), hookGraph(), hookItem) {
		t.Fatal("hook must not refine when no slot is free")
	}
	if out := logs.String(); !strings.Contains(out, "dispatching o/r#7 unrefined") ||
		!strings.Contains(out, "no refinement slot free") {
		t.Fatalf("expected an explicit unrefined-dispatch line, got:\n%s", out)
	}
}

// TestRefineBeforeDispatch_RateRailExhaustedDispatchesUnrefined: the hook is
// bounded by the SAME hourly rail as the scan, and a spent rail is not a reason
// to hold up a dispatch.
func TestRefineBeforeDispatch_RateRailExhaustedDispatchesUnrefined(t *testing.T) {
	logs := withCapturedLog(t)
	as := hookScheduler(t, 1)
	as.WithRefinementRunner(func(_ context.Context, _, _ string, _ int) error {
		t.Error("no refinement may run once the rate rail is spent")
		return nil
	})
	as.safetyRails = NewSafetyRails(SafetyConfig{RefinementRateLimitPerHour: 1})
	as.safetyRails.RecordRefinementStart()

	if as.refineBeforeDispatch(context.Background(), hookGraph(), hookItem) {
		t.Fatal("hook must not refine once the rail is exhausted")
	}
	if out := logs.String(); !strings.Contains(out, "refinement rate limit") {
		t.Fatalf("expected the rail reason in the unrefined-dispatch line, got:\n%s", out)
	}
	// The slot taken for the attempt must be given back, or one exhausted rail
	// permanently starves the refinement scan.
	if len(as.refinementSem) != 0 {
		t.Fatalf("refinement slot leaked after a rail refusal: %d held", len(as.refinementSem))
	}
}

// TestRefineBeforeDispatch_AlreadyRefinedIsSilent: the hook is called on every
// dispatch, so the common case — an already-refined issue — must do nothing and
// say nothing.
func TestRefineBeforeDispatch_AlreadyRefinedIsSilent(t *testing.T) {
	logs := withCapturedLog(t)
	as := hookScheduler(t, 1)
	as.WithRefinementRunner(func(_ context.Context, _, _ string, _ int) error {
		t.Error("must not refine an already-refined issue")
		return nil
	})

	if as.refineBeforeDispatch(context.Background(), hookGraph(gh.LabelRefined), hookItem) {
		t.Fatal("hook must not refine an issue carrying pipeline:refined")
	}
	if out := logs.String(); strings.Contains(out, "unrefined") {
		t.Fatalf("no line should be logged for an already-refined issue, got:\n%s", out)
	}
}

// TestRefineBeforeDispatch_TruncatedLabelsFailClosed: when the board scan could
// not return the whole label list we cannot prove the issue is unrefined, and a
// wrong answer here rewrites a human-reviewed body (#993/#998).
func TestRefineBeforeDispatch_TruncatedLabelsFailClosed(t *testing.T) {
	as := hookScheduler(t, 1)
	as.WithRefinementRunner(func(_ context.Context, _, _ string, _ int) error {
		t.Error("must not refine on a truncated label list")
		return nil
	})
	g := hookGraph()
	g.Nodes["o/r#7"].LabelsTruncated = true

	if as.refineBeforeDispatch(context.Background(), g, hookItem) {
		t.Fatal("hook must fail closed on a truncated label list")
	}
}

// TestRefineBeforeDispatch_UntrustedAuthorDispatchesUnrefined: the #270 gate is
// not a slot question — a stranger's issue text never reaches the model, and
// the dispatch decision is unaffected.
func TestRefineBeforeDispatch_UntrustedAuthorDispatchesUnrefined(t *testing.T) {
	logs := withCapturedLog(t)
	as := hookScheduler(t, 1)
	as.WithRefinementRunner(func(_ context.Context, _, _ string, _ int) error {
		t.Error("must not refine an untrusted author's issue")
		return nil
	})
	g := hookGraph()
	g.Nodes["o/r#7"].AuthorAssociation = "NONE"

	if as.refineBeforeDispatch(context.Background(), g, hookItem) {
		t.Fatal("hook must refuse an untrusted author")
	}
	if out := logs.String(); !strings.Contains(out, "untrusted author") {
		t.Fatalf("expected the untrusted-author reason, got:\n%s", out)
	}
}
