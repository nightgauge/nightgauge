package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// nextContextBudgetReroute is the one re-route hop ADR 023 §3 allows a stage
// that fails its context-budget fit check: the largest-window, non-deprecated
// model on the SAME provider that is not the model already rejected.
//
// Provider-scoped rather than adapter-scoped on purpose: the fit check runs
// after the model is already resolved to a provider (skillData.Provider, the
// exact descriptor OverlayKeys resolved), and re-routing within a provider
// needs no new auth, no new adapter check, and no per-adapter pricing
// surprise — only a bigger window on a model the dispatch can already reach.
// Cross-provider re-routing (e.g. opencode's local model to a hosted one) is
// exactly the kind of decision ADR 023 leaves to an operator or a later
// issue, not a silent one-hop reroute.
//
// Returns ok=false when no larger-window alternative exists (currentID is
// already the provider's biggest window, the provider has one model, or the
// provider is a local one with no registry catalog to search) — the caller
// refuses rather than re-rendering a model no better than the one that just
// failed.
func nextContextBudgetReroute(provider, currentID string, currentWindow int) (models.ModelDescriptor, bool) {
	best := models.ModelDescriptor{}
	found := false
	for _, m := range models.All() {
		if m.Provider != provider || m.Deprecated || m.ID == currentID {
			continue
		}
		if m.ContextWindow <= currentWindow {
			continue
		}
		if !found || m.ContextWindow > best.ContextWindow {
			best = m
			found = true
		}
	}
	return best, found
}

// renderProfileName is the profile a render was composed from, for logs and
// the stage-start trace: "compact" or "full".
func renderProfileName(r *skillrender.Result) string {
	if r != nil && r.Profile == skillrender.ProfileCompact {
		return skillrender.ProfileCompact
	}
	return "full"
}

// isLocalDispatch reports whether model, dispatched on adapter, runs on a
// self-hosted/local endpoint (#2186): a declared opencode endpoint that is
// local, or an opencode provider key that is itself local (lm-studio,
// ollama, excluding Ollama cloud models). The render-profile choice prefers
// compact for such a dispatch. Same locality rule as the stage timeout.
func isLocalDispatch(adapter, model, workspaceRoot string) bool {
	if adapter != "opencode" {
		return false
	}
	return execution.OpenCodeDeclaredEndpointLocal(model, workspaceRoot) || models.IsLocalModel(adapter, model, nil)
}

// profileReasonSuffix renders a non-empty profile reason for the
// context-budget log line: ", reason local_endpoint".
func profileReasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ", reason " + reason
}
