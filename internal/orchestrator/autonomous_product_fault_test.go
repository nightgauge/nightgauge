package orchestrator

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// #1487 — three issues reached the lifetime failure cap (2/2) on a stage-gate
// defect that had already been fixed and shipped. None of the failures was the
// issue's fault, and none of the issues could be dispatched again without an
// operator editing state.json by hand.
//
// The rule these tests pin: when the cascading-failures breaker trips and every
// failure inside its window carries ONE terminal kind, the window is one
// product defect observed N times, not N broken issues — so no issue is charged
// a lifetime failure for it, and the charges the earlier in-window failures
// already made are refunded.

// The exact incident shape: one terminal kind, three failures, two repos.
func TestCascadeProductFault_SharedKindAcrossTwoReposChargesNoIssue(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)

	failures := []struct {
		repo   string
		number int
	}{
		{"octocat/acme-site", 69},
		{"octocat/acme-app", 530},
		{"octocat/acme-app", 531},
	}
	for _, f := range failures {
		addRunning(as, f.repo, f.number, "issue")
		as.onPipelineComplete(f.repo, f.number, false, false,
			TerminalKindDevBuildVerificationMissing, "gate rejected a finished implementation")
		as.drainBackground()
	}

	if !as.cascadeTracker.IsTripped() {
		t.Fatal("the cascade breaker did not trip on 3 failures in the window — the premise of this test is gone")
	}
	if as.state.Status != "safety_tripped" {
		t.Errorf("Status = %q, want safety_tripped", as.state.Status)
	}
	for key, n := range as.state.LifetimeIssueFailures {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want the key absent — every failure in the window was %s, which is the pipeline's defect and not the issue's",
			key, n, TerminalKindDevBuildVerificationMissing)
	}
	if len(as.state.QuarantinedIssues) != 0 {
		t.Errorf("QuarantinedIssues = %v, want empty", as.state.QuarantinedIssues)
	}

	// The verdict has to be legible to the operator reading the pause, not
	// only visible as a counter that quietly failed to move.
	if !strings.Contains(as.state.PauseReason, "product fault") {
		t.Errorf("PauseReason does not record the product fault: %q", as.state.PauseReason)
	}
	if !strings.Contains(as.state.PauseReason, TerminalKindDevBuildVerificationMissing) {
		t.Errorf("PauseReason does not name the shared kind: %q", as.state.PauseReason)
	}
	if as.state.Safety == nil || !strings.Contains(as.state.Safety.TripReason, "product fault") {
		t.Errorf("the safety-rails trip reason does not carry the verdict: %+v", as.state.Safety)
	}
}

// The control: three failures of three different kinds are three independent
// problems, and the counters increment exactly as they did before #1487.
func TestCascadeProductFault_MixedKindsStillChargeTheIssues(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)

	failures := []struct {
		repo   string
		number int
		kind   string
	}{
		{"octocat/acme-site", 69, TerminalKindDevBuildVerificationMissing},
		{"octocat/acme-app", 530, "subagent_crash"},
		{"octocat/acme-app", 531, "validation_failed"},
	}
	for _, f := range failures {
		addRunning(as, f.repo, f.number, "issue")
		as.onPipelineComplete(f.repo, f.number, false, false, f.kind, "stage failed")
		as.drainBackground()
	}

	if !as.cascadeTracker.IsTripped() {
		t.Fatal("the cascade breaker did not trip on 3 failures in the window")
	}
	for _, f := range failures {
		key := f.repo + "#" + strconv.Itoa(f.number)
		if got := as.state.LifetimeIssueFailures[key]; got != 1 {
			t.Errorf("LifetimeIssueFailures[%q] = %d, want 1 — unrelated kinds are unrelated failures and each issue owns its own",
				key, got)
		}
	}
	if strings.Contains(as.state.PauseReason, "product fault") {
		t.Errorf("PauseReason claims a product fault over three different kinds: %q", as.state.PauseReason)
	}
}

// A refund only ever un-charges what was charged: an issue that failed twice
// inside the window is refunded twice, and a counter never goes negative.
func TestRefundLifetimeFailuresLocked(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{
		LifetimeIssueFailures: map[string]int{"o/r#1": 2, "o/r#2": 1},
		QuarantinedIssues:     map[string]bool{"o/r#1": true},
	}}

	if got := as.refundLifetimeFailuresLocked([]string{"o/r#1", "o/r#1", "o/r#2", "o/r#3"}); got != 3 {
		t.Errorf("refunded = %d, want 3 — o/r#3 was never charged and cannot be refunded", got)
	}
	if _, ok := as.state.LifetimeIssueFailures["o/r#1"]; ok {
		t.Errorf("o/r#1 = %d, want the key deleted at zero", as.state.LifetimeIssueFailures["o/r#1"])
	}
	if as.state.QuarantinedIssues["o/r#1"] {
		t.Error("the quarantine record outlived the counter that produced it — every later dispatch pass re-derives the skip from the counter, so the record would report a quarantine that no longer exists")
	}
	if _, ok := as.state.LifetimeIssueFailures["o/r#2"]; ok {
		t.Error("o/r#2 should have been deleted at zero")
	}
}

// SharedProductFaultKind is deliberately conservative: an unclassified failure
// is not evidence of a shared cause, and treating it as one would switch the
// lifetime cap off for every un-kinded cluster.
func TestSharedProductFaultKind(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		entries    [][2]string // {repo#n-ish repo, reason}
		wantShared bool
		wantKind   string
		wantKeys   []string
	}{
		{
			name:       "one failure is not a cascade",
			entries:    [][2]string{{"o/r", "k"}},
			wantShared: false,
		},
		{
			name:       "all one kind",
			entries:    [][2]string{{"o/a", "k"}, {"o/b", "k"}, {"o/c", "k"}},
			wantShared: true,
			wantKind:   "k",
			wantKeys:   []string{"o/a#1", "o/b#2"},
		},
		{
			name:       "one differing kind is enough to disqualify",
			entries:    [][2]string{{"o/a", "k"}, {"o/b", "j"}, {"o/c", "k"}},
			wantShared: false,
		},
		{
			name:       "an empty kind is not a shared cause",
			entries:    [][2]string{{"o/a", ""}, {"o/b", ""}},
			wantShared: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 99, Window: time.Hour})
			for i, e := range tc.entries {
				c.RecordFailure(e[0], i+1, e[1], now)
			}
			kind, keys, shared := c.SharedProductFaultKind(now)
			if shared != tc.wantShared {
				t.Fatalf("shared = %v, want %v", shared, tc.wantShared)
			}
			if !shared {
				return
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if strings.Join(keys, ",") != strings.Join(tc.wantKeys, ",") {
				t.Errorf("chargedKeys = %v, want %v — the newest entry has not been charged yet at the moment the breaker trips",
					keys, tc.wantKeys)
			}
		})
	}
}

// A kill the scheduler itself issued is not the issue's failure (#1487).
// Observed: an operator pressed Stop, the two stages in flight exited 143 with
// no classified terminal kind, each took its issue to 1/2 on the lifetime cap,
// and together with one unrelated failure they tripped the cascading-failures
// breaker — halting the fleet the operator had only asked to pause.
func TestOperatorStop_DoesNotChargeTheIssueOrFeedTheCascade(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.stopRequested = true

	for _, n := range []int{309, 74, 779} {
		addRunning(as, "octocat/acme-app", n, "issue")
		// terminal kind "" is the shape the incident actually produced: SIGTERM,
		// exit 143, signal_source none, nothing for a text classifier to read.
		as.onPipelineComplete("octocat/acme-app", n, false, false, "", "exit 143")
		as.drainBackground()
	}

	if len(as.state.LifetimeIssueFailures) != 0 {
		t.Errorf("LifetimeIssueFailures = %v, want empty — the scheduler killed these runs",
			as.state.LifetimeIssueFailures)
	}
	if as.cascadeTracker.IsTripped() {
		t.Error("the cascade breaker counted the scheduler's own kills — a Stop would halt the fleet it was only pausing")
	}
	if as.state.Status == "safety_tripped" {
		t.Errorf("Status = %q — a Stop must not trip safety mode", as.state.Status)
	}
	for _, f := range as.state.Failed {
		if f.Kind != TerminalKindOperatorStop {
			t.Errorf("Failed entry for #%d has kind %q, want %q", f.Number, f.Kind, TerminalKindOperatorStop)
		}
	}
	// HoldNone: nothing is waiting on a person, so the reconcile re-admits the
	// issues and a resume dispatches them straight away.
	if hold := HoldForTerminalKind(TerminalKindOperatorStop); hold != HoldNone {
		t.Errorf("HoldForTerminalKind(%q) = %q, want %q — a stopped run needs no triage, only a resume",
			TerminalKindOperatorStop, hold, HoldNone)
	}
}

// The classified half of the same rule: the stage carried
// TerminalKindOperatorStop (runPipeline set it off execution.Manager's own
// Cancelled flag) even though the scheduler is not in a stop.
func TestOperatorStop_ClassifiedKindIsExemptWithoutStopRequested(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)

	for _, n := range []int{1, 2, 3} {
		addRunning(as, "octocat/acme-app", n, "issue")
		as.onPipelineComplete("octocat/acme-app", n, false, false, TerminalKindOperatorStop, "cancelled")
		as.drainBackground()
	}

	if len(as.state.LifetimeIssueFailures) != 0 {
		t.Errorf("LifetimeIssueFailures = %v, want empty", as.state.LifetimeIssueFailures)
	}
	if as.cascadeTracker.IsTripped() {
		t.Error("operator_stop fed the cascade breaker")
	}
}

// An unclassified cluster is not a shared cause: every un-kinded failure
// carries the same `pipeline_failure` placeholder, so reading it as one defect
// would refund every cascade of unclassified failures — the lifetime cap
// switching itself off rather than a verdict about a defect.
func TestCascadeProductFault_UnclassifiedFailuresStillCharge(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)

	for _, n := range []int{1, 2, 3} {
		addRunning(as, "octocat/acme-app", n, "issue")
		as.onPipelineComplete("octocat/acme-app", n, false, false, "", "stage failed")
		as.drainBackground()
	}

	if !as.cascadeTracker.IsTripped() {
		t.Fatal("the cascade breaker did not trip")
	}
	for _, n := range []int{1, 2, 3} {
		key := "octocat/acme-app#" + strconv.Itoa(n)
		if got := as.state.LifetimeIssueFailures[key]; got != 1 {
			t.Errorf("LifetimeIssueFailures[%q] = %d, want 1", key, got)
		}
	}
}
