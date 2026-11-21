package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

func newPhaseTestRuntime() *state.RuntimeState {
	rs := state.NewRuntimeState("nightgauge/nightgauge", 1247, "item-1247", "run-1247")
	rs.BeginStage(state.StagePRMerge)
	return rs
}

// TestDeterministicPhaseReporter_ReachesTheDurableRunRecord is AC4.
//
// Lighting up the live tree is not the same claim as recording the run. The
// live view is torn down with the runtime; V2StageDetail.Phases is what a
// retro or a survival verdict reads afterwards, and before #1247 a
// deterministic stage wrote nothing to it.
func TestDeterministicPhaseReporter_ReachesTheDurableRunRecord(t *testing.T) {
	t.Parallel()

	rs := newPhaseTestRuntime()
	s := &Scheduler{}
	rep := s.newDeterministicPhaseReporter(rs, "nightgauge/nightgauge", 1247)

	rep.PhaseStart("pr-merge", "read-pr-context", 0, 14)
	rep.PhaseComplete("pr-merge", "read-pr-context")
	rep.PhaseSkip("pr-merge", "fetch-reviews", 5, 14)
	rep.PhaseSupersede("pr-merge", "ci-gate", 3, 14)

	rs.CompleteStage(0, tokens.TokenCounts{Input: 1, Output: 1}, "", "")

	hw := state.NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs, true, "", state.V2RunInput{Title: "phases", Branch: "fix/1247"}, time.Now())
	detail, ok := rec.Stages[string(state.StagePRMerge)]
	if !ok {
		t.Fatalf("no pr-merge stage detail; stages = %v", rec.Stages)
	}
	got := map[string]string{}
	for _, p := range detail.Phases {
		got[p.Name] = p.Status
	}
	for name, want := range map[string]string{
		"read-pr-context": "complete",
		"fetch-reviews":   "skipped",
		// The punt records the ATTEMPT as displaced, not the phase as failed
		// (#1850) — the skill is about to run ci-gate for real.
		"ci-gate": "superseded",
	} {
		if got[name] != want {
			t.Errorf("durable record has phase %q as %q, want %q (all: %v)", name, got[name], want, got)
		}
	}
}

// TestDeterministicPhaseReporter_FansOutToTheLiveView is AC1's other half: the
// tree updates WHILE the stage runs, which means the transitions have to leave
// the scheduler, not merely land in the runtime.
func TestDeterministicPhaseReporter_FansOutToTheLiveView(t *testing.T) {
	t.Parallel()

	rs := newPhaseTestRuntime()
	s := &Scheduler{}

	var starts, settled []string
	s.OnPhaseDetected(func(_ string, _ int, _, name string, _, _ int) {
		starts = append(starts, name)
	})
	s.OnPhaseSettled(func(_ string, _ int, _, name string, index, total int, status string) {
		settled = append(settled, name+"="+status)
		if total != 13 || index < 0 {
			t.Errorf("settled event for %q carries index=%d total=%d — consumers key on both", name, index, total)
		}
	})

	rep := s.newDeterministicPhaseReporter(rs, "nightgauge/nightgauge", 1247)
	rep.PhaseStart("pr-merge", "merge", 8, 13)
	rep.PhaseComplete("pr-merge", "merge")
	rep.PhaseSkip("pr-merge", "self-assessment", 12, 13)

	if len(starts) != 1 || starts[0] != "merge" {
		t.Errorf("live starts = %v, want [merge]", starts)
	}
	if len(settled) != 2 || settled[0] != "merge=complete" || settled[1] != "self-assessment=skipped" {
		t.Errorf("live settlements = %v, want [merge=complete self-assessment=skipped]", settled)
	}
}

// TestDeterministicPunt_SkillMarkersDoNotContradictEarlierPhases is AC5 at the
// level that actually matters — the durable record after BOTH producers have
// written to it.
//
// The runner completes read-pr-context and punts inside ci-gate; the pr-merge
// skill then runs the whole stage and emits its own markers for every phase,
// exactly as the extension replays them into RuntimeState. The record must end
// up saying the stage ran those phases. In particular `fetch-reviews` must not
// be `skipped`: SkipPhase is append-only and first-writer-wins, so a skip
// written during the punt would permanently outrank the skill's real record,
// and the run would close asserting it declined work it demonstrably did.
func TestDeterministicPunt_SkillMarkersDoNotContradictEarlierPhases(t *testing.T) {
	t.Parallel()

	rs := newPhaseTestRuntime()
	s := &Scheduler{}
	rep := s.newDeterministicPhaseReporter(rs, "nightgauge/nightgauge", 1247)

	// What the deterministic runner reports before punting on a conflict.
	rep.PhaseStart("pr-merge", "read-pr-context", 0, 14)
	rep.PhaseComplete("pr-merge", "read-pr-context")
	rep.PhaseStart("pr-merge", "ci-gate", 3, 14)
	rep.PhaseSupersede("pr-merge", "ci-gate", 3, 14)

	// The skill takes over and works the stage for real.
	for _, p := range []struct {
		name  string
		index int
	}{
		{"read-pr-context", 0}, {"ci-gate", 3}, {"fetch-reviews", 5}, {"merge", 9},
	} {
		rs.BeginPhase(state.StagePRMerge, p.name, p.index, 14)
		rs.CompletePhase(state.StagePRMerge, p.name)
	}
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1, Output: 1}, "", "")

	hw := state.NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs, true, "", state.V2RunInput{Title: "punt", Branch: "fix/1247"}, time.Now())
	detail := rec.Stages[string(state.StagePRMerge)]

	statuses := map[string][]string{}
	for _, p := range detail.Phases {
		statuses[p.Name] = append(statuses[p.Name], p.Status)
	}

	// Nothing the runner wrote was erased.
	if !containsStatus(statuses["read-pr-context"], "complete") {
		t.Errorf("read-pr-context = %v, want the deterministic completion preserved", statuses["read-pr-context"])
	}
	if !containsStatus(statuses["ci-gate"], "superseded") {
		t.Errorf("ci-gate = %v, want the deterministic attempt preserved as superseded", statuses["ci-gate"])
	}
	// And nothing it wrote contradicts what the skill then observed.
	if !containsStatus(statuses["ci-gate"], "complete") {
		t.Errorf("ci-gate = %v — the skill ran it to completion and the record must say so", statuses["ci-gate"])
	}
	for _, name := range []string{"fetch-reviews", "merge"} {
		if containsStatus(statuses[name], "skipped") {
			t.Errorf("%s = %v — the skill ran this phase; a punt must never pre-record it skipped", name, statuses[name])
		}
		if !containsStatus(statuses[name], "complete") {
			t.Errorf("%s = %v, want complete", name, statuses[name])
		}
	}
	// No phase may be left running when the stage ends.
	for name, ss := range statuses {
		if containsStatus(ss, "running") {
			t.Errorf("phase %q is still running after the stage completed: %v", name, ss)
		}
	}
}

func containsStatus(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

// TestWithPhaseReporter_NilIsSafe — the scheduler hands a nil reporter through
// whenever a run has no runtime, and the CLI verb attaches none at all.
func TestWithPhaseReporter_NilIsSafe(t *testing.T) {
	t.Parallel()

	s := &Scheduler{}
	if rep := s.newDeterministicPhaseReporter(nil, "r", 1); rep != nil {
		t.Fatalf("reporter for a nil runtime = %v, want nil", rep)
	}
	ctx := pmstages.WithPhaseReporter(context.Background(), s.newDeterministicPhaseReporter(nil, "r", 1))
	if ctx == nil {
		t.Fatal("WithPhaseReporter returned a nil context")
	}
}

// TestDeterministicPhaseReporter_EmitsPlatformPhases pins #3455 on the
// scheduler path: each reported transition reaches the platform as one
// stage_progress event with a 1-based phase, complete re-resolves its position
// from the registry, and supersede (no platform status) sends nothing.
func TestDeterministicPhaseReporter_EmitsPlatformPhases(t *testing.T) {
	t.Parallel()

	rs := newPhaseTestRuntime()
	tel := &mockTelemetry{}
	s := &Scheduler{}
	s.WithTelemetryService(tel, true)
	rep := s.newDeterministicPhaseReporter(rs, "nightgauge/nightgauge", 1247)

	rep.PhaseStart("pr-merge", "merge", 8, 13)
	rep.PhaseComplete("pr-merge", "merge")
	rep.PhaseFail("pr-merge", "ci-gate", 3, 13)
	rep.PhaseSkip("pr-merge", "self-assessment", 12, 13)
	rep.PhaseSupersede("pr-merge", "fetch-reviews", 5, 13)

	idx, total := pmstages.PhasePosition("pr-merge", "merge")
	want := []platform.PipelinePhase{
		{Name: "merge", Index: 9, Total: 13, Status: "started"},
		{Name: "merge", Index: idx + 1, Total: total, Status: "completed"},
		{Name: "ci-gate", Index: 4, Total: 13, Status: "failed"},
		{Name: "self-assessment", Index: 13, Total: 13, Status: "skipped"},
	}
	tel.mu.Lock()
	got := append([]platform.PipelineEvent(nil), tel.events...)
	tel.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d (supersede sends nothing): %+v", len(got), len(want), got)
	}
	for i, w := range want {
		e := got[i]
		if e.EventType != "stage_progress" || e.Stage != "pr-merge" || e.RunID != "run-1247" ||
			e.Repo != "nightgauge/nightgauge" || e.IssueNumber != 1247 {
			t.Errorf("event %d identity = %+v", i, e)
		}
		if e.Phase == nil || *e.Phase != w {
			t.Errorf("event %d phase = %+v, want %+v", i, e.Phase, w)
		}
	}
}

// TestDeterministicPhaseReporter_TelemetryDisabledSendsNothing: the config
// gate the scheduler's other run events honour holds for phases too.
func TestDeterministicPhaseReporter_TelemetryDisabledSendsNothing(t *testing.T) {
	t.Parallel()

	tel := &mockTelemetry{}
	s := &Scheduler{}
	s.WithTelemetryService(tel, false)
	rep := s.newDeterministicPhaseReporter(newPhaseTestRuntime(), "nightgauge/nightgauge", 1247)
	rep.PhaseStart("pr-merge", "merge", 8, 13)
	rep.PhaseComplete("pr-merge", "merge")

	tel.mu.Lock()
	defer tel.mu.Unlock()
	if len(tel.events) != 0 {
		t.Fatalf("telemetry disabled, yet %d events sent", len(tel.events))
	}
}

// TestEmitRunEvent_StampsThePR pins AC3 on the scheduler path: no PR before
// one is recorded, the PR and its number on every event after.
func TestEmitRunEvent_StampsThePR(t *testing.T) {
	t.Parallel()

	rs := newPhaseTestRuntime()
	tel := &mockTelemetry{}
	s := &Scheduler{}
	s.WithTelemetryService(tel, true)

	s.emitRunEvent(context.Background(), rs, platform.PipelineEvent{RunID: rs.RunID, EventType: "stage_started"})
	rs.SetPrUrl("https://github.com/nightgauge/nightgauge/pull/3460")
	s.emitRunEvent(context.Background(), rs, platform.PipelineEvent{RunID: rs.RunID, EventType: "pipeline_done"})

	tel.mu.Lock()
	defer tel.mu.Unlock()
	if len(tel.events) != 2 {
		t.Fatalf("got %d events, want 2", len(tel.events))
	}
	if tel.events[0].PRURL != "" || tel.events[0].PRNumber != 0 {
		t.Errorf("pre-PR event carries a PR: %q %d", tel.events[0].PRURL, tel.events[0].PRNumber)
	}
	if tel.events[1].PRURL != "https://github.com/nightgauge/nightgauge/pull/3460" || tel.events[1].PRNumber != 3460 {
		t.Errorf("pipeline_done PR = %q %d", tel.events[1].PRURL, tel.events[1].PRNumber)
	}
}

// TestRecordPhaseForRun_EmitsPlatformPhases: a scheduler-owned run's phases
// relayed by IPC are emitted by the scheduler (the IPC server stays silent).
func TestRecordPhaseForRun_EmitsPlatformPhases(t *testing.T) {
	t.Parallel()

	rs := newPhaseTestRuntime()
	tel := &mockTelemetry{}
	s := &Scheduler{}
	s.WithTelemetryService(tel, true)
	s.registerRuntime(rs)

	s.RecordPhaseStartForRun(rs.RunID, 1247, "pr-merge", "merge", 8, 13)
	s.RecordPhaseCompleteForRun(rs.RunID, 1247, "pr-merge", "merge")
	s.RecordPhaseStartForRun("someone-else", 1247, "pr-merge", "merge", 8, 13)

	tel.mu.Lock()
	defer tel.mu.Unlock()
	if len(tel.events) != 2 {
		t.Fatalf("got %d events, want 2 (foreign run id sends nothing): %+v", len(tel.events), tel.events)
	}
	if p := tel.events[1].Phase; p == nil || p.Status != "completed" || p.Index != 9 || p.Total != 13 {
		t.Errorf("complete phase = %+v, want merge 9/13 completed", p)
	}
}
