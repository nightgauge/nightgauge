package gitlab

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge/webhook"
)

// maxWebhookBodyBytes caps the request body for GitLab webhooks. GitLab's
// documented maximum payload is ~400 KB; 512 KB gives a generous headroom.
const maxWebhookBodyBytes = 512 * 1024

// healthPath and metricsPath are the canonical probe endpoints. GitLab
// servers may probe /-/health from external IPs so we do not apply the
// loopback-only restriction used by the Mattermost inbound handler.
const (
	healthPath  = "/-/health"
	metricsPath = "/-/metrics"
)

// Metrics tracks atomic counters for the /-/metrics endpoint.
type Metrics struct {
	EventsReceived int64
	EventsDropped  int64
	DedupeHits     int64
}

// GitLabHandler is the platform-specific HTTP handler for GitLab project hooks.
// It satisfies webhook.Handler so it can be mounted via webhook.NewMux.
type GitLabHandler struct {
	token     string
	dedup     *DedupeCache
	dispatch  webhook.EventDispatcher
	replayWin time.Duration
	metrics   Metrics
	version   string
}

// NewGitLabHandler constructs a GitLabHandler.
func NewGitLabHandler(token string, dedup *DedupeCache, dispatch webhook.EventDispatcher, opts ...HandlerOption) *GitLabHandler {
	h := &GitLabHandler{
		token:     token,
		dedup:     dedup,
		dispatch:  dispatch,
		replayWin: DefaultReplayWindow,
		version:   "dev",
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOption is a functional option for GitLabHandler.
type HandlerOption func(*GitLabHandler)

// WithReplayWindow sets the maximum age of an event before it is considered
// stale. Events older than the window are accepted with a log warning but not
// dispatched (returns 200 silently so GitLab does not retry).
func WithReplayWindow(d time.Duration) HandlerOption {
	return func(h *GitLabHandler) { h.replayWin = d }
}

// WithVersion sets the build version returned by /-/health.
func WithVersion(v string) HandlerOption {
	return func(h *GitLabHandler) { h.version = v }
}

// Register mounts the GitLab webhook POST receiver plus /-/health and
// /-/metrics onto mux at basePath. Satisfies webhook.Handler.
func (h *GitLabHandler) Register(mux *http.ServeMux, basePath string) {
	mux.Handle(basePath, h)
	mux.Handle(healthPath, h.healthHandler())
	mux.Handle(metricsPath, h.metricsHandler())
}

// ServeHTTP is the GitLab webhook POST receiver. It enforces:
//  1. POST only
//  2. Body size cap
//  3. Token verification (constant-time; 401 on failure with audit log)
//  4. Deduplication (200 on hit, no dispatch)
//  5. Payload parsing
//  6. Stale-event check (200 with warning, no dispatch)
//  7. IPC dispatch
func (h *GitLabHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	atomic.AddInt64(&h.metrics.EventsReceived, 1)

	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)

	token := r.Header.Get("X-Gitlab-Token")
	if !VerifyToken(token, h.token) {
		log.Printf("security-audit: gitlab webhook 401 from %s — token mismatch", r.RemoteAddr)
		atomic.AddInt64(&h.metrics.EventsDropped, 1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	eventKind := r.Header.Get("X-Gitlab-Event")
	deliveryID := r.Header.Get("X-Gitlab-Event-UUID")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		atomic.AddInt64(&h.metrics.EventsDropped, 1)
		http.Error(w, "request body read error", http.StatusBadRequest)
		return
	}

	isDup, err := h.dedup.IsDuplicate(r.Context(), deliveryID)
	if err != nil {
		log.Printf("gitlab webhook: dedup check error: %v", err)
		// Non-fatal — treat as not a duplicate and proceed.
	}
	if isDup {
		atomic.AddInt64(&h.metrics.DedupeHits, 1)
		w.WriteHeader(http.StatusOK)
		return
	}

	evt, err := ParseWebhookPayload(eventKind, deliveryID, body)
	if err != nil {
		// ErrUnsupportedEventKind → skip silently with 200 (do not penalise GitLab).
		log.Printf("gitlab webhook: parse payload (%q): %v", eventKind, err)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Stale-event check using payload timestamp. Log and skip — don't error
	// to GitLab or it will retry, creating a replay loop.
	if h.replayWin > 0 && IsStale(evt.OccurredAt, h.replayWin) {
		log.Printf("gitlab webhook: stale event %q delivery=%s occurred_at=%s — accepted but not dispatched",
			eventKind, evt.DeliveryID, evt.OccurredAt.Format(time.RFC3339))
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := h.dedup.MarkSeen(r.Context(), evt.DeliveryID); err != nil {
		log.Printf("gitlab webhook: mark seen: %v", err)
	}

	fwEvt := webhook.ForgeWebhookEvent{
		Source:     evt.Source,
		EventType:  evt.IPCEventName(),
		DeliveryID: evt.DeliveryID,
		OccurredAt: evt.OccurredAt,
		Payload:    evt,
		ProjectID:  fmt.Sprintf("%d", evt.ProjectID),
		ProjectURL: evt.ProjectURL,
	}

	if err := h.dispatch.Dispatch(r.Context(), fwEvt); err != nil {
		log.Printf("gitlab webhook: dispatch error: %v", err)
		// Return 200 — GitLab retries on 5xx, which is undesirable for
		// a successfully-parsed but un-dispatchable event.
	}

	w.WriteHeader(http.StatusOK)
}

func (h *GitLabHandler) healthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": h.version,
		})
	})
}

func (h *GitLabHandler) metricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]int64{
			"events_received": atomic.LoadInt64(&h.metrics.EventsReceived),
			"events_dropped":  atomic.LoadInt64(&h.metrics.EventsDropped),
			"dedupe_hits":     atomic.LoadInt64(&h.metrics.DedupeHits),
		})
	})
}
