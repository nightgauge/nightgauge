package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/hometest"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// The serve and autonomous verbs take the scheduler lease and write the serve
// claim record, both of which live in the machine-global directory under HOME.
// A test here that exercises either without isolating HOME leaves a lock file
// in the operator's registry — this suite left one on every full run before
// #1426. See internal/hometest.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	// No test here reaches GitHub for an OpenCode stage's MCP servers; one
	// that reads them swaps in a forge of its own.
	restoreForge := adapters.SwapOpenCodeMcpForgeForTest(openCodeVerbForge{err: errors.New("the cmd test binary reads no forge")})
	// Nor does one ask a model server for a local model's limits.
	restoreDiscovery := adapters.SwapOpenCodeLocalDiscoveryForTest(func(adapters.OpenCodeEndpoint, string) (models.LocalDescriptor, error) {
		return models.LocalDescriptor{}, errors.New("the cmd test binary asks no model server")
	})
	code := m.Run()
	restoreDiscovery()
	restoreForge()
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
