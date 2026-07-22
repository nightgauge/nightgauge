// Tests for the workspace-ceiling resolver. The unified source of truth is the
// concurrency: block (concurrency.workspace_max); ResolvedMaxConcurrent is a
// thin alias over ResolveConcurrency().WorkspaceMax. See Issue #3781.
package config

import "testing"

func TestResolvedMaxConcurrent_FromConcurrencyBlock(t *testing.T) {
	cfg := &Config{Concurrency: &ConcurrencyConfig{WorkspaceMax: 4}}
	if got := ResolvedMaxConcurrent(cfg); got != 4 {
		t.Fatalf("expected concurrency.workspace_max=4, got %d", got)
	}
}

func TestResolvedMaxConcurrent_DefaultWhenUnset(t *testing.T) {
	if got := ResolvedMaxConcurrent(&Config{}); got != DefaultWorkspaceMax {
		t.Fatalf("expected default %d when workspace_max unset, got %d", DefaultWorkspaceMax, got)
	}
}

func TestResolvedMaxConcurrent_NilConfig(t *testing.T) {
	if got := ResolvedMaxConcurrent(nil); got != DefaultWorkspaceMax {
		t.Fatalf("expected default %d on nil config, got %d", DefaultWorkspaceMax, got)
	}
}

func TestResolvedMaxConcurrent_ZeroFallsBackToDefault(t *testing.T) {
	cfg := &Config{Concurrency: &ConcurrencyConfig{WorkspaceMax: 0}}
	if got := ResolvedMaxConcurrent(cfg); got != DefaultWorkspaceMax {
		t.Fatalf("expected default when workspace_max is 0, got %d", got)
	}
}
