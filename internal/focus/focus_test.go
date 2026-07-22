package focus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinLenses(t *testing.T) {
	lenses := BuiltinLenses()
	if len(lenses) != 8 {
		t.Fatalf("expected 8 built-in lenses, got %d", len(lenses))
	}

	// Verify general is first and has no boosts
	if lenses[0].Name != "general" {
		t.Errorf("expected first lens to be 'general', got %q", lenses[0].Name)
	}
	if len(lenses[0].ScoringBoosts) != 0 {
		t.Errorf("general lens should have no scoring boosts")
	}

	// Verify all lenses are marked builtin
	for _, l := range lenses {
		if !l.Builtin {
			t.Errorf("lens %q should be marked builtin", l.Name)
		}
		if l.Description == "" {
			t.Errorf("lens %q has empty description", l.Name)
		}
	}

	// Verify expected names exist
	expected := map[string]bool{
		"general": false, "quality": false, "features": false,
		"security": false, "performance": false, "documentation": false,
		"reliability": false, "ux": false,
	}
	for _, l := range lenses {
		if _, ok := expected[l.Name]; ok {
			expected[l.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing expected built-in lens: %q", name)
		}
	}
}

func TestManagerLoadDefault(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	s, err := m.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if s.ActiveLens != "general" {
		t.Errorf("expected default lens 'general', got %q", s.ActiveLens)
	}
}

func TestManagerSetAndLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)

	// Set focus to quality
	s, err := m.Set("quality", "test")
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if s.ActiveLens != "quality" {
		t.Errorf("expected 'quality', got %q", s.ActiveLens)
	}
	if s.SetBy != "test" {
		t.Errorf("expected SetBy='test', got %q", s.SetBy)
	}
	if s.SetAt.IsZero() {
		t.Error("expected non-zero SetAt")
	}

	// Reload and verify persistence
	s2, err := m.Load()
	if err != nil {
		t.Fatalf("Load() after Set() error: %v", err)
	}
	if s2.ActiveLens != "quality" {
		t.Errorf("expected persisted 'quality', got %q", s2.ActiveLens)
	}
}

func TestManagerSetInvalidLens(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	_, err := m.Set("nonexistent", "test")
	if err == nil {
		t.Error("expected error for invalid lens name")
	}
}

func TestManagerClear(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)

	// Set then clear
	if _, err := m.Set("security", "test"); err != nil {
		t.Fatal(err)
	}
	s, err := m.Clear("test")
	if err != nil {
		t.Fatalf("Clear() error: %v", err)
	}
	if s.ActiveLens != "general" {
		t.Errorf("expected 'general' after clear, got %q", s.ActiveLens)
	}
}

func TestManagerShow(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)

	if _, err := m.Set("features", "test"); err != nil {
		t.Fatal(err)
	}

	s, lens, err := m.Show()
	if err != nil {
		t.Fatalf("Show() error: %v", err)
	}
	if s.ActiveLens != "features" {
		t.Errorf("expected 'features', got %q", s.ActiveLens)
	}
	if lens == nil {
		t.Fatal("expected non-nil lens")
	}
	if lens.Name != "features" {
		t.Errorf("expected lens name 'features', got %q", lens.Name)
	}
	if lens.ScoringBoosts["pipeline_stage"] != 10 {
		t.Errorf("expected pipeline_stage boost of 10, got %d", lens.ScoringBoosts["pipeline_stage"])
	}
}

func TestResolveLensCustom(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)

	// Save a custom lens
	s := &State{
		ActiveLens: "general",
		CustomLenses: []Lens{
			{
				Name:        "mobile",
				Description: "Focus on mobile app quality",
				ScoringBoosts: map[string]int{
					"cross_repo": 15,
				},
				Keywords: []string{"flutter", "ios", "android"},
			},
		},
	}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}

	// Set the custom lens
	s2, err := m.Set("mobile", "test")
	if err != nil {
		t.Fatalf("Set() custom lens error: %v", err)
	}
	if s2.ActiveLens != "mobile" {
		t.Errorf("expected 'mobile', got %q", s2.ActiveLens)
	}

	// Resolve it
	lens := m.ResolveLens("mobile", s2)
	if lens.Name != "mobile" {
		t.Errorf("expected resolved lens 'mobile', got %q", lens.Name)
	}
	if lens.ScoringBoosts["cross_repo"] != 15 {
		t.Errorf("expected cross_repo boost 15, got %d", lens.ScoringBoosts["cross_repo"])
	}
}

func TestAllLenses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)

	// Save a custom lens
	s := &State{
		ActiveLens: "general",
		CustomLenses: []Lens{
			{Name: "custom1", Description: "Custom 1"},
		},
	}
	if err := m.Save(s); err != nil {
		t.Fatal(err)
	}

	all := m.AllLenses()
	if len(all) != 9 { // 8 built-in + 1 custom
		t.Errorf("expected 9 lenses, got %d", len(all))
	}
}

func TestSetEmptyName(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	_, err := m.Set("", "test")
	if err == nil {
		t.Error("expected error for empty lens name")
	}
}

func TestSetNormalizesCase(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)

	s, err := m.Set("QUALITY", "test")
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if s.ActiveLens != "quality" {
		t.Errorf("expected normalized 'quality', got %q", s.ActiveLens)
	}
}
