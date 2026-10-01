package platform

import (
	"fmt"
	"regexp"
)

// ExecutionProfile is what a registered workspace would run a turn with —
// the resolved execution adapter, performance mode and default effort, each
// with the layer that produced it (#1567). It rides the agent registration
// and heartbeat bodies so a surface can render "Codex · Frontier · high"
// without re-implementing three precedence chains.
//
// It carries resolved VALUES, never raw config: every field is a short token
// from a closed vocabulary, so no path, credential or free text can reach the
// wire through it. Validate enforces that before a body is marshalled.
//
// Built by internal/executionprofile (this package cannot import the
// resolvers: routing and the adapter registry both import platform).
//
// The field set is pinned against the TypeScript ExecutionProfile
// (packages/nightgauge-vscode/src/services/executionProfile.ts) by
// executionProfileFields.test.ts: add a field here and there together.
type ExecutionProfile struct {
	Adapter               string `json:"adapter"`
	AdapterDisplayName    string `json:"adapter_display_name"`
	AdapterSource         string `json:"adapter_source"`
	PerformanceMode       string `json:"performance_mode"`
	PerformanceModeSource string `json:"performance_mode_source"`
	// Effort is "" when nothing names one and the mode sets no floor: the
	// model's declared default effort applies.
	Effort       string `json:"effort"`
	EffortSource string `json:"effort_source"`
}

// AgentCapabilityConversation is advertised only by a workspace whose selected
// adapter can host a conversational turn (#1567, gated on spike #1568).
const AgentCapabilityConversation = "conversation"

var (
	profileToken       = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	profileEffortToken = regexp.MustCompile(`^[a-z]{0,16}$`)
	profileDisplayName = regexp.MustCompile(`^[A-Za-z0-9 ]{1,40}$`)
	profileSources     = map[string]bool{
		"flag": true, "env": true, "config": true, "file": true, "default": true, "mode": true,
	}
)

// Validate rejects a profile carrying anything other than the short tokens
// the type promises. It is the scope bound the issue requires before the
// profile leaves the machine: a field that grew a path or a secret fails here
// rather than on the platform.
func (p ExecutionProfile) Validate() error {
	checks := []struct {
		field, value string
		ok           bool
	}{
		{"adapter", p.Adapter, profileToken.MatchString(p.Adapter)},
		{"adapter_display_name", p.AdapterDisplayName, profileDisplayName.MatchString(p.AdapterDisplayName)},
		{"adapter_source", p.AdapterSource, profileSources[p.AdapterSource]},
		{"performance_mode", p.PerformanceMode, profileToken.MatchString(p.PerformanceMode)},
		{"performance_mode_source", p.PerformanceModeSource, profileSources[p.PerformanceModeSource]},
		{"effort", p.Effort, profileEffortToken.MatchString(p.Effort)},
		{"effort_source", p.EffortSource, profileSources[p.EffortSource]},
	}
	for _, c := range checks {
		if !c.ok {
			return fmt.Errorf("execution profile: %s %q is outside its vocabulary", c.field, c.value)
		}
	}
	return nil
}
