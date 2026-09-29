package recall_test

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points the cache home (NIGHTGAUGE_CACHE_HOME, ADR-024 § 6) at a
// directory of this test binary's own, so no test writes a recall index into
// the operator's user cache directory. Tests that assert on the location set
// their own with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nightgauge-recall-cache-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall tests: could not create a cache home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("NIGHTGAUGE_CACHE_HOME", dir); err != nil {
		fmt.Fprintf(os.Stderr, "recall tests: could not set NIGHTGAUGE_CACHE_HOME: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
