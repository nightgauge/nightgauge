package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/agentworkspace"
	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// keyedAgentPlatform is a hosted service whose agent rows are keyed as the
// service keys them since #1421: on (machine_id, workspace_key), or on
// machine_id alone when no key is sent. A mirrored card belongs to the agent
// the syncing daemon names, and a resolve of it is published on that agent's
// channel, which fans out to every command stream open on the agent, and is
// replayed to one that opens later.
type keyedAgentPlatform struct {
	mu sync.Mutex
	// rows maps a row key to its agent id; live is the agents not evicted.
	rows map[string]string
	live map[string]bool
	// registrations is every registration body, raw, with the agent it got.
	registrations []keyedRegistration
	// cardAgent is the agent each mirrored card belongs to.
	cardAgent map[string]string
	// frames is every command published on an agent's channel.
	frames map[string][]string
	// deliveries counts the frames written to streams; acks the acks.
	deliveries int
	acks       int
	// streams counts the command streams each agent opened that were served.
	streams map[string]int
	served  int
	// evicted is the agent whose first stream was answered 404, once.
	evicted string
}

type keyedRegistration struct {
	raw, workspaceKey, agentID string
}

func newKeyedAgentPlatform() *keyedAgentPlatform {
	return &keyedAgentPlatform{
		rows:      map[string]string{},
		live:      map[string]bool{},
		cardAgent: map[string]string{},
		frames:    map[string][]string{},
		streams:   map[string]int{},
	}
}

func (p *keyedAgentPlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))

	case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			MachineID    string `json:"machine_id"`
			WorkspaceKey string `json:"workspace_key"`
		}
		_ = json.Unmarshal(raw, &body)
		row := body.MachineID + "\x00unkeyed"
		if body.WorkspaceKey != "" {
			row = body.MachineID + "\x00" + body.WorkspaceKey
		}
		p.mu.Lock()
		id, ok := p.rows[row]
		if !ok {
			id = fmt.Sprintf("agent-%d", len(p.rows)+1)
			p.rows[row] = id
		}
		p.live[id] = true // a re-registration revives the row
		p.registrations = append(p.registrations, keyedRegistration{string(raw), body.WorkspaceKey, id})
		p.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"agentId":%q,"ttl_seconds":90}`, id)

	case r.Method == http.MethodPut && r.URL.Path == "/v1/attention/sync":
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			AgentID  string                      `json:"agent_id"`
			Requests []attention.DecisionRequest `json:"requests"`
		}
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		for _, req := range body.Requests {
			if body.AgentID != "" {
				p.cardAgent[req.ID] = body.AgentID
			}
		}
		p.mu.Unlock()
		resp, _ := json.Marshal(map[string]any{"synced": len(body.Requests), "items": body.Requests, "rejected": []any{}})
		_, _ = w.Write(resp)

	case r.Method == http.MethodGet && len(parts) == 4 && parts[1] == "agents" && parts[3] == "commands":
		id := parts[2]
		p.mu.Lock()
		if !p.live[id] || p.evicted == "" {
			// The first agent to open a stream has been evicted: the daemon
			// re-registers, and must come back as the same workspace.
			if p.evicted == "" {
				p.evicted = id
			}
			p.live[id] = false
			p.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		p.streams[id]++
		p.served++
		p.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		sent := 0
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			p.mu.Lock()
			pending := append([]string(nil), p.frames[id][sent:]...)
			sent += len(pending)
			p.deliveries += len(pending)
			p.mu.Unlock()
			for _, frame := range pending {
				_, _ = fmt.Fprintf(w, "event: command\ndata: %s\n\n", frame)
			}
			if len(pending) > 0 && flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
		}

	case r.Method == http.MethodPost && len(parts) == 6 && parts[1] == "agents" && parts[5] == "ack":
		p.mu.Lock()
		p.acks++
		p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// relayResolve publishes a resolve of card on the channel of the agent the
// card was mirrored under, as the service's relay does.
func (p *keyedAgentPlatform) relayResolve(t *testing.T, card string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	agent := p.cardAgent[card]
	if agent == "" {
		t.Fatalf("card %s was never mirrored under an agent", card)
	}
	p.frames[agent] = append(p.frames[agent], fmt.Sprintf(
		`{"commandId":"cmd-%s","type":"attention_resolve","commandType":"attention_resolve","payload":{"requestId":%q,"optionId":"dismiss","actor":"dashboard"},"createdAt":"2026-10-12T00:00:00.000Z","expiresAt":"2026-10-12T00:05:00.000Z"}`,
		card, card))
}

// resolvingExtension is an extension side that records every relayed resolve
// its daemon applies.
type resolvingExtension struct {
	*extensionSide
	mu       sync.Mutex
	resolved []string
}

func (e *resolvingExtension) ApplyRelayedResolve(_ context.Context, requestID, _, _, _ string) (platform.AttentionResolveOutcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resolved = append(e.resolved, requestID)
	return platform.AttentionResolveOutcome{Applied: true}, nil
}

func (e *resolvingExtension) applied() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.resolved...)
}

// oneCard is an attention store holding a single open card.
type oneCard struct{ card attention.DecisionRequest }

func (o oneCard) List(attention.ListFilter) ([]attention.DecisionRequest, error) {
	return []attention.DecisionRequest{o.card}, nil
}

func openCard(id string) attention.DecisionRequest {
	now := time.Now().UTC()
	return attention.DecisionRequest{
		SchemaVersion:  attention.SchemaVersion,
		ID:             id,
		IdempotencyKey: "human-gate:" + id,
		Kind:           attention.KindResume,
		Severity:       attention.SeverityBlockingFleet,
		Title:          "Review the pull request",
		Body:           "A gate is waiting.",
		Producer:       "human-gate",
		Context:        attention.Context{Repo: "octocat/acme", Issue: 7},
		Options:        []attention.Option{{ID: "dismiss", Label: "Dismiss", Verb: attention.VerbNoop}},
		CreatedAt:      now.Format(time.RFC3339Nano),
		ExpiresAt:      now.Add(time.Hour).Format(time.RFC3339Nano),
		DefaultAction:  "dismiss",
		Lifecycle:      attention.Lifecycle{State: attention.StateOpen},
	}
}

// The acceptance test for #1421's option (a), on one machine running two
// daemons as serve runs them, for workspaces A and B, against a service keyed
// as the hosted service keys agents. A and B register as two agents; A's
// re-registration after an eviction comes back as A's agent; each card is
// mirrored under its own workspace's agent; and a resolve of A's card is
// applied by A's daemon and never reaches B's, and the same for B. With one
// agent per machine both daemons would share one channel, each would be
// handed the other's resolve, and the non-owner would consume it.
func TestRunDaemonPlatformAgent_TwoWorkspacesOneMachine_EachResolveReachesItsOwner(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "test-machine-uuid")
	t.Setenv(agentworkspace.WindowFoldersEnv, "")
	rootA, rootB := t.TempDir(), t.TempDir()

	plat := newKeyedAgentPlatform()
	srv := httptest.NewServer(plat)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	type daemon struct {
		root, card string
		ext        *resolvingExtension
		sync       *platform.AttentionSyncService
	}
	daemons := []*daemon{{root: rootA, card: "dr_workspace_a"}, {root: rootB, card: "dr_workspace_b"}}
	for _, d := range daemons {
		// On-demand health, so the attention sync sees the service online.
		client, err := platform.NewClient(platform.Config{BaseURL: srv.URL, APIKey: "test-key", OnDemandHealth: true})
		if err != nil {
			t.Fatal(err)
		}
		d.ext = &resolvingExtension{extensionSide: &extensionSide{}}
		d.sync = platform.NewAttentionSyncService(client)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Exactly as serve calls it.
			runDaemonPlatformAgent(ctx, client, d.sync, d.ext, "test", d.root, nil, nil, nil)
		}()
	}

	// Both daemons register, one is evicted and re-registers, and both
	// streams open.
	waitUntil(t, "both daemons' command streams", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return plat.served == 2
	})

	// Each daemon mirrors its own card, as its attention sync's sweep does,
	// until the service holds it under an agent.
	for _, d := range daemons {
		waitUntil(t, "card "+d.card+" mirrored under an agent", func() bool {
			if err := d.sync.SyncAll(ctx, oneCard{openCard(d.card)}); err != nil {
				t.Logf("sync %s: %v", d.card, err)
			}
			plat.mu.Lock()
			defer plat.mu.Unlock()
			return plat.cardAgent[d.card] != ""
		})
	}

	plat.mu.Lock()
	regs := append([]keyedRegistration(nil), plat.registrations...)
	agentA, agentB := plat.cardAgent[daemons[0].card], plat.cardAgent[daemons[1].card]
	evicted := plat.evicted
	plat.mu.Unlock()

	if agentA == agentB {
		t.Errorf("A's and B's cards are both held by agent %s: two workspaces on one machine share one agent", agentA)
	}
	if len(regs) != 3 {
		t.Errorf("recorded %d registrations, want 3 (A, B, and the evicted one again)", len(regs))
	}
	keyOf := map[string]string{}
	for _, reg := range regs {
		if reg.workspaceKey == "" {
			t.Errorf("a registration carried no workspace_key: %s", reg.raw)
		}
		if prev, ok := keyOf[reg.agentID]; ok && prev != reg.workspaceKey {
			t.Errorf("agent %s re-registered with key %q, want its first %q", reg.agentID, reg.workspaceKey, prev)
		}
		keyOf[reg.agentID] = reg.workspaceKey
		for _, root := range []string{rootA, rootB} {
			if strings.Contains(reg.raw, root) {
				t.Errorf("registration carries workspace root %q: %s", root, reg.raw)
			}
		}
	}
	if keyOf[agentA] == keyOf[agentB] {
		t.Errorf("A and B registered under one key %q", keyOf[agentA])
	}
	if evicted != agentA && evicted != agentB {
		t.Errorf("the evicted agent %q is neither A's %q nor B's %q: its re-registration took another row", evicted, agentA, agentB)
	}

	// The dashboard resolves both cards.
	for _, d := range daemons {
		plat.relayResolve(t, d.card)
	}
	waitUntil(t, "every delivered resolve acknowledged", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return plat.deliveries >= 2 && plat.acks == plat.deliveries
	})
	for i, d := range daemons {
		other := daemons[1-i].card
		got := d.ext.applied()
		if len(got) != 1 || got[0] != d.card {
			t.Errorf("daemon for %s applied %v, want only its own card %s (and never %s)", d.root, got, d.card, other)
		}
	}
	plat.mu.Lock()
	deliveries := plat.deliveries
	plat.mu.Unlock()
	if deliveries != 2 {
		t.Errorf("the service delivered %d resolve frames, want 2: one per card, to its owner only", deliveries)
	}
}
