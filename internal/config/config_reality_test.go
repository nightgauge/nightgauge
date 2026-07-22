package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_RealConfig verifies that the actual .nightgauge/config.yaml
// in this repository parses correctly. This test fails if the config format
// changes break the parser — providing an early warning before CI or users
// are affected.
//
// Skip conditions (graceful degradation):
//   - Config file not found (shallow CI clone, running from a fork)
//   - File unreadable (permissions issue)
func TestLoad_RealConfig(t *testing.T) {
	// Navigate from internal/config/ to repo root (two levels up).
	// This uses a relative path so it works regardless of GOPATH or checkout
	// location, matching the pattern established by fixtureYAML().
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Skipf("could not resolve repo root: %v", err)
	}

	configPath := filepath.Join(repoRoot, ".nightgauge", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Skipf("real config.yaml not found at %s — skipping reality test", configPath)
	}

	cfg, err := LoadYAML(configPath)
	if err != nil {
		t.Fatalf("LoadYAML real config: %v", err)
	}

	// Verify owner — this is the most critical field; an empty owner means
	// GitHub API calls will fail silently.
	if cfg.Owner == "" {
		t.Error("Owner is empty — config format may have changed")
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want %q", cfg.Owner, "nightgauge")
	}

	// The public-core dogfood config must not route a clone into an organization
	// Project. Operators configure a board locally when they opt into board-backed
	// commands.
	if cfg.ProjectNumber != 0 {
		t.Errorf("ProjectNumber = %d, want 0 for safe public defaults", cfg.ProjectNumber)
	}

	// Verify repo name is populated (hybrid format top-level field).
	if cfg.DefaultRepo == "" {
		t.Error("DefaultRepo (repo) is empty — hybrid format top-level repo field not parsed")
	}
	if cfg.DefaultRepo != "nightgauge" {
		t.Errorf("DefaultRepo = %q, want %q", cfg.DefaultRepo, "nightgauge")
	}
}
