package main

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
	"github.com/zalando/go-keyring"
)

// The serve and autonomous verbs take the scheduler lease and write the serve
// claim record, both of which live in the machine-global directory under HOME.
// A test here that exercises either without isolating HOME leaves a lock file
// in the operator's registry — this suite left one on every full run before
// #1426. See internal/hometest.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	// Commands under test commit with a plain `git`, which inherits
	// os.Environ(): neutralise ambient git config such as commit.gpgsign (#2283).
	gittest.IsolateProcess()
	// Nor does a command run from the package directory resolve per-clone
	// state into this source checkout's git directory (ADR-024 § 7).
	ceilGitDiscoveryAtThisCheckout()
	// No test here reads or writes the operator's OS keychain: go-keyring's
	// in-memory provider stands in for it (internal/keychain).
	keyring.MockInit()
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

// ceilGitDiscoveryAtThisCheckout sets GIT_CEILING_DIRECTORIES to every
// ancestor of the package directory up to the checkout holding it, so git
// discovery from the package directory (the default cwd of every test here)
// finds no repository. Without it, each command a test runs without a
// --workdir resolved the per-clone directories of this source checkout and
// created them in its real git directory. Temp repositories are unaffected.
func ceilGitDiscoveryAtThisCheckout() {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	forms := []string{wd}
	if resolved, err := filepath.EvalSymlinks(wd); err == nil && resolved != wd {
		forms = append(forms, resolved)
	}
	var ceilings []string
	for _, dir := range forms {
		for p := filepath.Dir(dir); ; p = filepath.Dir(p) {
			ceilings = append(ceilings, p)
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil || p == filepath.Dir(p) {
				break
			}
		}
	}
	_ = os.Setenv("GIT_CEILING_DIRECTORIES", strings.Join(ceilings, string(os.PathListSeparator)))
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
