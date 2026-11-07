package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hometest"
	"github.com/nightgauge/nightgauge/internal/models"
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
	// A doctor run fetches: the leaked-worktree scan's SweepMergedWorktrees
	// runs `git fetch origin <default>` with a bare exec.Command, which sees
	// only this process's environment. A successful fetch forks a detached
	// `git maintenance run --auto`, whose `commit-graph write` lands in .git
	// after the fetch and often the test have returned, so t.TempDir cleanup
	// failed with "directory not empty" under load (#2761, the class of #680
	// and #1293). IsolateProcess disarms that writer for inherited git.
	gittest.IsolateProcess()
	// markCoreCheckout gives fixtures a real origin URL because the #2758 trust
	// gate reads it, so without this each doctor run downloaded the whole
	// repository from GitHub. Local fixtures keep working over file.
	_ = os.Setenv("GIT_ALLOW_PROTOCOL", "file")
	// checkOpenCode builds a per-run config through
	// adapters.OpenCodeConfigInputFor/BuildOpenCodeConfig for every row this
	// package checks, and that unconditionally asks in.Discover for the
	// declared endpoint's limits (#1761) — so, unless a test opts back into
	// the real thing (SwapOpenCodeLocalDiscoveryForTest(nil), matching
	// cmd/nightgauge, internal/execution and internal/execution/adapters),
	// every row here was asking whatever model server this machine runs.
	// TestOpenCodeProbeRedactsBaseURL named the cost precisely: building a
	// row for an RFC 5737 base_url sent a real discovery request there and
	// blocked for the full ~2.4s timeout, next to ~0.2s for its own probe.
	restoreDiscovery := adapters.SwapOpenCodeLocalDiscoveryForTest(func(adapters.OpenCodeEndpoint, string) (models.LocalDescriptor, error) {
		return models.LocalDescriptor{}, errors.New("this package's TestMain asks no model server; a test that discovers swaps in a discovery of its own")
	})
	code := m.Run()
	restoreDiscovery()
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
