package orchestrator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/models"
)

// Dispatch-time OpenCode readiness (#1646, ADR-022 § Endpoints).
//
// Slow local model servers are not stalls, but a server that is down or a
// model that is not loaded is not a stage worth spawning: opencode would
// start, wait out its own request timeout against a closed port or an
// unloaded model, and land as a generic subagent_crash — feeding the cascade
// breaker with a condition that has nothing to do with the run's code. This
// probes the SPECIFIC endpoint the stage is about to dispatch to, once, before
// PreDispatch creates anything, and refuses with the kind the rest of the
// pipeline already treats as environmental (model_unavailable,
// network_unavailable) rather than a generic failure.
//
// A hosted OpenCode model (anthropic/, openai/, ...) and a model with no
// matching declared endpoint are untouched: nil settings load errors, a
// model that does not parse as "<endpoint-id>/<model>", or a key that names
// no endpoint all return ready=true so PreDispatch's own checks are the only
// ones that can refuse them.

// openCodeReadinessHTTPClient lets a test point the probe at an httptest
// server; nil uses adapters.ProbeOpenCodeEndpoint's own loopback-only,
// 2-second client. Never set outside a test.
var openCodeReadinessHTTPClient *http.Client

// openCodeReadinessLoadSettings lets a test replace how the machine-tier
// `opencode:` block is read; nil uses config.LoadOpenCodeConfig. Never set
// outside a test.
var openCodeReadinessLoadSettings func(worktreeDir string) (config.OpenCodeConfig, error)

// openCodeReadinessVerdict is one endpoint's dispatch-time readiness result:
// ready, or refused with the terminal kind and reason a pre-dispatch refusal
// books.
type openCodeReadinessVerdict struct {
	Ready  bool
	Kind   string
	Reason string
}

// resolveOpenCodeReadiness answers whether an OpenCode stage dispatching to
// model (the -m value: "<endpoint-id>/<model-id>") may spawn. It reads the
// machine-tier opencode: block for worktreeDir, matches model's endpoint key
// against the endpoint OpenCodeEndpoints declares (the legacy flat keys or an
// opencode.endpoints[] entry), and probes only that one endpoint — the
// per-endpoint verdict the 2026-09-12 multiple-local-endpoints amendment
// requires, never "some local server somewhere".
//
// Refused as network_unavailable when the endpoint does not answer, and
// model_unavailable when it answers but the model is not on it, not loaded,
// or the endpoint's configured opencode.limit.context is 0 (OpenCode never
// learns the loaded window, so it never compacts) or larger than the context
// the server has it loaded with.
func resolveOpenCodeReadiness(worktreeDir, model string) openCodeReadinessVerdict {
	key, bareID, qualified := strings.Cut(strings.TrimSpace(model), "/")
	if !qualified || key == "" || bareID == "" {
		return openCodeReadinessVerdict{Ready: true}
	}

	loadSettings := openCodeReadinessLoadSettings
	if loadSettings == nil {
		loadSettings = config.LoadOpenCodeConfig
	}
	settings, err := loadSettings(worktreeDir)
	if err != nil {
		// A malformed machine-tier config is PreDispatch's refusal to make,
		// not readiness's — it happens for every stage on this machine, not
		// just an OpenCode one dispatching to a down server.
		return openCodeReadinessVerdict{Ready: true}
	}
	endpoints, err := adapters.OpenCodeEndpoints(settings)
	if err != nil {
		return openCodeReadinessVerdict{Ready: true}
	}
	var target *adapters.OpenCodeEndpoint
	for i := range endpoints {
		if endpoints[i].ID == key {
			target = &endpoints[i]
			break
		}
	}
	if target == nil {
		// Not a declared local endpoint: a hosted OpenCode provider (or a
		// model this machine's config does not describe at all), neither of
		// which this dispatch-time server/model-loaded check applies to.
		return openCodeReadinessVerdict{Ready: true}
	}

	if target.Provider == "ollama" && models.IsOllamaCloudModel(bareID) {
		// Ollama serves a cloud-tagged model from its hosted service through
		// the same local API (ADR-022 § 3 and § Endpoints, #1679): the
		// endpoint would forward the stage's code to a hosted API and the
		// stage would be recorded as a local $0. Refused before spawn.
		return openCodeReadinessVerdict{
			Kind: TerminalKindModelUnavailable,
			Reason: fmt.Sprintf(
				"model %s on endpoint %s is an Ollama cloud model, served from Ollama's hosted service rather than the endpoint: dispatch it as ollama-cloud/%s, the hosted provider, instead",
				bareID, target.ID, bareID),
		}
	}

	result := adapters.ProbeOpenCodeEndpoint(openCodeReadinessHTTPClient, adapters.OpenCodeEndpointTarget{
		ID:        target.ID,
		Kind:      target.Provider,
		BaseURL:   target.BaseURL,
		APIKeyEnv: target.APIKeyEnv,
		Legacy:    target.Legacy,
	}, bareID, target.Limit.Context)

	if !result.Reachable {
		return openCodeReadinessVerdict{
			Kind:   TerminalKindNetworkUnavailable,
			Reason: result.Problem,
		}
	}
	if result.Problem != "" {
		return openCodeReadinessVerdict{
			Kind:   TerminalKindModelUnavailable,
			Reason: result.Problem,
		}
	}
	if target.Limit.Context <= 0 {
		return openCodeReadinessVerdict{
			Kind: TerminalKindModelUnavailable,
			Reason: fmt.Sprintf(
				"opencode.limit.context is not set for endpoint %s: without it OpenCode never learns %s's loaded window and never compacts, so a session can overflow the server instead — set opencode.limit.context at or below the context %s has loaded %s with",
				target.ID, target.ID, target.ID, bareID),
		}
	}
	if result.LoadedContext > 0 && target.Limit.Context > result.LoadedContext {
		return openCodeReadinessVerdict{
			Kind: TerminalKindModelUnavailable,
			Reason: fmt.Sprintf(
				"opencode.limit.context (%d) for endpoint %s is larger than the %d tokens it has %s loaded with: set opencode.limit.context at or below %d, or load the model with a larger context",
				target.Limit.Context, target.ID, result.LoadedContext, bareID, result.LoadedContext),
		}
	}
	return openCodeReadinessVerdict{Ready: true}
}

// openCodeDispatchWindow is the context window an OpenCode dispatch of model
// runs with when model names an endpoint the machine-tier opencode: block
// declares: adapters.OpenCodeContextWindow, the limit.context the run's
// config is built with (the declared value, clamped to the loaded window,
// else the window discovered from the server). 0 for a hosted model, and
// when the config or the limit does not resolve — the fail-open "unknown
// window" every consumer already handles.
func openCodeDispatchWindow(ctx context.Context, worktreeDir, model string) int {
	loadSettings := openCodeReadinessLoadSettings
	if loadSettings == nil {
		loadSettings = config.LoadOpenCodeConfig
	}
	settings, err := loadSettings(worktreeDir)
	if err != nil {
		return 0
	}
	window, err := adapters.OpenCodeContextWindow(ctx, settings, model)
	if err != nil {
		log.Printf("opencode: the context window of %s did not resolve, so it is treated as unknown: %v", model, err)
		return 0
	}
	return window
}

// openCodeConfiguredModel is the machine-tier opencode.model a stage on the
// opencode adapter dispatches when its own model names no provider (a tier,
// or a bare registry id), and "" when the model already names one, or
// opencode.model is unset or does not load: the adapter's model check then
// refuses the stage with its own reason.
func openCodeConfiguredModel(worktreeDir, model string) string {
	if strings.Contains(model, "/") {
		return ""
	}
	loadSettings := openCodeReadinessLoadSettings
	if loadSettings == nil {
		loadSettings = config.LoadOpenCodeConfig
	}
	settings, err := loadSettings(worktreeDir)
	if err != nil {
		return ""
	}
	configured := strings.TrimSpace(settings.Model)
	if _, err := adapters.OpenCodeModelArg(configured); err != nil {
		return ""
	}
	return configured
}
