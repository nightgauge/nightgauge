package survival

import (
	"encoding/json"
	"testing"
)

// The immediate post-merge observation (#1249) rides on the survival record
// next to the days-later Verdict, and must survive the journal's fold.
func TestRecord_MainCheckFieldsRoundTripThroughTheStore(t *testing.T) {
	store := NewStore(t.TempDir())
	rec := NewPending("nightgauge/nightgauge", 1249, 1360, "feedface", testMergedAt, "main")
	rec.MainCheckVerdict = "red"
	rec.MainCheckFailing = []string{"e2e"}
	if _, err := store.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}

	recs, err := store.Load()
	if err != nil || len(recs) != 1 {
		t.Fatalf("Load: %v (%d records)", err, len(recs))
	}
	got := recs[0]
	if got.MainCheckVerdict != "red" || len(got.MainCheckFailing) != 1 || got.MainCheckFailing[0] != "e2e" {
		t.Errorf("main check = %q %v, want red [e2e]", got.MainCheckVerdict, got.MainCheckFailing)
	}
	// Still pending on the survival axis: the two verdicts are independent.
	if got.Verdict != Pending {
		t.Errorf("Verdict = %q, want pending — a red main is not yet a survival verdict", got.Verdict)
	}

	// A record captured before the hook observed main serialises without the
	// fields at all, so old journals and new read the same way.
	raw, _ := json.Marshal(NewPending("o/r", 1, 2, "abc", testMergedAt, ""))
	var asMap map[string]any
	_ = json.Unmarshal(raw, &asMap)
	if _, present := asMap["main_check_verdict"]; present {
		t.Error("an unobserved record must not carry an empty main_check_verdict — absence is the signal")
	}
}
