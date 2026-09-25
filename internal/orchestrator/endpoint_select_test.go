package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// endpoint_select_test.go pins #1679's endpoint-aware OpenCode dispatch:
// spread across endpoints by free slots, a visible wait for a slot,
// failover only between endpoints serving the same model id, and never a
// move to a hosted provider.

const endpointTestModel = "qwen3-coder-30b"

// endpointTestConfig declares one endpoint per id, each serving
// endpointTestModel with one slot, unless models overrides what id serves.
func endpointTestConfig(ids []string, models map[string]string, hosted ...string) config.OpenCodeConfig {
	var cfg config.OpenCodeConfig
	for i, id := range ids {
		model := endpointTestModel
		if m, ok := models[id]; ok {
			model = m
		}
		ep := config.OpenCodeEndpointConfig{
			ID: id, Provider: "openai-compatible",
			BaseURL:        "http://127.0.0.1:" + string(rune('1'+i)) + "000/v1",
			Limit:          config.OpenCodeLimit{Context: 131072, Output: 8192},
			MaxConcurrency: 1,
			Models:         []config.OpenCodeEndpointModel{{ID: model}},
		}
		for _, h := range hosted {
			if h == id {
				no := false
				ep.SelfHosted = &no
			}
		}
		cfg.Endpoints = append(cfg.Endpoints, ep)
	}
	return cfg
}

// fakeEndpointProbe answers readiness per endpoint id: down ids refuse as
// network_unavailable. It records every model it was asked about.
type fakeEndpointProbe struct {
	mu     sync.Mutex
	down   map[string]bool
	probed []string
}

func (f *fakeEndpointProbe) probe(_ string, model string) openCodeReadinessVerdict {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, model)
	id, _, _ := strings.Cut(model, "/")
	if f.down[id] {
		return openCodeReadinessVerdict{Kind: TerminalKindNetworkUnavailable, Reason: "endpoint " + id + " did not answer"}
	}
	return openCodeReadinessVerdict{Ready: true}
}

func withFakeEndpointProbe(t *testing.T, down ...string) *fakeEndpointProbe {
	t.Helper()
	f := &fakeEndpointProbe{down: map[string]bool{}}
	for _, d := range down {
		f.down[d] = true
	}
	prev := openCodeEndpointProbe
	openCodeEndpointProbe = f.probe
	t.Cleanup(func() { openCodeEndpointProbe = prev })
	return f
}

func endpointReq(issue int) endpointRequest {
	return endpointRequest{
		WorktreeDir: "/workspace", Model: "gpu-a/" + endpointTestModel,
		Repo: "nightgauge/nightgauge", Issue: issue, Stage: state.StageFeatureDev,
	}
}

// TestOpenCodeEndpointSpread: two endpoints with one slot each and two
// parallel stages give one stage per endpoint, both asking for gpu-a.
func TestOpenCodeEndpointSpread(t *testing.T) {
	withOpenCodeReadinessConfig(t, endpointTestConfig([]string{"gpu-a", "gpu-b"}, nil))
	withFakeEndpointProbe(t)
	s := &Scheduler{}

	var wg sync.WaitGroup
	leases := make([]endpointLease, 2)
	for i := range leases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, v := s.acquireOpenCodeEndpoint(context.Background(), endpointReq(100+i))
			if !v.Ready {
				t.Errorf("stage %d refused: %+v", i, v)
			}
			leases[i] = lease
		}(i)
	}
	wg.Wait()
	if leases[0].Endpoint == leases[1].Endpoint {
		t.Fatalf("both stages dispatched to %s, want one per endpoint", leases[0].Endpoint)
	}
	for _, l := range leases {
		if l.Model != l.Endpoint+"/"+endpointTestModel {
			t.Errorf("lease on %s dispatches %s, want the same model id on that endpoint", l.Endpoint, l.Model)
		}
	}
	for _, e := range s.EndpointSlots().Endpoints {
		if e.InUse != 1 || e.Slots != 1 {
			t.Errorf("endpoint %s slots = %d/%d in use, want 1/1", e.Endpoint, e.InUse, e.Slots)
		}
	}
}

// TestOpenCodeEndpointSlotWait: two slots and three stages. The third waits,
// listed in the scheduler status, and is dispatched once a slot is released.
func TestOpenCodeEndpointSlotWait(t *testing.T) {
	withOpenCodeReadinessConfig(t, endpointTestConfig([]string{"gpu-a", "gpu-b"}, nil))
	withFakeEndpointProbe(t)
	published := filepath.Join(t.TempDir(), "endpoint-slots.json")
	prevPath := openCodeEndpointSlotsPath
	openCodeEndpointSlotsPath = func() string { return published }
	t.Cleanup(func() { openCodeEndpointSlotsPath = prevPath })
	s := &Scheduler{}

	first, _ := s.acquireOpenCodeEndpoint(context.Background(), endpointReq(1))
	second, _ := s.acquireOpenCodeEndpoint(context.Background(), endpointReq(2))

	got := make(chan endpointLease, 1)
	go func() {
		lease, _ := s.acquireOpenCodeEndpoint(context.Background(), endpointReq(3))
		got <- lease
	}()

	deadline := time.After(5 * time.Second)
	for len(s.EndpointSlots().Waiting) == 0 {
		select {
		case l := <-got:
			t.Fatalf("third stage dispatched to %s with both slots held", l.Endpoint)
		case <-deadline:
			t.Fatal("third stage never appeared as waiting in the scheduler status")
		case <-time.After(5 * time.Millisecond):
		}
	}
	w := s.EndpointSlots().Waiting[0]
	if w.Issue != 3 || w.Stage != string(state.StageFeatureDev) {
		t.Errorf("waiting entry = %+v, want issue 3 at feature-dev", w)
	}
	pub, err := adapters.ReadOpenCodeEndpointSlots(published)
	if err != nil || pub.InUse["gpu-a"] != 1 || pub.InUse["gpu-b"] != 1 || pub.Waiting["gpu-a"] != 1 || pub.PID != os.Getpid() {
		t.Errorf("published ledger = %+v, %v; want both slots in use and one stage waiting", pub, err)
	}

	second.Release()
	select {
	case third := <-got:
		if third.Endpoint != second.Endpoint {
			t.Errorf("third stage got %s, want the slot %s released", third.Endpoint, second.Endpoint)
		}
		third.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("third stage was not dispatched after a slot was released")
	}
	if n := len(s.EndpointSlots().Waiting); n != 0 {
		t.Errorf("%d stage(s) still waiting after dispatch", n)
	}
	first.Release()
	first.Release() // idempotent
	for _, e := range s.EndpointSlots().Endpoints {
		if e.InUse != 0 {
			t.Errorf("endpoint %s has %d slot(s) in use after every release", e.Endpoint, e.InUse)
		}
	}
}

// TestOpenCodeEndpointReadinessMovesToAnotherEndpoint: an endpoint that fails
// readiness at dispatch is skipped for another serving the same model; with
// none healthy the refusal is environmental and names every endpoint tried.
func TestOpenCodeEndpointReadinessMovesToAnotherEndpoint(t *testing.T) {
	withOpenCodeReadinessConfig(t, endpointTestConfig([]string{"gpu-a", "gpu-b"}, nil))
	withFakeEndpointProbe(t, "gpu-a")
	s := &Scheduler{}
	lease, v := s.acquireOpenCodeEndpoint(context.Background(), endpointReq(1))
	if !v.Ready || lease.Endpoint != "gpu-b" {
		t.Fatalf("lease = %+v, verdict = %+v; want gpu-b", lease, v)
	}
	lease.Release()

	withFakeEndpointProbe(t, "gpu-a", "gpu-b")
	_, v = s.acquireOpenCodeEndpoint(context.Background(), endpointReq(2))
	if v.Ready || v.Kind != TerminalKindNetworkUnavailable || !strings.Contains(v.Reason, "endpoints tried: gpu-a, gpu-b") {
		t.Fatalf("verdict = %+v, want network_unavailable naming gpu-a and gpu-b", v)
	}
	if strings.Contains(v.Reason, "127.0.0.1") {
		t.Errorf("refusal %q carries a base_url; it must name endpoint ids only", v.Reason)
	}
}

// failoverStageRunner fails feature-planning on gpu-a as a model server that
// stopped answering does, before any step (no usage), and succeeds on every
// other endpoint.
type failoverStageRunner struct {
	*refusalCapturingStageRunner
	mu     sync.Mutex
	models []string
}

func (r *failoverStageRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	if params.Stage == state.StageFeaturePlanning {
		r.mu.Lock()
		r.models = append(r.models, params.Model)
		r.mu.Unlock()
		if strings.HasPrefix(params.Model, "gpu-a/") {
			r.refusalCapturingStageRunner.mu.Lock()
			r.calls[params.Stage]++
			if r.runtime == nil {
				r.runtime = params.Runtime
			}
			r.refusalCapturingStageRunner.mu.Unlock()
			return &StageRunResult{
				ExitCode:  1,
				ErrorText: `error.error="AI_APICallError: Cannot connect to API: Unable to connect. Is the computer able to access the url?"`,
			}, nil
		}
	}
	return r.refusalCapturingStageRunner.RunStage(ctx, params)
}

// runFailoverPipeline runs a pipeline whose feature-planning is dispatched
// to gpu-a/<model>, through the same retry-engine seam the #1646 readiness
// tests use.
func runFailoverPipeline(t *testing.T, cfg config.OpenCodeConfig, down ...string) (*failoverStageRunner, *state.V2RunRecord, *fakeEndpointProbe) {
	t.Helper()
	stubReconcileGhUnreachable(t)
	withOpenCodeReadinessConfig(t, cfg)
	probe := withFakeEndpointProbe(t, down...)
	prevBackoff := openCodeEndpointFailoverBackoff
	openCodeEndpointFailoverBackoff = time.Millisecond
	t.Cleanup(func() { openCodeEndpointFailoverBackoff = prevBackoff })

	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)
	runner := &failoverStageRunner{refusalCapturingStageRunner: newRefusalCapturingStageRunner()}
	s := newRefusalSchedulerWithAdapter(root, runner, adapters.NewOpenCodeAdapter())
	s.telemetrySvc = &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
		if e.EventType == "stage_completed" && e.Stage == string(state.StageIssuePickup) {
			s.retryEngine.RecordEscalation(string(state.StageFeaturePlanning), "gpu-a/"+endpointTestModel)
		}
	}}
	s.telemetryEnabled = true
	item := types.BoardItem{Number: 1679, Repo: "nightgauge/nightgauge", ID: "item-1679"}
	s.runPipeline(context.Background(), item)
	return runner, recordForIssue(t, root, item.Number), probe
}

// TestOpenCodeEndpointFailoverSameModelOnly: the primary endpoint refuses
// connections before any step, the retry lands on the second endpoint
// serving the same model id, and history records endpoint_failover. With
// the second endpoint serving a different model there is no retry: the stage
// fails as an environment failure.
func TestOpenCodeEndpointFailoverSameModelOnly(t *testing.T) {
	t.Run("same model", func(t *testing.T) {
		runner, rec, _ := runFailoverPipeline(t, endpointTestConfig([]string{"gpu-a", "gpu-b"}, nil))
		want := []string{"gpu-a/" + endpointTestModel, "gpu-b/" + endpointTestModel}
		if strings.Join(runner.models, ",") != strings.Join(want, ",") {
			t.Fatalf("feature-planning dispatched %v, want %v", runner.models, want)
		}
		detail, ok := rec.Stages[string(state.StageFeaturePlanning)]
		if !ok || len(detail.EndpointFailover) != 1 || detail.EndpointFailover[0] != [2]string{"gpu-a", "gpu-b"} {
			t.Fatalf("feature-planning endpoint_failover = %v, want [[gpu-a gpu-b]]", detail.EndpointFailover)
		}
	})
	t.Run("different model", func(t *testing.T) {
		runner, rec, _ := runFailoverPipeline(t, endpointTestConfig([]string{"gpu-a", "gpu-b"}, map[string]string{"gpu-b": "llama-70b"}))
		if len(runner.models) != 1 {
			t.Fatalf("feature-planning dispatched %v, want one attempt and no failover to another model", runner.models)
		}
		if rec.TerminalFailureKind != TerminalKindNetworkUnavailable {
			t.Errorf("terminal kind = %q, want %q", rec.TerminalFailureKind, TerminalKindNetworkUnavailable)
		}
		if d := rec.Stages[string(state.StageFeaturePlanning)]; len(d.EndpointFailover) != 0 {
			t.Errorf("endpoint_failover = %v, want none", d.EndpointFailover)
		}
	})
}

// TestOpenCodeLocalNeverFailsOverToCloud: a local-only config whose every
// local endpoint is down never dispatches, and never even probes, an
// endpoint that forwards to a hosted service, even one declaring the model.
func TestOpenCodeLocalNeverFailsOverToCloud(t *testing.T) {
	cfg := endpointTestConfig([]string{"gpu-a", "gpu-b", "gateway"}, nil, "gateway")
	runner, rec, probe := runFailoverPipeline(t, cfg, "gpu-a", "gpu-b")
	if len(runner.models) != 0 {
		t.Fatalf("feature-planning dispatched %v with every local endpoint down, want no dispatch", runner.models)
	}
	for _, m := range probe.probed {
		if strings.HasPrefix(m, "gateway/") {
			t.Fatalf("probed %s: a hosted endpoint is never a failover target", m)
		}
	}
	if rec.TerminalFailureKind != TerminalKindNetworkUnavailable {
		t.Errorf("terminal kind = %q, want %q", rec.TerminalFailureKind, TerminalKindNetworkUnavailable)
	}
	cands, _ := openCodeEndpointCandidates(mustEndpoints(t, cfg), "gpu-a/"+endpointTestModel)
	for _, c := range cands {
		if c.ID == "gateway" {
			t.Error("the hosted gateway is a candidate for a local model")
		}
	}
}

// TestOpenCodeEndpointFailoverBounded: failover needs another endpoint, an
// endpoint-loss kind and no finished step, and stops at len(endpoints).
func TestOpenCodeEndpointFailoverBounded(t *testing.T) {
	lease := endpointLease{Endpoint: "gpu-a", Candidates: 3}
	cases := []struct {
		name      string
		lease     endpointLease
		kind      string
		noStep    bool
		failovers int
		want      bool
	}{
		{"endpoint lost before a step", lease, TerminalKindNetworkUnavailable, true, 0, true},
		{"model unloaded", lease, TerminalKindModelUnavailable, true, 1, true},
		{"bounded by the endpoints serving it", lease, TerminalKindNetworkUnavailable, true, 2, false},
		{"a step already finished", lease, TerminalKindNetworkUnavailable, false, 0, false},
		{"the stage's own failure", lease, TerminalKindSubagentCrash, true, 0, false},
		{"one endpoint only", endpointLease{Endpoint: "gpu-a", Candidates: 1}, TerminalKindNetworkUnavailable, true, 0, false},
		{"no endpoint lease", endpointLease{}, TerminalKindNetworkUnavailable, true, 0, false},
	}
	for _, tc := range cases {
		if got := endpointFailoverDecision(tc.lease, tc.kind, tc.noStep, tc.failovers); got != tc.want {
			t.Errorf("%s: failover = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func mustEndpoints(t *testing.T, cfg config.OpenCodeConfig) []adapters.OpenCodeEndpoint {
	t.Helper()
	eps, err := adapters.OpenCodeEndpoints(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return eps
}
