package scaffold

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hometest"
)

// TestMain neutralises ambient git configuration for the package (#542): the
// code under test shells out to git with a plain exec.Command.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	// A test here overrides the machine-state root; HOME moves with it
	// (#2311).
	cleanupHome := hometest.Isolate()
	code := m.Run()
	cleanupHome()
	os.Exit(code)
}
