package configpath

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInUseNamesLegacyFileWhenOnlyItExists: on Linux the loader falls back to
// ~/.nightgauge/config.yaml when the XDG file is absent, so a message naming
// "the file in use" must name that one.
func TestInUseNamesLegacyFileWhenOnlyItExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	canonical := filepath.Join(home, ".config", "nightgauge", "config.yaml")
	legacy := filepath.Join(home, ".nightgauge", "config.yaml")

	if got, _ := InUseForGOOS("linux"); got != canonical {
		t.Errorf("neither file present: InUse = %q, want canonical %q", got, canonical)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := InUseForGOOS("linux"); got != legacy {
		t.Errorf("only legacy present: InUse = %q, want %q", got, legacy)
	}
	if got, _ := InUseForGOOS("darwin"); got != legacy {
		t.Errorf("darwin: InUse = %q, want %q (same file)", got, legacy)
	}
}
