package heal

import (
	"os"
	"path/filepath"
	"testing"
)

// stubPattern is a minimal HealPattern used by registry tests so they don't
// depend on the production patterns.
type stubPattern struct {
	slug        string
	matchOn     func(failures []BaselineFailure) bool
	fix         HealFix
	fixProduces bool
}

func (s *stubPattern) Slug() string        { return s.slug }
func (s *stubPattern) Description() string { return "stub pattern for tests" }
func (s *stubPattern) Matches(failures []BaselineFailure) bool {
	if s.matchOn == nil {
		return false
	}
	return s.matchOn(failures)
}
func (s *stubPattern) GenerateFix(_ []BaselineFailure) (HealFix, bool) {
	return s.fix, s.fixProduces
}

func TestRegistry_Match_FirstMatchWins(t *testing.T) {
	patternA := &stubPattern{slug: "a", matchOn: func(_ []BaselineFailure) bool { return true }}
	patternB := &stubPattern{slug: "b", matchOn: func(_ []BaselineFailure) bool { return true }}

	r := New(patternA, patternB)
	p, ok := r.Match([]BaselineFailure{{Classification: "inherited"}})
	if !ok || p.Slug() != "a" {
		t.Fatalf("expected first match a; got %v ok=%v", p, ok)
	}
}

func TestRegistry_Match_NoMatch(t *testing.T) {
	r := New(&stubPattern{slug: "never", matchOn: func(_ []BaselineFailure) bool { return false }})
	if _, ok := r.Match([]BaselineFailure{{Classification: "inherited"}}); ok {
		t.Fatalf("expected no match")
	}
}

func TestRegistry_Match_EmptyFailures(t *testing.T) {
	r := New(&stubPattern{slug: "always", matchOn: func(_ []BaselineFailure) bool { return true }})
	if _, ok := r.Match(nil); ok {
		t.Fatalf("expected no match on nil failures")
	}
	if _, ok := r.Match([]BaselineFailure{}); ok {
		t.Fatalf("expected no match on empty failures")
	}
}

func TestRegistry_Default_ContainsBuiltins(t *testing.T) {
	r := Default()
	slugs := map[string]bool{}
	for _, p := range r.Patterns() {
		slugs[p.Slug()] = true
	}
	for _, want := range []string{"missing-fixture", "missing-seed-update", "removed-export"} {
		if !slugs[want] {
			t.Errorf("Default registry missing pattern %q (got %v)", want, slugs)
		}
	}
}

func TestGetHealConfig_DefaultsWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := GetHealConfig(dir)
	if cfg != DefaultConfig() {
		t.Fatalf("expected defaults, got %+v", cfg)
	}
}

func TestGetHealConfig_DefaultsWhenEmptyWorkspaceRoot(t *testing.T) {
	cfg := GetHealConfig("")
	if cfg != DefaultConfig() {
		t.Fatalf("expected defaults for empty root, got %+v", cfg)
	}
}

func TestGetHealConfig_ParsesAllKeys(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, ".nightgauge", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(yamlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `pipeline:
  heal:
    max_active_per_repo: 4
    max_24h_per_repo: 10
    diff_budget_lines: 75
    require_human_first: false
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := GetHealConfig(dir)
	if cfg.MaxActivePerRepo != 4 {
		t.Errorf("MaxActivePerRepo = %d; want 4", cfg.MaxActivePerRepo)
	}
	if cfg.Max24hPerRepo != 10 {
		t.Errorf("Max24hPerRepo = %d; want 10", cfg.Max24hPerRepo)
	}
	if cfg.DiffBudgetLines != 75 {
		t.Errorf("DiffBudgetLines = %d; want 75", cfg.DiffBudgetLines)
	}
	if cfg.RequireHumanFirst {
		t.Errorf("RequireHumanFirst = true; want false")
	}
}

func TestGetHealConfig_PartialOverrides(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, ".nightgauge", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(yamlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Only set diff_budget_lines; the rest must fall back to defaults.
	body := `pipeline:
  heal:
    diff_budget_lines: 99
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := GetHealConfig(dir)
	if cfg.DiffBudgetLines != 99 {
		t.Errorf("DiffBudgetLines = %d; want 99", cfg.DiffBudgetLines)
	}
	if cfg.MaxActivePerRepo != DefaultMaxActivePerRepo {
		t.Errorf("MaxActivePerRepo = %d; want default %d", cfg.MaxActivePerRepo, DefaultMaxActivePerRepo)
	}
	if !cfg.RequireHumanFirst {
		t.Errorf("RequireHumanFirst flipped; want default true")
	}
}

func TestGetHealConfig_UnrelatedTopLevelKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, ".nightgauge", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(yamlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `other_top:
  heal:
    max_active_per_repo: 999
pipeline:
  recovery:
    max_attempts_per_run: 5
  heal:
    max_active_per_repo: 2
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := GetHealConfig(dir)
	if cfg.MaxActivePerRepo != 2 {
		t.Errorf("MaxActivePerRepo = %d; want 2 (other_top.heal must be ignored)", cfg.MaxActivePerRepo)
	}
}
