package routing

import "testing"

// TestDashboardPerformanceMode pins the Go→dashboard perf-mode mapping. The
// three named modes pass through verbatim; the premium 'frontier' tier (and any
// unknown value) maps to "" so the emit site omits `mode` rather than sending a
// value the dashboard's PerformanceMode enum ('efficiency'|'elevated'|'maximum')
// can't render. If the dashboard enum changes, update both sides together.
func TestDashboardPerformanceMode(t *testing.T) {
	cases := []struct {
		in   PerformanceMode
		want string
	}{
		{ModeEfficiency, "efficiency"},
		{ModeElevated, "elevated"},
		{ModeMaximum, "maximum"},
		// 'frontier' has no dashboard representation — omit it.
		{ModeFrontier, ""},
		// Defensive: an unrecognised value is also not representable.
		{PerformanceMode("garbage"), ""},
		{PerformanceMode(""), ""},
	}
	for _, c := range cases {
		if got := DashboardPerformanceMode(c.in); got != c.want {
			t.Errorf("DashboardPerformanceMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestModeEnvelopeThinkingPolicyColumnIsEmpty pins the #606 half of spike
// #568 §4.1.3: the mode envelopes carry a thinking-policy axis with Go⇄TS
// parity (ModeEnvelope.ThinkingPolicy ⇄ modeProfiles.ts `thinkingPolicy`),
// and NO mode declares a value yet — adding the axis is behavior-preserving
// by construction. The TS twin assertion lives in
// packages/nightgauge-vscode/tests/utils/modeProfiles.test.ts; a mode that
// gains a policy must update both tables and both pins in one commit, like
// every other mode-profile mirror.
func TestModeEnvelopeThinkingPolicyColumnIsEmpty(t *testing.T) {
	for _, mode := range []PerformanceMode{ModeEfficiency, ModeElevated, ModeMaximum, ModeFrontier} {
		if got := Envelope(mode).ThinkingPolicy; got != "" {
			t.Fatalf("mode %s declares thinking policy %q — update the TS table and BOTH parity pins together", mode, got)
		}
	}
}
