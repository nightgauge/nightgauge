package hooks

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain neutralises ambient git configuration for the whole package
// (#2283). The guard shell under test runs a plain `git`, which inherits
// os.Environ(), so a developer's global commit.gpgsign=true would otherwise
// fail its commits for reasons unrelated to the hooks.
//
// It also isolates HOME and the machine-state root: the post-merge verifier
// raises Action Center cards, and the store is machine state (STATE/attention,
// #3453), so a test would otherwise write the operator's real store.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
