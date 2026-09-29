package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenCodeTimeoutBareNumberFailsTheBlock pins what an unquoted numeric
// opencode timeout does: YAMLDuration decodes the scalar 180000 as the string
// "180000", which time.ParseDuration refuses for its missing unit, so the whole
// opencode: block fails to load, naming the file. It is never read as
// nanoseconds, so the adapter's sub-second check cannot see a bare number.
func TestOpenCodeTimeoutBareNumberFailsTheBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("opencode:\n  timeouts:\n    header: 180000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SwapMachineConfigPathForTest(func() (string, error) { return path, nil }))

	_, err := LoadOpenCodeConfig("")
	if err == nil {
		t.Fatal("an unquoted numeric timeout loaded")
	}
	for _, want := range []string{path, `invalid duration "180000"`, "missing unit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}
