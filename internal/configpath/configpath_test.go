package configpath

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinuxNeverReadsTheLegacyFile (ADR-024 § 15): on Linux the machine-tier
// file is the XDG one whether or not an older build left
// ~/.nightgauge/config.yaml; `nightgauge doctor --fix` moves that file, and
// nothing reads it.
func TestLinuxNeverReadsTheLegacyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	canonical := filepath.Join(home, ".config", "nightgauge", "config.yaml")
	legacy := filepath.Join(home, ".nightgauge", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ForGOOS("linux"); got != canonical {
		t.Errorf("only legacy present: ForGOOS(linux) = %q, want the XDG file %q", got, canonical)
	}
	if got, _ := ForGOOS("darwin"); got != legacy {
		t.Errorf("darwin: ForGOOS = %q, want %q (the macOS default)", got, legacy)
	}
}
