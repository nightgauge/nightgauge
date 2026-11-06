package skillrender

import (
	"strings"
	"testing"
)

// #2989: pr-create never runs the full local gate. feature-validate's gate
// satisfies the once-before-push rule; the base merge is pushed straight away.
func TestPrCreateRender_NeverRunsFullGate(t *testing.T) {
	got := defaultRender(t, "pr-create")
	for _, want := range []string{
		"Never run the full local gate (#2989)",
		"push the merge straight away",
	} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("pr-create render lacks %q", want)
		}
	}
	for _, line := range strings.Split(got.Content, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") || strings.HasPrefix(code, "-") || strings.HasPrefix(code, "`") {
			continue
		}
		if gateRun.MatchString(code) {
			t.Errorf("pr-create render starts the full gate: %q", line)
		}
	}
}
