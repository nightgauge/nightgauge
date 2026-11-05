package github

import (
	"testing"
	"time"
)

// Drain comes from Remaining, so inflated pre-#2692 Cost values cannot make a
// bucket look hot.
func TestSummarizeWindowBucketDrain(t *testing.T) {
	now := time.Now()
	ts := func(m int) string { return now.Add(-time.Duration(m) * time.Minute).UTC().Format(time.RFC3339Nano) }
	r1, r2 := now.Add(5*time.Minute).Unix(), now.Add(10*time.Minute).Unix()
	recs := []APILedgerRecord{
		{TS: ts(50), Kind: "graphql", Identity: "app:x", Reset: r1, Remaining: 4860, HeaderObserved: true},
		{TS: ts(40), Kind: "graphql", Identity: "app:x", Reset: r2, Remaining: 4788, Cost: 0, HeaderObserved: true},
		{TS: ts(30), Kind: "graphql", Identity: "app:x", Reset: r1, Remaining: 4840, Cost: 4000, HeaderObserved: true},
		{TS: ts(20), Kind: "graphql", Identity: "app:x", Reset: r2, Remaining: 4780, Cost: 4000, HeaderObserved: true},
	}
	w := SummarizeWindow(recs, now.Add(-time.Hour), now)
	if len(w.Buckets) != 2 || w.Buckets[0].Drain != 20 || w.Buckets[1].Drain != 8 {
		t.Fatalf("buckets = %+v, want drains 20 and 8", w.Buckets)
	}
	if w.Points <= GraphQLHourlyLimit*IdleBudgetWarnFraction {
		t.Fatalf("fixture must have inflated summed Points; got %d", w.Points)
	}
	if w.OverIdleBudget() {
		t.Error("OverIdleBudget true for two quiet buckets")
	}
	if w.Buckets[0].Limit != GraphQLHourlyLimit {
		t.Errorf("default limit = %d", w.Buckets[0].Limit)
	}
}

// A bucket that reset minutes ago holds one startup burst; projecting that
// slice to an hour must not raise a false idle-budget warning.
func TestFreshBucketBurstIsNotProjected(t *testing.T) {
	now := time.Now()
	ts := func(m int) string { return now.Add(-time.Duration(m) * time.Minute).UTC().Format(time.RFC3339Nano) }
	reset := now.Add(55 * time.Minute).Unix() // bucket began 5 minutes ago
	recs := []APILedgerRecord{
		{TS: ts(5), Kind: "graphql", Identity: "app:x", Reset: reset, Remaining: 5000, HeaderObserved: true},
		{TS: ts(1), Kind: "graphql", Identity: "app:x", Reset: reset, Remaining: 3400, HeaderObserved: true},
	}
	w := SummarizeWindow(recs, now.Add(-time.Hour), now)
	if len(w.Buckets) != 1 || w.Buckets[0].Drain != 1600 {
		t.Fatalf("buckets = %+v, want one bucket draining 1600", w.Buckets)
	}
	if w.OverIdleBudget() {
		t.Errorf("OverIdleBudget true for a 1600-point burst in a fresh bucket (projected %.0f/h)",
			w.Buckets[0].ProjectedPerHour(w.Since, w.Until))
	}
}
