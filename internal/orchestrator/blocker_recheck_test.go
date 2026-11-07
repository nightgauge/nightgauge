package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// stubRefresh swaps refreshResumedBase for the test and counts its calls.
func stubRefresh(t *testing.T, res pmstages.BaseRefresh, err error) *int {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	prev := refreshResumedBase
	refreshResumedBase = func(context.Context, string, int) (pmstages.BaseRefresh, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return res, err
	}
	t.Cleanup(func() { refreshResumedBase = prev })
	return &calls
}

// stubIssueStates answers `gh api repos/<repo>/issues/<n>` from states, keyed
// by "repo#n"; anything else is an error.
func stubIssueStates(t *testing.T, states map[string]string) *[]string {
	t.Helper()
	var asked []string
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) < 2 || args[0] != "api" {
			return nil, errors.New("unexpected gh call")
		}
		key := strings.Replace(strings.TrimPrefix(args[1], "repos/"), "/issues/", "#", 1)
		asked = append(asked, key)
		if st, ok := states[key]; ok {
			return []byte(st + "\n"), nil
		}
		return nil, errors.New("not found")
	})
	return &asked
}

const blockedDetail = "BLOCKED — the deliverable depends on #77 and acme/lib#5, neither of which is on main (#42)"

func TestClosedNamedBlockers(t *testing.T) {
	asked := stubIssueStates(t, map[string]string{
		"nightgauge/nightgauge#77": "closed",
		"acme/lib#5":               "open",
	})
	got := closedNamedBlockers(context.Background(), "nightgauge/nightgauge", 42, blockedDetail+" — see #77 again")
	if strings.Join(got, ",") != "#77" {
		t.Fatalf("closed = %v, want [#77]", got)
	}
	// The issue itself is never read, and a repeated reference is read once.
	if strings.Join(*asked, ",") != "nightgauge/nightgauge#77,acme/lib#5" {
		t.Fatalf("forge reads = %v", *asked)
	}
}

func TestClosedNamedBlockers_UnreadableIsOpen(t *testing.T) {
	stubIssueStates(t, nil)
	if got := closedNamedBlockers(context.Background(), "o/r", 42, "BLOCKED — waits on #9"); len(got) != 0 {
		t.Fatalf("closed = %v, want none when the forge cannot answer", got)
	}
}

func TestRecheckBlockedOutcome(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	cases := []struct {
		name        string
		states      map[string]string
		detail      string
		refresh     pmstages.BaseRefresh
		refreshErr  error
		wantCalls   int
		wantRerun   bool
		wantNote    string
		wantConflct bool
	}{
		{
			name:      "closed blocker and an advanced base re-run the stage",
			states:    map[string]string{repo + "#77": "closed"},
			detail:    blockedDetail,
			refresh:   pmstages.BaseRefresh{Base: "main", Behind: 3, Outcome: gitpkg.BaseMergeMerged},
			wantCalls: 1,
			wantRerun: true,
		},
		{
			name:      "open blockers leave the verdict alone and refresh nothing",
			states:    map[string]string{repo + "#77": "open", "acme/lib#5": "open"},
			detail:    blockedDetail,
			wantCalls: 0,
		},
		{
			name:      "a self-reference is not a blocker",
			states:    map[string]string{repo + "#42": "closed"},
			detail:    "BLOCKED — see #42",
			wantCalls: 0,
		},
		{
			name:      "an up-to-date base books the verdict with a note",
			states:    map[string]string{repo + "#77": "closed"},
			detail:    blockedDetail,
			refresh:   pmstages.BaseRefresh{Base: "main", Outcome: gitpkg.BaseMergeUpToDate},
			wantCalls: 1,
			wantNote:  "(named blocker(s) #77 closed; base already contains origin/main)",
		},
		{
			name:       "a refresh that cannot run books the verdict with a note",
			states:     map[string]string{repo + "#77": "closed"},
			detail:     blockedDetail,
			refreshErr: errors.New("offline"),
			wantCalls:  1,
			wantNote:   "base refresh could not run",
		},
		{
			name:        "a conflicting refresh replaces the verdict",
			states:      map[string]string{repo + "#77": "closed"},
			detail:      blockedDetail,
			refresh:     pmstages.BaseRefresh{Base: "main", Outcome: gitpkg.BaseMergeConflict, ConflictFiles: []string{"a.go"}},
			wantCalls:   1,
			wantConflct: true,
		},
		{
			name:      "an ordinary failure is not re-checked",
			states:    map[string]string{repo + "#77": "closed"},
			detail:    "exit 1: tests failed after #77",
			wantCalls: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubIssueStates(t, tc.states)
			calls := stubRefresh(t, tc.refresh, tc.refreshErr)
			got := recheckBlockedOutcome(context.Background(), repo, 42, t.TempDir(), tc.detail)
			if *calls != tc.wantCalls {
				t.Fatalf("refresh calls = %d, want %d", *calls, tc.wantCalls)
			}
			if got.Ran != (tc.wantCalls > 0) || got.Rerun != tc.wantRerun {
				t.Fatalf("got %+v", got)
			}
			if !strings.Contains(got.Note, tc.wantNote) || (tc.wantNote == "" && got.Note != "") {
				t.Fatalf("note = %q, want %q", got.Note, tc.wantNote)
			}
			if tc.wantConflct != (got.ConflictReason != "") {
				t.Fatalf("conflict reason = %q", got.ConflictReason)
			}
			if tc.wantConflct && ClassifyTerminalKind(got.ConflictReason) != TerminalKindMergeConflict {
				t.Fatalf("%q classifies %q, want merge_conflict", got.ConflictReason, ClassifyTerminalKind(got.ConflictReason))
			}
		})
	}
}

// A resumed worktree whose base merge conflicts stops before any stage, with a
// merge_conflict reason naming the files rather than not_pipeline_actionable.
func TestRunPipeline_BaseRefreshConflictStopsBeforeDispatch(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	calls := stubRefresh(t, pmstages.BaseRefresh{Base: "main", Behind: 2, Ahead: 1,
		Outcome: gitpkg.BaseMergeConflict, ConflictFiles: []string{"internal/x.go"}}, nil)
	var snap *state.RuntimeState
	var ok bool
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, success bool) { snap, ok = rt, success })

	s.runPipeline(context.Background(), types.BoardItem{Number: 992897, Repo: "nightgauge/nightgauge",
		ID: "item-992897", Title: "t", Labels: []string{"type:docs"}, Size: "S"})

	if *calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", *calls)
	}
	if n := len(runner.captured()); n != 0 {
		t.Fatalf("%d stage(s) dispatched over a conflicting base, want 0", n)
	}
	if snap == nil || ok {
		t.Fatalf("run reported success (snap=%v)", snap)
	}
	var reason string
	for _, e := range snap.StageErrors {
		reason = e
	}
	if !strings.Contains(reason, "internal/x.go") || ClassifyTerminalKind(reason) != TerminalKindMergeConflict {
		t.Fatalf("stage error %q, want a merge_conflict reason naming internal/x.go", reason)
	}
}

// A resumed worktree behind its base is brought up to date before the first
// stage runs, in the run worktree, and the run goes on to dispatch.
func TestRunPipeline_BaseRefreshRunsBeforeTheFirstStage(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	var refreshedIn string
	prev := refreshResumedBase
	refreshResumedBase = func(_ context.Context, workdir string, _ int) (pmstages.BaseRefresh, error) {
		if len(runner.captured()) != 0 {
			t.Error("refresh ran after a stage was dispatched")
		}
		refreshedIn = workdir
		return pmstages.BaseRefresh{Base: "main", Behind: 4, Outcome: gitpkg.BaseMergeMerged}, nil
	}
	t.Cleanup(func() { refreshResumedBase = prev })
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	s.runPipeline(context.Background(), types.BoardItem{Number: 992898, Repo: "nightgauge/nightgauge",
		ID: "item-992898", Title: "update CONTRIBUTING.md", Labels: []string{"type:docs"}, Size: "S"})

	if len(runner.captured()) == 0 {
		t.Fatal("no stage dispatched after a clean refresh")
	}
	if snap == nil || refreshedIn == "" || refreshedIn != snap.WorktreeDir {
		t.Fatalf("refreshed %q, want the run worktree %v", refreshedIn, snap)
	}
}

// blockedRunner ends every dispatch BLOCKED on #77.
type blockedRunner struct{ runIDCapturingRunner }

func (r *blockedRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	r.mu.Unlock()
	return &StageRunResult{ExitCode: 1, ErrorText: "BLOCKED — the change needs #77, which is not on this branch's base"}, nil
}

// A stage blocked on a now-closed issue triggers one base refresh in the run
// worktree, at most once per run however often the stage re-runs; an open
// blocker triggers none.
func TestRunPipeline_BlockedOnAClosedIssueRefreshesOnce(t *testing.T) {
	for _, tc := range []struct {
		state     string
		wantCalls int
	}{{"closed", 2}, {"open", 1}} {
		t.Run(tc.state, func(t *testing.T) {
			isolateRoutingEnv(t)
			root := gitWorkspace(t)
			runner := &blockedRunner{}
			s := newRunIdentityTestScheduler(t, root, runner)
			stubIssueStates(t, map[string]string{"nightgauge/nightgauge#77": tc.state})
			calls := stubRefresh(t, pmstages.BaseRefresh{Base: "main", Behind: 1, Outcome: gitpkg.BaseMergeMerged}, nil)
			var ok bool
			s.OnPipelineComplete(func(_ string, _ int, _ *state.RuntimeState, success bool) { ok = success })

			s.runPipeline(context.Background(), types.BoardItem{Number: 992899, Repo: "nightgauge/nightgauge",
				ID: "item-992899", Title: "update CONTRIBUTING.md", Labels: []string{"type:docs"}, Size: "S"})

			if *calls != tc.wantCalls {
				t.Fatalf("refresh calls = %d, want %d (one at dispatch, one per closed-blocker re-check)", *calls, tc.wantCalls)
			}
			if ok {
				t.Fatal("a stage that always blocks reported success")
			}
			first := runner.captured()[0].Stage
			n := 0
			for _, c := range runner.captured() {
				if c.Stage == first {
					n++
				}
			}
			if tc.state == "closed" && n < 2 {
				t.Fatalf("%s dispatched %d time(s), want a re-run on the refreshed base", first, n)
			}
		})
	}
}
