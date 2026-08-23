package git

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestMain neutralises ambient git configuration for the whole package (#542).
//
// internal/git is the package whose production code shells out to git most, and
// Service.gitExec deliberately inherits os.Environ() so that a real run honours
// the operator's hooks, signing and includeIf rules. That inheritance is
// correct in production and wrong in a test binary: a contributor with
// commit.gpgsign=true, or a CI image with a global core.hooksPath, otherwise
// sees failures inside ResetPipeline's safety checkpoint that look like defects
// in this package.
//
// Fixing it here rather than in Service keeps the production path honest — the
// service still respects operator config everywhere except under `go test`.
func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}
