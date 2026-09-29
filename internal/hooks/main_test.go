package hooks

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestMain neutralises ambient git configuration for the whole package
// (#2283). The guard shell under test runs a plain `git`, which inherits
// os.Environ(), so a developer's global commit.gpgsign=true would otherwise
// fail its commits for reasons unrelated to the hooks.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
