package ipc

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// TestMarkDoneDoesNotDeadlockTheDaemon is the #2528 regression pin.
//
// Store.Resolve holds the attention store lock while it runs the option's
// verb. The mark-done verb (autonomous.complete) calls NotifyComplete, whose
// success path retracts the issue's architecture-approval card. That retract
// used to run inline under as.mu and re-enter the store lock on the goroutine
// that already held it: Resolve never returned, the store lock and as.mu were
// never released, and the scheduler stopped at its next as.mu.Lock().
//
// The open architecture-approval card makes the retract do real store I/O.
// Without it the retract would still list the store, so either way the old
// code hangs here; the deadline turns the hang into a failure with a dump.
func TestMarkDoneDoesNotDeadlockTheDaemon(t *testing.T) {
	server, as, ctx := newHaltedServer(t, haltedState())
	store := as.Attention()
	if store == nil {
		t.Fatal("autonomous scheduler has no attention store")
	}

	const repo, issue = "octocat/acme", 2528
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	raise := func(req attention.DecisionRequest) string {
		t.Helper()
		id, err := attention.NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		req.ID = id
		req.Severity = attention.SeverityBlockingRun
		req.Body = "test"
		req.Context = attention.Context{Repo: repo, Issue: issue}
		req.ExpiresAt = expires
		if _, gotID, err := store.Raise(req); err != nil {
			t.Fatalf("Raise %s: %v", req.IdempotencyKey, err)
		} else if gotID != "" {
			id = gotID
		}
		return id
	}

	archID := raise(attention.DecisionRequest{
		IdempotencyKey: "architecture-approval:octocat/acme#2528",
		Kind:           attention.KindApprove,
		Title:          "Architecture approval required",
		Producer:       "architecture-approval",
		Standing:       true,
		Fingerprint:    "pending",
		Options: []attention.Option{
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	})
	cardID := raise(attention.DecisionRequest{
		IdempotencyKey: "owner-action-handoff:octocat/acme#2528",
		Kind:           attention.KindChoose,
		Title:          "Owner action",
		Producer:       "owner-action-handoff",
		Options: []attention.Option{
			{ID: "mark-done", Label: "Mark done", Verb: attention.VerbAutonomousComplete},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	})

	resolved := make(chan error, 1)
	go func() {
		_, err := store.Resolve(ctx, cardID, "mark-done", "test", "", "", server)
		resolved <- err
	}()
	rescanned := make(chan struct{})
	go func() {
		// Scheduler work that takes as.mu while Resolve holds the store lock.
		for i := 0; i < 50; i++ {
			as.TriggerRescan()
		}
		close(rescanned)
	}()

	deadline := time.After(10 * time.Second)
	for _, wait := range []<-chan struct{}{waitErr(t, resolved), rescanned} {
		select {
		case <-wait:
		case <-deadline:
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			// Exit rather than t.Fatalf: the wedged goroutine still holds as.mu,
			// so the harness cleanup (as.Stop) would hang the binary instead.
			fmt.Fprintf(os.Stderr, "FAIL: mark-done Resolve and scheduler work did not finish within 10s — "+
				"the store lock / as.mu inversion is back (#2528)\n%s", buf[:n])
			os.Exit(1)
		}
	}

	// The scheduler is not wedged: as.mu is free again.
	done := make(chan struct{})
	go func() { as.TriggerRescan(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler mutex still held after mark-done returned (#2528)")
	}

	// The dispatched retract lands once the store lock is released.
	retractDeadline := time.Now().Add(5 * time.Second)
	for {
		req, ok, err := store.Get(archID)
		if err != nil || !ok {
			t.Fatalf("Get architecture-approval card: ok=%v err=%v", ok, err)
		}
		if req.Lifecycle.State == attention.StateAutoResolved {
			break
		}
		if time.Now().After(retractDeadline) {
			t.Fatalf("architecture-approval card state = %q, want auto_resolved after mark-done", req.Lifecycle.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitErr adapts the Resolve result channel to a done signal, failing the test
// if Resolve returned an error.
func waitErr(t *testing.T, ch <-chan error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		if err := <-ch; err != nil {
			t.Errorf("Resolve(mark-done): %v", err)
		}
		close(done)
	}()
	return done
}
