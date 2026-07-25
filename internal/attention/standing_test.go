package attention

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testRepo = "octocat/acme"

// standingObservation builds a well-formed repo-scoped observation. The
// fingerprint is the caller's — it is the whole point of these tests.
func standingObservation(key, fingerprint string) DecisionRequest {
	return DecisionRequest{
		IdempotencyKey: key,
		Kind:           KindUnblock,
		Severity:       SeverityBlockingRun,
		Title:          "default branch is failing",
		Body:           "a required check is red",
		Producer:       "default-branch-health",
		Context:        Context{Repo: testRepo},
		Fingerprint:    fingerprint,
		Options:        []Option{{ID: "wait", Label: "Wait — human fixing", Verb: VerbNoop}},
		DefaultAction:  ExpireNoop,
	}
}

// sweepOnce reconciles a single observation from a single producer.
func sweepOnce(t *testing.T, s *Store, obs ...DecisionRequest) StandingResult {
	t.Helper()
	producers := map[string]bool{}
	for _, o := range obs {
		producers[o.Producer] = true
	}
	names := make([]string, 0, len(producers))
	for p := range producers {
		names = append(names, p)
	}
	if len(names) == 0 {
		names = []string{"default-branch-health"}
	}
	res, err := s.ReconcileStanding(StandingSweep{Repo: testRepo, Producers: names, Observed: obs})
	if err != nil {
		t.Fatalf("ReconcileStanding: %v", err)
	}
	return res
}

// notifications counts the journal transitions that would alert an operator.
func notifications(t *testing.T, s *Store) int {
	t.Helper()
	entries, err := s.ReadJournal()
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.ShouldNotify() {
			n++
		}
	}
	return n
}

func openRequests(t *testing.T, s *Store) []DecisionRequest {
	t.Helper()
	reqs, err := s.List(ListFilter{Repo: testRepo})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return reqs
}

// TestTenSweepsOverAnUnchangedConditionYieldOneRequestAndOneNotification is the
// headline contract: a standing condition observed many times is ONE card and
// ONE alert. Ten is arbitrary — the property is that the counts do not grow
// with the number of observations.
func TestTenSweepsOverAnUnchangedConditionYieldOneRequestAndOneNotification(t *testing.T) {
	s := New(t.TempDir())
	obs := standingObservation("default-branch-health:"+testRepo+":main", "check:build=failure")

	const sweeps = 10
	for i := 0; i < sweeps; i++ {
		// The body changes every sweep the way real check details do — elapsed
		// time, run numbers. None of it is material.
		o := obs
		o.Body = "a required check is red (observed " + strings.Repeat("·", i) + ")"
		sweepOnce(t, s, o)
	}

	open := openRequests(t, s)
	if len(open) != 1 {
		t.Fatalf("want exactly 1 open request after %d sweeps, got %d", sweeps, len(open))
	}
	if n := notifications(t, s); n != 1 {
		t.Fatalf("want exactly 1 notifying transition after %d sweeps, got %d", sweeps, n)
	}
	if !open[0].Standing {
		t.Error("a reconciled request must be marked standing")
	}
}

// TestSecondSweepOverAnUnchangedRepoChangesNothing is the #89 idempotency AC
// stated as a property of the result, independent of how many conditions hold.
func TestSecondSweepOverAnUnchangedRepoChangesNothing(t *testing.T) {
	s := New(t.TempDir())
	obs := []DecisionRequest{
		standingObservation("k:branch", "check:build=failure"),
		standingObservation("k:pr", "pr:41:review_required"),
	}

	first := sweepOnce(t, s, obs...)
	if !first.Changed() {
		t.Fatal("the first sweep over a blocked repo must change the store")
	}

	second := sweepOnce(t, s, obs...)
	if second.Changed() {
		t.Errorf("a second sweep over an unchanged repo must be a no-op, got %+v", second)
	}
	if second.Refreshed != len(obs) {
		t.Errorf("every still-true condition should refresh: want %d, got %d", len(obs), second.Refreshed)
	}
}

// TestClearedConditionAutoResolvesDistinguishablyFromHumanResolution covers the
// terminal-state separation the scorecard depends on.
func TestClearedConditionAutoResolvesDistinguishablyFromHumanResolution(t *testing.T) {
	s := New(t.TempDir())
	sweepOnce(t, s, standingObservation("k:branch", "check:build=failure"))

	// The next sweep observes nothing: the branch went green.
	res := sweepOnce(t, s)
	if res.AutoResolved != 1 {
		t.Fatalf("want 1 auto-resolution when the condition clears, got %d", res.AutoResolved)
	}
	if len(openRequests(t, s)) != 0 {
		t.Error("an auto-resolved request must leave the open inbox")
	}

	all, err := s.List(ListFilter{IncludeTerminal: true, Repo: testRepo})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 terminal request, got %d", len(all))
	}
	got := all[0]
	if got.Lifecycle.State == StateResolved {
		t.Error("a system retraction must NOT be recorded as a human resolution")
	}
	if got.Lifecycle.State != StateAutoResolved || !got.Lifecycle.State.IsTerminal() {
		t.Errorf("want a distinct terminal auto-resolved state, got %q", got.Lifecycle.State)
	}
	if got.Lifecycle.AutoResolved == nil || got.Lifecycle.AutoResolved.Reason == "" {
		t.Error("an auto-resolution must record why the card was retracted")
	}
	if got.Lifecycle.Resolved != nil {
		t.Error("an auto-resolution must not fabricate a resolution record")
	}
}

// TestAutoResolveOnlyAppliesToProducersThatActuallyLooked is the fail-safe: "I
// could not observe" must never be mistaken for "the condition cleared".
func TestAutoResolveOnlyAppliesToProducersThatActuallyLooked(t *testing.T) {
	s := New(t.TempDir())
	branch := standingObservation("k:branch", "check:build=failure")
	gate := standingObservation("k:pr", "pr:41:review_required")
	gate.Producer = "human-gate"
	sweepOnce(t, s, branch, gate)

	// Only the branch producer got to look this time; the gate producer errored.
	res, err := s.ReconcileStanding(StandingSweep{
		Repo:      testRepo,
		Producers: []string{branch.Producer},
		Observed:  nil,
	})
	if err != nil {
		t.Fatalf("ReconcileStanding: %v", err)
	}
	if res.AutoResolved != 1 {
		t.Fatalf("only the producer that looked should retract: want 1, got %d", res.AutoResolved)
	}
	open := openRequests(t, s)
	if len(open) != 1 || open[0].Producer != gate.Producer {
		t.Fatalf("the unobserved producer's card must survive, got %+v", open)
	}
}

// TestAnotherReposCardsSurviveASweep — reconciliation is repo-scoped.
func TestAnotherReposCardsSurviveASweep(t *testing.T) {
	s := New(t.TempDir())
	other := standingObservation("k:branch", "check:build=failure")
	other.Context.Repo = "octocat/other"
	if _, err := s.ReconcileStanding(StandingSweep{
		Repo:      other.Context.Repo,
		Producers: []string{other.Producer},
		Observed:  []DecisionRequest{other},
	}); err != nil {
		t.Fatalf("seed other repo: %v", err)
	}

	sweepOnce(t, s) // sweep testRepo, observing nothing

	still, err := s.List(ListFilter{Repo: "octocat/other"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(still) != 1 {
		t.Fatalf("a sweep of one repo must not retract another repo's cards, got %d", len(still))
	}
}

// TestContentUpdatesDoNotReNotifyButMaterialChangesDo separates the two kinds
// of update. This is what keeps duration/detail churn out of the alert stream.
func TestContentUpdatesDoNotReNotifyButMaterialChangesDo(t *testing.T) {
	s := New(t.TempDir())
	obs := standingObservation("k:branch", "check:build=failure")
	sweepOnce(t, s, obs)
	afterCreate := notifications(t, s)

	// Content-only churn: the same failing check, a longer outage.
	churn := obs
	churn.Body = "a required check has been red for 4h"
	churn.Title = "default branch is failing (4h)"
	res := sweepOnce(t, s, churn)
	if res.Refreshed != 1 || res.Updated != 0 {
		t.Errorf("content churn must refresh, not update: %+v", res)
	}
	if n := notifications(t, s); n != afterCreate {
		t.Errorf("content churn must not re-notify: %d → %d", afterCreate, n)
	}
	if openRequests(t, s)[0].Title != churn.Title {
		t.Error("a refresh must still update the card's content")
	}

	// A second check starts failing: the condition itself changed.
	moved := obs
	moved.Fingerprint = "check:build=failure,check:lint=failure"
	res = sweepOnce(t, s, moved)
	if res.Updated != 1 {
		t.Errorf("a changed fingerprint must count as an update: %+v", res)
	}
	if n := notifications(t, s); n != afterCreate+1 {
		t.Errorf("a material change must notify exactly once: %d → %d", afterCreate, n)
	}
}

// TestMuteSuppressesUntilTheConditionChangesNotUntilATimer is the mute rule
// stated exactly as the ticket words it.
func TestMuteSuppressesUntilTheConditionChangesNotUntilATimer(t *testing.T) {
	s := New(t.TempDir())
	obs := standingObservation("k:branch", "check:build=failure")
	sweepOnce(t, s, obs)
	id := openRequests(t, s)[0].ID

	if _, err := s.Mute(id, "octocat"); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	muted := notifications(t, s)

	// Time passes and the same condition is re-observed many times. No timer
	// un-mutes it.
	for i := 0; i < 5; i++ {
		sweepOnce(t, s, obs)
	}
	if n := notifications(t, s); n != muted {
		t.Errorf("a muted, unchanged condition must stay silent: %d → %d", muted, n)
	}
	if !openRequests(t, s)[0].IsMuted() {
		t.Error("re-observing the same condition must not drop the mute")
	}

	// A second check starts failing — the operator IS told.
	moved := obs
	moved.Fingerprint = "check:build=failure,check:lint=failure"
	sweepOnce(t, s, moved)
	if n := notifications(t, s); n != muted+1 {
		t.Errorf("a changed condition must break through a mute: %d → %d", muted, n)
	}
	if openRequests(t, s)[0].IsMuted() {
		t.Error("a mute must be dropped once the condition it silenced has changed")
	}
}

// TestMutedCardsAreSilencedNotHidden — mute governs alerting, not membership.
func TestMutedCardsAreSilencedNotHidden(t *testing.T) {
	s := New(t.TempDir())
	sweepOnce(t, s, standingObservation("k:branch", "check:build=failure"))
	id := openRequests(t, s)[0].ID
	if _, err := s.Mute(id, "octocat"); err != nil {
		t.Fatalf("Mute: %v", err)
	}

	if got := len(openRequests(t, s)); got != 1 {
		t.Errorf("a muted card stays in the inbox by default, got %d", got)
	}
	quiet, err := s.List(ListFilter{Repo: testRepo, ExcludeMuted: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(quiet) != 0 {
		t.Errorf("ExcludeMuted must drop muted cards for alert-worthy views, got %d", len(quiet))
	}

	if _, err := s.Unmute(id, "octocat"); err != nil {
		t.Fatalf("Unmute: %v", err)
	}
	quiet, _ = s.List(ListFilter{Repo: testRepo, ExcludeMuted: true})
	if len(quiet) != 1 {
		t.Error("unmuting must restore the card to alert-worthy views")
	}
}

// TestAcknowledgementIsScopedToTheConditionTheOperatorSaw — an ack silences the
// badge for the condition that was on screen, not for whatever it becomes.
func TestAcknowledgementIsScopedToTheConditionTheOperatorSaw(t *testing.T) {
	s := New(t.TempDir())
	obs := standingObservation("k:branch", "check:build=failure")
	sweepOnce(t, s, obs)
	id := openRequests(t, s)[0].ID
	if _, err := s.Acknowledge(id, "octocat"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	sweepOnce(t, s, obs) // unchanged: the ack holds
	if got := openRequests(t, s)[0].Lifecycle.State; got != StateAcknowledged {
		t.Errorf("an unchanged condition must stay acknowledged, got %q", got)
	}

	moved := obs
	moved.Fingerprint = "check:build=failure,check:lint=failure"
	sweepOnce(t, s, moved)
	req := openRequests(t, s)[0]
	if req.Lifecycle.State != StateOpen || req.Lifecycle.Acknowledged != nil {
		t.Errorf("a materially changed condition must re-open the card, got %q", req.Lifecycle.State)
	}
	if req.ID != id {
		t.Error("re-opening must keep the sticky identity, not mint a new request")
	}
}

// TestHumanResolutionIsNotUndoneByTheNextSweep — a human who dismissed this
// exact condition is not handed it back a minute later.
func TestHumanResolutionIsNotUndoneByTheNextSweep(t *testing.T) {
	s := New(t.TempDir())
	obs := standingObservation("k:branch", "check:build=failure")
	sweepOnce(t, s, obs)
	id := openRequests(t, s)[0].ID
	if _, err := s.Resolve(context.Background(), id, "wait", "octocat", "", "", NoopExecutor{}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	res := sweepOnce(t, s, obs)
	if res.Suppressed != 1 || res.Created != 0 {
		t.Fatalf("an unchanged, already-resolved condition must be suppressed: %+v", res)
	}
	if got := len(openRequests(t, s)); got != 0 {
		t.Fatalf("suppression must not re-open the inbox, got %d open", got)
	}

	// But a CHANGED condition is news again.
	moved := obs
	moved.Fingerprint = "check:build=failure,check:lint=failure"
	res = sweepOnce(t, s, moved)
	if res.Created != 1 {
		t.Fatalf("a changed condition must raise again after a human resolution: %+v", res)
	}
}

// TestAConditionThatClearsAndReturnsIsRaisedAgain — an auto-resolution, unlike
// a human decision, carries no judgement, so it must not suppress.
func TestAConditionThatClearsAndReturnsIsRaisedAgain(t *testing.T) {
	s := New(t.TempDir())
	obs := standingObservation("k:branch", "check:build=failure")
	sweepOnce(t, s, obs)
	sweepOnce(t, s) // clears → auto-resolved

	res := sweepOnce(t, s, obs)
	if res.Created != 1 {
		t.Fatalf("a condition that returns after auto-resolving must raise again: %+v", res)
	}
}

// TestStandingExpiryIsDeclaredAndRefreshedWhileTheConditionHolds.
func TestStandingExpiryIsDeclaredAndRefreshedWhileTheConditionHolds(t *testing.T) {
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	now := base
	s := New(t.TempDir()).WithClock(func() time.Time { return now })
	obs := standingObservation("k:branch", "check:build=failure")

	sweepOnce(t, s, obs)
	first := openRequests(t, s)[0]
	firstExpiry, err := time.Parse(tsLayout, first.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	if want := base.Add(StandingExpiry); !firstExpiry.Equal(want) {
		t.Errorf("a standing request must default to the declared expiry window: want %s, got %s", want, firstExpiry)
	}
	if first.DefaultAction != ExpireNoop {
		t.Errorf("a standing request must expire without mutating anything, got %q", first.DefaultAction)
	}

	// Most of the window elapses; the condition is still true.
	now = base.Add(StandingExpiry - time.Hour)
	sweepOnce(t, s, obs)
	if n, err := s.SweepExpired(context.Background(), NoopExecutor{}); err != nil || n != 0 {
		t.Fatalf("an actively-observed condition must not expire: n=%d err=%v", n, err)
	}
	refreshed := openRequests(t, s)[0]
	if !(refreshed.ExpiresAt > first.ExpiresAt) {
		t.Errorf("every observation must push expiry forward: %s → %s", first.ExpiresAt, refreshed.ExpiresAt)
	}

	// The producer stops being evaluated entirely — nothing observes, nothing
	// retracts, and the safety net eventually fires.
	now = mustExpiry(t, refreshed).Add(time.Minute)
	n, err := s.SweepExpired(context.Background(), NoopExecutor{})
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("a standing card whose producer went away must expire, got %d", n)
	}
}

func mustExpiry(t *testing.T, r DecisionRequest) time.Time {
	t.Helper()
	ts, err := time.Parse(tsLayout, r.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at %q: %v", r.ExpiresAt, err)
	}
	return ts
}

// TestReconcileRejectsCallerMistakesBeforeTouchingTheStore — a producer bug
// must not leave the store half-reconciled.
func TestReconcileRejectsCallerMistakesBeforeTouchingTheStore(t *testing.T) {
	cases := map[string]StandingSweep{
		"no repo scope": {
			Producers: []string{"p"},
			Observed:  []DecisionRequest{standingObservation("k", "f")},
		},
		"observation for another repo": func() StandingSweep {
			o := standingObservation("k", "f")
			o.Context.Repo = "octocat/elsewhere"
			return StandingSweep{Repo: testRepo, Producers: []string{o.Producer}, Observed: []DecisionRequest{o}}
		}(),
		"missing fingerprint": {
			Repo:      testRepo,
			Producers: []string{"default-branch-health"},
			Observed:  []DecisionRequest{standingObservation("k", "")},
		},
		"duplicate key in one sweep": {
			Repo:      testRepo,
			Producers: []string{"default-branch-health"},
			Observed: []DecisionRequest{
				standingObservation("k", "f1"),
				standingObservation("k", "f2"),
			},
		},
		"unregistered verb": func() StandingSweep {
			o := standingObservation("k", "f")
			o.Options[0].Verb = "rm -rf"
			return StandingSweep{Repo: testRepo, Producers: []string{o.Producer}, Observed: []DecisionRequest{o}}
		}(),
	}
	for name, sw := range cases {
		t.Run(name, func(t *testing.T) {
			s := New(t.TempDir())
			if _, err := s.ReconcileStanding(sw); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
			if got := len(openRequests(t, s)); got != 0 {
				t.Errorf("a rejected sweep must write nothing, got %d requests", got)
			}
		})
	}
}

// TestShouldNotifyIsLimitedToGenuineStateChanges pins the alerting rule itself
// rather than any particular producer's use of it.
func TestShouldNotifyIsLimitedToGenuineStateChanges(t *testing.T) {
	alerting := map[string]bool{
		ActionCreated:      true,
		ActionUpdated:      true,
		ActionRefreshed:    false,
		ActionAcknowledged: false,
		ActionMuted:        false,
		ActionUnmuted:      false,
		ActionResolved:     false,
		ActionAutoResolved: false,
		ActionExpired:      false,
	}
	for action, want := range alerting {
		if got := (JournalEntry{Action: action}).ShouldNotify(); got != want {
			t.Errorf("%s: ShouldNotify = %v, want %v", action, got, want)
		}
	}
	if (JournalEntry{Action: ActionUpdated, Muted: true}).ShouldNotify() {
		t.Error("a transition recorded while muted must never alert")
	}
}
