package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// #2258: a trivial / docs-only fast-track run in IPC mode (no Go-side adapter)
// used to dispatch every stage with the workspace root as its working
// directory — the operator's primary checkout (#1907). Every dispatch must name
// the run's linked worktree.
func TestRunPipeline_DocsOnlyFastTrackRunsInALinkedWorktree(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	var snap *state.RuntimeState
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { snap = rt })

	item := types.BoardItem{Number: 992258, Repo: "nightgauge/nightgauge", ID: "item-992258",
		Title: "update CONTRIBUTING.md", Labels: []string{"type:docs"}, Size: "S"}
	if d := deriveRoutingDecision(root, item); d.MatchedChangeRule != "docs-only" {
		t.Fatalf("fixture routes %q, want the docs-only fast track", d.MatchedChangeRule)
	}
	s.runPipeline(context.Background(), item)

	if snap == nil || snap.WorktreeDir == "" || snap.WorktreeDir == root {
		t.Fatalf("run worktree = %v, want a linked worktree other than the primary checkout %s", snap, root)
	}
	calls := runner.captured()
	if len(calls) == 0 {
		t.Fatal("no stage was dispatched")
	}
	for _, c := range calls {
		if c.Stage == state.StageFeaturePlanning || c.Stage == state.StageFeatureValidate {
			t.Errorf("%s dispatched, want it skipped by the docs-only route", c.Stage)
		}
		if c.WorktreePath != snap.WorktreeDir {
			t.Errorf("%s dispatched in %q, want the run worktree %q", c.Stage, c.WorktreePath, snap.WorktreeDir)
		}
	}
	if err := recoveryCommitTargetRefusal(snap.WorktreeDir); err != nil && strings.Contains(err.Error(), "primary checkout") {
		t.Errorf("run worktree is not a linked worktree: %v", err)
	}
}

// When the run worktree cannot be provisioned the run is refused before any
// dispatch; it never falls back to the primary checkout.
func TestRunPipeline_RefusesWithoutARunWorktree(t *testing.T) {
	isolateRoutingEnv(t)
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)
	// A repo root that does not exist: EnsureWorktree cannot provision.
	s.execMgr = execution.NewManager(filepath.Join(root, "missing"), nil)
	var snap *state.RuntimeState
	var ok bool
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, success bool) { snap, ok = rt, success })

	s.runPipeline(context.Background(), types.BoardItem{Number: 992259, Repo: "nightgauge/nightgauge",
		ID: "item-992259", Title: "t", Labels: []string{"type:docs"}, Size: "S"})

	if n := len(runner.captured()); n != 0 {
		t.Errorf("%d stage(s) dispatched without a run worktree, want 0", n)
	}
	if snap != nil && ok {
		t.Error("run reported success without a run worktree")
	}
}
