package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Run records keep every gate row, so the earlier "catch" stays visible after
// a later "pass" supersedes it for the gate (#2746).
func TestLoadGateResults_KeepsSupersededCatch(t *testing.T) {
	ws := layouttest.Repo(t)
	if err := state.AppendGateMetric(ws, 2746, "adversarial-review", "catch", "x", "2026-10-31T00:00:00Z"); err != nil {
		t.Fatalf("append catch: %v", err)
	}
	if err := state.AppendGateMetric(ws, 2746, "adversarial-review", "pass", "", "2026-10-31T00:01:00Z"); err != nil {
		t.Fatalf("append pass: %v", err)
	}

	got := loadGateResults(ws, 2746)
	if len(got) != 2 || got[0].Result != "catch" || got[1].Result != "pass" {
		t.Fatalf("loadGateResults = %+v, want catch then pass", got)
	}
}
