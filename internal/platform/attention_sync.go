package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// The Action Center platform bridge — client → platform mirror (ADR 015 §C/§E,
// nightgauge/nightgauge#330). The Go binary's local `.nightgauge/attention/`
// store is the single authoritative writer and source of truth; this uploader is
// additive: it pushes open + resolved DecisionRequests to
// PUT /v1/attention/sync (idempotent by `id`) so multi-device surfaces (the
// dashboard) mirror the same queue. It rides the telemetry-uploader pattern:
// license-key config gating (constructed only when a platform client is
// configured — see cmd/nightgauge/main.go), watermark/dirty tracking so a
// request is pushed only when it actually changes, and offline-safety (no
// platform / offline → no-op; local-first behavior unchanged).

// attentionSyncMaxBatch bounds one PUT /v1/attention/sync body — the platform
// caps `requests` at maxItems:500.
const attentionSyncMaxBatch = 500

// attentionSyncInterval is the periodic full-sweep reconciliation cadence. The
// per-transition push (OnTransition) delivers within milliseconds; the sweep is
// the safety net that recovers requests skipped while offline.
const attentionSyncInterval = 30 * time.Second

// attentionSyncBody is the PUT /v1/attention/sync request body
// The requests are the verbatim decision-request read model
// (snake_case), byte-for-byte the store's on-disk shape, so the platform mirror
// and every surface render the identical object with zero per-surface mapping.
type attentionSyncBody struct {
	AgentID   string                      `json:"agent_id,omitempty"`
	MachineID string                      `json:"machine_id,omitempty"`
	Requests  []attention.DecisionRequest `json:"requests"`
}

// attentionSyncResponse mirrors the platform's 200 body — {synced, items}.
type attentionSyncResponse struct {
	Synced int                         `json:"synced"`
	Items  []attention.DecisionRequest `json:"items"`
}

// AttentionLister is the read side of the attention store the uploader sweeps.
// *attention.Store satisfies it.
type AttentionLister interface {
	List(filter attention.ListFilter) ([]attention.DecisionRequest, error)
}

// AttentionSyncService mirrors DecisionRequests to the platform, idempotent by id.
type AttentionSyncService struct {
	client    *Client
	machineID string

	mu         sync.Mutex
	agentID    string            // platform-assigned agent id; empty = mirror-only (agent_id omitted)
	generation uint64            // bumped on every agent-id change; guards stale watermark writes
	watermark  map[string]string // request id -> last-synced content fingerprint
}

// NewAttentionSyncService creates a sync service bound to the platform client.
// The `machine_id` is the client's resolved machine id (always sent, scoping the
// mirror). The `agent_id` starts EMPTY — the machine id is NOT a registered
// platform agent, and sending it as `agent_id` violates the platform's
// decision_requests → agents FK and 500s the sweep (#341). The service therefore
// begins in mirror-only mode (agent_id omitted) and the serve bridge late-binds
// the platform-assigned agent id via SetAgentID once registration succeeds.
func NewAttentionSyncService(client *Client) *AttentionSyncService {
	machineID := ""
	if client != nil {
		machineID = client.AgentID()
	}
	return &AttentionSyncService{
		client:    client,
		machineID: machineID,
		watermark: make(map[string]string),
	}
}

// SetAgentID late-binds the platform-registered agent id onto the sync body.
// Called after POST /v1/agents/register succeeds (and again after a re-register
// on heartbeat 404). It clears the watermark so the next sweep re-pushes every
// request once, backfilling `agent_id` on rows the platform already mirrored in
// mirror-only mode — otherwise the unchanged-content watermark would suppress
// the re-push and those rows keep a null agent_id. The generation bump makes an
// in-flight pushBatch (which captured the old id) skip re-populating the cleared
// watermark, so no stale entry defeats the backfill. Thread-safe: the sweep
// goroutine reads the agent id under this same mutex.
func (s *AttentionSyncService) SetAgentID(agentID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentID == agentID {
		return
	}
	s.agentID = agentID
	s.generation++
	s.watermark = make(map[string]string)
}

// Attach subscribes the uploader to the store's transition stream (push each
// raise/resolve/expire immediately) and starts the periodic reconciliation
// sweep. This is the single wiring entry point; it re-uses the store's existing
// listener fan-out — it never adds a second event emitter (ADR 015 §D).
func (s *AttentionSyncService) Attach(ctx context.Context, store AttentionLister) {
	if s == nil || store == nil {
		return
	}
	if sub, ok := store.(interface {
		Subscribe(attention.TransitionListener)
	}); ok {
		sub.Subscribe(func(_ attention.JournalEntry, req *attention.DecisionRequest) {
			s.OnTransition(ctx, req)
		})
	}
	s.StartPeriodicSync(ctx, store)
}

// OnTransition pushes a single request the moment its lifecycle changes. It runs
// as a store TransitionListener, so it MUST NOT re-enter the store or block the
// writer's mutex — it launches the HTTP push on a goroutine and returns. Offline
// → no-op (the periodic sweep re-pushes it once connectivity returns, because the
// watermark is only advanced on a successful push).
func (s *AttentionSyncService) OnTransition(ctx context.Context, req *attention.DecisionRequest) {
	if s == nil || req == nil {
		return
	}
	reqCopy := *req
	go func() {
		if err := s.syncOne(ctx, reqCopy); err != nil {
			log.Printf("attention sync: push %s failed (will retry on sweep): %v", reqCopy.ID, err)
		}
	}()
}

// StartPeriodicSync runs a background reconciliation sweep every
// attentionSyncInterval until ctx is cancelled.
func (s *AttentionSyncService) StartPeriodicSync(ctx context.Context, store AttentionLister) {
	if s == nil || store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(attentionSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.SyncAll(ctx, store); err != nil {
					log.Printf("attention sync: periodic sweep failed: %v", err)
				}
			}
		}
	}()
}

// SyncAll lists open + resolved requests and pushes every one whose content has
// changed since it was last synced (watermark diff). Idempotent and offline-safe:
// a no-op when offline or when nothing is dirty. Batches to the platform's 500
// item ceiling.
func (s *AttentionSyncService) SyncAll(ctx context.Context, store AttentionLister) error {
	if !s.online() {
		return nil
	}
	reqs, err := store.List(attention.ListFilter{IncludeTerminal: true})
	if err != nil {
		return fmt.Errorf("attention sync: list: %w", err)
	}
	var dirty []attention.DecisionRequest
	s.mu.Lock()
	for _, r := range reqs {
		if s.watermark[r.ID] != fingerprint(r) {
			dirty = append(dirty, r)
		}
	}
	s.mu.Unlock()
	if len(dirty) == 0 {
		return nil
	}
	for start := 0; start < len(dirty); start += attentionSyncMaxBatch {
		end := start + attentionSyncMaxBatch
		if end > len(dirty) {
			end = len(dirty)
		}
		if err := s.pushBatch(ctx, dirty[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// syncOne pushes exactly one request when it is dirty. Offline or unchanged → a
// no-op returning nil.
func (s *AttentionSyncService) syncOne(ctx context.Context, req attention.DecisionRequest) error {
	if !s.online() {
		return nil
	}
	s.mu.Lock()
	unchanged := s.watermark[req.ID] == fingerprint(req)
	s.mu.Unlock()
	if unchanged {
		return nil
	}
	return s.pushBatch(ctx, []attention.DecisionRequest{req})
}

// pushBatch PUTs a batch to /v1/attention/sync and, on success, advances the
// watermark for every pushed request so an unchanged request is not re-sent.
func (s *AttentionSyncService) pushBatch(ctx context.Context, reqs []attention.DecisionRequest) error {
	// Read the late-bound agent id (and the generation it belongs to) under the
	// lock so a concurrent SetAgentID cannot tear the body/watermark accounting.
	s.mu.Lock()
	agentID := s.agentID
	gen := s.generation
	s.mu.Unlock()

	body := attentionSyncBody{AgentID: agentID, MachineID: s.machineID, Requests: reqs}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("attention sync: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, s.client.base+"/v1/attention/sync", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("attention sync: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if s.client.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("attention sync: PUT: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("attention sync: server returned %d", resp.StatusCode)
	}

	// A 2xx confirms the mirror accepted the batch. Advance the watermark for
	// every request we sent (the response echoes them, but success alone is
	// sufficient — the endpoint is idempotent by id). Skip the advance if the
	// agent id changed mid-flight (SetAgentID bumped the generation and cleared
	// the map): this batch carried the OLD id, so leaving it unwatermarked lets
	// the next sweep re-push it under the new agent id (FK backfill), rather than
	// re-populating the just-cleared watermark with a stale entry.
	s.mu.Lock()
	if s.generation == gen {
		for _, r := range reqs {
			s.watermark[r.ID] = fingerprint(r)
		}
	}
	s.mu.Unlock()

	// Best-effort parse for observability; a parse failure does not undo the sync.
	var parsed attentionSyncResponse
	_ = json.Unmarshal(respBody, &parsed)
	return nil
}

// online reports whether a push should be attempted. A nil client (no platform
// configured) or an offline client is a no-op — local-first behavior is unchanged.
func (s *AttentionSyncService) online() bool {
	return s != nil && s.client != nil && s.client.IsOnline()
}

// fingerprint is a stable content hash of a request. Any change — a lifecycle
// transition or an in-place re-raise payload edit — changes it, so the watermark
// diff pushes exactly the requests that actually changed.
func fingerprint(req attention.DecisionRequest) string {
	data, err := json.Marshal(req)
	if err != nil {
		// Fall back to a per-call unique value so a marshal failure forces a push
		// rather than silently skipping the request.
		return fmt.Sprintf("unmarshalable-%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}
