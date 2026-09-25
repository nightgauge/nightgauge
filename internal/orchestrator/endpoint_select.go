package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Endpoint-aware dispatch (#1679, ADR-022 § Endpoints "Failover").
//
// When a stage's OpenCode model names a declared endpoint and other declared
// endpoints serve the same model id, the stage may run on any of them. The
// scheduler picks one with a free slot, so two parallel stages do not both
// land on one GPU while a second sits idle. A stage that cannot get a slot
// waits, visibly, rather than overloading an endpoint. A dispatch never moves
// to an endpoint that is not a server the operator runs: moving to a hosted
// provider is only ever the operator's explicit adapter_fallback_chain.
//
// The slot ledger is part of the Scheduler's own concurrency accounting and
// is guarded by s.mu, the lock DequeueIndependent's per-repo and workspace
// accounting already uses. It is not a second semaphore: a slot is a count
// under that lock, and a waiter re-checks it whenever a slot is released.

// openCodeEndpointRecheck bounds how long a stage waiting for a slot sleeps
// before it probes again, so an endpoint that comes back is noticed even when
// no slot is released. A variable so a test can shorten it.
var openCodeEndpointRecheck = 30 * time.Second

// openCodeEndpointFailoverBackoff is the base wait before a stage that lost
// its endpoint is re-dispatched on another; the n-th failover of a stage
// waits n times this. A variable so a test can shorten it.
var openCodeEndpointFailoverBackoff = 2 * time.Second

// openCodeEndpointSlotsPath lets a test move the published ledger; nil uses
// adapters.OpenCodeEndpointSlotsPath under the user's home. Never set outside
// a test.
var openCodeEndpointSlotsPath func() string

// openCodeEndpointProbe lets a test replace the dispatch-time readiness probe
// of one endpoint model; nil uses resolveOpenCodeReadiness. Never set outside
// a test.
var openCodeEndpointProbe func(worktreeDir, model string) openCodeReadinessVerdict

// endpointLatencyWindow is how many recent readiness-probe latencies the
// ledger keeps per endpoint for its tie-break.
const endpointLatencyWindow = 16

// endpointLatencyBucket is the resolution the latency tie-break compares
// medians at, so noise of a few milliseconds never outranks config order.
const endpointLatencyBucket = 10 * time.Millisecond

// endpointCandidate is one declared endpoint that serves a stage's model.
type endpointCandidate struct {
	ID    string
	Model string // "<endpoint-id>/<model-id>", the -m value on this endpoint
	Slots int    // declared max_concurrency, 1 when not declared
}

// endpointLedger is the per-endpoint slot accounting. Every field is guarded
// by Scheduler.mu.
type endpointLedger struct {
	inUse    map[string]int
	slots    map[string]int
	latency  map[string][]time.Duration
	waiting  map[string]EndpointSlotWait
	released chan struct{}
}

// EndpointSlotStatus is one endpoint's slots in the scheduler status.
type EndpointSlotStatus struct {
	Endpoint string `json:"endpoint"`
	InUse    int    `json:"inUse"`
	Slots    int    `json:"slots"`
}

// EndpointSlotWait is one stage queued for an endpoint slot.
type EndpointSlotWait struct {
	Repo   string   `json:"repo"`
	Issue  int      `json:"issue"`
	Stage  string   `json:"stage"`
	Model  string   `json:"model"`
	Tried  []string `json:"endpoints"`
	Since  string   `json:"since"`
	waitID string
}

// EndpointSlotsStatus is the ledger in the scheduler status: slots in use per
// endpoint and every stage waiting for one.
type EndpointSlotsStatus struct {
	Endpoints []EndpointSlotStatus `json:"endpoints"`
	Waiting   []EndpointSlotWait   `json:"waiting"`
}

// endpointLease is a slot held on one endpoint. The zero lease holds nothing
// (a hosted model, or one on no declared endpoint).
type endpointLease struct {
	Endpoint   string
	Model      string
	Candidates int
	release    func()
}

// Release returns the slot. It is idempotent and safe on the zero lease.
func (l *endpointLease) Release() {
	if l == nil || l.release == nil {
		return
	}
	l.release()
	l.release = nil
}

// endpointRequest is one stage's ask for an endpoint slot.
type endpointRequest struct {
	WorktreeDir string
	Model       string
	Repo        string
	Issue       int
	Stage       state.PipelineStage
	// Exclude are endpoints this stage already lost (failover).
	Exclude map[string]bool
}

// openCodeEndpointCandidates returns the declared endpoints that serve
// model, in config order: the endpoint model names, then every other
// endpoint that declares the same model id and is a server the operator runs
// (models.IsLocalEndpoint). ok is false when model names no declared
// endpoint, which leaves it to the ordinary readiness path.
func openCodeEndpointCandidates(endpoints []adapters.OpenCodeEndpoint, model string) (cands []endpointCandidate, ok bool) {
	key, bareID, qualified := strings.Cut(strings.TrimSpace(model), "/")
	if !qualified || key == "" || bareID == "" {
		return nil, false
	}
	for _, ep := range endpoints {
		if ep.ID == key {
			ok = true
		}
	}
	if !ok {
		return nil, false
	}
	for _, ep := range endpoints {
		if ep.ID != key {
			if !endpointDeclaresModel(ep, bareID) {
				continue
			}
			// Never move a stage to an endpoint that forwards to a hosted
			// service (self_hosted: false), or to an Ollama cloud model:
			// that is a provider change, which only the operator's
			// adapter_fallback_chain may make (ADR-022 § Endpoints).
			le := models.LocalEndpoint{ID: ep.ID, Provider: ep.Provider, BaseURL: ep.BaseURL, SelfHosted: ep.SelfHosted}
			if !models.IsLocalModel("opencode", ep.ID+"/"+bareID, []models.LocalEndpoint{le}) {
				continue
			}
		}
		slots := ep.MaxConcurrency
		if slots <= 0 {
			slots = 1
		}
		cands = append(cands, endpointCandidate{ID: ep.ID, Model: ep.ID + "/" + bareID, Slots: slots})
	}
	return cands, true
}

func endpointDeclaresModel(ep adapters.OpenCodeEndpoint, id string) bool {
	for _, m := range ep.Models {
		if m.ID == id {
			return true
		}
	}
	return false
}

// endpointLedgerLocked returns the ledger, creating it. Caller holds s.mu.
func (s *Scheduler) endpointLedgerLocked() *endpointLedger {
	if s.endpointSlots == nil {
		s.endpointSlots = &endpointLedger{
			inUse:    map[string]int{},
			slots:    map[string]int{},
			latency:  map[string][]time.Duration{},
			waiting:  map[string]EndpointSlotWait{},
			released: make(chan struct{}),
		}
	}
	return s.endpointSlots
}

// acquireOpenCodeEndpoint picks the endpoint a stage runs on and takes a slot
// on it. Selection order (#1679): endpoints serving the model; healthy per
// dispatch-time readiness (#1646); most free slots; lowest recent median
// readiness latency; config order. With every healthy endpoint full the stage
// waits, listed in the scheduler status, until a slot is released or the
// context ends. With no healthy endpoint left it is refused as an
// environment failure naming every endpoint tried.
func (s *Scheduler) acquireOpenCodeEndpoint(ctx context.Context, req endpointRequest) (endpointLease, openCodeReadinessVerdict) {
	loadSettings := openCodeReadinessLoadSettings
	if loadSettings == nil {
		loadSettings = config.LoadOpenCodeConfig
	}
	probe := openCodeEndpointProbe
	if probe == nil {
		probe = resolveOpenCodeReadiness
	}
	settings, err := loadSettings(req.WorktreeDir)
	if err != nil {
		return endpointLease{}, probe(req.WorktreeDir, req.Model)
	}
	endpoints, err := adapters.OpenCodeEndpoints(settings)
	if err != nil {
		return endpointLease{}, probe(req.WorktreeDir, req.Model)
	}
	cands, ok := openCodeEndpointCandidates(endpoints, req.Model)
	if !ok {
		return endpointLease{}, probe(req.WorktreeDir, req.Model)
	}

	waitID := fmt.Sprintf("%s#%d/%s", req.Repo, req.Issue, req.Stage)
	defer func() {
		s.mu.Lock()
		if l := s.endpointSlots; l != nil {
			if _, waited := l.waiting[waitID]; waited {
				delete(l.waiting, waitID)
				s.publishEndpointSlotsLocked()
			}
		}
		s.mu.Unlock()
	}()

	for {
		var healthy []endpointCandidate
		var tried []string
		var refusal openCodeReadinessVerdict
		var reasons []string
		var last openCodeReadinessVerdict
		for _, c := range cands {
			tried = append(tried, c.ID)
			if req.Exclude[c.ID] {
				reasons = append(reasons, c.ID+": lost earlier in this stage")
				if refusal.Kind == "" {
					refusal.Kind = TerminalKindNetworkUnavailable
				}
				continue
			}
			start := time.Now()
			v := probe(req.WorktreeDir, c.Model)
			elapsed := time.Since(start)
			if v.Ready {
				healthy = append(healthy, c)
				s.mu.Lock()
				l := s.endpointLedgerLocked()
				l.latency[c.ID] = append(l.latency[c.ID], elapsed)
				if n := len(l.latency[c.ID]); n > endpointLatencyWindow {
					l.latency[c.ID] = l.latency[c.ID][n-endpointLatencyWindow:]
				}
				s.mu.Unlock()
				continue
			}
			last = v
			reasons = append(reasons, c.ID+": "+v.Reason)
			// network_unavailable outranks model_unavailable: an endpoint
			// that does not answer is the environment's failure.
			if refusal.Kind != TerminalKindNetworkUnavailable {
				refusal.Kind = v.Kind
			}
		}
		if len(healthy) == 0 {
			if len(cands) == 1 && len(reasons) == 1 && !req.Exclude[cands[0].ID] {
				// One endpoint serves the model: keep its own refusal, as
				// before endpoint-aware dispatch.
				return endpointLease{}, last
			}
			refusal.Reason = fmt.Sprintf("no endpoint serving %s is ready; endpoints tried: %s (%s)",
				modelIDOf(req.Model), strings.Join(tried, ", "), strings.Join(reasons, "; "))
			return endpointLease{}, refusal
		}

		s.mu.Lock()
		l := s.endpointLedgerLocked()
		for _, c := range healthy {
			l.slots[c.ID] = c.Slots
		}
		best := pickEndpoint(healthy, l)
		if best != nil {
			l.inUse[best.ID]++
			delete(l.waiting, waitID)
			s.publishEndpointSlotsLocked()
			s.mu.Unlock()
			id := best.ID
			once := false
			return endpointLease{
				Endpoint:   id,
				Model:      best.Model,
				Candidates: len(cands),
				release: func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					if once {
						return
					}
					once = true
					l := s.endpointLedgerLocked()
					if l.inUse[id] > 0 {
						l.inUse[id]--
					}
					close(l.released)
					l.released = make(chan struct{})
					s.publishEndpointSlotsLocked()
				},
			}, openCodeReadinessVerdict{Ready: true}
		}
		if _, already := l.waiting[waitID]; !already {
			l.waiting[waitID] = EndpointSlotWait{
				Repo: req.Repo, Issue: req.Issue, Stage: string(req.Stage), Model: req.Model,
				Tried: tried, Since: time.Now().UTC().Format(time.RFC3339), waitID: waitID,
			}
			log.Printf("#%d: stage %s waits for a slot on %s (every healthy endpoint serving it is full)",
				req.Issue, req.Stage, strings.Join(tried, ", "))
			s.publishEndpointSlotsLocked()
		}
		released := l.released
		s.mu.Unlock()

		timer := time.NewTimer(openCodeEndpointRecheck)
		select {
		case <-ctx.Done():
			timer.Stop()
			return endpointLease{}, openCodeReadinessVerdict{
				Kind:   TerminalKindOperatorStop,
				Reason: "cancelled while waiting for an endpoint slot",
			}
		case <-released:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// pickEndpoint returns the healthy candidate with the most free slots, ties
// broken by the lowest median readiness latency and then config order, or
// nil when every one is full. Caller holds s.mu.
func pickEndpoint(healthy []endpointCandidate, l *endpointLedger) *endpointCandidate {
	type scored struct {
		c      endpointCandidate
		free   int
		median time.Duration
		order  int
	}
	var open []scored
	for i, c := range healthy {
		free := c.Slots - l.inUse[c.ID]
		if free <= 0 {
			continue
		}
		open = append(open, scored{c: c, free: free, median: medianDuration(l.latency[c.ID]).Truncate(endpointLatencyBucket), order: i})
	}
	if len(open) == 0 {
		return nil
	}
	sort.SliceStable(open, func(i, j int) bool {
		if open[i].free != open[j].free {
			return open[i].free > open[j].free
		}
		if open[i].median != open[j].median {
			return open[i].median < open[j].median
		}
		return open[i].order < open[j].order
	})
	return &open[0].c
}

func medianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func modelIDOf(model string) string {
	if _, id, ok := strings.Cut(model, "/"); ok {
		return id
	}
	return model
}

// EndpointSlots is the endpoint slot ledger for the scheduler status: slots
// in use per endpoint, and every stage waiting for one.
func (s *Scheduler) EndpointSlots() EndpointSlotsStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpointSlotsLocked()
}

func (s *Scheduler) endpointSlotsLocked() EndpointSlotsStatus {
	out := EndpointSlotsStatus{Endpoints: []EndpointSlotStatus{}, Waiting: []EndpointSlotWait{}}
	l := s.endpointSlots
	if l == nil {
		return out
	}
	for id, slots := range l.slots {
		out.Endpoints = append(out.Endpoints, EndpointSlotStatus{Endpoint: id, InUse: l.inUse[id], Slots: slots})
	}
	sort.Slice(out.Endpoints, func(i, j int) bool { return out.Endpoints[i].Endpoint < out.Endpoints[j].Endpoint })
	for _, w := range l.waiting {
		w.Tried = append([]string(nil), w.Tried...)
		out.Waiting = append(out.Waiting, w)
	}
	sort.Slice(out.Waiting, func(i, j int) bool { return out.Waiting[i].waitID < out.Waiting[j].waitID })
	return out
}

// publishEndpointSlotsLocked writes the ledger where `nightgauge doctor
// --adapters` reads slots in use. Best effort: a failed write costs only the
// doctor's view. Caller holds s.mu.
func (s *Scheduler) publishEndpointSlotsLocked() {
	path := ""
	if openCodeEndpointSlotsPath != nil {
		path = openCodeEndpointSlotsPath()
	} else if home, err := os.UserHomeDir(); err == nil {
		path = adapters.OpenCodeEndpointSlotsPath(home)
	}
	if path == "" {
		return
	}
	snap := s.endpointSlotsLocked()
	pub := adapters.OpenCodeEndpointSlots{PID: os.Getpid(), UpdatedAt: time.Now().UTC(), InUse: map[string]int{}}
	for _, e := range snap.Endpoints {
		pub.InUse[e.Endpoint] = e.InUse
	}
	for _, w := range snap.Waiting {
		if pub.Waiting == nil {
			pub.Waiting = map[string]int{}
		}
		key, _, _ := strings.Cut(w.Model, "/")
		pub.Waiting[key]++
	}
	if err := adapters.WriteOpenCodeEndpointSlots(path, pub); err != nil {
		log.Printf("opencode: publishing endpoint slots failed: %v", err)
	}
}

// endpointFailoverKinds are the terminal kinds that mean a stage lost its
// endpoint rather than failed on its own (#1631): the server stopped
// answering, or stopped serving the model.
func endpointFailoverKind(kind string) bool {
	return kind == TerminalKindNetworkUnavailable || kind == TerminalKindModelUnavailable
}

// endpointFailoverDecision says whether a stage that failed on lease should be
// re-dispatched on another endpoint serving the same model: only before its
// first step finished (no token usage reported), only for a kind that means
// the endpoint was lost, and at most lease.Candidates attempts per stage.
func endpointFailoverDecision(lease endpointLease, kind string, noStepFinished bool, failovers int) bool {
	return lease.Endpoint != "" && lease.Candidates > 1 && noStepFinished &&
		endpointFailoverKind(kind) && failovers+1 < lease.Candidates
}

// sleepEndpointFailoverBackoff waits before the n-th failover of a stage
// (n starting at 1), returning false when ctx ends first.
func sleepEndpointFailoverBackoff(ctx context.Context, n int) bool {
	timer := time.NewTimer(time.Duration(n) * openCodeEndpointFailoverBackoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
