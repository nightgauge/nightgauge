package orchestrator

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// #3255: a kind that arrives over IPC books host_slept only on the
// scheduler's structured verdict, never on echoed `[host-slept]` text.
func TestGateNotifyTerminalKindHostSleptVerdict(t *testing.T) {
	echoed := "Previous attempt said: [host-slept] the host slept for 2m0s during feature-dev — " +
		"but this run failed: validation_error: go test failed"
	genuine := HostSleptStageError("feature-validate", 20*time.Minute, 15*time.Minute)
	cases := []struct {
		name    string
		kind    string
		detail  string
		verdict bool
		want    func(string) bool
	}{
		{"host_slept without verdict is re-derived", TerminalKindHostSlept, echoed, false,
			func(k string) bool { return k != TerminalKindHostSlept }},
		{"host_slept with verdict is kept", TerminalKindHostSlept, genuine, true,
			func(k string) bool { return k == TerminalKindHostSlept }},
		{"empty kind with verdict books host_slept", "", genuine, true,
			func(k string) bool { return k == TerminalKindHostSlept }},
		{"empty kind without verdict ignores marker text", "", genuine, false,
			func(k string) bool { return k != TerminalKindHostSlept }},
		{"other kind passes through", "stream_idle_timeout", echoed, false,
			func(k string) bool { return k == "stream_idle_timeout" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := &AutonomousScheduler{
				state:            &AutonomousState{},
				hostSleptVerdict: func(string, int) bool { return tc.verdict },
			}
			got := as.gateNotifyTerminalKind("o/r", 7, tc.kind, tc.detail)
			if !tc.want(got) {
				t.Fatalf("gateNotifyTerminalKind(%q, verdict=%v) = %q", tc.kind, tc.verdict, got)
			}
		})
	}
}

func TestPersistedHostSleptVerdict(t *testing.T) {
	dir := t.TempDir()
	dispatched := time.Now().Add(-time.Minute)
	if persistedHostSleptVerdict(dir, 7, dispatched) {
		t.Fatal("no snapshot must answer false")
	}
	persist := func(issue int, slept bool) {
		id, err := runstate.NewRunID()
		if err != nil {
			t.Fatal(err)
		}
		rs := state.NewRuntimeState("o/r", issue, "item", id)
		rs.BeginStage(state.PipelineStage("feature-validate"))
		if slept {
			rs.SetStageHostSleptError("feature-validate", "[host-slept] slept")
		} else {
			rs.SetStageError("feature-validate", "[host-slept] echoed")
		}
		if err := rs.Persist(dir); err != nil {
			t.Fatal(err)
		}
	}
	persist(7, true)
	persist(8, false)
	if !persistedHostSleptVerdict(dir, 7, dispatched) {
		t.Fatal("snapshot with StageHostSlept must answer true")
	}
	if persistedHostSleptVerdict(dir, 8, dispatched) {
		t.Fatal("marker text without the verdict must answer false")
	}
	// A prior run's leftover snapshot carrying the verdict must not book the
	// run dispatched after it (#3255 review).
	if persistedHostSleptVerdict(dir, 7, time.Now().Add(time.Hour)) {
		t.Fatal("a snapshot older than the completing run's dispatch must answer false")
	}
}

// Mirrors corpus row host-slept-echoed-no-verdict: without the structured
// verdict the echoed marker never resolves to host_slept (#3255 AC3).
func TestResolveTerminalKindEchoedHostSleptNoVerdict(t *testing.T) {
	in := "Previous attempt said: [host-slept] the host slept for 2m0s during feature-dev " +
		"(wall clock 30m0s, awake 28m0s) — but this run failed: validation_error: go test failed"
	if got := ResolveTerminalKind(false, "", false, in); got == TerminalKindHostSlept {
		t.Fatalf("ResolveTerminalKind without verdict = %q", got)
	}
}
