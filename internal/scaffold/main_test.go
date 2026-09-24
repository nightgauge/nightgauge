package scaffold

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestMain neutralises ambient git configuration for the package (#542): the
// code under test shells out to git with a plain exec.Command.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
