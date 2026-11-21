package ipc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// phaseTelemetryServer is an extension-path server whose platform emission is
// observable: telemetry allowed, the emitter a recording fake.
func phaseTelemetryServer(t *testing.T) (*Server, *countingEmitter, string) {
	t.Helper()
	root := layouttest.Repo(t)
	s := NewServer(nil, WithWorkspaceRoot(root), WithTelemetryPolicy(true, true))
	fake := &countingEmitter{}
	s.analyticsSvc = fake
	return s, fake, root
}

func eventsOfType(events []platform.PipelineEvent, eventType string) []platform.PipelineEvent {
	var out []platform.PipelineEvent
	for _, e := range events {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

// TestExtensionRun_PhaseTransitionsReachThePlatform pins AC0/AC5/AC6 on the
// extension path: each reported phase transition becomes one stage_progress
// event with a 1-based phase, a "complete" with no position reads the one its
// "start" recorded, evidence-only transitions send nothing, and the stage's
// live progress event carries the latest phase as its message.
func TestExtensionRun_PhaseTransitionsReachThePlatform(t *testing.T) {
	s, fake, _ := phaseTelemetryServer(t)
	const repo, issue = "acme/platform", 3455
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	phase := func(name string, index, total int, eventType, status string) {
		mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: name,
			Index: index, Total: total, EventType: eventType, Status: status, RunID: runID,
		})
	}
	phase("implementation", 7, 17, "start", "")
	phase("implementation", 0, 0, "complete", "")
	phase("testing", 8, 17, "skip", "")
	phase("e2e-testing", 9, 17, "fail", "")
	phase("quality-review", 10, 17, "unreported", "")
	phase("quality-review", 10, 17, "observed", "complete")

	got := eventsOfType(fake.snapshot(), "stage_progress")
	want := []platform.PipelinePhase{
		{Name: "implementation", Index: 8, Total: 17, Status: "started"},
		{Name: "implementation", Index: 8, Total: 17, Status: "completed"},
		{Name: "testing", Index: 9, Total: 17, Status: "skipped"},
		{Name: "e2e-testing", Index: 10, Total: 17, Status: "failed"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d phase events, want %d (unreported/observed must send nothing): %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Phase == nil || *got[i].Phase != w {
			t.Errorf("event %d phase = %+v, want %+v", i, got[i].Phase, w)
		}
		if got[i].RunID != runID || got[i].Repo != repo || got[i].Stage != "feature-dev" {
			t.Errorf("event %d identity = %q %q %q", i, got[i].RunID, got[i].Repo, got[i].Stage)
		}
	}

	// AC5: the live progress estimate describes the stage's latest phase.
	mustCall(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", RunID: runID, InputTokens: 10,
	})
	progress := eventsOfType(fake.snapshot(), "stage_progress")
	last := progress[len(progress)-1]
	if last.Phase != nil {
		t.Fatalf("token progress event must carry no phase, got %+v", last.Phase)
	}
	// The last record is quality-review: unreported, then upgraded by observed.
	if msg, _ := last.Metadata["message"].(string); msg != "quality-review (11/17) complete" {
		t.Fatalf("progress message = %q, want the latest phase label %q", msg, "quality-review (11/17) complete")
	}
}

// TestSchedulerOwnedRun_IPCSendsNoPhaseEvents pins one emitter per run owner:
// a scheduler-owned run's phases relayed through IPC are emitted by the
// scheduler, so the IPC server must send none of its own.
func TestSchedulerOwnedRun_IPCSendsNoPhaseEvents(t *testing.T) {
	s, fake, _ := phaseTelemetryServer(t)
	sched := newFakeSchedulerRuns()
	s.schedulerRuns = sched
	const repo, issue = "acme/platform", 3455
	schedRun := state.NewRuntimeState(repo, issue, "item-1", newTestRunID())
	sched.register(issue, schedRun)

	for _, ev := range []string{"start", "complete", "skip", "fail"} {
		mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
			Index: 1, Total: 3, EventType: ev, RunID: schedRun.RunID,
		})
	}
	for _, e := range fake.snapshot() {
		if e.Phase != nil {
			t.Fatalf("IPC emitted a phase event for a scheduler-owned run: %+v", e)
		}
	}
}

// TestExtensionRun_BookendStagePhaseNotSent: a stage outside the platform's
// enum would be rejected, so its phases are never sent.
func TestExtensionRun_BookendStagePhaseNotSent(t *testing.T) {
	s, fake, _ := phaseTelemetryServer(t)
	runID := newTestRunID()
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: "acme/platform", IssueNumber: 1, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: "acme/platform", IssueNumber: 1, Stage: "pipeline-start", Name: "x",
		Index: 0, Total: 2, EventType: "start", RunID: runID,
	})
	if n := len(eventsOfType(fake.snapshot(), "stage_progress")); n != 0 {
		t.Fatalf("bookend stage sent %d phase events, want 0", n)
	}
}

// TestExtensionRun_EstimateOnStageStartedOnly pins AC4: the estimate the
// extension sends with the first running transition rides that stage_started,
// and a later transition without one carries none.
func TestExtensionRun_EstimateOnStageStartedOnly(t *testing.T) {
	s, fake, _ := phaseTelemetryServer(t)
	const repo, issue = "acme/platform", 3455
	runID := newTestRunID()
	est := 4.25

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "issue-pickup", Status: "running", RunID: runID,
		EstimatedCostUsd: &est, EstimateBasis: "historical",
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "issue-pickup", Status: "complete", RunID: runID,
		EstimatedCostUsd: &est, EstimateBasis: "historical",
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-planning", Status: "running", RunID: runID,
	})

	started := eventsOfType(fake.snapshot(), "stage_started")
	if len(started) != 2 {
		t.Fatalf("got %d stage_started, want 2", len(started))
	}
	if started[0].EstimatedCostUsd == nil || *started[0].EstimatedCostUsd != est || started[0].EstimateBasis != "historical" {
		t.Errorf("first stage_started estimate = %v %q, want %v historical", started[0].EstimatedCostUsd, started[0].EstimateBasis, est)
	}
	if started[1].EstimatedCostUsd != nil || started[1].EstimateBasis != "" {
		t.Errorf("second stage_started carries an estimate: %v %q", started[1].EstimatedCostUsd, started[1].EstimateBasis)
	}
	for _, e := range eventsOfType(fake.snapshot(), "stage_completed") {
		if e.EstimatedCostUsd != nil || e.EstimateBasis != "" {
			t.Errorf("stage_completed carries an estimate: %v %q", e.EstimatedCostUsd, e.EstimateBasis)
		}
	}
}

// TestExtensionRun_PRStampedAfterPRCreate pins AC3: no event carries a PR
// before pr-create completes; from its stage_completed on, every event does,
// pipeline_done included.
func TestExtensionRun_PRStampedAfterPRCreate(t *testing.T) {
	s, fake, root := phaseTelemetryServer(t)
	const repo, issue = "acme/platform", 3455
	const prURL = "https://github.com/acme/platform/pull/77"
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "running", RunID: runID,
	})
	if err := os.WriteFile(filepath.Join(layouttest.PipelineDir(t, root), "pr-3455.json"),
		[]byte(`{"pr_url":"`+prURL+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-create", Status: "complete", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "pr-merge", Status: "running", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyComplete", PipelineNotifyCompleteParams{
		Repo: repo, IssueNumber: issue, RunID: runID, Success: true,
		StagesRun: []string{"pr-create", "pr-merge"},
	})

	events := fake.snapshot()
	if len(events) < 4 {
		t.Fatalf("got %d events, want at least 4: %+v", len(events), events)
	}
	if events[0].EventType != "stage_started" || events[0].PRURL != "" || events[0].PRNumber != 0 {
		t.Errorf("pre-PR stage_started = %s url=%q number=%d, want no PR", events[0].EventType, events[0].PRURL, events[0].PRNumber)
	}
	for _, e := range events[1:] {
		if e.PRURL != prURL || e.PRNumber != 77 {
			t.Errorf("%s: url=%q number=%d, want %q/77", e.EventType, e.PRURL, e.PRNumber, prURL)
		}
	}
	if len(eventsOfType(events, "pipeline_done")) != 1 {
		t.Errorf("want exactly one pipeline_done, got %+v", events)
	}
}

// TestExtensionRun_ProgressBeforeFirstPhaseHasActivityLine pins the #3455
// activity line for a model-driven stage: a token snapshot that arrives
// before the stage's first phase marker still sends a non-empty message, and
// one after a phase sends that phase's label.
func TestExtensionRun_ProgressBeforeFirstPhaseHasActivityLine(t *testing.T) {
	s, fake, _ := phaseTelemetryServer(t)
	const repo, issue = "acme/platform", 3455
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})
	progress := func() string {
		mustCall(t, s, "pipeline.notifyStageProgress", PipelineNotifyStageProgressParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-dev", RunID: runID, InputTokens: 10,
		})
		events := eventsOfType(fake.snapshot(), "stage_progress")
		if len(events) == 0 || events[len(events)-1].Phase != nil {
			t.Fatalf("no token progress event emitted: %+v", events)
		}
		return platform.ProgressMessage(events[len(events)-1])
	}

	if got := progress(); got != "feature-dev in progress" {
		t.Fatalf("pre-phase message = %q, want the stage fallback", got)
	}
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
		Index: 7, Total: 17, EventType: "start", RunID: runID,
	})
	if got := progress(); got != "implementation (8/17) running" {
		t.Fatalf("post-phase message = %q, want the latest phase label", got)
	}
}
