// Package executionprofile resolves the execution profile a workspace
// advertises on agent registration and heartbeat (#1567): the adapter, the
// performance mode and the default effort, each with its provenance.
//
// Nothing here resolves anything itself. Each value comes from the resolver
// the pipeline already dispatches with — the adapter registry's precedence
// chain, ResolvePerformanceMode, and the default-effort layers of the stage
// effort chain clamped by ClampEffortToEnvelope — so the advertised profile
// and what a run actually uses cannot drift apart.
package executionprofile

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// EffortSourceMode marks an effort the performance mode's envelope set or
// clamped: the configured value (or its absence) is not what runs.
const EffortSourceMode = "mode"

// EffortSourceDefault marks an effort nothing named: "" and the model's
// declared default applies.
const EffortSourceDefault = "default"

// Resolve returns the workspace's execution profile, validated. An error
// means no profile should be advertised: an unreadable config or an unknown
// adapter is not something to guess about on the wire.
func Resolve(workspaceRoot string) (platform.ExecutionProfile, error) {
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		return platform.ExecutionProfile{}, fmt.Errorf("execution profile: load config: %w", err)
	}

	configAdapter := ""
	if cfg.UI != nil && cfg.UI.Core != nil {
		configAdapter = strings.TrimSpace(cfg.UI.Core.Adapter)
	}
	adapter, adapterSource, err := adapters.NewRegistry().ResolveName("", configAdapter)
	if err != nil {
		return platform.ExecutionProfile{}, fmt.Errorf("execution profile: %w", err)
	}

	mode, modeSource := routing.ResolvePerformanceModeWithSource(workspaceRoot)

	raw, effortSource := config.ResolveDefaultEffort(cfg, os.Getenv)
	effort := routing.ClampEffortToEnvelope(raw, routing.Envelope(mode))
	switch {
	case effort != raw:
		effortSource = EffortSourceMode
	case effortSource == config.DefaultEffortSourceNone:
		effortSource = EffortSourceDefault
	}

	p := platform.ExecutionProfile{
		Adapter:               adapter,
		AdapterDisplayName:    adapters.DisplayName(adapter),
		AdapterSource:         adapterSource,
		PerformanceMode:       string(mode),
		PerformanceModeSource: modeSource,
		Effort:                effort,
		EffortSource:          effortSource,
	}
	if err := p.Validate(); err != nil {
		return platform.ExecutionProfile{}, err
	}
	return p, nil
}

// conversationViableAdapters is the set of adapters that passed the
// conversation spike's viability bar (nightgauge#1568). It is EMPTY on
// purpose: the spike has not run, so no workspace can host a turn yet and
// none advertises the capability. The spike's result fills this in; a
// workspace must never claim a capability nothing has demonstrated.
var conversationViableAdapters = map[string]bool{}

// ConversationViable reports whether a workspace on this adapter can host a
// conversational turn, and so may advertise the `conversation` capability.
func ConversationViable(adapter string) bool {
	return conversationViableAdapters[adapter]
}

// Capabilities appends the conversation capability to base when the
// profile's adapter is viable. base is not modified.
func Capabilities(base []string, p platform.ExecutionProfile) []string {
	out := append([]string(nil), base...)
	if ConversationViable(p.Adapter) {
		out = append(out, platform.AgentCapabilityConversation)
	}
	return out
}
