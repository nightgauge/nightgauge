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
