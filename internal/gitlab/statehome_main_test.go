package gitlab

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain isolates HOME and the machine-state root (NIGHTGAUGE_STATE_HOME,
// ADR-024 § 8) for this test binary: DefaultSharedTrackerPath creates the
// state root and moves a legacy ~/.nightgauge/ratelimit-gitlab-<host>.json
// into it, which must never touch the operator's real files.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
