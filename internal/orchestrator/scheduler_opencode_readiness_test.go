package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// scheduler_opencode_readiness_test.go pins #1646's dispatch-time OpenCode
// readiness gate: a local model server that is down, or a model it has not
// loaded, refuses the stage BEFORE anything spawns — network_unavailable and
// model_unavailable respectively, never a generic subagent_crash.

// openCodeReadinessLMStudioModel is one entry of LM Studio's
// GET /api/v0/models, the shape probeLMStudio (opencode_preflight.go) reads.
type openCodeReadinessLMStudioModel struct {
	ID                  string `json:"id"`
	State               string `json:"state"`
	LoadedContextLength int    `json:"loaded_context_length"`
}

// newLMStudioStub serves GET /api/v0/models with models, and fails the test
// on any other path.
func newLMStudioStub(t *testing.T, models []openCodeReadinessLMStudioModel) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/models" {
			t.Fatalf("unexpected readiness probe path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
	}))
}

// withOpenCodeReadinessConfig points resolveOpenCodeReadiness's settings load
// at cfg for the duration of the test, restoring the real loader after.
func withOpenCodeReadinessConfig(t *testing.T, cfg config.OpenCodeConfig) {
	t.Helper()
	prev := openCodeReadinessLoadSettings
	openCodeReadinessLoadSettings = func(string) (config.OpenCodeConfig, error) { return cfg, nil }
	t.Cleanup(func() { openCodeReadinessLoadSettings = prev })
}

const openCodeReadinessModel = "lmstudio/qwen/qwen3.8-27b"

// ─── resolveOpenCodeReadiness unit coverage ──────────────────────────────

func TestResolveOpenCodeReadiness_ServerDownIsNetworkUnavailable(t *testing.T) {
	server := newLMStudioStub(t, nil)
	server.Close() // closed before any request — connection refused

	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		Limit: config.OpenCodeLimit{Context: 8192},
	})

	got := resolveOpenCodeReadiness("/workspace", openCodeReadinessModel)
	if got.Ready {
		t.Fatal("expected a refusal for a closed server")
	}
	if got.Kind != TerminalKindNetworkUnavailable {
		t.Errorf("kind = %q, want %q", got.Kind, TerminalKindNetworkUnavailable)
	}
	if got.Reason == "" {
		t.Error("expected a non-empty reason naming the fix")
	}
}

func TestResolveOpenCodeReadiness_ModelNotLoadedIsModelUnavailable(t *testing.T) {
	server := newLMStudioStub(t, []openCodeReadinessLMStudioModel{
		{ID: "qwen/qwen3.8-27b", State: "not-loaded"},
	})
	t.Cleanup(server.Close)

	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		Limit: config.OpenCodeLimit{Context: 8192},
	})

	got := resolveOpenCodeReadiness("/workspace", openCodeReadinessModel)
	if got.Ready {
		t.Fatal("expected a refusal for a not-loaded model")
	}
	if got.Kind != TerminalKindModelUnavailable {
		t.Errorf("kind = %q, want %q", got.Kind, TerminalKindModelUnavailable)
	}
	if !strings.Contains(got.Reason, "lms load") {
		t.Errorf("reason = %q, want it to name the fix (`lms load ...`)", got.Reason)
	}
}

func TestResolveOpenCodeReadiness_InjectedContextZeroIsModelUnavailable(t *testing.T) {
	server := newLMStudioStub(t, []openCodeReadinessLMStudioModel{
		{ID: "qwen/qwen3.8-27b", State: "loaded", LoadedContextLength: 32768},
	})
	t.Cleanup(server.Close)

	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		// Limit.Context intentionally unset (0): the machine-tier config
		// never told OpenCode a window, so it would never compact.
	})

	got := resolveOpenCodeReadiness("/workspace", openCodeReadinessModel)
	if got.Ready {
		t.Fatal("expected a refusal for an unconfigured (0) limit.context")
	}
	if got.Kind != TerminalKindModelUnavailable {
		t.Errorf("kind = %q, want %q", got.Kind, TerminalKindModelUnavailable)
	}
	if !strings.Contains(got.Reason, "limit.context") {
		t.Errorf("reason = %q, want it to name opencode.limit.context", got.Reason)
	}
}

func TestResolveOpenCodeReadiness_InjectedContextLargerThanLoadedIsModelUnavailable(t *testing.T) {
	server := newLMStudioStub(t, []openCodeReadinessLMStudioModel{
		{ID: "qwen/qwen3.8-27b", State: "loaded", LoadedContextLength: 8192},
	})
	t.Cleanup(server.Close)

	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		Limit: config.OpenCodeLimit{Context: 32768}, // larger than the 8192 loaded
	})

	got := resolveOpenCodeReadiness("/workspace", openCodeReadinessModel)
	if got.Ready {
		t.Fatal("expected a refusal when the configured limit exceeds the loaded window")
	}
	if got.Kind != TerminalKindModelUnavailable {
		t.Errorf("kind = %q, want %q", got.Kind, TerminalKindModelUnavailable)
	}
}

func TestResolveOpenCodeReadiness_ReadyWhenLoadedAndFits(t *testing.T) {
	server := newLMStudioStub(t, []openCodeReadinessLMStudioModel{
		{ID: "qwen/qwen3.8-27b", State: "loaded", LoadedContextLength: 32768},
	})
	t.Cleanup(server.Close)

	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		Limit: config.OpenCodeLimit{Context: 8192},
	})

	got := resolveOpenCodeReadiness("/workspace", openCodeReadinessModel)
	if !got.Ready {
		t.Fatalf("expected readiness, got refusal: kind=%q reason=%q", got.Kind, got.Reason)
	}
}

// TestResolveOpenCodeReadiness_HostedModelIsUntouched proves a hosted
// OpenCode model (no declared local endpoint matches its key) is never
// probed: the machine-tier config below declares only "lmstudio", so
// "anthropic/claude-opus-4-8" matches no endpoint and is left to PreDispatch's
// own checks.
func TestResolveOpenCodeReadiness_HostedModelIsUntouched(t *testing.T) {
	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: "http://127.0.0.1:0", // never dialed
		Limit: config.OpenCodeLimit{Context: 8192},
	})
	got := resolveOpenCodeReadiness("/workspace", "anthropic/claude-opus-4-8")
	if !got.Ready {
		t.Fatalf("expected a hosted model to be untouched, got refusal: kind=%q reason=%q", got.Kind, got.Reason)
	}
}

// newOpenAICompatibleStub serves GET /models with models (the generic shape
// probeOpenAICompatible reads — every declared opencode.endpoints[] entry
// gets this probe regardless of its Provider label, 2026-09-20 scope
// narrowing), and fails the test on any other path.
func newOpenAICompatibleStub(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	type entry struct {
		ID string `json:"id"`
	}
	data := make([]entry, len(ids))
	for i, id := range ids {
		data[i] = entry{ID: id}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected readiness probe path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

// TestOpenCodeReadinessPerEndpoint is the 2026-09-12 multiple-local-endpoints
// amendment: readiness is evaluated for the SPECIFIC endpoint a stage is
// about to dispatch to, never "some local server somewhere" — endpoint A down
// and endpoint B up on the same machine-tier config must produce different
// verdicts.
func TestOpenCodeReadinessPerEndpoint(t *testing.T) {
	down := newOpenAICompatibleStub(t, "qwen3-coder:30b")
	down.Close()
	up := newOpenAICompatibleStub(t, "qwen3-coder:30b")
	t.Cleanup(up.Close)

	cfg := config.OpenCodeConfig{
		Endpoints: []config.OpenCodeEndpointConfig{
			{ID: "lmstudio-a", Provider: "openai-compatible", BaseURL: down.URL, Limit: config.OpenCodeLimit{Context: 8192, Output: 4096}},
			{ID: "lmstudio-b", Provider: "openai-compatible", BaseURL: up.URL, Limit: config.OpenCodeLimit{Context: 8192, Output: 4096}},
		},
	}
	withOpenCodeReadinessConfig(t, cfg)

	a := resolveOpenCodeReadiness("/workspace", "lmstudio-a/qwen3-coder:30b")
	if a.Ready || a.Kind != TerminalKindNetworkUnavailable {
		t.Errorf("endpoint A (down) = ready=%v kind=%q, want a network_unavailable refusal", a.Ready, a.Kind)
	}

	b := resolveOpenCodeReadiness("/workspace", "lmstudio-b/qwen3-coder:30b")
	if !b.Ready {
		t.Errorf("endpoint B (up, model listed, context configured) = refused (kind=%q reason=%q), want ready", b.Kind, b.Reason)
	}
}

// TestOpenCodeReadinessRefusesOllamaCloudModel: a cloud-tagged model on an
// ollama endpoint is served by Ollama's hosted service, so it is refused
// before spawn with the ollama-cloud remediation, and the endpoint is never
// probed (#1679).
func TestOpenCodeReadinessRefusesOllamaCloudModel(t *testing.T) {
	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Endpoints: []config.OpenCodeEndpointConfig{
			{ID: "gpu-box", Provider: "ollama", BaseURL: "http://127.0.0.1:1/v1", Limit: config.OpenCodeLimit{Context: 8192, Output: 4096}},
		},
	})
	v := resolveOpenCodeReadiness("/workspace", "gpu-box/gpt-oss:120b-cloud")
	if v.Ready || v.Kind != TerminalKindModelUnavailable || !strings.Contains(v.Reason, "ollama-cloud/gpt-oss:120b-cloud") {
		t.Errorf("verdict = %+v, want a model_unavailable refusal naming ollama-cloud/gpt-oss:120b-cloud", v)
	}
}

// ─── scheduler-level wiring: refuses before spawn, books the refusal like
// other pre-dispatch refusals, never dispatches ──────────────────────────

// TestOpenCodeReadinessRefusal_NeverDispatches proves the scheduler-level
// wiring: a closed local server refuses the FIRST stage before the fake
// runner is ever called, with the network_unavailable kind and a booked
// stage error — not the generic subagent_crash a spawned-and-then-failed
// opencode process would have landed as.
func TestOpenCodeReadinessRefusal_NeverDispatches(t *testing.T) {
	server := newLMStudioStub(t, nil)
	server.Close()
	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		Limit: config.OpenCodeLimit{Context: 8192},
	})

	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)

	runner := newRefusalCapturingStageRunner()
	s := newRefusalSchedulerWithAdapter(root, runner, adapters.NewOpenCodeAdapter())
	// Force feature-planning's dispatch model to the local endpoint without
	// going through the config/env band-only validation path
	// (validStageModel/stageEnvModel only accept registry tier bands) —
	// RecordEscalation is the one unfiltered model-override seam, the same
	// one an escalated retry uses in production. It has to be set from a
	// hook that fires AFTER runPipeline's own s.retryEngine.Reset() (issue-
	// pickup's stage_completed event, the earliest such seam), not before
	// runPipeline is called — Reset() would otherwise wipe it before
	// feature-planning ever resolves its model.
	s.telemetrySvc = &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
		if e.EventType == "stage_completed" && e.Stage == string(state.StageIssuePickup) {
			s.retryEngine.RecordEscalation(string(state.StageFeaturePlanning), openCodeReadinessModel)
		}
	}}
	s.telemetryEnabled = true

	item := types.BoardItem{Number: 1646, Repo: "nightgauge/nightgauge", ID: "item-1646"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageIssuePickup); got != 1 {
		t.Fatalf("issue-pickup ran %d time(s), want 1", got)
	}
	if got := runner.count(state.StageFeaturePlanning); got != 0 {
		t.Fatalf("feature-planning was dispatched %d time(s) — readiness must refuse BEFORE any spawn", got)
	}
	if runner.runtime == nil {
		t.Fatal("stage runner never captured a *state.RuntimeState — BeginStage must still run on refusal")
	}
	snap := runner.runtime.Snapshot()
	if snap.Stage != state.StageFeaturePlanning {
		t.Fatalf("snap.Stage = %q, want %q (the refused stage)", snap.Stage, state.StageFeaturePlanning)
	}
	gotReason, ok := snap.StageErrors[string(snap.Stage)]
	if !ok || gotReason == "" {
		t.Fatalf("snap.StageErrors[%q] = (%q, ok=%v), want the readiness refusal reason", snap.Stage, gotReason, ok)
	}

	rec := recordForIssue(t, root, item.Number)
	if rec.TerminalFailureKind != TerminalKindNetworkUnavailable {
		t.Errorf("rec.TerminalFailureKind = %q, want %q — refusePreDispatch's own default is validation_error, "+
			"so this pins that the readiness call site overrides it to the precise environmental kind",
			rec.TerminalFailureKind, TerminalKindNetworkUnavailable)
	}
	if rec.TerminalFailureKind == TerminalKindSubagentCrash {
		t.Error("a pre-spawn readiness refusal must never book subagent_crash")
	}
}

// TestOpenCodeReadinessRefusal_ModelUnavailable_RescuesUncommittedWork mirrors
// the #3542 rescue other pre-dispatch refusals get (refusePreDispatch):
// readiness is checked on every stage, so a run reaching feature-validate
// with feature-dev's uncommitted implementation still on disk (AGENTS.md
// #1608 — feature-dev does not commit) must recover that work into a commit
// even though the refusal that stops the run is model_unavailable, not
// validation_error.
func TestOpenCodeReadinessRefusal_ModelUnavailable_RescuesUncommittedWork(t *testing.T) {
	server := newLMStudioStub(t, []openCodeReadinessLMStudioModel{
		{ID: "qwen/qwen3.8-27b", State: "not-loaded"},
	})
	t.Cleanup(server.Close)
	withOpenCodeReadinessConfig(t, config.OpenCodeConfig{
		Provider: "lm-studio", BaseURL: server.URL,
		Limit: config.OpenCodeLimit{Context: 8192},
	})

	root := t.TempDir()
	seedRefusalRepo(t, root, allRefusalStageSkills)
	workFile := seedUncommittedDevWork(t, root)
	commitsBefore := len(gitLog(t, root))

	runner := newRefusalCapturingStageRunner()
	s := newRefusalSchedulerWithAdapter(root, runner, adapters.NewOpenCodeAdapter())
	// Set the escalation from a post-Reset() seam (see the sibling test
	// above for why it cannot be set before runPipeline is called).
	// feature-dev's own dev-context handoff is left alone, so the run
	// reaches feature-validate's dispatch — not the earlier prerequisite
	// gate — with feature-dev's implementation still uncommitted.
	s.telemetrySvc = &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
		if e.EventType == "stage_completed" && e.Stage == string(state.StageIssuePickup) {
			s.retryEngine.RecordEscalation(string(state.StageFeatureValidate), openCodeReadinessModel)
		}
	}}
	s.telemetryEnabled = true

	item := types.BoardItem{Number: 1646, Repo: "nightgauge/nightgauge", ID: "item-1646"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageFeatureValidate); got != 0 {
		t.Fatalf("feature-validate was dispatched %d time(s) — readiness refuses BEFORE dispatch", got)
	}
	if got := len(gitLog(t, root)); got != commitsBefore+1 {
		t.Fatalf("commit count %d -> %d, want exactly one recovery commit", commitsBefore, got)
	}
	if !refusalTrackedAtHEAD(t, root, workFile) {
		t.Errorf("%s is not in the HEAD tree — the rescue committed something, but not the work", workFile)
	}

	rec := recordForIssue(t, root, item.Number)
	// The FIRST cause is model_unavailable, never worktree_uncommitted or
	// validation_error (#875's rule: the rescue is not the cause).
	if rec.TerminalFailureKind != TerminalKindModelUnavailable {
		t.Errorf("rec.TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, TerminalKindModelUnavailable)
	}
}
