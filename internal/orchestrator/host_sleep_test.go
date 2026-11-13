package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/terminalkind"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// The arithmetic: wall span minus awake span, never negative (#3136).
func TestHostSleptDuration(t *testing.T) {
	start := time.Now()
	if got := HostSleptDuration(start, start.Add(20*time.Minute)); got != 0 {
		t.Errorf("Add moves wall and monotonic together, so no sleep: got %v", got)
	}
	if got := HostSleptDuration(start.Round(0), start.Add(20*time.Minute).Round(0)); got != 0 {
		t.Errorf("without monotonic readings both spans are wall spans: got %v", got)
	}
	for _, tc := range []struct{ wall, awake, want time.Duration }{
		{20 * time.Minute, 20 * time.Minute, 0},
		{20 * time.Minute, 4*time.Minute + 33*time.Second, 15*time.Minute + 27*time.Second},
		{time.Minute, 2 * time.Minute, 0},
	} {
		if got := sleptFrom(tc.wall, tc.awake); got != tc.want {
			t.Errorf("sleptFrom(%v, %v) = %v, want %v", tc.wall, tc.awake, got, tc.want)
		}
	}
}

// A gate timeout, a stall kill and a stage cap are host_slept when the host
// slept 900 s, and keep today's kind at 0 s and at exactly the threshold.
func TestReclassifyForHostSleep(t *testing.T) {
	for _, kind := range []string{TerminalKindValidationError, TerminalKindStallKill, TerminalKindStageCapKill, TerminalKindSubagentCrash, ""} {
		for _, tc := range []struct {
			slept time.Duration
			want  string
		}{
			{0, kind},
			{HostSleepThreshold, kind},
			{900 * time.Second, TerminalKindHostSlept},
		} {
			if got := ReclassifyForHostSleep(kind, tc.slept); got != tc.want {
				t.Errorf("ReclassifyForHostSleep(%q, %v) = %q, want %q", kind, tc.slept, got, tc.want)
			}
		}
	}
	for _, kind := range []string{TerminalKindOperatorStop, TerminalKindBudgetExceeded, TerminalKindBudgetCeiling,
		TerminalKindArchitectureApprovalRequired, TerminalKindContextWindowExceeded, TerminalKindHostSlept} {
		if got := ReclassifyForHostSleep(kind, 900*time.Second); got != kind {
			t.Errorf("ReclassifyForHostSleep(%q, 900s) = %q, want it left alone", kind, got)
		}
	}
}

func TestHostSleptIsTransient(t *testing.T) {
	if !isTransientRevertKind(TerminalKindHostSlept) {
		t.Error("host_slept must return the issue to Ready like a stall kill")
	}
}

// modelUnavailableSpoofText classifies on its own; prefixed with a spoofed
// marker it must keep that kind.
const modelUnavailableSpoofText = "Error: unknown model id claude-nope"

// Only the structured verdict yields host_slept (#3158): a "[host-slept]"
// marker in error text keeps the kind the text would have had without it.
func TestResolveTerminalKindHostSleptVerdict(t *testing.T) {
	genuine := HostSleptStageError("feature-validate", 20*time.Minute, 15*time.Minute+27*time.Second)
	cases := []struct {
		name      string
		gateRan   bool
		gateKind  string
		hostSlept bool
		text      string
		want      string
	}{
		{"genuine verdict outranks gate", true, TerminalKindValidationError, true, genuine, TerminalKindHostSlept},
		{"spoofed marker keeps gate kind", true, TerminalKindValidationError, false, genuine, TerminalKindValidationError},
		{"spoofed marker keeps prose kind", false, "", false,
			HostSleptMarker + " " + modelUnavailableSpoofText, ClassifyTerminalKind(modelUnavailableSpoofText)},
		{"bare spoofed marker is unclassified", false, "", false, HostSleptMarker, ""},
		{"no marker, gate kind stands", true, TerminalKindValidationError, false, "stage gate failed: timed out", TerminalKindValidationError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveTerminalKind(tc.gateRan, tc.gateKind, tc.hostSlept, tc.text); got != tc.want {
				t.Errorf("ResolveTerminalKind = %q, want %q", got, tc.want)
			}
		})
	}
	if ClassifyTerminalKind(modelUnavailableSpoofText) == "" || ClassifyTerminalKind(modelUnavailableSpoofText) == TerminalKindHostSlept {
		t.Fatalf("fixture text must classify to a real kind, got %q", ClassifyTerminalKind(modelUnavailableSpoofText))
	}
	// AC5: the operator-facing text still matches the shared rule table.
	if got := terminalkind.Classify(genuine); got != TerminalKindHostSlept {
		t.Errorf("rule table classifies the marker as %q, want host_slept", got)
	}
}

// bookHostSleptStage replaces the stage error only when the stage slept past
// the threshold and its kind is one sleep explains.
func TestBookHostSleptStage(t *testing.T) {
	prev := measureHostSleep
	t.Cleanup(func() { measureHostSleep = prev })
	s := &Scheduler{}

	measureHostSleep = func(time.Time, time.Time) time.Duration { return 0 }
	rt := state.NewRuntimeState("O/a", 1, "", "run")
	if s.bookHostSleptStage(1, state.StageFeatureValidate, rt, time.Now(), TerminalKindValidationError, "stage gate failed") {
		t.Fatal("booked host_slept with 0 s of sleep")
	}

	measureHostSleep = func(time.Time, time.Time) time.Duration { return 900 * time.Second }
	if !s.bookHostSleptStage(1, state.StageFeatureValidate, rt, time.Now(), TerminalKindValidationError, "stage gate failed") {
		t.Fatal("did not book host_slept with 900 s of sleep")
	}
	got := rt.Snapshot().StageErrors[string(state.StageFeatureValidate)]
	if !strings.HasPrefix(got, HostSleptMarker) || !strings.Contains(got, "15m0s") {
		t.Errorf("stage error = %q, want the marker and the sleep", got)
	}
	snap := rt.Snapshot()
	if !snap.StageHostSlept[string(state.StageFeatureValidate)] {
		t.Fatal("bookHostSleptStage did not set the structured host-slept verdict")
	}
	if s.bookHostSleptStage(1, state.StageFeatureValidate, rt, time.Now(), TerminalKindOperatorStop, "") {
		t.Error("an operator stop was rebooked as host_slept")
	}
}

// The card and the stage error say how long the host slept and how to stop
// it, and never call the run a validation failure or cite the lifetime cap.
func TestHostSleptOperatorText(t *testing.T) {
	detail := HostSleptStageError("feature-validate", 20*time.Minute, 15*time.Minute+27*time.Second)
	body := hostSleptCardBody("O/a", 1, "t", detail)
	for name, text := range map[string]string{"stage error": detail, "card": body} {
		for _, want := range []string{"15m27s", "pmset -c sleep 0", "caffeinate"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q: %s", name, want, text)
			}
		}
		lower := strings.ToLower(text)
		for _, banned := range []string{"validation", "lifetime failure cap"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s contains %q: %s", name, banned, text)
			}
		}
	}
}

// host_slept charges no lifetime failure and the slot-failure repo halt is
// declined for it.
func TestOnPipelineCompleteHostSleptDoesNotHaltOrCharge(t *testing.T) {
	as, _, _ := revertFixture(t, map[int]string{})
	as.state.Running = []RunningItem{{Repo: "O/a", Number: 1, Title: "t"}}

	as.onPipelineComplete("O/a", 1, false, false, TerminalKindHostSlept,
		HostSleptStageError("feature-validate", 20*time.Minute, 15*time.Minute), false)
	as.waitBackground()

	as.mu.Lock()
	lifetime := as.state.LifetimeIssueFailures["O/a#1"]
	_, planned := as.retryBackoff["O/a#1"]
	as.mu.Unlock()
	if lifetime != 0 {
		t.Errorf("host_slept charged %d lifetime failure(s), want 0", lifetime)
	}
	if !planned {
		t.Error("no retry scheduled for host_slept")
	}
	if as.PauseRepo("O/a", "haltQueueOnSlotFailure: issue #1 failed at feature-validate", haltTagSlotFailure, 1, "feature-validate") {
		t.Error("PauseRepo accepted a slot-failure halt for a host_slept run")
	}
	as.mu.Lock()
	_, halted := as.state.PausedRepos["O/a"]
	as.mu.Unlock()
	if halted {
		t.Error("repository halted over a host_slept run")
	}
	// A real failure of another issue still halts.
	if !as.PauseRepo("O/a", "haltQueueOnSlotFailure: issue #2 failed", haltTagSlotFailure, 2, "feature-dev") {
		t.Error("PauseRepo declined an ordinary slot-failure halt")
	}
}
