package routing

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApplyMaxModel is the Go half of #1201. TypeScript mirror:
// packages/nightgauge-vscode/tests/utils/modeProfiles.maxModel.test.ts.
// Both sides must agree, or one config file dispatches two tiers depending on
// which path dispatched it.
func TestApplyMaxModel(t *testing.T) {
	tests := []struct {
		name        string
		env         ModeEnvelope
		maxModel    string
		wantCeiling string
		wantFloor   string
	}{
		{"lowers a ceiling above the cap",
			ModeEnvelope{Floor: TierHaiku, Ceiling: TierFable}, TierOpus, TierOpus, TierHaiku},
		{"no-op when the cap equals the ceiling",
			ModeEnvelope{Floor: TierHaiku, Ceiling: TierOpus}, TierOpus, TierOpus, TierHaiku},
		{"never raises a ceiling below the cap",
			ModeEnvelope{Floor: TierHaiku, Ceiling: TierSonnet}, TierFable, TierSonnet, TierHaiku},
		{"no-op when unset",
			ModeEnvelope{Floor: TierHaiku, Ceiling: TierFable}, "", TierFable, TierHaiku},
		{"no-op on an unknown tier — an unreadable cap must not reroute",
			ModeEnvelope{Floor: TierHaiku, Ceiling: TierFable}, "not-a-model", TierFable, TierHaiku},
		{"leaves the floor alone even when the cap lands below it",
			ModeEnvelope{Floor: TierOpus, Ceiling: TierFable}, TierHaiku, TierHaiku, TierOpus},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyMaxModel(tc.env, tc.maxModel)
			if got.Ceiling != tc.wantCeiling {
				t.Errorf("ceiling = %q, want %q", got.Ceiling, tc.wantCeiling)
			}
			if got.Floor != tc.wantFloor {
				t.Errorf("floor = %q, want %q", got.Floor, tc.wantFloor)
			}
		})
	}
}

func TestRoutedTierEnvelopeForWorkspace(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		root := t.TempDir()
		dir := filepath.Join(root, ".nightgauge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}

	// Premise: without a cap, frontier keeps its fable ceiling on feature-dev.
	// If this stops holding, every assertion below is vacuous.
	bare := write(t, "owner: acme\n")
	if got := RoutedTierEnvelopeForWorkspace(bare, ModeFrontier, "feature-dev").Ceiling; got != TierFable {
		t.Fatalf("premise: uncapped frontier feature-dev ceiling = %q, want %q", got, TierFable)
	}

	capped := write(t, "model_routing:\n  max_model: opus\n")
	if got := RoutedTierEnvelopeForWorkspace(capped, ModeFrontier, "feature-dev").Ceiling; got != TierOpus {
		t.Errorf("capped ceiling = %q, want %q", got, TierOpus)
	}

	// A cap cannot widen a narrower mode.
	if got := RoutedTierEnvelopeForWorkspace(
		write(t, "model_routing:\n  max_model: fable\n"), ModeEfficiency, "feature-dev",
	).Ceiling; got != TierSonnet {
		t.Errorf("efficiency ceiling with a fable cap = %q, want %q", got, TierSonnet)
	}

	// A missing config is "no cap", not an error.
	if got := RoutedTierEnvelopeForWorkspace(t.TempDir(), ModeFrontier, "feature-dev").Ceiling; got != TierFable {
		t.Errorf("missing config ceiling = %q, want %q (fail open)", got, TierFable)
	}

	// An empty workspace root is "no cap" too — the CLI passes "" when it has
	// no checkout to read.
	if got := RoutedTierEnvelopeForWorkspace("", ModeFrontier, "feature-dev").Ceiling; got != TierFable {
		t.Errorf("empty root ceiling = %q, want %q", got, TierFable)
	}
}
