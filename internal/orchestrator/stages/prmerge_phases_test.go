package stages

import (
	"context"
	"errors"
	"testing"
)

// TestPRMergePhases_CountAdvancesWhileRunning is AC1. Before #1247 a
// deterministic pr-merge produced no phase transitions at all, so this asserts
// on the presence of progress, not merely on the final tally.
func TestPRMergePhases_CountAdvancesWhileRunning(t *testing.T) {
	gh := &fakeGh{
		preMerge: PRViewSnapshot{
			State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			ReviewDecision: "APPROVED",
		},
		postMerge: PRViewSnapshot{State: "MERGED"},
	}
	r := newRunnerWith(gh, 42)
	r.knowledgeConformance = nil

	rec := &recordingReporter{}
	res, err := r.Run(reporterCtx(rec), 100, "owner/repo", t.TempDir())
	if err != nil || res.Path != PathMerged {
		t.Fatalf("setup: Path=%q err=%v, want a clean deterministic merge", res.Path, err)
	}

	started := rec.namesWithStatus("running")
	if len(started) == 0 {
		t.Fatalf("the runner reported no phase starts — the tree would show 0/%d for the whole stage",
			prMergePhases.total())
	}
	for _, want := range []string{"read-pr-context", "ci-gate", "freshness-check", "merge"} {
		if !rec.has(want, "running") {
			t.Errorf("phase %q never started; started = %v", want, started)
		}
		if !rec.has(want, "complete") {
			t.Errorf("phase %q never completed; completed = %v", want, rec.namesWithStatus("complete"))
		}
	}

	// Every start carries the registry position, because the consumers index on it.
	for _, e := range rec.events {
		if e.status != "running" {
			continue
		}
		if e.total != prMergePhases.total() {
			t.Errorf("phase %q reported total=%d, want %d", e.name, e.total, prMergePhases.total())
		}
		if e.index != prMergePhases.index(e.name) {
			t.Errorf("phase %q reported index=%d, want %d", e.name, e.index, prMergePhases.index(e.name))
		}
	}
}

// TestPRMergePhases_LLMOnlyPhasesAreSkippedOnSuccess is AC2. The skip set is
// the explicit phaseOffPath roles — nothing here is derived from "whatever the
// runner did not touch".
func TestPRMergePhases_LLMOnlyPhasesAreSkippedOnSuccess(t *testing.T) {
	gh := &fakeGh{
		preMerge: PRViewSnapshot{
			State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			ReviewDecision: "APPROVED",
		},
		postMerge: PRViewSnapshot{State: "MERGED"},
	}
	r := newRunnerWith(gh, 42)
	r.knowledgeConformance = nil

	rec := &recordingReporter{}
	if res, _ := r.Run(reporterCtx(rec), 100, "owner/repo", t.TempDir()); res.Path != PathMerged {
		t.Fatalf("setup: Path=%q, want merged", res.Path)
	}

	skipped := map[string]bool{}
	for _, n := range rec.namesWithStatus("skipped") {
		skipped[n] = true
	}
	for _, name := range prMergePhases.order {
		switch prMergePhases.roles[name] {
		case phaseOffPath:
			if !skipped[name] {
				t.Errorf("LLM-only phase %q was not recorded skipped", name)
			}
		default:
			if skipped[name] {
				t.Errorf("phase %q is on the deterministic path and must not be recorded skipped", name)
			}
		}
	}
	// post-merge-cleanup belongs to the caller (the scheduler's branch/worktree
	// teardown). The runner must neither claim it nor skip it.
	if rec.has("post-merge-cleanup", "complete") {
		t.Error("the runner claimed post-merge-cleanup, which it does not perform")
	}
}

// TestPRMergePhases_PuntPreservesEarlierPhases is AC5, the subtle one.
//
// The runner gets through read-pr-context and then punts on a conflict. The
// skill takes over and runs the whole stage for real. Nothing the runner wrote
// may contradict what the skill is about to observe — which means above all
// that a punt must write NO skips: SkipPhase is append-only and
// first-writer-wins, so a skip recorded here would outrank the skill's real
// record permanently.
func TestPRMergePhases_PuntPreservesEarlierPhases(t *testing.T) {
	gh := &fakeGh{preMerge: PRViewSnapshot{
		State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY",
	}}
	r := newRunnerWith(gh, 42)
	r.knowledgeConformance = nil

	rec := &recordingReporter{}
	res, err := r.Run(reporterCtx(rec), 100, "owner/repo", t.TempDir())
	if err != nil || res.Path != PathPunt {
		t.Fatalf("setup: Path=%q err=%v, want a punt", res.Path, err)
	}

	if got := rec.namesWithStatus("skipped"); len(got) != 0 {
		t.Errorf("punt recorded %v as skipped — the LLM path is about to run those phases", got)
	}
	if !rec.has("read-pr-context", "complete") {
		t.Error("read-pr-context ran to completion before the punt and must stay recorded complete")
	}
	if !rec.has("ci-gate", "complete") {
		t.Error("ci-gate reached its verdict before the punt and must stay recorded complete")
	}
	// The phase the punt happened inside must not be left open: a `running`
	// record survives to the stage boundary and is rewritten `abandoned`
	// (#1009), which reads as "the run got stuck here".
	if !rec.has("freshness-check", "failed") {
		t.Errorf("the in-flight phase was not closed on the punt; events = %+v", rec.events)
	}
	for _, e := range rec.events {
		if e.status == "running" && !rec.has(e.name, "complete") && !rec.has(e.name, "failed") {
			t.Errorf("phase %q was left running when the runner returned", e.name)
		}
	}
}

// TestPRMergePhases_ContextReadFailurePuntsWithoutSkips covers the earliest
// punt there is — the runner reports one failed phase and nothing else, so the
// skill starts from a clean slate.
func TestPRMergePhases_ContextReadFailurePuntsWithoutSkips(t *testing.T) {
	r := newRunnerWith(&fakeGh{}, 0)
	r.prContextRead = func(string, int) (int, error) { return 0, errors.New("no such file") }
	r.knowledgeConformance = nil

	rec := &recordingReporter{}
	if res, _ := r.Run(reporterCtx(rec), 100, "owner/repo", t.TempDir()); res.Path != PathPunt {
		t.Fatalf("setup: Path=%q, want punt", res.Path)
	}
	if got := rec.namesWithStatus("skipped"); len(got) != 0 {
		t.Errorf("recorded %v as skipped on a punt", got)
	}
	if !rec.has("read-pr-context", "failed") {
		t.Errorf("read-pr-context should be recorded failed; events = %+v", rec.events)
	}
}

// TestPRMergePhases_NoReporterIsANoOp — the `pr-stage` CLI verb runs the same
// runner with no reporter attached and must behave exactly as before.
func TestPRMergePhases_NoReporterIsANoOp(t *testing.T) {
	gh := &fakeGh{
		preMerge: PRViewSnapshot{
			State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			ReviewDecision: "APPROVED",
		},
		postMerge: PRViewSnapshot{State: "MERGED"},
	}
	r := newRunnerWith(gh, 42)
	r.knowledgeConformance = nil
	res, err := r.Run(context.Background(), 100, "owner/repo", t.TempDir())
	if err != nil || res.Path != PathMerged {
		t.Fatalf("Path=%q err=%v, want merged", res.Path, err)
	}
}
