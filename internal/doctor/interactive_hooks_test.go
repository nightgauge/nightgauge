package doctor

import (
	"context"
	"sync"
	"testing"
)

// #2095. The hooks the interactive CLI drives the engine through.

// ConsentAuto asks about an auto remedy too, with the verb's live preview and
// a redacted finding; a no leaves it unapplied.
func TestRemedyEngine_ConsentAutoAsksAboutAutoRemedies(t *testing.T) {
	w := &fakeWorld{evidence: "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))

	var seenPreview, seenNote string
	r := onlyResult(t, fx.Run(context.Background(), FixOptions{
		ConsentAuto: true,
		Consent: func(f Finding, rem Remedy) bool {
			seenPreview, seenNote = rem.Preview, f.Evidence["note"]
			return false
		},
	}))
	if r.Action != ActionAwaitingConsent || w.applied.Load() != 0 {
		t.Fatalf("declined auto remedy: %s/%s, applied %d", r.Action, r.Outcome, w.applied.Load())
	}
	if seenPreview == "" {
		t.Errorf("Consent saw no preview")
	}
	if seenNote == w.evidence {
		t.Errorf("Consent received an unredacted credential-shaped value")
	}

	r = onlyResult(t, fx.Run(context.Background(), FixOptions{
		ConsentAuto: true,
		Consent:     func(Finding, Remedy) bool { return true },
	}))
	if r.Outcome != OutcomeFixed || w.applied.Load() != 1 {
		t.Fatalf("consented auto remedy: %s/%s, applied %d", r.Action, r.Outcome, w.applied.Load())
	}
}

// Without ConsentAuto an auto remedy is applied without asking, as --fix does.
func TestRemedyEngine_AutoRemedyNotAskedWithoutConsentAuto(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))
	asked := 0
	r := onlyResult(t, fx.Run(context.Background(), FixOptions{Consent: func(Finding, Remedy) bool { asked++; return false }}))
	if asked != 0 || r.Outcome != OutcomeFixed {
		t.Fatalf("asked %d, outcome %s", asked, r.Outcome)
	}
}

// Observe sees every check start and finish, including a skipped dependent
// that never starts.
func TestRunner_ObserveReportsEveryStateChange(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(Check{ID: "root", Group: "g", Code: "NGD001", Run: func(context.Context, *Env) []Finding {
		return []Finding{{Code: "NGD001", Check: "root", Severity: SeverityBlocker, Fingerprint: "x"}}
	}})
	reg.MustRegister(Check{ID: "child", Group: "g", Code: "NGD002", DependsOn: []string{"root"},
		Run: func(context.Context, *Env) []Finding { return nil }})
	reg.MustRegister(Check{ID: "other", Group: "h", Code: "NGD003", Run: func(context.Context, *Env) []Finding { return nil }})

	var mu sync.Mutex
	started := map[string]int{}
	finished := map[string]CheckStatus{}
	Runner{Observe: func(ev CheckEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.Running {
			started[ev.ID]++
			return
		}
		finished[ev.ID] = ev.Result.Status
	}}.Run(context.Background(), reg, &Env{})

	if started["root"] != 1 || started["other"] != 1 || started["child"] != 0 {
		t.Errorf("started = %v", started)
	}
	want := map[string]CheckStatus{"root": StatusFailed, "child": StatusSkipped, "other": StatusPassed}
	for id, st := range want {
		if finished[id] != st {
			t.Errorf("%s finished %q, want %q", id, finished[id], st)
		}
	}
}
