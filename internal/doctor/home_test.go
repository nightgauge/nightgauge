package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/hometest"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// This package reads the machine-global serve claim registry
// (runstate.ServeSidecarDir, via eachServeClaim) and takes the scheduler lease
// to inspect it, so a test here that forgets to isolate HOME operates on the
// operator's own registry: it plants records under it and leaves lock files
// behind. Before #1426 this suite left three unattributable `.lock` files in
// the ambient HOME on every full run.
//
// isolateMachineState still exists and is still worth calling — it keeps two
// tests in one package from seeing each other's registry — but it is no longer
// what stands between this suite and the operator's state.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// The pin for that (#1426 AC3). It deliberately isolates nothing itself: what
// it asserts is that a test which never asked still cannot reach the real
// registry.
func TestServeRegistryNeverResolvesUnderTheRealHome(t *testing.T) {
	if hometest.Home == "" {
		t.Fatal("this package's TestMain does not isolate HOME; every test in it that resolves the claim directory writes into the operator's real registry (#1426)")
	}
	dir, err := runstate.ServeSidecarDir()
	if err != nil {
		t.Fatalf("ServeSidecarDir: %v", err)
	}
	if !strings.HasPrefix(dir, hometest.Home) {
		t.Fatalf("ServeSidecarDir() = %q, which is not under this binary's isolated HOME %q", dir, hometest.Home)
	}
	if real := hometest.RealPath(".nightgauge"); real != "" && strings.HasPrefix(dir, real+string(filepath.Separator)) {
		t.Fatalf("ServeSidecarDir() = %q resolves inside the real home's %q", dir, real)
	}
}
