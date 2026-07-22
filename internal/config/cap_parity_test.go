package config

import "testing"

// TestCapForRepoParity_FixtureA mirrors the TS fixture in
// packages/nightgauge-vscode/tests/config/concurrency-parity.test.ts.
//
// Both tests use the same numeric values. When they diverge, Go and TS
// concurrency resolution have drifted. Fix the resolver that changed.
//
// Fixture A:
//   concurrency:
//     workspace_max: 4
//     per_repo_max: 2
//     repository_overrides:
//       flutter: 3
func TestCapForRepoParity_FixtureA(t *testing.T) {
	cfg := &Config{
		Concurrency: &ConcurrencyConfig{
			WorkspaceMax: 4,
			PerRepoMax:   2,
			RepositoryOverrides: map[string]int{
				"flutter": 3,
			},
		},
	}
	rc := ResolveConcurrency(cfg)

	if rc.WorkspaceMax != 4 {
		t.Errorf("WorkspaceMax = %d, want 4", rc.WorkspaceMax)
	}
	// explicit override by short name
	if got := rc.CapForRepo("flutter"); got != 3 {
		t.Errorf(`CapForRepo("flutter") = %d, want 3 (explicit override)`, got)
	}
	// owner/repo lookup falls back to short-name match
	if got := rc.CapForRepo("nightgauge/flutter"); got != 3 {
		t.Errorf(`CapForRepo("nightgauge/flutter") = %d, want 3 (short-name override via suffix)`, got)
	}
	// no override → per_repo_max
	if got := rc.CapForRepo("other-repo"); got != 2 {
		t.Errorf(`CapForRepo("other-repo") = %d, want 2 (per_repo_max)`, got)
	}
}

// TestCapForRepoParity_FixtureB mirrors concurrency-parity.test.ts FixtureB.
//
// Fixture B — defaults only (neither workspace_max nor per_repo_max set):
//   concurrency: {}
func TestCapForRepoParity_FixtureB(t *testing.T) {
	cfg := &Config{Concurrency: &ConcurrencyConfig{}}
	rc := ResolveConcurrency(cfg)

	if rc.WorkspaceMax != DefaultWorkspaceMax {
		t.Errorf("WorkspaceMax = %d, want %d (default)", rc.WorkspaceMax, DefaultWorkspaceMax)
	}
	if got := rc.CapForRepo("any-repo"); got != DefaultPerRepoMax {
		t.Errorf(`CapForRepo("any-repo") = %d, want %d (default per_repo_max)`, got, DefaultPerRepoMax)
	}
}
