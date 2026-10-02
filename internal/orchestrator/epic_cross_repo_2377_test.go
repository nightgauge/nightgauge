package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/git"
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
			if _, err := r.Run(context.Background(), in); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got != c.want {
				t.Errorf("EnsureIssueBranch parent = %d, want %d", got, c.want)
			}
		})
	}
}
