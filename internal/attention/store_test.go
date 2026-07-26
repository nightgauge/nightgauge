package attention

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// validRequest builds a well-formed request for tests (noop option, so it needs
// no live executor). id is filled by the caller.
func validRequest(id, key string) DecisionRequest {
	return DecisionRequest{
		ID:             id,
		IdempotencyKey: key,
		Kind:           KindChoose,
		Severity:       SeverityBlockingRun,
		Title:          "test request",
		Body:           "why",
		Producer:       "test",
		Context:        Context{Repo: "octocat/acme", Issue: 7},
		Options: []Option{
			{ID: "go", Label: "Go", Verb: VerbNoop},
			{ID: "leave", Label: "Leave", Verb: VerbNoop},
		},
		DefaultAction: "leave",
		ExpiresAt:     time.Now().UTC().Add(time.Hour).Format(tsLayout),
	}
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

func TestRaiseRejectsIdentitylessRecords(t *testing.T) {
	s := New(t.TempDir())
	cases := map[string]func(r *DecisionRequest){
		"empty id":              func(r *DecisionRequest) { r.ID = "" },
		"bad id":                func(r *DecisionRequest) { r.ID = "not-a-dr-id" },
		"empty idempotency_key": func(r *DecisionRequest) { r.IdempotencyKey = "" },
		"empty producer":        func(r *DecisionRequest) { r.Producer = "" },
		"bad kind":              func(r *DecisionRequest) { r.Kind = "nonsense" },
		"bad severity":          func(r *DecisionRequest) { r.Severity = "nonsense" },
		"unregistered verb":     func(r *DecisionRequest) { r.Options[0].Verb = "rm -rf" },
		"bad default_action":    func(r *DecisionRequest) { r.DefaultAction = "no-such-option" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := validRequest(mustID(t), "k:"+name)
			mutate(&req)
			if _, err := s.Raise(req); err == nil {
				t.Fatalf("expected Raise to reject %s, got nil error", name)
			}
		})
	}
}

func TestRaiseAndGet(t *testing.T) {
	s := New(t.TempDir())
	id := mustID(t)
	if _, err := s.Raise(validRequest(id, "cond:1")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	got, found, err := s.Get(id)
	if err != nil || !found {
		t.Fatalf("Get(%s): found=%v err=%v", id, found, err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if got.Lifecycle.State != StateOpen {
		t.Errorf("state = %q, want open", got.Lifecycle.State)
	}
	if got.CreatedAt == "" {
		t.Error("created_at not defaulted")
	}
}

func TestRaiseDedupsOnIdempotencyKey(t *testing.T) {
	s := New(t.TempDir())
	first := mustID(t)
	if _, err := s.Raise(validRequest(first, "same-cond")); err != nil {
		t.Fatalf("Raise 1: %v", err)
	}
	// A second raise for the same condition (different id) must UPDATE in place,
	// not spawn a duplicate.
	second := validRequest(mustID(t), "same-cond")
	second.Title = "updated title"
	returnedID, err := s.Raise(second)
	if err != nil {
		t.Fatalf("Raise 2: %v", err)
	}
	if returnedID != first {
		t.Errorf("dedup returned id %q, want the existing %q", returnedID, first)
	}
	open, err := s.List(ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open request after dedup, got %d", len(open))
	}
	if open[0].Title != "updated title" {
		t.Errorf("update-in-place lost the new title: %q", open[0].Title)
	}
}

// standingRaise builds a well-formed STANDING request for the Raise path — the
// shape the scheduler's re-evaluated producers emit (#108).
func standingRaise(id, key, fingerprint string) DecisionRequest {
	r := validRequest(id, key)
	r.Standing = true
	r.Fingerprint = fingerprint
	return r
}

func TestRaiseRejectsAStandingRequestWithNoFingerprint(t *testing.T) {
	s := New(t.TempDir())
	r := standingRaise(mustID(t), "cond", "")
	if _, err := s.Raise(r); err == nil {
		t.Fatal("a standing request with no fingerprint must be rejected at the boundary")
	}
}

// TestReRaisingAnExpiredKeyRevivesTheSameRecord is the #108 regression. A
// condition that outlives its own TTL is the definition of a standing
// condition; before the fix each re-raise took the create path and minted a new
// id, so the card count tracked the number of TTL windows rather than the
// number of distinct problems.
func TestReRaisingAnExpiredKeyRevivesTheSameRecord(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	s := New(t.TempDir()).WithClock(func() time.Time { return now })

	// The producer re-detects the same condition, generating a fresh candidate
	// id every time — exactly as the fail-open raise path does.
	raise := func() string {
		t.Helper()
		r := standingRaise(mustID(t), "stuck-epic:octocat/acme#100", "stall:#101 ready but undispatched")
		r.ExpiresAt = now.Add(30 * time.Minute).Format(tsLayout)
		id, err := s.Raise(r)
		if err != nil {
			t.Fatalf("Raise: %v", err)
		}
		return id
	}

	first := raise()
	// Four TTL windows elapse with nobody looking. The condition never changes.
	for i := 0; i < 4; i++ {
		now = now.Add(31 * time.Minute)
		n, err := s.SweepExpired(context.Background(), NoopExecutor{})
		if err != nil {
			t.Fatalf("SweepExpired: %v", err)
		}
		if n != 1 {
			t.Fatalf("window %d: expired %d requests, want 1", i, n)
		}
		if got := raise(); got != first {
			t.Fatalf("window %d: re-raise minted id %q, want the existing %q", i, got, first)
		}
	}

	all, err := s.List(ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("one condition across five TTL windows materialized %d records, want exactly 1", len(all))
	}
	if all[0].Lifecycle.State != StateOpen {
		t.Errorf("state = %q, want open — a revived record is a live card", all[0].Lifecycle.State)
	}
	if all[0].Lifecycle.Expired != nil {
		t.Error("a revived record still carries the expiry audit of the window it outlived")
	}
}

// TestRaiseIntoAStoreThatAlreadyAccumulatedDuplicates covers the state the bug
// left behind: four expired records under one key, written before the fix. The
// next raise must fold into the newest and add nothing, so an affected store
// stops growing without a migration.
func TestRaiseIntoAStoreThatAlreadyAccumulatedDuplicates(t *testing.T) {
	s := New(t.TempDir())
	var ids []string
	for i := 0; i < 4; i++ {
		r := standingRaise(mustID(t), "stuck-epic:octocat/acme#100", "stall:#101")
		r.SchemaVersion = SchemaVersion
		r.CreatedAt = fmt.Sprintf("2026-07-2%dT00:00:00Z", 1+i)
		r.ExpiresAt = fmt.Sprintf("2026-07-2%dT00:30:00Z", 1+i)
		r.Lifecycle = Lifecycle{State: StateExpired, Expired: &ExpiredRecord{
			At: fmt.Sprintf("2026-07-2%dT00:30:00Z", 1+i), Applied: ExpireNoop,
		}}
		path, err := s.pathFor(r.ID)
		if err != nil {
			t.Fatalf("pathFor: %v", err)
		}
		if err := s.writeMaterializedLocked(path, &r); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
		ids = append(ids, r.ID)
	}

	id, err := s.Raise(standingRaise(mustID(t), "stuck-epic:octocat/acme#100", "stall:#101"))
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if got := len(mustList(t, s, ListFilter{IncludeTerminal: true})); got != 4 {
		t.Fatalf("an already-duplicated store grew to %d records, want 4", got)
	}
	if id != ids[3] {
		t.Errorf("revived %q, want the most recently expired record %q", id, ids[3])
	}
	if got := len(mustList(t, s, ListFilter{})); got != 1 {
		t.Errorf("%d open cards for one condition, want 1", got)
	}
}

// TestRaiseRefreshesAStandingConditionWithoutReAlerting pins the §M rule on the
// Raise path: prose moves on every observation, so only a moved fingerprint is
// a genuine change worth interrupting an operator for.
func TestRaiseRefreshesAStandingConditionWithoutReAlerting(t *testing.T) {
	s := New(t.TempDir())
	raise := func(title, fingerprint string) {
		t.Helper()
		r := standingRaise(mustID(t), "cond", fingerprint)
		r.Title = title
		if _, err := s.Raise(r); err != nil {
			t.Fatalf("Raise: %v", err)
		}
	}
	raise("epic stalled — 12m", "stall:#101 ready but undispatched")
	for i := 0; i < 5; i++ {
		raise(fmt.Sprintf("epic stalled — %dm", 13+i), "stall:#101 ready but undispatched")
	}

	entries, err := s.ReadJournal()
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	alerts := 0
	for _, e := range entries {
		if e.ShouldNotify() {
			alerts++
		}
	}
	if alerts != 1 {
		t.Errorf("six observations of one unchanged condition alerted %d times, want 1", alerts)
	}

	// A second sub-issue blocking the epic IS a change, and does alert.
	raise("epic stalled", "stall:#101 ready but undispatched; #102 blocked by #101")
	entries, _ = s.ReadJournal()
	alerts = 0
	for _, e := range entries {
		if e.ShouldNotify() {
			alerts++
		}
	}
	if alerts != 2 {
		t.Errorf("a moved fingerprint must alert: got %d alerts, want 2", alerts)
	}
}

// TestRaiseDoesNotHandBackAConditionAHumanJustResolved — dismissing a card for
// a condition that is still true must not return it on the next cycle.
func TestRaiseDoesNotHandBackAConditionAHumanJustResolved(t *testing.T) {
	s := New(t.TempDir())
	id, err := s.Raise(standingRaise(mustID(t), "cond", "fp:1"))
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Resolve(context.Background(), id, "leave", "octocat", "", "", &spyExecutor{}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, err := s.Raise(standingRaise(mustID(t), "cond", "fp:1")); err != nil || got != id {
		t.Fatalf("re-raise after resolve returned (%q, %v), want the resolved id %q and no new card", got, err, id)
	}
	if open := mustList(t, s, ListFilter{}); len(open) != 0 {
		t.Fatalf("a resolved condition that has not changed must stay resolved, got %d open", len(open))
	}
	// The condition itself changing is news. The resolution stays on the record
	// that earned it — a decision is a closed chapter — and the new condition
	// gets its own card, matching what a repo sweep does with the same facts.
	if _, err := s.Raise(standingRaise(mustID(t), "cond", "fp:2")); err != nil {
		t.Fatalf("Raise after change: %v", err)
	}
	open := mustList(t, s, ListFilter{})
	if len(open) != 1 {
		t.Fatalf("a changed condition must be carded: got %d open", len(open))
	}
	if open[0].ID == id {
		t.Error("the resolved record was reopened; a human's resolution must stay auditable on its own record")
	}
	if resolved, _, _ := s.Get(id); resolved.Lifecycle.State != StateResolved {
		t.Errorf("prior record state = %q, want resolved", resolved.Lifecycle.State)
	}
}

// TestAutoResolveUnobservedRetractsOnlyTheProducersOwnUnseenConditions covers
// the retraction half of standing semantics for the run-loop producers: a
// condition that stopped being true clears its card, and nothing else moves.
func TestAutoResolveUnobservedRetractsOnlyTheProducersOwnUnseenConditions(t *testing.T) {
	s := New(t.TempDir())
	mk := func(key, producer string, standing bool) string {
		t.Helper()
		r := standingRaise(mustID(t), key, "fp")
		r.Producer = producer
		r.Standing = standing
		if !standing {
			r.Fingerprint = ""
		}
		id, err := s.Raise(r)
		if err != nil {
			t.Fatalf("Raise %s: %v", key, err)
		}
		return id
	}
	stillTrue := mk("p:a", "watchdog-stuck-epic", true)
	cleared := mk("p:b", "watchdog-stuck-epic", true)
	otherProducer := mk("q:a", "owner-action-handoff", true)
	event := mk("p:event", "watchdog-stuck-epic", false)

	n, err := s.AutoResolveUnobserved("watchdog-stuck-epic", []string{"p:a"})
	if err != nil {
		t.Fatalf("AutoResolveUnobserved: %v", err)
	}
	if n != 1 {
		t.Fatalf("retracted %d cards, want 1", n)
	}
	got, _, _ := s.Get(cleared)
	if got.Lifecycle.State != StateAutoResolved {
		t.Errorf("cleared card state = %q, want auto_resolved", got.Lifecycle.State)
	}
	if got.Lifecycle.AutoResolved == nil || got.Lifecycle.AutoResolved.Reason != ReasonConditionCleared {
		t.Error("a retraction must be auditable as a system withdrawal, not a human decision")
	}
	for _, survivor := range []string{stillTrue, otherProducer, event} {
		r, _, _ := s.Get(survivor)
		if r.Lifecycle.State.IsTerminal() {
			t.Errorf("request %s was retracted; only the producer's own unobserved standing cards may be", survivor)
		}
	}

	if _, err := s.AutoResolveUnobserved("", nil); err == nil {
		t.Error("auto-resolve without a producer scope must be rejected — it would retract the whole inbox")
	}
}

func mustList(t *testing.T, s *Store, f ListFilter) []DecisionRequest {
	t.Helper()
	out, err := s.List(f)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return out
}

// spyExecutor counts verb executions and records the last option.
type spyExecutor struct {
	count atomic.Int64
	last  atomic.Value // string
	err   error
}

func (e *spyExecutor) ExecuteVerb(_ context.Context, _ *DecisionRequest, opt Option) error {
	e.count.Add(1)
	e.last.Store(opt.ID)
	return e.err
}

func TestResolveIsIdempotentAndExecutesOnce(t *testing.T) {
	s := New(t.TempDir())
	id := mustID(t)
	if _, err := s.Raise(validRequest(id, "cond")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	exec := &spyExecutor{}
	res, err := s.Resolve(context.Background(), id, "go", "octocat", "", "done", exec)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.AlreadyResolved {
		t.Error("first resolve reported AlreadyResolved")
	}
	// Replayed resolve is a safe no-op and must NOT re-execute the verb.
	res2, err := s.Resolve(context.Background(), id, "go", "octocat", "", "", exec)
	if err != nil {
		t.Fatalf("Resolve replay: %v", err)
	}
	if !res2.AlreadyResolved {
		t.Error("replay did not report AlreadyResolved")
	}
	if got := exec.count.Load(); got != 1 {
		t.Errorf("verb executed %d times, want exactly 1", got)
	}
	got, _, _ := s.Get(id)
	if got.Lifecycle.State != StateResolved {
		t.Errorf("state = %q, want resolved", got.Lifecycle.State)
	}
	if got.Lifecycle.Resolved == nil || got.Lifecycle.Resolved.OptionID != "go" {
		t.Error("resolved audit record missing/incorrect")
	}
}

func TestResolveRejectsUnknownOption(t *testing.T) {
	s := New(t.TempDir())
	id := mustID(t)
	if _, err := s.Raise(validRequest(id, "cond")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Resolve(context.Background(), id, "no-such-option", "octocat", "", "", &spyExecutor{}); err == nil {
		t.Fatal("expected rejection of unknown option")
	}
	// The request must remain open — a rejected resolve does not transition.
	got, _, _ := s.Get(id)
	if got.Lifecycle.State != StateOpen {
		t.Errorf("state = %q after rejected resolve, want open", got.Lifecycle.State)
	}
}

func TestAcknowledgeIsNonBlocking(t *testing.T) {
	s := New(t.TempDir())
	id := mustID(t)
	if _, err := s.Raise(validRequest(id, "cond")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Acknowledge(id, "octocat"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	got, _, _ := s.Get(id)
	if got.Lifecycle.State != StateAcknowledged {
		t.Fatalf("state = %q, want acknowledged", got.Lifecycle.State)
	}
	// A resolve still works after acknowledge.
	if _, err := s.Resolve(context.Background(), id, "leave", "octocat", "", "", &spyExecutor{}); err != nil {
		t.Fatalf("Resolve after ack: %v", err)
	}
	got, _, _ = s.Get(id)
	if got.Lifecycle.State != StateResolved {
		t.Errorf("state = %q, want resolved", got.Lifecycle.State)
	}
}

func TestExpirySweepAppliesDefault(t *testing.T) {
	s := New(t.TempDir())
	// One request already past expiry, one still valid.
	expired := validRequest(mustID(t), "stale")
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(tsLayout)
	if _, err := s.Raise(expired); err != nil {
		t.Fatalf("Raise expired: %v", err)
	}
	fresh := validRequest(mustID(t), "fresh")
	if _, err := s.Raise(fresh); err != nil {
		t.Fatalf("Raise fresh: %v", err)
	}
	n, err := s.SweepExpired(context.Background(), NoopExecutor{})
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	got, _, _ := s.Get(expired.ID)
	if got.Lifecycle.State != StateExpired {
		t.Errorf("state = %q, want expired", got.Lifecycle.State)
	}
	if got.Lifecycle.Expired == nil || got.Lifecycle.Expired.Applied != "leave" {
		t.Error("expiry audit record missing/incorrect applied default")
	}
	// Sweep is idempotent — a second sweep expires nothing.
	if n2, _ := s.SweepExpired(context.Background(), NoopExecutor{}); n2 != 0 {
		t.Errorf("second sweep expired %d, want 0", n2)
	}
}

func TestListOrdersBySeverityThenNewest(t *testing.T) {
	s := New(t.TempDir())
	mk := func(sev Severity, key string) {
		r := validRequest(mustID(t), key)
		r.Severity = sev
		if _, err := s.Raise(r); err != nil {
			t.Fatalf("Raise: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at
	}
	mk(SeverityFYI, "a")
	mk(SeverityBlockingFleet, "b")
	mk(SeverityBlockingRun, "c")
	list, err := s.List(ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d, want 3", len(list))
	}
	want := []Severity{SeverityBlockingFleet, SeverityBlockingRun, SeverityFYI}
	for i, w := range want {
		if list[i].Severity != w {
			t.Errorf("position %d severity = %q, want %q", i, list[i].Severity, w)
		}
	}
}

func TestJournalRecordsEveryTransition(t *testing.T) {
	s := New(t.TempDir())
	id := mustID(t)
	if _, err := s.Raise(validRequest(id, "cond")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Acknowledge(id, "octocat"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, err := s.Resolve(context.Background(), id, "go", "octocat", "", "", &spyExecutor{}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	entries, err := s.ReadJournal()
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	want := []string{ActionCreated, ActionAcknowledged, ActionResolved}
	if len(actions) != len(want) {
		t.Fatalf("journal actions = %v, want %v", actions, want)
	}
	for i, a := range want {
		if actions[i] != a {
			t.Errorf("journal[%d] = %q, want %q", i, actions[i], a)
		}
	}
}

// TestConcurrentProducersAndResolvesNoTear stresses the single-writer
// serialization: many goroutines raise distinct conditions and resolve the same
// request concurrently. Run under -race. Invariants: every materialized file
// parses (no tear), and a given request's verb executes exactly once despite
// many concurrent resolvers.
func TestConcurrentProducersAndResolvesNoTear(t *testing.T) {
	s := New(t.TempDir())

	// Pre-create one request that many goroutines will race to resolve.
	hot := mustID(t)
	if _, err := s.Raise(validRequest(hot, "hot")); err != nil {
		t.Fatalf("Raise hot: %v", err)
	}
	exec := &spyExecutor{}

	const producers = 40
	const resolvers = 20
	var wg sync.WaitGroup

	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("dr_%016x-cafe-7000-8000-000000000000", i)
			_, _ = s.Raise(validRequest(id, fmt.Sprintf("cond-%d", i)))
		}(i)
	}
	for i := 0; i < resolvers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Resolve(context.Background(), hot, "go", "octocat", "", "", exec)
		}()
	}
	wg.Wait()

	// Every open request (including the ones just raised) must parse cleanly.
	all, err := s.List(ListFilter{IncludeTerminal: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != producers+1 {
		t.Fatalf("got %d requests, want %d (no tear / no dup)", len(all), producers+1)
	}
	// The hot request resolved exactly once — CAS makes every other resolver a
	// no-op, so the verb ran a single time.
	if got := exec.count.Load(); got != 1 {
		t.Errorf("hot verb executed %d times, want exactly 1", got)
	}
}
