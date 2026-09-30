package telemetrynotice

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain isolates HOME and the machine-state root for this test binary.
// Tests here override NIGHTGAUGE_STATE_HOME or XDG_STATE_HOME; a STATE
// override with the real HOME is the pairing that once moved, and then
// deleted, a developer's legacy ~/.nightgauge (#2311). The lint in
// internal/hometest requires this for every such package.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
