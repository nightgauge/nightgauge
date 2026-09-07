package state

import (
	"sync"
	"testing"
)

// The cap-recovery provider pin is RUN-scoped (#1545), and these pin why.
//
// The first cut held it on the Scheduler, which the wave orchestrator shares
// across pipelines it runs concurrently. `go test -race` caught the write race;
// the worse defect it was hiding is the one below — a pin held on the shared
// scheduler re-points every OTHER run in flight, so a run that never hit a cap
// gets moved off its configured provider by one that did.

func TestTheCapPinIsScopedToOneRun(t *testing.T) {
	capped := &RuntimeState{}
	healthy := &RuntimeState{}

	capped.PinCapAdapter("codex")

	if got := capped.CapAdapterPin(); got != "codex" {
		t.Fatalf("capped run's pin = %q, want codex", got)
	}
	if got := healthy.CapAdapterPin(); got != "" {
		t.Fatalf("a concurrent run that never hit a cap was re-pointed to %q — "+
			"one run's recovery must never move another run's provider", got)
	}
}

// TestAnUnpinnedRunReportsNothing keeps the ordinary case honest: "" is what
// every consumer already treats as "no pin", so a run that never hits a cap
// behaves exactly as it did before the pin existed.
func TestAnUnpinnedRunReportsNothing(t *testing.T) {
	rs := &RuntimeState{}
	if got := rs.CapAdapterPin(); got != "" {
		t.Fatalf("pin = %q on a fresh run, want empty", got)
	}
	if got := rs.CapAdaptersTried(); got != nil {
		t.Fatalf("tried = %v on a fresh run, want nil", got)
	}
	// An empty pin is ignored rather than stored, so a caller cannot blank the
	// pin by accident and silently un-recover a capped run.
	rs.PinCapAdapter("")
	if got := rs.CapAdapterPin(); got != "" {
		t.Fatalf("an empty pin was stored as %q", got)
	}
}

// TestEveryPinnedAdapterIsMarkedTried pins the cycle guard's substrate: pinning
// is also what records the hop, so a later walk in the same run cannot place a
// provider that already refused it.
func TestEveryPinnedAdapterIsMarkedTried(t *testing.T) {
	rs := &RuntimeState{}
	rs.PinCapAdapter("codex")
	rs.PinCapAdapter("grok")

	tried := rs.CapAdaptersTried()
	for _, want := range []string{"codex", "grok"} {
		if !tried[want] {
			t.Errorf("%q was pinned but not marked tried", want)
		}
	}
	if got := rs.CapAdapterPin(); got != "grok" {
		t.Fatalf("pin = %q, want the most recent hop (grok)", got)
	}

	// The returned map is a COPY — the walk reads it outside the lock, and
	// handing out the live map would reintroduce the race this scope move
	// exists to remove.
	tried["codex"] = false
	if !rs.CapAdaptersTried()["codex"] {
		t.Fatal("CapAdaptersTried handed out the live map — a caller mutated run state")
	}
}

// TestConcurrentPinsAndReadsDoNotRace is the direct guard for the failure that
// forced this scope move. It is meaningless without -race and cheap with it.
func TestConcurrentPinsAndReadsDoNotRace(t *testing.T) {
	rs := &RuntimeState{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); rs.PinCapAdapter("codex") }()
		go func() { defer wg.Done(); _ = rs.CapAdapterPin(); _ = rs.CapAdaptersTried() }()
	}
	wg.Wait()
}
