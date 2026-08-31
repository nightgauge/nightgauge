package state

import "testing"

// The end-of-stage back-fill records UNREPORTED, not skipped (#1246).
//
// "Skipped" asserts the stage decided not to run the phase. What the back-fill
// actually observes is silence: the stage ended and nothing ever reported it.
// feature-dev's 18 phase markers are unconditional in the skill and the model
// emits them in roughly 11% of runs, so on issue #336 the durable record
// claimed fourteen deliberate skips — including `testing` and
// `write-dev-context`, both of which that run's own gate record and session
// log prove ran. A retro reading those records learns the opposite of the
// truth, which is worse than learning nothing.
func TestUnreportedPhase_IsDistinctFromSkipped(t *testing.T) {
	rs := &RuntimeState{}
	rs.UnreportedPhase(StageFeatureDev, "testing", 9, 18)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d record(s), want 1", len(rs.PhaseHistory))
	}
	p := rs.PhaseHistory[0]
	if p.Status != "unreported" {
		t.Errorf("Status = %q, want unreported (a back-fill is not a decision)", p.Status)
	}
	if p.Name != "testing" || p.Index != 9 || p.Total != 18 {
		t.Errorf("record = %+v, want name=testing index=9 total=18", p)
	}
	// Terminal on arrival, like a skip: nothing will ever close it.
	if p.CompletedAt == nil {
		t.Error("CompletedAt is nil — an unreported phase is terminal, not running")
	}
}

func TestUnreportedPhase_IsIdempotent(t *testing.T) {
	rs := &RuntimeState{}
	rs.UnreportedPhase(StageFeatureDev, "testing", 9, 18)
	rs.UnreportedPhase(StageFeatureDev, "testing", 9, 18)

	if len(rs.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory has %d record(s), want 1 — a re-notification must not double-count",
			len(rs.PhaseHistory))
	}
}

// A phase that reported a real outcome must survive the back-fill. The sweep
// runs over every registry phase unconditionally (#1232), so it reaches phases
// that already completed — and an unreported record overwriting a complete one
// would erase the very evidence the split exists to preserve.
func TestUnreportedPhase_NeverOverwritesARealOutcome(t *testing.T) {
	for _, existing := range []struct {
		name  string
		apply func(rs *RuntimeState)
		want  string
	}{
		{"complete", func(rs *RuntimeState) {
			rs.BeginPhase(StageFeatureDev, "testing", 9, 18)
			rs.CompletePhase(StageFeatureDev, "testing")
		}, "complete"},
		{"skipped", func(rs *RuntimeState) {
			rs.SkipPhase(StageFeatureDev, "testing", 9, 18)
		}, "skipped"},
		{"failed", func(rs *RuntimeState) {
			rs.FailPhase(StageFeatureDev, "testing", 9, 18)
		}, "failed"},
	} {
		t.Run(existing.name, func(t *testing.T) {
			rs := &RuntimeState{}
			existing.apply(rs)
			rs.UnreportedPhase(StageFeatureDev, "testing", 9, 18)

			if len(rs.PhaseHistory) != 1 {
				t.Fatalf("PhaseHistory has %d record(s), want 1", len(rs.PhaseHistory))
			}
			if got := rs.PhaseHistory[0].Status; got != existing.want {
				t.Errorf("Status = %q, want %q — the back-fill must not overwrite a real outcome",
					got, existing.want)
			}
		})
	}
}
