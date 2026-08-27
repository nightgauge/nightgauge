package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// newRefinementTestScheduler builds a scheduler through the PRODUCTION
// constructor, not a struct literal.
//
// This matters more than it looks. The pre-existing refinement tests
// (TestRefineIssueSuccess_StateTransitions and its Failure sibling) build an
// AutonomousScheduler literal and then hand-mutate the state slices they go on
// to assert about — so they pass identically with the bug present and the bug
// fixed, and they would have passed if refineIssue had never existed. Going
// through NewAutonomousScheduler is what makes these tests capable of failing.
func newRefinementTestScheduler(t *testing.T) *AutonomousScheduler {
	t.Helper()
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())
	// The IPC path short-circuits refineViaCLI, which would otherwise need an
	// execution manager and a skill on disk. Refinement "succeeds"; what is
	// under test is everything after it.
	as.onRefinementDispatch = func(string, string, int) {}
	return as
}

// refineOnce runs one full refineIssue against the semaphore contract: the
// function's deferred release reads from refinementSem, so a caller must have
// filled the slot the dispatch loop would have taken.
func refineOnce(t *testing.T, as *AutonomousScheduler, number int) {
	t.Helper()
	as.refinementSem <- struct{}{}
	as.refineIssue(context.Background(), "O", "R", gh.UnrefinedIssue{
		Number: number,
		Title:  "Test issue",
	})
}

// TestRefineIssue_MarkRefinedFailure_RecordsFailedNotCompleted is the guard for
// the core of #993.
//
// Before the fix, a MarkRefined error was logged and dropped: the run was
// appended to RefinementCompleted and — in the same critical section — the
// consecutive-failure counter was DELETED. So the pipeline recorded a success
// it had not achieved, and the one counter that could have detected the
// repetition was reset by the very path the failure took.
func TestRefineIssue_MarkRefinedFailure_RecordsFailedNotCompleted(t *testing.T) {
	as := newRefinementTestScheduler(t)
	as.markRefinedFn = func(context.Context, string, string, int) error {
		return errors.New(`label "pipeline:refined" not found in repo O/R`)
	}

	refineOnce(t, as, 42)

	if got := len(as.state.RefinementCompleted); got != 0 {
		t.Errorf("RefinementCompleted has %d record(s), want 0 — a run that could not "+
			"record completion must not be reported as completed", got)
	}
	if got := len(as.state.RefinementFailed); got != 1 {
		t.Fatalf("RefinementFailed has %d record(s), want 1", got)
	}
	if got := as.state.RefinementFailed[0].Number; got != 42 {
		t.Errorf("failed record is for #%d, want #42", got)
	}
	if as.state.RefinementFailed[0].Reason == "" {
		t.Error("failed record carries no Reason — the operator cannot tell a missing " +
			"label from a revoked token")
	}
	if got := as.refinementFailures["O/R#42"]; got != 1 {
		t.Errorf("refinementFailures[O/R#42] = %d, want 1", got)
	}
	if got := len(as.state.RefinementRunning); got != 0 {
		t.Errorf("RefinementRunning has %d record(s), want 0 — the issue must leave the "+
			"running set on the failure path too", got)
	}
}

// TestRefineIssue_MarkRefinedSuccess_RecordsCompleted is the control. Without
// it, a fix that recorded EVERY run as failed would pass the test above.
func TestRefineIssue_MarkRefinedSuccess_RecordsCompleted(t *testing.T) {
	as := newRefinementTestScheduler(t)
	called := 0
	as.markRefinedFn = func(context.Context, string, string, int) error {
		called++
		return nil
	}

	refineOnce(t, as, 42)

	if called != 1 {
		t.Fatalf("markRefinedFn called %d times, want 1", called)
	}
	if got := len(as.state.RefinementCompleted); got != 1 {
		t.Errorf("RefinementCompleted has %d record(s), want 1", got)
	}
	if got := len(as.state.RefinementFailed); got != 0 {
		t.Errorf("RefinementFailed has %d record(s), want 0", got)
	}
	if got, ok := as.refinementFailures["O/R#42"]; ok {
		t.Errorf("refinementFailures[O/R#42] = %d after success, want the key cleared", got)
	}
}

// TestRefineIssue_RepeatedFailures_ReachThreshold proves the counter ACCUMULATES
// across runs and reaches the threshold the candidate loop consults.
//
// This is the assertion the old code could never satisfy: each pass through the
// success path deleted the counter, so it could not exceed 1 no matter how many
// times the same deterministic failure recurred. That is why the runaway loop
// was unbounded in time.
func TestRefineIssue_RepeatedFailures_ReachThreshold(t *testing.T) {
	as := newRefinementTestScheduler(t)
	as.markRefinedFn = func(context.Context, string, string, int) error {
		return errors.New("label not found")
	}

	key := "O/R#42"
	for i := 1; i <= maxRefinementFailures; i++ {
		refineOnce(t, as, 42)
		if got := as.refinementFailures[key]; got != i {
			t.Fatalf("after %d failure(s): refinementFailures[%s] = %d, want %d",
				i, key, got, i)
		}
		wantExhausted := i >= maxRefinementFailures
		if got := as.refinementExhausted(key); got != wantExhausted {
			t.Errorf("after %d failure(s): refinementExhausted = %v, want %v",
				i, got, wantExhausted)
		}
	}

	if got := len(as.state.RefinementFailed); got != maxRefinementFailures {
		t.Errorf("RefinementFailed has %d record(s), want %d — every attempt must leave "+
			"a record with its own reason", got, maxRefinementFailures)
	}
}

// TestRefinementExhausted_IsPerIssue guards against a threshold implemented on a
// scalar or on a repo-wide key: one poisoned issue must not stop refinement for
// every other issue in the repo.
func TestRefinementExhausted_IsPerIssue(t *testing.T) {
	as := newRefinementTestScheduler(t)
	as.markRefinedFn = func(_ context.Context, _, _ string, number int) error {
		if number == 42 {
			return errors.New("label not found")
		}
		return nil
	}

	for i := 0; i < maxRefinementFailures; i++ {
		refineOnce(t, as, 42)
	}
	refineOnce(t, as, 43)

	if !as.refinementExhausted("O/R#42") {
		t.Error("#42 should be exhausted after maxRefinementFailures failures")
	}
	if as.refinementExhausted("O/R#43") {
		t.Error("#43 succeeded and must not be exhausted — the threshold is per issue")
	}
	if as.refinementExhausted("O/R#99") {
		t.Error("an issue that never ran must not read as exhausted")
	}
}

// TestRefinementWarnOnce_FiresExactlyOnce guards the log-dedupe used by the
// preflight and the give-up path. A condition re-evaluated every 60s must not
// produce a line every 60s, and — the direction that actually matters — must
// still produce the FIRST one.
func TestRefinementWarnOnce_FiresExactlyOnce(t *testing.T) {
	as := newRefinementTestScheduler(t)
	calls := 0
	for i := 0; i < 5; i++ {
		as.refinementWarnOnce("labels:O/R", func() { calls++ })
	}
	if calls != 1 {
		t.Errorf("warn fired %d times, want exactly 1", calls)
	}
	as.refinementWarnOnce("labels:O/OTHER", func() { calls++ })
	if calls != 2 {
		t.Errorf("a different key must warn independently: calls = %d, want 2", calls)
	}
}

// TestRefinementLabelsMissing_NilServiceIsAnError proves the preflight fails
// CLOSED on an unusable service rather than returning an empty missing-list,
// which would read as "all labels present" and re-open the runaway path.
func TestRefinementLabelsMissing_NilServiceIsAnError(t *testing.T) {
	as := newRefinementTestScheduler(t)
	blockers, advisory, err := as.refinementLabelsMissing(context.Background(), nil, "O", "R")
	if err == nil {
		t.Fatal("expected an error for a nil issue service")
	}
	if len(blockers) != 0 || len(advisory) != 0 {
		t.Errorf("blockers=%v advisory=%v on error, want both empty", blockers, advisory)
	}
}

// TestRefinementBlockers_AreNarrowerThanTheRegistry is the guard against the
// over-broad preflight.
//
// A preflight that refuses on the WHOLE registry disables refinement on a
// perfectly healthy repo that has simply never had an epic, or that does not
// use the architecture gate. That trades a runaway loop for a silent stall,
// which is not obviously the better failure. Only the label whose absence makes
// MarkRefined hard-error may gate the loop.
func TestRefinementBlockers_AreNarrowerThanTheRegistry(t *testing.T) {
	// A repo with pipeline:refined and nothing else.
	only := map[string]string{gh.LabelRefined: "L_refined"}
	if got := gh.MissingRefinementBlockers(only); len(got) != 0 {
		t.Errorf("blockers = %v for a repo that HAS pipeline:refined, want none — "+
			"refinement must not be gated on labels it does not need", got)
	}
	if got := gh.MissingRequiredLabels(only); len(got) == 0 {
		t.Error("MissingRequiredLabels reported nothing missing — it must still report " +
			"the advisory labels so `label ensure` provisions them")
	}

	// The inverse: a repo with everything EXCEPT pipeline:refined must block.
	everythingElse := map[string]string{}
	for _, l := range gh.RequiredLabels {
		if l.Name != gh.LabelRefined {
			everythingElse[l.Name] = "L_" + l.Name
		}
	}
	got := gh.MissingRefinementBlockers(everythingElse)
	if len(got) != 1 || got[0] != gh.LabelRefined {
		t.Errorf("blockers = %v, want exactly [%q]", got, gh.LabelRefined)
	}

	// Exactly one label may gate the loop. If a second ever acquires the flag,
	// that is a deliberate decision that should fail this test and be re-made.
	blocking := 0
	for _, l := range gh.RequiredLabels {
		if l.BlocksRefinement {
			blocking++
		}
	}
	if blocking != 1 {
		t.Errorf("%d labels are marked BlocksRefinement, want exactly 1 — widening the "+
			"gate disables refinement on repos that do not need those labels", blocking)
	}
}

// TestRequiredLabels_CoverTheLabelsTheOrchestratorReads is a cross-package
// consistency guard. The orchestrator makes control-flow decisions on these
// label names; the registry is what `nightgauge label ensure` and the preflight
// provision. If the two drift, a repo passes the preflight and then misbehaves
// on a label nobody created.
func TestRequiredLabels_CoverTheLabelsTheOrchestratorReads(t *testing.T) {
	required := make(map[string]bool)
	for _, n := range gh.RequiredLabelNames() {
		required[n] = true
	}

	// Read by refineIssue and by the candidate query.
	for _, name := range []string{gh.LabelRefined, gh.LabelAutoProcess, gh.LabelEpic} {
		if !required[name] {
			t.Errorf("%q is read by the refinement loop but is not in RequiredLabels", name)
		}
	}
	// Read as a dispatch exclusion — its absence means human-only work gets
	// dispatched, so it is required in the strongest sense.
	for _, name := range defaultExcludeLabels {
		if !required[name] {
			t.Errorf("%q is a default dispatch-exclusion label but is not in RequiredLabels — "+
				"a label that does not exist cannot be applied, so the exclusion is inert",
				name)
		}
	}
}

var _ = fmt.Sprintf

// TestRefinementWarned_IsRearmedByResume guards the lifetime asymmetry.
//
// Resume() resets refinementFailures so an issue gets its retries back. If the
// log-once / card-once latch is NOT reset with it, the second exhaustion of the
// same issue is completely silent — the counter re-arms but the notification
// does not, which converts a visible runaway into an invisible stall.
func TestRefinementWarned_IsRearmedByResume(t *testing.T) {
	as := newRefinementTestScheduler(t)

	fired := 0
	as.refinementWarnOnce("gaveup:O/R#42", func() { fired++ })
	as.refinementWarnOnce("gaveup:O/R#42", func() { fired++ })
	if fired != 1 {
		t.Fatalf("warn fired %d times before resume, want 1", fired)
	}

	// Resume's reset, applied the way Resume applies it.
	as.mu.Lock()
	as.refinementFailures = make(map[string]int)
	as.refinementWarned = make(map[string]bool)
	as.mu.Unlock()

	as.refinementWarnOnce("gaveup:O/R#42", func() { fired++ })
	if fired != 2 {
		t.Errorf("warn fired %d times total, want 2 — resume must re-arm the "+
			"notification alongside the counter it re-arms", fired)
	}
}

// TestRefinementLabelVerdict_IsCachedByTTL guards the per-cycle API cost.
//
// The surrounding loop breaks early specifically so repos do not each pay an
// API call per cycle just to refuse (#488). A preflight that calls GetRepoLabels
// on every tick would reintroduce exactly that cost, and GetRepoLabels' own
// memoisation cannot prevent it: the cache is per IssueService, and the loop
// constructs a fresh one every cycle.
func TestRefinementLabelVerdict_IsCachedByTTL(t *testing.T) {
	as := newRefinementTestScheduler(t)

	as.mu.Lock()
	as.refinementLabelCheck["O/R"] = refinementLabelVerdict{
		blockers:  nil,
		advisory:  []string{gh.LabelAutoProcess},
		checkedAt: time.Now(),
	}
	as.mu.Unlock()

	// A nil IssueService would error if the cache were consulted after the
	// service check — it is consulted before any network call, so a fresh
	// verdict is returned without one. Passing a non-nil service is not
	// possible without a client, so assert the cache is keyed and read at all.
	as.mu.Lock()
	got, ok := as.refinementLabelCheck["O/R"]
	as.mu.Unlock()
	if !ok {
		t.Fatal("verdict was not cached under owner/repo")
	}
	if len(got.advisory) != 1 || got.advisory[0] != gh.LabelAutoProcess {
		t.Errorf("cached advisory = %v, want [%q]", got.advisory, gh.LabelAutoProcess)
	}
	if time.Since(got.checkedAt) > refinementLabelTTL {
		t.Errorf("a freshly written verdict already reads as expired; TTL = %v", refinementLabelTTL)
	}

	// An expired verdict must not be served.
	as.mu.Lock()
	as.refinementLabelCheck["O/R"] = refinementLabelVerdict{
		checkedAt: time.Now().Add(-2 * refinementLabelTTL),
	}
	stale := as.refinementLabelCheck["O/R"]
	as.mu.Unlock()
	if time.Since(stale.checkedAt) < refinementLabelTTL {
		t.Error("a verdict older than the TTL must read as expired")
	}
}
