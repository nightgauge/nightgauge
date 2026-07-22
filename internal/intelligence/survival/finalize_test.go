package survival

import (
	"testing"
	"time"
)

const testMergedAt = "2026-06-01T12:00:00Z"

func mergedAtTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, testMergedAt)
	if err != nil {
		t.Fatalf("parse testMergedAt: %v", err)
	}
	return parsed
}

func pendingRec() Record {
	return NewPending("nightgauge/nightgauge", 4151, 4200, "abc123def456", testMergedAt, "main")
}

func TestDecideVerdict_RevertActsImmediately(t *testing.T) {
	merged := mergedAtTime(t)
	// Inside the window — a revert is proven ground truth and finalizes now.
	now := merged.Add(2 * 24 * time.Hour)
	got, changed := DecideVerdict(pendingRec(), now, 7, Observation{RevertFound: true, RevertSHA: "rev999"})
	if !changed {
		t.Fatal("expected a terminal transition on revert")
	}
	if got.Verdict != Reverted {
		t.Errorf("verdict = %q, want reverted", got.Verdict)
	}
	if got.Evidence != EvidenceRevertCommit {
		t.Errorf("evidence = %q, want %q", got.Evidence, EvidenceRevertCommit)
	}
	if got.ObservedAt == "" {
		t.Error("expected ObservedAt to be stamped")
	}
}

func TestDecideVerdict_BreakageActsImmediately(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(1 * 24 * time.Hour) // still inside window
	got, changed := DecideVerdict(pendingRec(), now, 7, Observation{Broke: true, BrokeDetail: "check X"})
	if !changed || got.Verdict != Broke {
		t.Fatalf("verdict=%q changed=%v, want broke/true", got.Verdict, changed)
	}
	if got.Evidence != EvidenceAncestryCI {
		t.Errorf("evidence = %q, want %q", got.Evidence, EvidenceAncestryCI)
	}
}

func TestDecideVerdict_RevertTakesPrecedenceOverBreakage(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(8 * 24 * time.Hour)
	got, _ := DecideVerdict(pendingRec(), now, 7, Observation{RevertFound: true, Broke: true})
	if got.Verdict != Reverted {
		t.Errorf("verdict = %q, want reverted (revert precedence)", got.Verdict)
	}
}

func TestDecideVerdict_WindowElapsedClean_Survived(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(8 * 24 * time.Hour) // past 7d window, under 14d
	got, changed := DecideVerdict(pendingRec(), now, 7, Observation{})
	if !changed || got.Verdict != Survived {
		t.Fatalf("verdict=%q changed=%v, want survived/true", got.Verdict, changed)
	}
	if got.Evidence != EvidenceWindowClean {
		t.Errorf("evidence = %q, want %q", got.Evidence, EvidenceWindowClean)
	}
}

func TestDecideVerdict_AgedOut_Unobserved(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(15 * 24 * time.Hour) // past 2×window
	got, changed := DecideVerdict(pendingRec(), now, 7, Observation{})
	if !changed || got.Verdict != Unobserved {
		t.Fatalf("verdict=%q changed=%v, want unobserved/true", got.Verdict, changed)
	}
	if got.Evidence != EvidenceWindowUnobserved {
		t.Errorf("evidence = %q, want %q", got.Evidence, EvidenceWindowUnobserved)
	}
}

func TestDecideVerdict_NotYetDue_StaysPending(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(3 * 24 * time.Hour) // inside window, no evidence
	got, changed := DecideVerdict(pendingRec(), now, 7, Observation{})
	if changed {
		t.Fatalf("expected no transition before window elapses, got %q", got.Verdict)
	}
	if got.Verdict != Pending {
		t.Errorf("verdict = %q, want pending", got.Verdict)
	}
}

func TestDecideVerdict_AgedOutButReverted_StaysNegative(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(20 * 24 * time.Hour) // well past 2×window
	// A revert is proven negative and must win even past the age-out horizon.
	got, _ := DecideVerdict(pendingRec(), now, 7, Observation{RevertFound: true})
	if got.Verdict != Reverted {
		t.Errorf("verdict = %q, want reverted (negative beats age-out)", got.Verdict)
	}
}

func TestDecideVerdict_TerminalIsIdempotent(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(20 * 24 * time.Hour)
	rec := pendingRec()
	rec.Verdict = Survived
	rec.Evidence = EvidenceWindowClean
	got, changed := DecideVerdict(rec, now, 7, Observation{RevertFound: true})
	if changed {
		t.Error("expected no re-finalization of a terminal record")
	}
	if got.Verdict != Survived {
		t.Errorf("verdict = %q, want survived (unchanged)", got.Verdict)
	}
}

func TestDecideVerdict_UnparseableMergedAt_StaysPendingButNegativeActs(t *testing.T) {
	now := mergedAtTime(t).Add(30 * 24 * time.Hour)
	bad := pendingRec()
	bad.MergedAt = "not-a-timestamp"

	// No evidence + cannot determine window → stay pending.
	got, changed := DecideVerdict(bad, now, 7, Observation{})
	if changed || got.Verdict != Pending {
		t.Errorf("verdict=%q changed=%v, want pending/false on unparseable mergedAt", got.Verdict, changed)
	}
	// Negative evidence still finalizes (it does not depend on the window).
	got2, changed2 := DecideVerdict(bad, now, 7, Observation{Broke: true})
	if !changed2 || got2.Verdict != Broke {
		t.Errorf("verdict=%q changed=%v, want broke/true even with unparseable mergedAt", got2.Verdict, changed2)
	}
}

func TestDecideVerdict_ZeroWindowUsesDefault(t *testing.T) {
	merged := mergedAtTime(t)
	now := merged.Add(8 * 24 * time.Hour) // past default 7d
	got, changed := DecideVerdict(pendingRec(), now, 0, Observation{})
	if !changed || got.Verdict != Survived {
		t.Fatalf("verdict=%q changed=%v, want survived with defaulted window", got.Verdict, changed)
	}
}

func TestIsDue(t *testing.T) {
	merged := mergedAtTime(t)
	cases := []struct {
		name string
		rec  func() Record
		now  time.Time
		want bool
	}{
		{"not yet due", pendingRec, merged.Add(3 * 24 * time.Hour), false},
		{"exactly at window", pendingRec, merged.Add(7 * 24 * time.Hour), true},
		{"past window", pendingRec, merged.Add(10 * 24 * time.Hour), true},
		{"terminal record never due", func() Record { r := pendingRec(); r.Verdict = Survived; return r }, merged.Add(30 * 24 * time.Hour), false},
		{"unparseable mergedAt never due", func() Record { r := pendingRec(); r.MergedAt = "bad"; return r }, merged.Add(30 * 24 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDue(tc.rec(), tc.now, 7); got != tc.want {
				t.Errorf("IsDue = %v, want %v", got, tc.want)
			}
		})
	}
}
