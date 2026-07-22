package orchestrator

import "testing"

// TestTierRank verifies the floor's tier ordering (haiku < sonnet < opus <
// fable) and that concrete IDs rank identically to their tier aliases, so a
// predicted concrete ID and a config floor alias compare correctly. Unknown /
// local models rank -1 (never floored).
func TestTierRank(t *testing.T) {
	// Ordering by alias.
	if !(tierRank("haiku") < tierRank("sonnet") &&
		tierRank("sonnet") < tierRank("opus") &&
		tierRank("opus") < tierRank("fable")) {
		t.Fatalf("tier ranks not strictly increasing: haiku=%d sonnet=%d opus=%d fable=%d",
			tierRank("haiku"), tierRank("sonnet"), tierRank("opus"), tierRank("fable"))
	}

	// Concrete registry IDs rank identically to their aliases.
	cases := map[string]string{
		"claude-haiku-4-5-20251001": "haiku",
		"claude-sonnet-5":           "sonnet",
		"claude-opus-4-8":           "opus",
		"claude-fable-5":            "fable",
	}
	for id, alias := range cases {
		if got, want := tierRank(id), tierRank(alias); got != want {
			t.Errorf("tierRank(%q) = %d, want %d (== tierRank(%q))", id, got, want, alias)
		}
	}

	// Unknown / local models are unrankable.
	for _, unknown := range []string{"", "my-local-model", "ollama/llama3"} {
		if got := tierRank(unknown); got != -1 {
			t.Errorf("tierRank(%q) = %d, want -1", unknown, got)
		}
	}
}

// TestEnforceMinimumModel mirrors skillRunner.ts's enforceMinimumModel: raise a
// below-floor selection to the floor, leave at/above-floor selections and the
// no-floor case untouched, and honor a fable floor. Plus the two Go-only guard
// cases: an unrecognized floor is ignored, and a local/unknown selected model
// is never floored.
func TestEnforceMinimumModel(t *testing.T) {
	tests := []struct {
		name     string
		selected string
		minimum  string
		want     string
	}{
		{"below floor is raised", "sonnet", "opus", "opus"},
		{"above floor is unchanged", "opus", "sonnet", "opus"},
		{"at floor is unchanged", "sonnet", "sonnet", "sonnet"},
		{"fable floor is includable", "sonnet", "fable", "fable"},
		{"fable selection is not lowered", "fable", "opus", "fable"},
		{"no floor returns selected", "sonnet", "", "sonnet"},
		{"concrete id below floor is raised to alias", "claude-sonnet-5", "opus", "opus"},
		{"unrecognized floor is ignored", "sonnet", "totally-bogus", "sonnet"},
		{"local selection is never floored", "my-local-model", "opus", "my-local-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := enforceMinimumModel(tt.selected, tt.minimum); got != tt.want {
				t.Errorf("enforceMinimumModel(%q, %q) = %q, want %q", tt.selected, tt.minimum, got, tt.want)
			}
		})
	}
}

// TestStageModelFloor verifies the env-var override wins over the config map
// (parity with TS modelResolver.getMinimumModel precedence), that the stage
// name is upper-cased with dashes → underscores for the env key, and that an
// absent floor returns "".
func TestStageModelFloor(t *testing.T) {
	floors := map[string]string{"feature-dev": "sonnet"}

	if got := stageModelFloor(floors, "feature-dev"); got != "sonnet" {
		t.Errorf("config floor: stageModelFloor = %q, want sonnet", got)
	}
	if got := stageModelFloor(nil, "feature-dev"); got != "" {
		t.Errorf("absent floor: stageModelFloor = %q, want empty", got)
	}
	if got := stageModelFloor(floors, "pr-create"); got != "" {
		t.Errorf("unconfigured stage: stageModelFloor = %q, want empty", got)
	}

	// Env var overrides config; stage name is upper-cased, dashes → underscores.
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV", "opus")
	if got := stageModelFloor(floors, "feature-dev"); got != "opus" {
		t.Errorf("env override: stageModelFloor = %q, want opus", got)
	}
	// Whitespace-only env value falls through to config.
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV", "  ")
	if got := stageModelFloor(floors, "feature-dev"); got != "sonnet" {
		t.Errorf("blank env override: stageModelFloor = %q, want sonnet (config)", got)
	}
}
