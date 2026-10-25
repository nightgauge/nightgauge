package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestRefreshBlockedReadyPRs_RecordsReadyOpenPRs (#2541): a dispatchable
// issue's open, not-blocked PR — CLEAN or DIRTY — is kept by number so its
// dispatch can resume at pr-merge; a BLOCKED PR, a fork PR, and an issue with
// no PR are not.
func TestRefreshBlockedReadyPRs_RecordsReadyOpenPRs(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if !ghArgsContain(args, "list") {
			return []byte("[]"), nil
		}
		return []byte(`[
			{"number":101,"headRefName":"feat/1-clean","mergeStateStatus":"CLEAN"},
			{"number":102,"headRefName":"feat/2-dirty","mergeStateStatus":"DIRTY"},
			{"number":103,"headRefName":"feat/3-blocked","mergeStateStatus":"BLOCKED"},
			{"number":104,"headRefName":"feat/4-fork","isCrossRepository":true,"mergeStateStatus":"CLEAN"}
		]`), nil
	})
	const repo = "acme/app"
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{}}
	for n := 1; n <= 5; n++ {
		key := fmt.Sprintf("%s#%d", repo, n)
		g.Nodes[key] = &depgraph.Node{Repo: repo, Number: n, State: "OPEN", BoardStatus: "Ready"}
	}
	as := &AutonomousScheduler{}
	as.refreshBlockedReadyPRs(context.Background(), g)

	want := map[string]int{repo + "#1": 101, repo + "#2": 102}
	if len(as.readyOpenPRs) != len(want) {
		t.Fatalf("readyOpenPRs = %v, want %v", as.readyOpenPRs, want)
	}
	for k, v := range want {
		if as.readyOpenPRs[k] != v {
			t.Errorf("readyOpenPRs[%s] = %d, want %d", k, as.readyOpenPRs[k], v)
		}
	}
	if !as.blockedReadyPRIssues[repo+"#3"] || as.blockedReadyPRIssues[repo+"#2"] {
		t.Errorf("blocked set = %v, want only #3", as.blockedReadyPRIssues)
	}
}

// TestLocalDispatcher_PassesOpenPRNumber (#2541): the dispatch callback
// receives the candidate's open PR number.
func TestLocalDispatcher_PassesOpenPRNumber(t *testing.T) {
	got := -1
	d := NewLocalDispatcher(func(_, _ string, _ int, _ string, pr int) { got = pr }, nil)
	if _, err := d.Dispatch(context.Background(), CandidateItem{Repo: "acme/app", Number: 7, OpenPRNumber: 42}); err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("onDispatch openPRNumber = %d, want 42", got)
	}
}

// TestQueueAdd_CarriesOpenPRNumber (#2541): the field survives the queue so
// the extension's dequeued item carries it.
func TestQueueAdd_CarriesOpenPRNumber(t *testing.T) {
	s := &Scheduler{}
	s.QueueAdd(QueueEntry{Repo: "app", IssueNumber: 7, OpenPRNumber: 42}, QueueEntry{Repo: "app", IssueNumber: 8})
	if s.queue[0].OpenPRNumber != 42 || s.queue[1].OpenPRNumber != 0 {
		t.Fatalf("queue = %+v", s.queue)
	}
}

func resumeStages() []state.PipelineStage {
	return []state.PipelineStage{state.StageIssuePickup, state.StageFeatureDev, state.StagePRCreate, state.StagePRMerge}
}

// TestDispatchedPRResumeIndex (#2541): the Go CLI path starts at pr-merge only
// when the dispatched PR is confirmed open on the issue's branch, and restores
// pr-{N}.json from it; otherwise every stage runs.
func TestDispatchedPRResumeIndex(t *testing.T) {
	cases := []struct {
		name    string
		pr      int
		view    string
		wantIdx int
	}{
		{"no dispatched PR", 0, "", 0},
		{"open PR on the issue branch", 42, `{"state":"OPEN","headRefName":"feat/7-x"}`, 3},
		{"PR merged since the scan", 42, `{"state":"MERGED","headRefName":"feat/7-x"}`, 0},
		{"PR on another issue's branch", 42, `{"state":"OPEN","headRefName":"feat/8-x"}`, 0},
		{"fork PR", 42, `{"state":"OPEN","headRefName":"feat/7-x","isCrossRepository":true}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := layouttest.Repo(t)
			calls := 0
			stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
				calls++
				return []byte(tc.view), nil
			})
			got := dispatchedPRResumeIndex(context.Background(), resumeStages(), root, "acme/app", 7, tc.pr)
			if got != tc.wantIdx {
				t.Fatalf("index = %d, want %d", got, tc.wantIdx)
			}
			restored := loadPRNumberForRecovery(root, 7)
			if tc.wantIdx > 0 && restored != tc.pr {
				t.Fatalf("pr-7.json pr_number = %d, want %d", restored, tc.pr)
			}
			if tc.wantIdx == 0 && restored != 0 {
				t.Fatalf("pr-7.json written (%d) for a run that must not resume", restored)
			}
			if tc.pr == 0 && calls != 0 {
				t.Fatalf("no dispatched PR must make no forge call; got %d", calls)
			}
		})
	}
}
