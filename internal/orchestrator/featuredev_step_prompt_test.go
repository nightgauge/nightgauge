package orchestrator

import (
	"strings"
	"testing"
)

// TestComposeFeatureDevStepPromptSkipsRepeatedPhases: a step after the first
// is told the setup phases already passed, and a step before the last is told
// to stop after implementation and testing. Step 1 and the last step keep
// the phases they own.
func TestComposeFeatureDevStepPromptSkipsRepeatedPhases(t *testing.T) {
	const skipSetup = "Do not repeat them: start at implementation"
	const stopEarly = "Stop after this step's implementation and testing"
	cases := []struct {
		k, total           int
		last               bool
		wantSkip, wantStop bool
	}{
		{1, 3, false, false, true},
		{2, 3, false, true, true},
		{3, 3, true, true, false},
		{1, 1, true, false, false},
	}
	for _, c := range cases {
		got := composeFeatureDevStepPrompt("BASE", 3, c.k, c.total, c.last, "do the step", "")
		if strings.Contains(got, skipSetup) != c.wantSkip {
			t.Errorf("step %d/%d: skip-setup line present = %v, want %v", c.k, c.total, !c.wantSkip, c.wantSkip)
		}
		if strings.Contains(got, stopEarly) != c.wantStop {
			t.Errorf("step %d/%d: stop-early line present = %v, want %v", c.k, c.total, !c.wantStop, c.wantStop)
		}
		if !strings.HasPrefix(got, "BASE") {
			t.Errorf("step %d/%d: the stable prefix must come first", c.k, c.total)
		}
	}
}
