package sweep

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain isolates HOME and the machine-state root for this test binary. The
// Action Center store is machine state (STATE/attention, #3453): a test that
// forgot layouttest.StateHome would otherwise raise its fixture cards into
// the operator's real store, which the daemon uploads to the platform.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
