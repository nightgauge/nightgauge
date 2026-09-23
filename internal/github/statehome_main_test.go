package github

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain isolates HOME and the machine-state root (NIGHTGAUGE_STATE_HOME,
// ADR-024 § 8) for this test binary. Every client this package builds
// attaches the machine-wide rate-limit tracker (DefaultSharedTrackerPath),
// whose first use moves a legacy ~/.nightgauge/rate-limit.json into the state
// root; without this a test run would move and write the operator's real
// files.
//
// It also clears CI, which a CI runner sets and which changes token
// resolution (ADR-024 § 5); tests of the CI rules set it themselves.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CI")
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
