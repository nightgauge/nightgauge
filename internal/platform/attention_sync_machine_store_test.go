package platform

// #3453 — a card raised in a checkout other than the daemon's never reached
// the platform: each checkout had its own store and the daemon's bridge synced
// only its own. The store is now one per workspace,
// STATE/attention/<workspace-key>, shared by every checkout of the workspace
// and by no checkout of another.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// uploadRecorder is a /v1/attention/sync stub that accepts every card and
// records the ids each PUT carried.
type uploadRecorder struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (u *uploadRecorder) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/attention/sync" || r.Method != http.MethodPut {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Requests []attention.DecisionRequest `json:"requests"`
	}
	_ = json.Unmarshal(raw, &body)
	u.mu.Lock()
	for _, req := range body.Requests {
		u.ids[req.ID] = true
	}
	u.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	echoAcceptAll(w, r)
}

func (u *uploadRecorder) sent(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.ids[id]
}

// raiseIn raises a card through the store every CLI producer opens
// (attention.New(<checkout>).Raise — `nightgauge attention sweep`, the
// post-merge report), in the checkout root.
func raiseIn(t *testing.T, root, key string) string {
	t.Helper()
	id, err := attention.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := attention.New(root).Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: key,
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          "raised in a second checkout",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/platform", Issue: 3453},
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise in %s: %v", root, err)
	}
	return id
}

func TestAttentionSync_CardRaisedInAnotherCheckoutReachesThePlatform(t *testing.T) {
	layouttest.StateHome(t)
	repos := layouttest.WorkspaceRepos(t, 2)
	daemonCheckout := repos[0]    // the manifest root the daemon serves
	otherCheckout := repos[1]     // a sibling repository the manifest lists
	foreign := layouttest.Repo(t) // another workspace on the same machine

	rec := &uploadRecorder{ids: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	// The daemon's bridge syncs the store its scheduler opened over its own
	// workspace root (autonomous.go: attention.New(workspaceRoot)).
	daemonStore := attention.New(daemonCheckout)

	// AC 1: a card raised in the other checkout is uploaded by the daemon's
	// next sweep.
	first := raiseIn(t, otherCheckout, "k:other-checkout")
	svc := NewAttentionSyncService(onlineClient(t, srv.URL))
	if err := svc.SyncAll(context.Background(), daemonStore); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if !rec.sent(first) {
		t.Fatalf("the daemon's sweep did not upload %s, raised in another checkout", first)
	}

	// The workspace boundary: a card raised in another workspace on the same
	// machine is that workspace's daemon's to upload, resolve and expire,
	// never this one's (ADR-019).
	notMine := raiseIn(t, foreign, "k:foreign-workspace")
	if err := svc.SyncAll(context.Background(), attention.New(daemonCheckout)); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	if rec.sent(notMine) {
		t.Fatalf("the daemon uploaded %s, a card raised in another workspace", notMine)
	}

	// AC 2: a card raised while no bridge runs is uploaded by the first sweep
	// of the next bridge.
	offline := raiseIn(t, otherCheckout, "k:no-bridge")
	restarted := NewAttentionSyncService(onlineClient(t, srv.URL))
	if err := restarted.SyncAll(context.Background(), attention.New(daemonCheckout)); err != nil {
		t.Fatalf("SyncAll after restart: %v", err)
	}
	if !rec.sent(offline) {
		t.Fatalf("the restarted bridge's first sweep did not upload %s", offline)
	}

	// AC 3: `attention list` reads the same open cards from either checkout.
	fromDaemon, err := attention.New(daemonCheckout).List(attention.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	fromOther, err := attention.New(otherCheckout).List(attention.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fromDaemon) != 2 || len(fromOther) != len(fromDaemon) {
		t.Fatalf("list from the daemon checkout = %d cards, from the other = %d, want 2 each", len(fromDaemon), len(fromOther))
	}
	for i := range fromDaemon {
		if fromDaemon[i].ID != fromOther[i].ID {
			t.Fatalf("the two checkouts list different cards: %s vs %s", fromDaemon[i].ID, fromOther[i].ID)
		}
	}
}

// AC 2: the bridge sweeps as soon as it attaches, so a card raised while no
// bridge ran is uploaded at once, not after the first attentionSyncInterval
// tick.
func TestAttentionSync_AttachUploadsWaitingCardsImmediately(t *testing.T) {
	layouttest.StateHome(t)
	repos := layouttest.WorkspaceRepos(t, 2)
	root := repos[0]
	waiting := raiseIn(t, repos[1], "k:waiting")

	rec := &uploadRecorder{ids: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewAttentionSyncService(onlineClient(t, srv.URL)).Attach(ctx, attention.New(root))

	deadline := time.Now().Add(5 * time.Second)
	for !rec.sent(waiting) {
		if time.Now().After(deadline) {
			t.Fatalf("Attach did not upload %s within 5s (the periodic interval is %s)", waiting, attentionSyncInterval)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
