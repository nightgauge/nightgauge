package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a config.yaml and loads it the way the product does, so
// these tests exercise real YAML decoding rather than a hand-built struct. The
// bug in #991 lived precisely in the gap between "a struct with a false field"
// and "a YAML file with the key omitted" — a struct-only test cannot see it.
func writeConfig(t *testing.T, body string) *Config {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// TestResolveEpicCheckpoint_OmittedKeyKeepsDefault is the core regression guard.
//
// The operator writes a normal-looking tuning block to raise ONE rate limit.
// Before #991, that silently set epic_checkpoint to false and removed the
// between-epic human pause — no warning, no log line, and the tool reported the
// rail as configured.
func TestResolveEpicCheckpoint_OmittedKeyKeepsDefault(t *testing.T) {
	cfg := writeConfig(t, `
owner: acme
repo: widget
autonomous:
  safety_rails:
    rate_limit_per_hour: 40
`)

	if got := ResolveEpicCheckpoint(cfg); got != true {
		t.Errorf("ResolveEpicCheckpoint = %v, want true — tuning a NEIGHBOURING rail "+
			"must not disable the epic checkpoint", got)
	}
	// The sibling must still have been read, or this test would pass against a
	// build that ignores the block entirely.
	if cfg.Autonomous == nil || cfg.Autonomous.SafetyRails == nil {
		t.Fatal("safety_rails block was not parsed at all")
	}
	if got := cfg.Autonomous.SafetyRails.RateLimitPerHour; got != 40 {
		t.Errorf("RateLimitPerHour = %d, want 40", got)
	}
}

func TestResolveEpicCheckpoint_ExplicitFalseIsHonoured(t *testing.T) {
	cfg := writeConfig(t, `
owner: acme
repo: widget
autonomous:
  safety_rails:
    epic_checkpoint: false
`)
	if got := ResolveEpicCheckpoint(cfg); got != false {
		t.Errorf("ResolveEpicCheckpoint = %v, want false — an explicit opt-out must "+
			"survive, or the pointer change traded one bug for its mirror image", got)
	}
}

func TestResolveEpicCheckpoint_ExplicitTrueIsHonoured(t *testing.T) {
	cfg := writeConfig(t, `
owner: acme
repo: widget
autonomous:
  safety_rails:
    epic_checkpoint: true
`)
	if got := ResolveEpicCheckpoint(cfg); got != true {
		t.Errorf("ResolveEpicCheckpoint = %v, want true", got)
	}
}

func TestResolveEpicCheckpoint_NoBlockAtAll(t *testing.T) {
	// The shape every repo in this workspace is actually in today: no
	// safety_rails block. Must be the default, not the zero value.
	cfg := writeConfig(t, "owner: acme\nrepo: widget\n")
	if got := ResolveEpicCheckpoint(cfg); got != DefaultEpicCheckpoint {
		t.Errorf("ResolveEpicCheckpoint = %v, want %v", got, DefaultEpicCheckpoint)
	}
	if got := ResolveEpicCheckpoint(nil); got != DefaultEpicCheckpoint {
		t.Errorf("ResolveEpicCheckpoint(nil) = %v, want %v", got, DefaultEpicCheckpoint)
	}
}

// TestEpicCheckpointDefault_MatchesOrchestrator pins the two places the default
// is written down. The docs disagreed about this value for the rail's entire
// life — AUTONOMOUS_ORCHESTRATOR.md said true, CONFIGURATION.md said false —
// because it was prose in two files and code in one. Now it is code in two, and
// this fails if they diverge.
func TestEpicCheckpointDefault_MatchesOrchestrator(t *testing.T) {
	// Imported lazily via the constant rather than the orchestrator package to
	// avoid an import cycle (config is reached transitively from orchestrator).
	// The orchestrator side asserts the same pairing from its end.
	if DefaultEpicCheckpoint != true {
		t.Errorf("config.DefaultEpicCheckpoint = %v; orchestrator.DefaultSafetyConfig() "+
			"sets EpicCheckpoint: true", DefaultEpicCheckpoint)
	}
}
