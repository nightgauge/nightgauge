package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestNewAutonomousScheduler_WiresEpicCheckpointIntoScheduler is the guard for
// the missing production caller.
//
// `RecordEpicComplete` had NO non-test caller. Every existing test invoked it by
// hand, which is why the gap survived: the rail was exercised constantly and
// reached never. This asserts the CONSTRUCTOR installs the wiring, so deleting
// the registration fails here rather than in production six weeks later.
func TestNewAutonomousScheduler_WiresEpicCheckpointIntoScheduler(t *testing.T) {
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())

	if sched.epicCheckpoint == nil {
		t.Fatal("NewAutonomousScheduler did not register an epic-checkpoint callback on " +
			"the Scheduler — RecordEpicComplete is unreachable again")
	}
	if as.safetyRails == nil {
		t.Fatal("no safety rails on the autonomous scheduler")
	}
	if as.safetyRails.State().PausedForCheckpoint {
		t.Fatal("fixture is already paused before the epic completed")
	}

	// Fire it the way epic.go does.
	sched.epicCheckpoint(42)

	st := as.safetyRails.State()
	if !st.PausedForCheckpoint {
		t.Error("PausedForCheckpoint = false after an epic completed — the rail is wired " +
			"but does not latch")
	}
	if st.LastEpicNumber != 42 {
		t.Errorf("LastEpicNumber = %d, want 42", st.LastEpicNumber)
	}

	// The pause must actually stop the next dispatch, or the latch is decoration.
	allowed, reason := as.safetyRails.CheckBeforeEnqueue(0)
	if allowed {
		t.Error("CheckBeforeEnqueue allowed a dispatch while paused for checkpoint")
	}
	if reason == "" {
		t.Error("refusal carried no reason")
	}
}

// TestEpicCheckpoint_DisabledDoesNotLatch is the control. Without it, a fix that
// paused unconditionally would pass the test above — and would stop the fleet
// for operators who explicitly opted out.
func TestEpicCheckpoint_DisabledDoesNotLatch(t *testing.T) {
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	cfg := DefaultAutonomousConfig()
	cfg.SafetyRails = &SafetyConfig{RateLimitPerHour: 100, EpicCheckpoint: false}
	as := NewAutonomousScheduler(sched, nil, nil, nil, cfg, t.TempDir())

	sched.epicCheckpoint(42)

	if as.safetyRails.State().PausedForCheckpoint {
		t.Error("PausedForCheckpoint = true with the checkpoint disabled")
	}
	if allowed, _ := as.safetyRails.CheckBeforeEnqueue(0); !allowed {
		t.Error("dispatch refused with the checkpoint disabled")
	}
}

// TestEpicCheckpoint_SurvivesOnEpicCompleteReassignment is the reason the
// checkpoint gets its own field instead of chaining onEpicComplete.
//
// `OnEpicComplete` is a single slot, and internal/ipc/server.go re-registers it
// inside the `pipeline.run` method closure — per REQUEST. A checkpoint chained
// onto that slot is silently discarded by the next pipeline.run, with no error
// and no log line. This test reproduces that re-registration and asserts the
// checkpoint still fires.
func TestEpicCheckpoint_SurvivesOnEpicCompleteReassignment(t *testing.T) {
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())

	// Exactly what the IPC server does on every pipeline.run request.
	otherCalled := false
	sched.OnEpicComplete(func(string, int) { otherCalled = true })

	if sched.epicCheckpoint == nil {
		t.Fatal("registering OnEpicComplete destroyed the epic-checkpoint wiring")
	}
	sched.epicCheckpoint(7)
	sched.onEpicComplete("o/r", 7)

	if !as.safetyRails.State().PausedForCheckpoint {
		t.Error("checkpoint did not latch after OnEpicComplete was re-registered — " +
			"this is the clobber the dedicated field exists to prevent")
	}
	if !otherCalled {
		t.Error("the IPC server's own callback stopped firing — the two must coexist")
	}
}

// TestSafetyRailKind_FingerprintsAreStable guards the Action Center's
// mute-until-changed contract.
//
// The raw refusal reasons embed live counters, so fingerprinting on them would
// produce a new fingerprint on almost every evaluation and re-alert a condition
// the operator had already muted.
func TestSafetyRailKind_FingerprintsAreStable(t *testing.T) {
	cases := []struct{ reason, want string }{
		{"budget ceiling exceeded: used 412300 + estimate 0 > ceiling 500000", "budget-ceiling"},
		{"budget ceiling exceeded: used 499999 + estimate 0 > ceiling 500000", "budget-ceiling"},
		{"circuit breaker tripped: 3 consecutive failures (max 3)", "circuit-breaker"},
		{"rate limit exceeded: 20 starts this hour (max 20)", "rate-limit"},
		{"health gate failed: score 12 < minimum 30", "health-gate"},
		{"paused for epic checkpoint (epic #42 complete — awaiting human review)", "epic-checkpoint"},
		{"something nobody has written yet", "other"},
	}
	for _, c := range cases {
		if got := safetyRailKind(c.reason); got != c.want {
			t.Errorf("safetyRailKind(%q) = %q, want %q", c.reason, got, c.want)
		}
	}

	// The two budget reasons differ only in their counters and MUST share a
	// fingerprint — this is the whole point.
	a := safetyRailKind(cases[0].reason)
	b := safetyRailKind(cases[1].reason)
	if a != b {
		t.Errorf("two evaluations of the same rail produced different kinds (%q vs %q) — "+
			"mute-until-changed would re-alert on every cycle", a, b)
	}
	// Different rails must NOT share one, or resolving a budget halt would mute
	// a later circuit-breaker halt.
	if safetyRailKind(cases[0].reason) == safetyRailKind(cases[2].reason) {
		t.Error("distinct rails share a fingerprint — resolving one would mute the other")
	}
}

// TestRaiseSafetyRailTrip_RaisesABlockingFleetCard guards the signal that did
// not exist.
//
// The trip site latches a MACHINE halt — it survives restart, and
// ResumeUnlessMachineHalted refuses to auto-resume it, logging "resolve the card
// or Resume explicitly". There was no card. With the epic checkpoint now firing
// on every epic close, a routine completion reaches this path.
func TestRaiseSafetyRailTrip_RaisesABlockingFleetCard(t *testing.T) {
	store := attention.New(t.TempDir())
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())
	as.attention = store

	as.raiseSafetyRailTrip("paused for epic checkpoint (epic #42 complete — awaiting human review)", 42)

	reqs, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var card *attention.DecisionRequest
	for i := range reqs {
		if reqs[i].Producer == producerSafetyRailTrip {
			card = &reqs[i]
			break
		}
	}
	if card == nil {
		t.Fatal("a fleet-stopping machine halt raised no Action Center card")
	}
	if card.Severity != attention.SeverityBlockingFleet {
		t.Errorf("severity = %q, want %q — the whole fleet is stopped, across every repo",
			card.Severity, attention.SeverityBlockingFleet)
	}
	if card.Fingerprint != "rail:epic-checkpoint" {
		t.Errorf("fingerprint = %q, want %q", card.Fingerprint, "rail:epic-checkpoint")
	}

	// Resuming must retract it, or the fleet runs while the inbox says stopped.
	as.resolveSafetyRailTrip()
	reqs, err = store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("list after resolve: %v", err)
	}
	for _, r := range reqs {
		if r.Producer == producerSafetyRailTrip && r.Lifecycle.State == attention.StateOpen {
			t.Error("the halt card is still open after the fleet resumed")
		}
	}
}

// TestEpicCheckpointDefaults_AgreeAcrossPackages pins the orchestrator's default
// to the config package's. They are written down in two places because an import
// cycle prevents one from reading the other.
func TestEpicCheckpointDefaults_AgreeAcrossPackages(t *testing.T) {
	if got := DefaultSafetyConfig().EpicCheckpoint; got != config.DefaultEpicCheckpoint {
		t.Errorf("orchestrator default = %v, config.DefaultEpicCheckpoint = %v — an omitted "+
			"key and an absent safety_rails block would resolve differently",
			got, config.DefaultEpicCheckpoint)
	}
}

// TestCheckEpicCompletion_LatchesTheCheckpoint is the test that actually pins
// the wiring, and it exists because a weaker one did not.
//
// An earlier version of this file fired `sched.epicCheckpoint(42)` directly.
// That proves the callback works, not that anything CALLS it — deleting the
// call site in epic.go left the whole suite green. Mutation testing caught it.
// This drives `checkEpicCompletion` itself, so removing that call site fails
// here.
func TestCheckEpicCompletion_LatchesTheCheckpoint(t *testing.T) {
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())

	sched.evaluatePostMergeFn = func(_ context.Context, _ hooks.IssueFetcher, _ hooks.IssueCloser,
		_ hooks.EpicAutoCloser, _ hooks.PRVerifier, _ hooks.BoardSyncer,
		_ hooks.PostMergeInput) hooks.PostMergeResult {
		return hooks.PostMergeResult{IssueClosed: true, AutoClosed: true, EpicNumber: 77}
	}

	sched.checkEpicCompletion(context.Background(), types.BoardItem{Repo: "o/r", Number: 5}, 0)

	st := as.safetyRails.State()
	if !st.PausedForCheckpoint {
		t.Fatal("an epic auto-closed and the checkpoint did not latch — RecordEpicComplete " +
			"still has no reachable production caller")
	}
	if st.LastEpicNumber != 77 {
		t.Errorf("LastEpicNumber = %d, want 77 (the epic the hook reported)", st.LastEpicNumber)
	}
	if allowed, _ := as.safetyRails.CheckBeforeEnqueue(0); allowed {
		t.Error("dispatch still allowed after the between-epic checkpoint latched")
	}
}

// TestCheckEpicCompletion_DoesNotLatchWithoutAutoClose is the control: a merged
// sub-issue that does NOT complete its epic must not pause the fleet. Without
// this, a fix that latched on every post-merge call would pass the test above
// and stop the fleet on every single merge.
func TestCheckEpicCompletion_DoesNotLatchWithoutAutoClose(t *testing.T) {
	sched := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	as := NewAutonomousScheduler(sched, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())

	sched.evaluatePostMergeFn = func(_ context.Context, _ hooks.IssueFetcher, _ hooks.IssueCloser,
		_ hooks.EpicAutoCloser, _ hooks.PRVerifier, _ hooks.BoardSyncer,
		_ hooks.PostMergeInput) hooks.PostMergeResult {
		return hooks.PostMergeResult{IssueClosed: true, AutoClosed: false}
	}

	sched.checkEpicCompletion(context.Background(), types.BoardItem{Repo: "o/r", Number: 5}, 0)

	if as.safetyRails.State().PausedForCheckpoint {
		t.Error("the fleet paused on a merge that closed no epic")
	}
	if allowed, _ := as.safetyRails.CheckBeforeEnqueue(0); !allowed {
		t.Error("dispatch refused after an ordinary sub-issue merge")
	}
}

// TestResumeCheckpoint_LeavesOtherRailsAlone guards the narrow resume.
//
// The old implementation cleared TripReason unconditionally, behind a condition
// that could never be false. That was harmless while the checkpoint could not
// fire; now that it can, an operator acknowledging an epic pause must not erase
// a budget or circuit-breaker refusal that has nothing to do with it.
func TestResumeCheckpoint_LeavesOtherRailsAlone(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{EpicCheckpoint: true, CircuitBreakerMax: 3})

	// A circuit-breaker refusal, with no checkpoint pause in play.
	for i := 0; i < 3; i++ {
		sr.RecordCompletion(false, 0)
	}
	if allowed, _ := sr.CheckBeforeEnqueue(0); allowed {
		t.Fatal("fixture: circuit breaker did not trip")
	}
	before := sr.State().TripReason
	if before == "" {
		t.Fatal("fixture: no trip reason recorded")
	}

	sr.ResumeCheckpoint()

	if got := sr.State().TripReason; got != before {
		t.Errorf("ResumeCheckpoint cleared an unrelated rail's TripReason (%q → %q)", before, got)
	}
	if allowed, _ := sr.CheckBeforeEnqueue(0); allowed {
		t.Error("ResumeCheckpoint un-tripped the circuit breaker — it must only clear the " +
			"epic checkpoint")
	}

	// And it must still do its actual job.
	sr.RecordEpicComplete(9)
	if !sr.State().PausedForCheckpoint {
		t.Fatal("fixture: checkpoint did not latch")
	}
	sr.ResumeCheckpoint()
	if sr.State().PausedForCheckpoint {
		t.Error("ResumeCheckpoint did not clear the checkpoint pause")
	}
}
