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
