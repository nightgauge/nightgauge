package platform

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain isolates HOME and the machine-state root (NIGHTGAUGE_STATE_HOME,
// ADR-024 § 8) for this test binary. Machine binding and agent registration
// resolve the machine id, whose first use moves a legacy
// ~/.nightgauge/machine-id into the state root; a test that forgot its own
// t.Setenv would otherwise move the operator's real device identity.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
