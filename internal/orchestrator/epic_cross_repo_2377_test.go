package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/intelligence/batch"
	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// Regression tests for #2377: an epic is named by its own repository and
// number at every call site that keys on it. Throughout, the sub-issue is
// example-org/app#21, its epic is example-org/platform#20, and
// example-org/app has an unrelated #20 of its own.

// crossRepoEpicScheduler returns a Scheduler launched in the app checkout,
// with checkouts registered for example-org/app (launch) and
// example-org/platform (platform). The launch identity is known, so a
// repository with no checkout is refused rather than rooted at launch (#882).
func crossRepoEpicScheduler(launch, platform string) *Scheduler {
	s := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: launch})
	s.launchRepo = "example-org/app"
	s.WithRepoPathResolver(func(repo string) string {
		switch repo {
		case "example-org/app":
			return launch
		case "example-org/platform":
			return platform
		}
		return ""
	})
	return s
}

// writeEpicCtxAt writes epic-context-{N}.json into root's pipeline state.
func writeEpicCtxAt(t *testing.T, root string, epicNumber int, note string) {
	t.Helper()
	dir := layouttest.PipelineDir(t, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(epicContext{
		EpicNumber:     epicNumber,
		SharedResearch: sharedResearch{CodebaseNotes: []string{note}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "epic-context-"+strconv.Itoa(epicNumber)+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A sub-issue's prompt gets its parent's accumulated context from the parent's
// own repository. The launch checkout's epic-context-20.json belongs to
// example-org/app#20 and used to be injected into a sub-issue of
// example-org/platform#20.
func TestEpicContextSection_ReadsTheParentInItsOwnRepository(t *testing.T) {
	launch, platform := layouttest.Repo(t), layouttest.Repo(t)
	writeEpicCtxAt(t, launch, 20, "note recorded for example-org/app#20")
	writeEpicCtxAt(t, platform, 20, "note recorded for example-org/platform#20")
	s := crossRepoEpicScheduler(launch, platform)

	got := s.epicContextSection(types.BoardItem{Repo: "example-org/app", Number: 21,
		ParentNumber: 20, ParentRepo: "example-org/platform"})
	if !strings.Contains(got, "example-org/platform#20") || strings.Contains(got, "example-org/app#20") {
		t.Errorf("cross-repository parent got the wrong epic's context:\n%s", got)
	}

	got = s.epicContextSection(types.BoardItem{Repo: "example-org/app", Number: 22, ParentNumber: 20})
	if !strings.Contains(got, "example-org/app#20") || strings.Contains(got, "example-org/platform#20") {
		t.Errorf("same-repository parent got the wrong epic's context:\n%s", got)
	}

	// No checkout of the parent's repository: nothing, never the launch
	// checkout's same-numbered file.
	if got := s.epicContextSection(types.BoardItem{Repo: "example-org/app", Number: 23,
		ParentNumber: 20, ParentRepo: "example-org/elsewhere"}); got != "" {
		t.Errorf("parent with no checkout here got context:\n%s", got)
	}
	if got := s.epicContextSection(types.BoardItem{Repo: "example-org/app", Number: 24}); got != "" {
		t.Errorf("item without a parent got context:\n%s", got)
	}
}

// The wave orchestrator writes an epic's context into its own repository's
// pipeline state, where the reader above looks for it; two epics that share a
// number no longer share one file.
func TestWaveOrchestrator_EpicContextLivesInTheEpicsRepository(t *testing.T) {
	launch, platform := layouttest.Repo(t), layouttest.Repo(t)
	writeEpicCtxAt(t, launch, 20, "note recorded for example-org/app#20")
	s := crossRepoEpicScheduler(launch, platform)

	wo := newWaveOrchestrator(s, 20, "example-org/platform", 2, 0)
	wo.appendSubIssueToEpicContext(teams.SubIssue{Number: 21, Files: []string{"internal/platform/api.go"}})

	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, platform), "epic-context-20.json")); err != nil {
		t.Fatalf("epic context not written in the epic's repository: %v", err)
	}
	launchData, err := os.ReadFile(filepath.Join(layouttest.PipelineDir(t, launch), "epic-context-20.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(launchData), "internal/platform/api.go") {
		t.Errorf("example-org/platform#20's findings were merged into example-org/app#20's file:\n%s", launchData)
	}

	got := s.epicContextSection(types.BoardItem{Repo: "example-org/app", Number: 22,
		ParentNumber: 20, ParentRepo: "example-org/platform"})
	if !strings.Contains(got, "internal/platform/api.go") {
		t.Errorf("sibling did not receive the findings recorded for its epic:\n%s", got)
	}
}

// ensureEpicBranchForItem creates no epic branch in the sub-issue's checkout
// for a parent in another repository. epic/2000-* there is the branch of that
// repository's own #2000, and a second one would be stranded: the epic's
// completion PR is opened in the epic's repository.
func TestEnsureEpicBranchForItem_CrossRepoParentGetsNoEpicBranchHere(t *testing.T) {
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "true")
	root := gitWorkspace(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, root, "init", "--bare", remote)
	gitIn(t, root, "remote", "add", "origin", remote)
	gitIn(t, root, "push", "origin", "main")
	gitIn(t, root, "push", "origin", "main:refs/heads/epic/2000-unrelated-issue")

	// No issue fixtures: reading any epic title fails the call, so an empty
	// result also proves no title was read.
	s := &Scheduler{issueSvc: newMockIssueSvc()}

	if failure := s.ensureEpicBranchForItem(context.Background(), root, types.BoardItem{
		Number: 2001, ParentNumber: 2000, ParentRepo: "Org/platform", Repo: "Org/repo",
	}); failure != "" {
		t.Fatalf("epic branch failure = %q, want the cross-repository parent skipped", failure)
	}
	heads := remoteHeads(t, root)
	if n := strings.Count(heads, "refs/heads/epic/"); n != 1 {
		t.Fatalf("remote heads:\n%s\nwant only this repository's epic/2000-unrelated-issue", heads)
	}

	// A parent in this repository (matched case-insensitively) still gets its
	// branch.
	if failure := s.ensureEpicBranchForItem(context.Background(), root, types.BoardItem{
		Number: 3001, ParentNumber: 3000, ParentRepo: "org/REPO", ParentTitle: "Local Epic", Repo: "Org/repo",
	}); failure != "" {
		t.Fatalf("epic branch failure = %q for a parent in this repository", failure)
	}
	if heads := remoteHeads(t, root); !strings.Contains(heads, "refs/heads/epic/3000-local-epic") {
		t.Fatalf("remote heads:\n%s\nwant epic/3000-local-epic for the same-repository parent", heads)
	}
}

func remoteHeads(t *testing.T, root string) string {
	t.Helper()
	out, err := gittest.Command(root, "ls-remote", "--heads", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v\n%s", err, out)
	}
	return string(out)
}

// The deterministic pickup runner hands EnsureIssueBranch a parent only when
// it lives in the issue's own repository, so a sub-issue of an epic elsewhere
// is based on its default branch, not on epic/<N>-* of its repository's #N.
// The issue context it writes records the same parent_issue, which is read as
// a number in this repository, as `git branch-create` reports it.
func TestDeterministicIssuePickup_EpicBranchOnlyForAParentInThisRepository(t *testing.T) {
	cases := []struct {
		name       string
		parentRepo string
		want       int
	}{
		{"parent in another repository", "example-org/platform", 0},
		{"parent in this repository", "example-org/app", 20},
		{"parent repository not recorded", "", 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := -1
			r := &deterministicIssuePickup{
				ensure: func(_ string, branch string, parent int, _ func() (string, error)) (git.IssueBranchResult, error) {
					got = parent
					return git.IssueBranchResult{Branch: branch, BaseBranch: "main", Action: "created"}, nil
				},
				now: time.Now,
			}
			in := pickupInput(layouttest.Repo(t))
			in.Issue.Repo = "example-org/app"
			in.Issue.ParentIssueNumber = 20
			in.Issue.ParentIssueRepo = c.parentRepo
			res, err := r.Run(context.Background(), in)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got != c.want {
				t.Errorf("EnsureIssueBranch parent = %d, want %d", got, c.want)
			}

			data, err := os.ReadFile(res.ContextPath)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				ParentIssue *int `json:"parent_issue"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			gotParent := 0
			if doc.ParentIssue != nil {
				gotParent = *doc.ParentIssue
			}
			if gotParent != c.want {
				t.Errorf("issue context parent_issue = %d, want %d (0 = null)", gotParent, c.want)
			}
		})
	}
}

// EnqueueEpic records the epic's repository on every queued sub-issue. The
// extension bases a slot on epic/<N>-* only when that repository is the
// sub-issue's own: in example-org/app, epic/20-* is the branch of
// example-org/app#20, not of example-org/platform#20.
func TestEnqueueEpic_RecordsTheEpicsRepository(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("example-org", "platform", 20, &types.Issue{
		NodeID: "I_p20", Number: 20, Title: "Platform Epic", State: "OPEN", Repo: "example-org/platform",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_p21", Number: 21, Title: "Platform task", State: "OPEN", Repo: "example-org/platform"},
			{NodeID: "I_a21", Number: 21, Title: "App task", State: "OPEN", Repo: "example-org/app"},
		},
	})
	mock.addIssue("example-org", "platform", 21, &types.Issue{NodeID: "I_p21", Number: 21, State: "OPEN", Repo: "example-org/platform"})
	mock.addIssue("example-org", "app", 21, &types.Issue{NodeID: "I_a21", Number: 21, State: "OPEN", Repo: "example-org/app"})
	s := &Scheduler{issueSvc: mock, repoRunning: map[string]int{}, mergeLocks: map[string]*sync.Mutex{}}

	if err := s.EnqueueEpic(context.Background(), "example-org", "platform", 20, "Platform Epic", nil, nil, ""); err != nil {
		t.Fatalf("EnqueueEpic: %v", err)
	}
	if len(s.queue) != 2 {
		t.Fatalf("queue = %+v, want both sub-issues", s.queue)
	}
	for _, it := range s.queue {
		if it.EpicNumber == nil || *it.EpicNumber != 20 || it.EpicRepo != "example-org/platform" {
			t.Errorf("%s#%d: epic = %v in %q, want example-org/platform#20", it.Repo, it.IssueNumber, it.EpicNumber, it.EpicRepo)
		}
	}
	if s.queue[0].Repo != "example-org/platform" || s.queue[1].Repo != "example-org/app" {
		t.Errorf("queued repositories = %s, %s; want each sub-issue's own", s.queue[0].Repo, s.queue[1].Repo)
	}
}

// A wave runs each sub-issue in its own repository. fetchSubIssueDetails read
// example-org/app#21 from example-org/app, but the item the wave ran carried
// the epic's repository, so it ran as example-org/platform#21.
func TestWaveOrchestrator_RunsASubIssueInItsOwnRepository(t *testing.T) {
	issueSvc := newMockEpicIssueSvc()
	issueSvc.addEpic("example-org", "platform", 20, &types.EpicProgress{
		Number: 20, Title: "Platform Epic", Repo: "example-org/platform", Total: 2, Open: 2,
		SubIssues: []types.SubIssueRef{
			{Number: 21, Title: "App task", State: "OPEN", Repo: "example-org/app"},
			{Number: 22, Title: "Platform task", State: "OPEN", Repo: "example-org/platform"},
		},
	})
	issueSvc.addIssue("example-org", "app", 21, &types.Issue{Number: 21, Title: "App task"})
	issueSvc.addIssue("example-org", "platform", 22, &types.Issue{Number: 22, Title: "Platform task"})
	wo := newWaveOrchestrator(&Scheduler{issueSvc: issueSvc}, 20, "example-org/platform", 2, 0)
	epicItem := types.BoardItem{Number: 20, Repo: "example-org/platform"}

	subIssues, _, _, err := wo.fetchSubIssueDetails(context.Background(), "example-org", "platform", epicItem)
	if err != nil {
		t.Fatalf("fetchSubIssueDetails: %v", err)
	}
	if len(subIssues) != 2 {
		t.Fatalf("sub-issues = %+v, want both", subIssues)
	}
	want := map[int]string{21: "example-org/app", 22: "example-org/platform"}
	for _, si := range subIssues {
		item := wo.subIssueItem(si, epicItem)
		if item.Repo != want[si.Number] {
			t.Errorf("#%d runs in %q, want %q", si.Number, item.Repo, want[si.Number])
		}
		if item.ParentNumber != 20 || item.ParentRepo != "example-org/platform" {
			t.Errorf("#%d parent = %s#%d, want example-org/platform#20", si.Number, item.ParentRepo, item.ParentNumber)
		}
	}
}

// readyBoardNode is one Ready issue on a fake board read, authored by the
// owner. subIssues and blockedBy are JSON node lists; parent is the parent
// object, or "null".
func readyBoardNode(id string, number int, repo, title, labels, subIssues, blockedBy, parent string) string {
	return fmt.Sprintf(`{
		"id": %q,
		"content": {
			"__typename": "Issue", "number": %d, "title": %q, "state": "OPEN",
			"url": "https://github.com/%s/issues/%d",
			"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
			"authorAssociation": "OWNER",
			"labels": {"nodes": [%s]},
			"repository": {"nameWithOwner": %q},
			"subIssues": {"nodes": [%s]},
			"blockedBy": {"nodes": [%s]},
			"blocking": {"nodes": []},
			"parent": %s
		},
		"fieldValues": {"nodes": []}
	}`, id, number, title, repo, number, labels, repo, subIssues, blockedBy, parent)
}

// PickNext holds a sub-issue whose epic is blocked, and only that sub-issue.
// Keyed by number alone, example-org/platform#21 was held because
// example-org/app#21 is a sub-issue of the blocked example-org/platform#20.
func TestPickNext_EpicBlockingHoldsOnlyTheEpicsOwnSubIssue(t *testing.T) {
	nodes := strings.Join([]string{
		readyBoardNode("ITEM_EPIC", 20, "example-org/platform", "Platform Epic", `{"name": "type:epic"}`,
			`{"id": "I_a21", "number": 21, "title": "App task", "state": "OPEN", "repository": {"nameWithOwner": "example-org/app"}}`,
			`{"id": "I_p5", "number": 5, "title": "Open blocker", "state": "OPEN", "repository": {"nameWithOwner": "example-org/platform"}}`,
			"null"),
		readyBoardNode("ITEM_A21", 21, "example-org/app", "App task", "", "", "",
			`{"number": 20, "title": "Platform Epic", "repository": {"nameWithOwner": "example-org/platform"}}`),
		readyBoardNode("ITEM_P21", 21, "example-org/platform", "Unrelated platform task", "", "", "", "null"),
	}, ",")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"organization": {"projectV2": {"id": "PVT_1", "title": "Board",
			"items": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": [%s]}}}}}`, nodes)
	}))
	defer srv.Close()
	client := gh.NewClientWithURL("test-token", srv.URL)
	s := &Scheduler{client: client, boardSvc: gh.NewBoardService(client, "example-org", 1), repoRunning: map[string]int{}}

	item, err := s.PickNext(context.Background())
	if err != nil {
		t.Fatalf("PickNext: %v", err)
	}
	if item == nil || item.Repo != "example-org/platform" || item.Number != 21 {
		t.Fatalf("PickNext = %+v, want example-org/platform#21: the blocked epic's sub-issue is example-org/app#21", item)
	}
}

// platformLaunchedScheduler is crossRepoEpicScheduler launched in the epic's
// repository: the wave orchestrator runs in the checkout of
// example-org/platform, and example-org/app has a checkout of its own.
func platformLaunchedScheduler(platform, app string) *Scheduler {
	s := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: platform})
	s.launchRepo = "example-org/platform"
	s.WithRepoPathResolver(func(repo string) string {
		switch repo {
		case "example-org/app":
			return app
		case "example-org/platform":
			return platform
		}
		return ""
	})
	return s
}

// persistWaveRun persists a run of repo#number into root's pipeline state, as
// runPipeline does: merged runs account for all six stages and end at
// pr-merge, failed ones stop at feature-dev.
func persistWaveRun(t *testing.T, root, repo string, number int, merged bool, inputTokens int) {
	t.Helper()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	rs := state.NewRuntimeState(repo, number, "item", id)
	rs.InputTokens = inputTokens
	rs.Stage = state.StageFeatureDev
	if merged {
		rs.Stage = state.StagePRMerge
		for _, st := range []state.PipelineStage{state.StageIssuePickup, state.StageFeaturePlanning,
			state.StageFeatureDev, state.StageFeatureValidate, state.StagePRCreate, state.StagePRMerge} {
			rs.CompletedStages = append(rs.CompletedStages, state.StageResult{Stage: st})
		}
	}
	if err := persistPipelineState(rs, root); err != nil {
		t.Fatal(err)
	}
}

// describeRun names the run a readback returned, for failure messages.
func describeRun(rs *state.RuntimeState) string {
	if rs == nil {
		return "no run"
	}
	return fmt.Sprintf("%s#%d at %s (%d input tokens)", rs.Repo, rs.IssueNumber, rs.Stage, rs.InputTokens)
}

// A wave reads a sub-issue's run back from the checkout the run was rooted
// in, and only that repository's run. Since a wave runs example-org/app#21 in
// its own repository, its snapshot is in example-org/app's pipeline state;
// read from the launch checkout by number, a merged sub-issue counted as
// failed, or example-org/platform#21's run answered for it.
func TestWaveOrchestrator_ReadsASubIssuesRunInItsOwnRepository(t *testing.T) {
	sub := teams.SubIssue{Number: 21, Repo: "example-org/app"}
	epicItem := types.BoardItem{Number: 20, Repo: "example-org/platform"}

	t.Run("merged in its own checkout", func(t *testing.T) {
		platform, app := layouttest.Repo(t), layouttest.Repo(t)
		s := platformLaunchedScheduler(platform, app)
		wo := newWaveOrchestrator(s, 20, "example-org/platform", 2, 0)
		item := wo.subIssueItem(sub, epicItem)
		// The root runPipeline persists the run's snapshot in.
		runRoot, err := s.resolveRunRoot(item.Repo)
		if err != nil || runRoot != app {
			t.Fatalf("run root = %q (%v), want the app checkout", runRoot, err)
		}
		persistWaveRun(t, runRoot, item.Repo, 21, true, 700)

		ok, rs := wo.readPipelineState(item.Repo, 21)
		if !ok || rs == nil || rs.InputTokens != 700 {
			t.Errorf("readPipelineState = %v, %s; want example-org/app#21's merged run (700 input tokens)", ok, describeRun(rs))
		}
	})

	t.Run("the launch repository's same-numbered run does not answer for it", func(t *testing.T) {
		platform, app := layouttest.Repo(t), layouttest.Repo(t)
		s := platformLaunchedScheduler(platform, app)
		wo := newWaveOrchestrator(s, 20, "example-org/platform", 2, 0)
		persistWaveRun(t, platform, "example-org/platform", 21, true, 999)
		persistWaveRun(t, app, "example-org/app", 21, false, 700)

		ok, rs := wo.readPipelineState("example-org/app", 21)
		if ok || rs == nil || rs.InputTokens != 700 {
			t.Errorf("readPipelineState = %v, %s; want example-org/app#21's failed run (700 input tokens)", ok, describeRun(rs))
		}
	})

	t.Run("one pipeline state holding both repositories' runs", func(t *testing.T) {
		root := layouttest.Repo(t)
		s := platformLaunchedScheduler(root, root)
		wo := newWaveOrchestrator(s, 20, "example-org/platform", 2, 0)
		persistWaveRun(t, root, "example-org/platform", 21, true, 999)
		persistWaveRun(t, root, "example-org/app", 21, false, 700)

		ok, rs := wo.readPipelineState("example-org/app", 21)
		if ok || rs == nil || rs.InputTokens != 700 {
			t.Errorf("readPipelineState = %v, %s; want example-org/app#21's failed run (700 input tokens)", ok, describeRun(rs))
		}
		if ok, rs := wo.readPipelineState("example-org/elsewhere", 21); ok || rs != nil {
			t.Errorf("a repository with no run of #21 read back %v, %s", ok, describeRun(rs))
		}
	})
}

// Everything else a wave keeps per sub-issue is keyed by repository and
// number: the dependency edges, the token budget, the results and the epic
// context's findings. example-org/platform#21 and example-org/app#21 are both
// sub-issues of example-org/platform#20.
func TestWaveOrchestrator_KeysSubIssuesByRepository(t *testing.T) {
	platform, app := layouttest.Repo(t), layouttest.Repo(t)
	s := platformLaunchedScheduler(platform, app)
	wo := newWaveOrchestrator(s, 20, "example-org/platform", 2, 0)
	p21 := teams.SubIssue{Number: 21, Repo: "example-org/platform", Files: []string{"internal/p.go"}}
	a21 := teams.SubIssue{Number: 21, Repo: "example-org/app", Files: []string{"lib/a.dart"}}
	p22 := teams.SubIssue{Number: 22, Repo: "example-org/platform"}
	subs := []teams.SubIssue{p21, a21, p22}

	// example-org/platform#22 is blocked by example-org/app#21 only.
	details := []batch.IssueInput{{Number: 21, Body: "platform body"}, {Number: 21, Body: "app body"}, {Number: 22}}
	deps := wo.detectDependencies(subs, details, map[string][]string{
		wo.subIssueKey(p22): {repoIssueKey("example-org/app", 21)},
	})
	if got := deps[2]; len(got) != 1 || got[0] != 1 {
		t.Errorf("example-org/platform#22 depends on %v, want only index 1 (example-org/app#21)", got)
	}
	if len(deps[0]) != 0 || len(deps[1]) != 0 {
		t.Errorf("the #21s gained edges: %v", deps)
	}

	budget := teams.SplitBudget(subs, 900, teams.StrategyEqual)
	budget.Allocations[1].TokenBudget = 123
	if got := wo.budgetForIssue(budget, a21); got != 123 {
		t.Errorf("example-org/app#21's budget = %d, want its own allocation 123", got)
	}
	if got := wo.budgetForIssue(budget, p21); got != 300 {
		t.Errorf("example-org/platform#21's budget = %d, want 300", got)
	}

	wo.waves = []teams.WaveAssignment{{WaveIndex: 0, Issues: []teams.SubIssue{p21, a21}}}
	wo.recordResult(&AgentResult{IssueNumber: 21, Repo: "example-org/platform", Success: true})
	wo.recordResult(&AgentResult{IssueNumber: 21, Repo: "example-org/app", Success: false})
	if summary := wo.buildSummary(time.Second); summary.TotalIssues != 2 || summary.Failed != 1 {
		t.Errorf("summary = %+v, want two results, one failed", summary)
	}
	wo.persistWaveStatus(wo.buildSummary(time.Second))
	data, err := os.ReadFile(filepath.Join(layouttest.PipelineDir(t, platform), "wave-status-20.json"))
	if err != nil {
		t.Fatalf("wave status not written in the epic's repository: %v", err)
	}
	var status WaveStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Waves) != 1 || status.Waves[0].Status != "failed" ||
		strings.Join(status.Waves[0].Issues, ",") != "example-org/platform#21,example-org/app#21" {
		t.Errorf("wave status = %+v, want the two #21s by repository, and failed", status.Waves)
	}

	wo.appendSubIssueToEpicContext(p21)
	wo.appendSubIssueToEpicContext(a21)
	ec := readEpicContextFile(platform, 20)
	if ec == nil || ec.SubIssueFindings["21"] == nil || ec.SubIssueFindings["example-org/app#21"] == nil {
		t.Fatalf("epic context findings = %+v, want \"21\" and \"example-org/app#21\"", ec)
	}
	if got := ec.SubIssueFindings["21"].FilesTouched; len(got) != 1 || got[0] != "internal/p.go" {
		t.Errorf("example-org/platform#21's findings = %v, overwritten by example-org/app#21's", got)
	}
}

// The wave plan and the wave status are kept in the epic repository's
// pipeline state, named by the epic's number like its context; in the launch
// checkout, example-org/app#20's wave files were example-org/platform#20's.
func TestWaveOrchestrator_WaveFilesLiveInTheEpicsRepository(t *testing.T) {
	launch, platform := layouttest.Repo(t), layouttest.Repo(t)
	s := crossRepoEpicScheduler(launch, platform)
	wo := newWaveOrchestrator(s, 20, "example-org/platform", 2, 0)
	wo.waves = []teams.WaveAssignment{{WaveIndex: 0, Issues: []teams.SubIssue{{Number: 21, Repo: "example-org/app"}}}}

	wo.persistWavePlan()
	wo.persistWaveStatus(wo.buildSummary(time.Second))
	for _, name := range []string{"wave-plan-20.json", "wave-status-20.json"} {
		if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, platform), name)); err != nil {
			t.Errorf("%s not written in the epic's repository: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, launch), name)); err == nil {
			t.Errorf("%s written in the launch checkout, where #20 is example-org/app#20", name)
		}
	}
}

// EnqueueEpic keeps each sub-issue's blockers apart by repository. Keyed by
// number, example-org/app#21's empty blocker list replaced
// example-org/platform#21's, and example-org/platform#21 was queued without
// its open blocker example-org/platform#30.
func TestEnqueueEpic_SameNumberedSubIssuesKeepTheirOwnBlockers(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("example-org", "platform", 20, &types.Issue{
		NodeID: "I_p20", Number: 20, Title: "Platform Epic", State: "OPEN", Repo: "example-org/platform",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_p21", Number: 21, Title: "Platform task", State: "OPEN", Repo: "example-org/platform"},
			{NodeID: "I_a21", Number: 21, Title: "App task", State: "OPEN", Repo: "example-org/app"},
		},
		// The epic's own blocker, in the epic's repository but named without one.
		BlockedBy: []types.BlockingRef{{Number: 5, Title: "Epic blocker", State: "OPEN"}},
	})
	mock.addIssue("example-org", "platform", 21, &types.Issue{NodeID: "I_p21", Number: 21, State: "OPEN", Repo: "example-org/platform",
		BlockedBy: []types.BlockingRef{{Number: 30, Title: "Platform blocker", State: "OPEN", Repo: "example-org/platform"}}})
	mock.addIssue("example-org", "app", 21, &types.Issue{NodeID: "I_a21", Number: 21, State: "OPEN", Repo: "example-org/app"})
	s := &Scheduler{issueSvc: mock, repoRunning: map[string]int{}, mergeLocks: map[string]*sync.Mutex{}}

	if err := s.EnqueueEpic(context.Background(), "example-org", "platform", 20, "Platform Epic", nil, nil, ""); err != nil {
		t.Fatalf("EnqueueEpic: %v", err)
	}
	if len(s.queue) != 2 {
		t.Fatalf("queue = %+v, want both sub-issues", s.queue)
	}
	want := map[string]string{
		"example-org/platform": "[example-org/platform#5 example-org/platform#30]",
		"example-org/app":      "[example-org/platform#5]",
	}
	for _, it := range s.queue {
		var got []string
		for _, b := range it.BlockedBy {
			got = append(got, fmt.Sprintf("%s#%d", b.Repo, b.Number))
		}
		if fmt.Sprint(got) != want[it.Repo] {
			t.Errorf("%s#%d blockers = %v, want %s", it.Repo, it.IssueNumber, got, want[it.Repo])
		}
	}
}

// A queued sub-issue's blocker is refreshed and matched in the blocker's own
// repository. example-org/app#21 is blocked by example-org/platform#5, and
// example-org/app has an unrelated #5.
func TestQueueBlockers_AreReadAndMatchedInTheirOwnRepository(t *testing.T) {
	blocked := QueueItem{Repo: "example-org/app", IssueNumber: 21, Status: "pending",
		BlockedBy: []QueueBlockingRef{{Number: 5, State: "OPEN", Repo: "example-org/platform"}}}

	t.Run("refresh reads the blocker's repository", func(t *testing.T) {
		mock := newMockIssueSvc()
		mock.addIssue("example-org", "platform", 5, &types.Issue{Number: 5, State: "CLOSED"})
		mock.addIssue("example-org", "app", 5, &types.Issue{Number: 5, State: "OPEN"})
		s := &Scheduler{issueSvc: mock}
		s.queue = []QueueItem{blocked}
		s.queue[0].BlockedBy = append([]QueueBlockingRef(nil), blocked.BlockedBy...)

		s.refreshBlockerStates(context.Background())
		if got := s.queue[0].BlockedBy[0].State; got != "CLOSED" {
			t.Errorf("blocker state = %s, want example-org/platform#5's CLOSED", got)
		}
	})

	dequeue := func(queue []QueueItem, running []RunningItem) []string {
		s := &Scheduler{repoRunning: map[string]int{}, mergeLocks: map[string]*sync.Mutex{}, maxPerRepo: 4}
		s.queue = append([]QueueItem(nil), queue...)
		var got []string
		for _, it := range s.DequeueIndependent(context.Background(), 4, running) {
			got = append(got, fmt.Sprintf("%s#%d", it.Repo, it.IssueNumber))
		}
		return got
	}
	appFive := QueueItem{Repo: "example-org/app", IssueNumber: 5, Status: "pending"}

	t.Run("a same-numbered queued issue elsewhere does not hold it", func(t *testing.T) {
		if got := dequeue([]QueueItem{appFive, blocked}, nil); fmt.Sprint(got) != "[example-org/app#5 example-org/app#21]" {
			t.Errorf("dequeued %v, want both: example-org/app#5 is not the blocker", got)
		}
	})
	t.Run("the blocker in flight holds it", func(t *testing.T) {
		got := dequeue([]QueueItem{blocked}, []RunningItem{{Repo: "example-org/platform", Number: 5}})
		if len(got) != 0 {
			t.Errorf("dequeued %v while example-org/platform#5 is running", got)
		}
	})
	t.Run("a same-numbered issue in flight elsewhere does not hold it", func(t *testing.T) {
		got := dequeue([]QueueItem{blocked}, []RunningItem{{Repo: "example-org/app", Number: 5}})
		if fmt.Sprint(got) != "[example-org/app#21]" {
			t.Errorf("dequeued %v, want example-org/app#21: example-org/app#5 is not the blocker", got)
		}
	})
	t.Run("a running item that names no repository holds it", func(t *testing.T) {
		if got := dequeue([]QueueItem{blocked}, []RunningItem{{Number: 5}}); len(got) != 0 {
			t.Errorf("dequeued %v while an unnamed repository's #5 is running", got)
		}
	})
}

// promptCapturingRunner records each stage's prompt. issue-pickup writes its
// output context so feature-planning dispatches; feature-planning fails, which
// ends the run.
type promptCapturingRunner struct {
	mu      sync.Mutex
	prompts map[state.PipelineStage]string
}

func (r *promptCapturingRunner) HonoursPrompt() bool { return true }

func (r *promptCapturingRunner) RunStage(_ context.Context, p StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	if r.prompts == nil {
		r.prompts = map[state.PipelineStage]string{}
	}
	r.prompts[p.Stage] = p.Prompt
	r.mu.Unlock()
	if p.Stage == state.StageFeaturePlanning {
		return &StageRunResult{ExitCode: 1, ErrorText: "planning failed (fixture)"}, nil
	}
	if p.OutputFile != "" {
		data, _ := json.Marshal(map[string]any{"schema_version": "1.0", "issue_number": p.IssueNumber, "ok": true})
		_ = os.MkdirAll(filepath.Dir(p.OutputFile), 0o755)
		_ = os.WriteFile(p.OutputFile, data, 0o644)
	}
	return &StageRunResult{}, nil
}

func (r *promptCapturingRunner) prompt(stage state.PipelineStage) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.prompts[stage]
	return p, ok
}

// runPipeline appends the epic context of a sub-issue's parent from the
// parent's own repository to the feature-planning prompt. Read from the
// checkout the run is rooted in, example-org/app#21 was handed
// example-org/app#20's accumulated findings instead of its epic's.
func TestRunPipeline_PromptCarriesTheParentsEpicContextFromItsOwnRepository(t *testing.T) {
	stubReconcileGhUnreachable(t)
	launch := gitWorkspace(t) // example-org/app's checkout
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, launch, dir)
	}
	gitIn(t, launch, "add", ".")
	gitIn(t, launch, "commit", "-m", "fixture")
	platform := layouttest.Repo(t)
	writeEpicCtxAt(t, launch, 20, "note recorded for example-org/app#20")
	writeEpicCtxAt(t, platform, 20, "note recorded for example-org/platform#20")

	runner := &promptCapturingRunner{}
	s := &Scheduler{
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(launch, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  launch,
		launchRepo:     "example-org/app",
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
	s.WithRepoPathResolver(func(repo string) string {
		switch repo {
		case "example-org/app":
			return launch
		case "example-org/platform":
			return platform
		}
		return ""
	})

	s.runPipeline(context.Background(), types.BoardItem{Number: 21, Repo: "example-org/app", ID: "item-a21",
		Title: "App task", ParentNumber: 20, ParentRepo: "example-org/platform"})

	prompt, ok := runner.prompt(state.StageFeaturePlanning)
	if !ok {
		t.Fatalf("feature-planning was not dispatched; stages run: %v", runner.prompts)
	}
	if !strings.Contains(prompt, "Accumulated Epic Context") || !strings.Contains(prompt, "example-org/platform#20") {
		t.Errorf("feature-planning prompt lacks example-org/platform#20's epic context:\n%s", prompt)
	}
	if strings.Contains(prompt, "example-org/app#20") {
		t.Errorf("feature-planning prompt carries example-org/app#20's epic context:\n%s", prompt)
	}
}
