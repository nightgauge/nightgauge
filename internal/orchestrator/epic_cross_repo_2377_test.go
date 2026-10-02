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

	"github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
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

	if err := s.EnqueueEpic(context.Background(), "example-org", "platform", 20, "Platform Epic", nil, nil); err != nil {
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

	subIssues, _, err := wo.fetchSubIssueDetails(context.Background(), "example-org", "platform", epicItem)
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
