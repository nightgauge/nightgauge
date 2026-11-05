package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadStallIdle loads body as the machine-tier config through LoadMerged, the
// loader `nightgauge config show` uses.
func loadStallIdle(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("owner: acme\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SwapMachineConfigPathForTest(func() (string, error) { return path, nil }))
	return LoadMerged(t.TempDir())
}

// TestStallIdleMsIntegerLoads pins #2714: the integer form documented and read
// by the extension (stallIdleMs.test.ts shares the literal 480000) loads in Go.
func TestStallIdleMsIntegerLoads(t *testing.T) {
	cfg, err := loadStallIdle(t, "pipeline:\n  stall_idle_ms: 480000\n")
	if err != nil {
		t.Fatalf("integer stall_idle_ms failed to load: %v", err)
	}
	if cfg.Pipeline.StallIdleMs != 480000 || cfg.Pipeline.StallIdleMs.Duration() != 8*time.Minute {
		t.Fatalf("got %d ms, want 480000", cfg.Pipeline.StallIdleMs)
	}
}

func TestStallIdleMsZeroAndUnset(t *testing.T) {
	for _, body := range []string{"pipeline:\n  stall_idle_ms: 0\n", "pipeline:\n  max_tokens: 1\n"} {
		cfg, err := loadStallIdle(t, body)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if cfg.Pipeline.StallIdleMs != 0 {
			t.Fatalf("%q: got %d, want 0", body, cfg.Pipeline.StallIdleMs)
		}
	}
}

func TestStallIdleMsRejectsOtherForms(t *testing.T) {
	for _, v := range []string{"8m", `"480000"`, "-1", "1.5"} {
		_, err := loadStallIdle(t, "pipeline:\n  stall_idle_ms: "+v+"\n")
		if err == nil || !strings.Contains(err.Error(), "pipeline.stall_idle_ms") {
			t.Errorf("stall_idle_ms: %s: want an error naming the key, got %v", v, err)
		}
	}
}
