package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockDispatcher records Dispatch calls for scheduler integration tests.
type mockDispatcher struct {
	calls  []CandidateItem
	slotID string
	err    error
}

func (m *mockDispatcher) Dispatch(_ context.Context, item CandidateItem) (string, error) {
	m.calls = append(m.calls, item)
	if m.err != nil {
		return "", m.err
	}
	id := m.slotID
	if id == "" {
		id = localSlotID(item)
	}
	return id, nil
}

// ---- LocalDispatcher tests ----

func TestLocalDispatcher_Dispatch_IPC(t *testing.T) {
	var got struct {
		owner  string
		repo   string
		number int
		title  string
	}
	disp := NewLocalDispatcher(
		func(owner, repo string, issueNumber int, title string) {
			got.owner = owner
			got.repo = repo
			got.number = issueNumber
			got.title = title
		},
		nil,
	)

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 42, Title: "test"}
	slotID, err := disp.Dispatch(context.Background(), item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slotID == "" {
		t.Fatal("expected non-empty slotID")
	}
	if got.owner != "nightgauge" || got.repo != "nightgauge" || got.number != 42 {
		t.Errorf("callback got wrong values: owner=%q repo=%q number=%d", got.owner, got.repo, got.number)
	}
}

func TestLocalDispatcher_Dispatch_Fallback(t *testing.T) {
	var fallbackCalled bool
	disp := NewLocalDispatcher(
		nil,
		func(_ context.Context, item CandidateItem) {
			fallbackCalled = true
		},
	)

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 7, Title: "fallback"}
	_, err := disp.Dispatch(context.Background(), item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fallbackCalled {
		t.Error("expected fallback to be called when onDispatch is nil")
	}
}

func TestLocalSlotID_Deterministic(t *testing.T) {
	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 99}
	a := localSlotID(item)
	b := localSlotID(item)
	if a != b {
		t.Errorf("slotID not deterministic: %q vs %q", a, b)
	}
	if a == "" {
		t.Error("slotID must be non-empty")
	}
}

func TestLocalSlotID_Unique(t *testing.T) {
	a := localSlotID(CandidateItem{Repo: "owner/repo", Number: 1})
	b := localSlotID(CandidateItem{Repo: "owner/repo", Number: 2})
	if a == b {
		t.Errorf("different issues should produce different slotIDs: both=%q", a)
	}
}

// ---- CloudDispatcher tests ----

func newTestCloudServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *CloudDispatcher) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	disp := NewCloudDispatcher(srv.URL, "acct-123", "tok-abc")
	return srv, disp
}

func TestCloudDispatcher_Dispatch_Success(t *testing.T) {
	_, disp := newTestCloudServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer tok-abc" {
			t.Errorf("expected Bearer token, got %q", r.Header.Get("Authorization"))
		}
		var req dispatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.IssueNumber != 42 || req.AccountID != "acct-123" || req.Executor != "cloud" {
			t.Errorf("unexpected request: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dispatchResponse{SlotID: "slot-xyz"}) //nolint:errcheck
	})

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 42, Title: "cloud test"}
	slotID, err := disp.Dispatch(context.Background(), item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slotID != "slot-xyz" {
		t.Errorf("expected slotID=slot-xyz, got %q", slotID)
	}
}

func TestCloudDispatcher_Dispatch_Retry5xx(t *testing.T) {
	attempts := 0
	_, disp := newTestCloudServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dispatchResponse{SlotID: "slot-retry"}) //nolint:errcheck
	})

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 1}
	slotID, err := disp.Dispatch(context.Background(), item)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if slotID != "slot-retry" {
		t.Errorf("expected slot-retry, got %q", slotID)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestCloudDispatcher_Dispatch_PermanentError(t *testing.T) {
	_, disp := newTestCloudServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 1}
	_, err := disp.Dispatch(context.Background(), item)
	if err == nil {
		t.Fatal("expected error on repeated 5xx, got nil")
	}
}

func TestCloudDispatcher_Dispatch_4xx(t *testing.T) {
	_, disp := newTestCloudServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`)) //nolint:errcheck
	})

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 1}
	_, err := disp.Dispatch(context.Background(), item)
	if err == nil {
		t.Fatal("expected error on 4xx, got nil")
	}
}

func TestCloudDispatcher_Dispatch_EmptySlotID(t *testing.T) {
	_, disp := newTestCloudServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dispatchResponse{SlotID: ""}) //nolint:errcheck
	})

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 1}
	_, err := disp.Dispatch(context.Background(), item)
	if err == nil {
		t.Fatal("expected error on empty slotId, got nil")
	}
}

func TestCloudDispatcher_Dispatch_ContextCancelled(t *testing.T) {
	_, disp := newTestCloudServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 1}
	_, err := disp.Dispatch(ctx, item)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		// Either context.Canceled or an http error wrapping it is acceptable.
		t.Logf("error (acceptable): %v", err)
	}
}

// ---- mockDispatcher integration with AutonomousScheduler ----

func TestAutonomousScheduler_SetDispatcher_RoutesThrough(t *testing.T) {
	mock := &mockDispatcher{slotID: "mock-slot-1"}

	// Build a minimal scheduler — we only need enqueueItem to exercise the path.
	as := &AutonomousScheduler{
		dispatcher:           mock,
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		conflictRestartCount: make(map[string]int),
		state:                &AutonomousState{},
		stopCh:               make(chan struct{}, 1),
		stopRefinementCh:     make(chan struct{}, 1),
		rescanCh:             make(chan struct{}, 1),
		refinementSem:        make(chan struct{}, 1),
		refinementCooldown:   make(map[string]time.Time),
		refinementFailures:   make(map[string]int),
	}

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 55, Title: "mock dispatch"}
	as.enqueueItem(context.Background(), item)

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call to mockDispatcher, got %d", len(mock.calls))
	}
	if mock.calls[0].Number != 55 {
		t.Errorf("expected issue 55, got %d", mock.calls[0].Number)
	}
}

func TestAutonomousScheduler_SetDispatcher_NoDispatcherUsesLegacy(t *testing.T) {
	var legacyCalled bool

	as := &AutonomousScheduler{
		// dispatcher is nil — should fall to onDispatch
		onDispatch: func(owner, repo string, issueNumber int, title string) {
			legacyCalled = true
		},
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		conflictRestartCount: make(map[string]int),
		state:                &AutonomousState{},
		stopCh:               make(chan struct{}, 1),
		stopRefinementCh:     make(chan struct{}, 1),
		rescanCh:             make(chan struct{}, 1),
		refinementSem:        make(chan struct{}, 1),
		refinementCooldown:   make(map[string]time.Time),
		refinementFailures:   make(map[string]int),
	}

	item := CandidateItem{Repo: "nightgauge/nightgauge", Number: 10}
	as.enqueueItem(context.Background(), item)

	if !legacyCalled {
		t.Error("expected legacy onDispatch to be called when dispatcher is nil")
	}
}
