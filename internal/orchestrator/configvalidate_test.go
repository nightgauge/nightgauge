package orchestrator

import (
	"testing"
)

// helper: boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool { return &b }

// Per-repo concurrency policy is expressed via RepositoryMaxConcurrent
// (concurrency.repository_overrides). ValidateAutonomousConfig warns when an
// override targets a repo that isn't in enabled_repos (silently excluded), and
// when the workspace ceiling is below the number of per-repo-capped repos.
func TestValidateAutonomousConfig(t *testing.T) {
	t.Run("clean — override repo is in enabled_repos", func(t *testing.T) {
		cfg := AutonomousConfig{
			MaxConcurrent:           3,
			RepositoryMaxConcurrent: map[string]int{"my-repo": 1},
		}
		warnings := ValidateAutonomousConfig(cfg, []string{"nightgauge/my-repo"}, nil, nil)
		if len(warnings) != 0 {
			t.Errorf("expected 0 warnings, got %d: %v", len(warnings), warnings)
		}
	})

	t.Run("policy-without-allowlist — override repo not enabled", func(t *testing.T) {
		cfg := AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"ghost-repo": 1},
		}
		warnings := ValidateAutonomousConfig(cfg, []string{"nightgauge/my-repo"}, nil, nil)
		if len(warnings) != 1 {
			t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
		}
		if warnings[0].Kind != "policy-without-allowlist" {
			t.Errorf("expected kind=policy-without-allowlist, got %q", warnings[0].Kind)
		}
	})

	t.Run("multiple overrides, partial overlap", func(t *testing.T) {
		cfg := AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{
				"enabled-repo": 2,
				"missing-a":    1,
				"missing-b":    1,
			},
		}
		warnings := ValidateAutonomousConfig(cfg, []string{"nightgauge/enabled-repo"}, nil, nil)
		var policy int
		for _, w := range warnings {
			if w.Kind == "policy-without-allowlist" {
				policy++
			}
		}
		if policy != 2 {
			t.Fatalf("expected 2 policy-without-allowlist warnings, got %d: %v", policy, warnings)
		}
	})

	t.Run("empty enabled_repos — no warnings (scan-all)", func(t *testing.T) {
		cfg := AutonomousConfig{RepositoryMaxConcurrent: map[string]int{"other-repo": 1}}
		warnings := ValidateAutonomousConfig(cfg, nil, nil, nil)
		if len(warnings) != 0 {
			t.Errorf("expected 0 warnings for scan-all mode, got %d: %v", len(warnings), warnings)
		}
	})

	t.Run("concurrency cap — capped repos exceed max_concurrent", func(t *testing.T) {
		cfg := AutonomousConfig{
			MaxConcurrent:           1,
			RepositoryMaxConcurrent: map[string]int{"repo-a": 1, "repo-b": 1},
		}
		warnings := ValidateAutonomousConfig(cfg,
			[]string{"nightgauge/repo-a", "nightgauge/repo-b"}, nil, nil)
		var cap int
		for _, w := range warnings {
			if w.Kind == "concurrency-cap" {
				cap++
			}
		}
		if cap != 1 {
			t.Fatalf("expected 1 concurrency-cap warning, got %d: %v", cap, warnings)
		}
	})

	t.Run("no cap warning — max_concurrent >= capped count", func(t *testing.T) {
		cfg := AutonomousConfig{
			MaxConcurrent:           2,
			RepositoryMaxConcurrent: map[string]int{"repo-a": 1, "repo-b": 1},
		}
		warnings := ValidateAutonomousConfig(cfg,
			[]string{"nightgauge/repo-a", "nightgauge/repo-b"}, nil, nil)
		for _, w := range warnings {
			if w.Kind == "concurrency-cap" {
				t.Errorf("unexpected concurrency-cap warning: %v", w)
			}
		}
	})

	t.Run("empty config — no panics, no warnings", func(t *testing.T) {
		warnings := ValidateAutonomousConfig(AutonomousConfig{}, nil, nil, nil)
		if len(warnings) != 0 {
			t.Errorf("expected empty warnings for zero config, got %v", warnings)
		}
	})

	t.Run("fully-qualified override key matches enabled repo", func(t *testing.T) {
		cfg := AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"nightgauge/my-repo": 2},
		}
		warnings := ValidateAutonomousConfig(cfg, []string{"nightgauge/my-repo"}, nil, nil)
		if len(warnings) != 0 {
			t.Errorf("expected 0 warnings when override key matches enabled repo, got %d: %v", len(warnings), warnings)
		}
	})

	_ = boolPtr // retained helper used by sibling tests
}
