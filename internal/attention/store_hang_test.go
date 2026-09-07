package attention

// Regression tests for #1539 — the Action Center's mutating verbs hung forever
// while its reads stayed instant.
//
// The mechanism, in three parts, each pinned by a test below:
//
//  1. Every mutation serialises on the per-directory lock; List does not. So
//     anything that wedges a lock holder makes writes hang and leaves reads
//     looking healthy — which is exactly what an operator saw.
//  2. The lock holder was wedged by the store itself: emitLocked ran the
//     transition listeners INSIDE the critical section, and the daemon's
//     listener writes `attention.event` to the extension's stdio pipe. A peer
//     that stops draining that pipe blocks the write, and the write was holding
//     the lock that serialises every writer in every nightgauge process.
//  3. Nothing bounded the wait, because a sync.Mutex has no bounded acquire.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// raiseTestCard puts one open, resolvable card in the store.
func raiseTestCard(t *testing.T, s *Store, key string) string {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := s.Raise(DecisionRequest{
		ID:             id,
		IdempotencyKey: key,
		Kind:           KindChoose,
		Severity:       SeverityBlockingRun,
		Title:          "hang regression",
		Body:           "why",
		Producer:       "test",
		Context:        Context{Repo: "octocat/acme"},
		Options: []Option{
			{ID: "go", Label: "Go", Verb: VerbNoop},
			{ID: "leave", Label: "Leave", Verb: VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	return id
}

// TestMutationsGiveUpOnAWedgedStore is the core acceptance criterion: no
// mutating path may block indefinitely. A holder that never releases stands in
// for the wedged stdio write that caused the incident.
func TestMutationsGiveUpOnAWedgedStore(t *testing.T) {
	s := New(t.TempDir())
	id := raiseTestCard(t, s, "wedged:1")

	// Wedge the directory: taken and never released for the test's duration.
	release := acquireDir(s.dir)
	defer release()

	cases := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"resolve", func(ctx context.Context) error {
			_, err := s.Resolve(ctx, id, "go", "octocat", "", "", nil)
			return err
		}},
		{"acknowledge", func(ctx context.Context) error {
			_, err := s.Acknowledge(ctx, id, "octocat")
			return err
		}},
		{"mute", func(ctx context.Context) error {
			_, err := s.Mute(ctx, id, "octocat")
			return err
		}},
		{"unmute", func(ctx context.Context) error {
			_, err := s.Unmute(ctx, id, "octocat")
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- tc.call(ctx) }()

			select {
			case err := <-done:
				if !errors.Is(err, ErrStoreBusy) {
					t.Fatalf("err = %v, want ErrStoreBusy", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the call never returned — this is the #1539 hang")
			}
		})
	}

	// The read path is unaffected, which is why the incident was so hard to
	// read from the outside.
	if _, err := s.List(ListFilter{}); err != nil {
		t.Fatalf("List while wedged: %v", err)
	}
}

// TestListenersRunOutsideTheDirectoryLock pins the cause. A listener is
// arbitrary code — the daemon's writes to a pipe — and running it inside the
// critical section makes an unread pipe wedge every writer on the machine.
func TestListenersRunOutsideTheDirectoryLock(t *testing.T) {
	s := New(t.TempDir())
	id := raiseTestCard(t, s, "fanout:1")

	lockFree := make(chan bool, 1)
	s.Subscribe(func(entry JournalEntry, _ *DecisionRequest) {
		if entry.Action != ActionResolved {
			return
		}
		// If the fan-out still ran under the lock this cannot succeed, because
		// the lock is not reentrant.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		release, err := acquireDirCtx(ctx, s.dir)
		if err != nil {
			lockFree <- false
			return
		}
		release()
		lockFree <- true
	})

	if _, err := s.Resolve(context.Background(), id, "go", "octocat", "", "", nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case ok := <-lockFree:
		if !ok {
			t.Fatal("the transition listener ran while the directory lock was held (#1539)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never ran")
	}
}

// TestASlowListenerDoesNotBlockAMutation is the same property stated the way an
// operator experiences it: one surface that stops reading its event stream must
// not stop anybody resolving a card — not the next resolve, and not the one
// whose own transition the stuck listener is sitting on.
func TestASlowListenerDoesNotBlockAMutation(t *testing.T) {
	s := New(t.TempDir())
	first := raiseTestCard(t, s, "slow:1")
	second := raiseTestCard(t, s, "slow:2")

	blocked := make(chan struct{})
	entered := make(chan struct{}, 1)
	s.Subscribe(func(entry JournalEntry, _ *DecisionRequest) {
		if entry.Action != ActionResolved {
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-blocked // the unread pipe
	})
	defer close(blocked)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The resolve whose OWN event the listener is stuck on still returns.
	if _, err := s.Resolve(ctx, first, "go", "octocat", "", "", nil); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never ran")
	}

	// …and so does the next one, from a different card.
	if _, err := s.Resolve(ctx, second, "go", "octocat", "", "", nil); err != nil {
		t.Fatalf("a second resolve was blocked by a stuck listener: %v", err)
	}
	if got, _, _ := s.Get(second); got.Lifecycle.State != StateResolved {
		t.Errorf("state = %q, want resolved", got.Lifecycle.State)
	}
}

// TestTooShortActorIsRefusedLocally — the platform's validator refuses a
// two-character actor, so a card resolved with one can never sync. Refusing it
// here means an unsyncable card is never created.
func TestTooShortActorIsRefusedLocally(t *testing.T) {
	s := New(t.TempDir())
	id := raiseTestCard(t, s, "actor:1")

	if _, err := s.Resolve(context.Background(), id, "go", "po", "", "", nil); err == nil {
		t.Fatal("Resolve accepted a 2-character actor")
	} else {
		var actorErr *ActorError
		if !errors.As(err, &actorErr) {
			t.Fatalf("err = %v, want *ActorError", err)
		}
		if !strings.Contains(err.Error(), "lifecycle.*.actor") {
			t.Errorf("error does not name the field the mirror names: %v", err)
		}
	}

	// Refused BEFORE anything is written: the card is untouched and still open.
	got, ok, err := s.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get: %v ok=%v", err, ok)
	}
	if got.Lifecycle.State != StateOpen {
		t.Errorf("state = %q, want open — a refused resolve must not mutate", got.Lifecycle.State)
	}

	if _, err := s.Acknowledge(context.Background(), id, "po"); err == nil {
		t.Error("Acknowledge accepted a 2-character actor")
	}
	if _, err := s.Mute(context.Background(), id, "po"); err == nil {
		t.Error("Mute accepted a 2-character actor")
	}

	// The fallback labels every caller uses must themselves pass.
	for _, actor := range []string{"cli", "vscode", "platform"} {
		if err := ValidateActor(actor); err != nil {
			t.Errorf("ValidateActor(%q) = %v, want nil", actor, err)
		}
	}
}

// TestWorstCaseHoldIsTheTwoBoundsTogether keeps the exported figure the layers
// above size their deadlines against honest.
func TestWorstCaseHoldIsTheTwoBoundsTogether(t *testing.T) {
	if got, want := WorstCaseHold(), flockTimeout+verbTimeout; got != want {
		t.Fatalf("WorstCaseHold() = %s, want %s", got, want)
	}
}
