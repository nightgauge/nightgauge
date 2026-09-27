package adapters

import (
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestOpenCodeExplorationBudget2188 proves feature-planning on a local
// endpoint gets the default budget, an operator value (0 = off) wins, and
// every other stage or a hosted dispatch gets none.
func TestOpenCodeExplorationBudget2188(t *testing.T) {
	zero, five := 0, 5
	cases := []struct {
		name     string
		settings config.OpenCodeToolOutput
		stage    string
		local    bool
		want     int
	}{
		{"local planning default", config.OpenCodeToolOutput{}, "feature-planning", true, 12},
		{"hosted planning", config.OpenCodeToolOutput{}, "feature-planning", false, 0},
		{"local dev", config.OpenCodeToolOutput{}, "feature-dev", true, 0},
		{"operator value", config.OpenCodeToolOutput{PlanningExplorationBudget: &five}, "feature-planning", true, 5},
		{"operator off", config.OpenCodeToolOutput{PlanningExplorationBudget: &zero}, "feature-planning", true, 0},
		{"operator value hosted", config.OpenCodeToolOutput{PlanningExplorationBudget: &five}, "feature-planning", false, 0},
	}
	for _, tc := range cases {
		if got := openCodeExplorationBudget(tc.settings, tc.stage, tc.local); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestOpenCodeConfigCarriesExplorationBudget2188 proves the built config
// carries the budget for a local feature-planning dispatch.
func TestOpenCodeConfigCarriesExplorationBudget2188(t *testing.T) {
	for stage, want := range map[string]int{"feature-planning": 12, "feature-dev": 0} {
		in, err := OpenCodeConfigInputFor(lmStudioSettings(), RunOptions{
			Stage:       stage,
			Model:       "lmstudio/qwen/qwen3.8-27b",
			WorktreeDir: goldenWorktree,
		}, goldenRunRoot, envLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		built, err := BuildOpenCodeConfig(in)
		if err != nil {
			t.Fatal(err)
		}
		if built.ExplorationBudget != want {
			t.Errorf("%s: ExplorationBudget = %d, want %d", stage, built.ExplorationBudget, want)
		}
	}
}

// TestOpenCodeKnowledgeDir2193 proves the plugin is handed the workspace
// root's knowledge base, absolute, and nothing for no root.
func TestOpenCodeKnowledgeDir2193(t *testing.T) {
	if got := openCodeKnowledgeDir(""); got != "" {
		t.Errorf("no root: %q", got)
	}
	if got, want := openCodeKnowledgeDir("/w/repo"), filepath.Join("/w/repo", ".nightgauge", "knowledge"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
