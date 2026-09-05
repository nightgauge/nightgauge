// Package hometest points a Go test binary's HOME at a directory of its own.
//
// # Why this exists (#1426)
//
// <home>/.nightgauge is machine-global, per-user state: the serve claim
// registry, the machine-tier config, the API request ledger. A test that
// resolves any of it without an isolated HOME operates on the OPERATOR's copy
// — it can take over, and on cleanup DELETE, the claim of the serve daemon
// actually serving the editor that is running the test.
//
// That hazard was already known and was handled one test at a time with
// t.Setenv("HOME", t.TempDir()). Per-test opt-in is the wrong shape for it:
// forgetting is silent, the damage lands in a directory nobody looks at, and
// it only ever accumulates. The measurement that opened #1426 found 150 claim
// records on a maintainer machine of which 143 named a workspace root that no
// longer existed — for the most part finished t.TempDir()s, which is to say
// exactly this leak, compounded over months.
//
// So the isolation moves from the test to the test BINARY. A package whose
// TestMain calls Isolate cannot reach the real home from any test in it,
// whether or not that test remembered to ask. Per-test t.Setenv is still
// worth doing — it keeps two tests in one package from seeing each other's
// registry — but it is no longer the thing standing between a suite and the
// operator's state.
//
// Isolate EXITS rather than returning an error, because the fallback for
// "could not isolate HOME" is running the suite against the real one, and that
// is the outcome this package exists to make impossible.
package hometest

import (
	"fmt"
	"os"
	"path/filepath"
)

// Home is the isolated HOME this test binary runs under. Empty until Isolate
// has run, which is what a pin test asserts against: an empty Home means the
// package's TestMain never isolated anything.
var Home string

// RealHome is the home Isolate replaced, or "" if it could not be resolved.
// Kept so a test can assert the real registry is not what it just read.
var RealHome string

// Isolate repoints HOME at a fresh directory and returns the cleanup for it.
//
// Call it from TestMain, before m.Run, and defer nothing: TestMain must end in
// os.Exit, which runs no defers, so the returned func has to be called
// explicitly around the run.
func Isolate() (cleanup func()) {
	RealHome, _ = os.UserHomeDir()
	dir, err := os.MkdirTemp("", "nightgauge-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hometest: could not create an isolated HOME: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", dir); err != nil {
		fmt.Fprintf(os.Stderr, "hometest: could not set HOME: %v\n", err)
		os.Exit(1)
	}
	Home = dir
	return func() { _ = os.RemoveAll(dir) }
}

// RealPath is a path inside the home Isolate replaced, for an assertion that
// names it. "" when the real home could not be resolved, which no assertion
// should then act on.
func RealPath(elem ...string) string {
	if RealHome == "" {
		return ""
	}
	return filepath.Join(append([]string{RealHome}, elem...)...)
}
