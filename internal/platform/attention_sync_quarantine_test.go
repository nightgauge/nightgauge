package platform

// #1539 — the mirror bridge retried one card it could never accept, 931 times,
// and the store it read from had to keep working while it did.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// TestAttentionSync_ValidationRejectionIsQuarantined: a rejection that names a
// field is a validator verdict, and a validator is a pure function of the
// payload. Retrying it is not resilience, it is a loop.
func TestAttentionSync_ValidationRejectionIsQuarantined(t *testing.T) {
	const rejectedID = "dr_01912d3e-7f4a-7b1e-8c2a-00000000000b"
	const reason = "lifecycle.resolved.actor: Too small: expected string to have >=3 characters"

	var pushesCarryingTheCard int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushesCarryingTheCard, 1)
		echoRejecting(map[string]string{rejectedID: reason})(w, r)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest(rejectedID, attention.StateResolved)})

	// Well past quarantineAfter: without a terminal state this is the 931-sweep
	// loop, one push per sweep forever.
	for i := 1; i <= 10; i++ {
		if err := svc.SyncAll(context.Background(), lister); err != nil {
			t.Fatalf("SyncAll #%d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&pushesCarryingTheCard); got != quarantineAfter {
		t.Fatalf("the card was pushed %d times over 10 sweeps, want %d — a schema rejection must be terminal", got, quarantineAfter)
	}

	svc.mu.Lock()
	q, quarantined := svc.quarantine[rejectedID]
	svc.mu.Unlock()
	if !quarantined {
		t.Fatal("the card was never quarantined")
	}
	if q.field != "lifecycle.resolved.actor" {
		t.Errorf("quarantine field = %q, want the offending field named", q.field)
	}
	if !strings.Contains(q.reason, "Too small") {
		t.Errorf("quarantine reason = %q, want the validator's verbatim message", q.reason)
	}
}

// TestAttentionSync_QuarantineLiftsWhenTheCardChanges: quarantine is pinned to
// the payload that was refused, not to the id. An operator who re-resolves the
// card with a valid actor must see it reach the mirror without restarting the
// daemon.
func TestAttentionSync_QuarantineLiftsWhenTheCardChanges(t *testing.T) {
	const id = "dr_01912d3e-7f4a-7b1e-8c2a-00000000000c"

	var rejecting atomic.Bool
	rejecting.Store(true)
	var pushes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushes, 1)
		if rejecting.Load() {
			echoRejecting(map[string]string{id: "lifecycle.resolved.actor: Too small"})(w, r)
			return
		}
		echoAcceptAll(w, r)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	card := sampleRequest(id, attention.StateResolved)
	lister.set([]attention.DecisionRequest{card})

	for i := 0; i < 5; i++ {
		if err := svc.SyncAll(context.Background(), lister); err != nil {
			t.Fatalf("SyncAll: %v", err)
		}
	}
	if got := atomic.LoadInt32(&pushes); got != quarantineAfter {
		t.Fatalf("pushes = %d, want %d before the content changes", got, quarantineAfter)
	}

	// The operator fixes the card: new content, new fingerprint.
	rejecting.Store(false)
	card.Title = "Fleet stopped — resolved by a validly-named operator"
	lister.set([]attention.DecisionRequest{card})

	if err := svc.SyncAll(context.Background(), lister); err != nil {
		t.Fatalf("SyncAll after the fix: %v", err)
	}
	if got := atomic.LoadInt32(&pushes); got != quarantineAfter+1 {
		t.Fatalf("pushes = %d, want %d — a changed card must get a fresh attempt", got, quarantineAfter+1)
	}
	svc.mu.Lock()
	_, stillQuarantined := svc.quarantine[id]
	_, watermarked := svc.watermark[id]
	svc.mu.Unlock()
	if stillQuarantined {
		t.Error("the quarantine survived a content change")
	}
	if !watermarked {
		t.Error("the accepted card was not watermarked")
	}
}

// TestAttentionSync_UnnamedRejectionIsStillRetried keeps the terminal state
// narrow. A rejection with no reason could be anything — a transient
// server-side error included — so it stays on the existing retry path.
func TestAttentionSync_UnnamedRejectionIsStillRetried(t *testing.T) {
	const id = "dr_01912d3e-7f4a-7b1e-8c2a-00000000000d"

	var pushes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushes, 1)
		// Echoes nothing and explains nothing: "cannot tell why".
		echoRejecting(map[string]string{id: ""})(w, r)
	}))
	defer srv.Close()

	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	lister := &fakeLister{}
	lister.set([]attention.DecisionRequest{sampleRequest(id, attention.StateOpen)})

	for i := 0; i < 6; i++ {
		if err := svc.SyncAll(context.Background(), lister); err != nil {
			t.Fatalf("SyncAll: %v", err)
		}
	}
	if got := atomic.LoadInt32(&pushes); got != 6 {
		t.Fatalf("pushes = %d, want 6 — an unexplained miss is not a schema verdict", got)
	}
}

// TestLocalResolveSurvivesAnUnreachableMirror is the acceptance criterion
// stated end to end: with the bridge attached to a real store and the mirror
// hanging on every request, resolving a card locally still completes promptly
// and is durable. Local-first is not a slogan — the store never waits on the
// network.
func TestLocalResolveSurvivesAnUnreachableMirror(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	// LIFO: release the handler BEFORE Close waits on it.
	defer srv.Close()
	defer close(hang)

	store := attention.New(t.TempDir())
	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Attach(ctx, store)

	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "mirror-down:1",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          "the mirror is down",
		Body:           "resolve me anyway",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme"},
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		_, err := store.Resolve(rctx, id, "go", "octocat", "", "", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Resolve against a hanging mirror: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Resolve never returned while the mirror hung — this is the #1539 hang")
	}

	got, ok, err := store.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get: %v ok=%v", err, ok)
	}
	if got.Lifecycle.State != attention.StateResolved {
		t.Fatalf("state = %q, want resolved — the local write must not depend on the mirror", got.Lifecycle.State)
	}
}
