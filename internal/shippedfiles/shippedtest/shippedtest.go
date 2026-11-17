// Package shippedtest points shippedfiles at the source checkout for tests,
// whose binaries live in a build cache with nothing shipped beside them.
// Import it only from _test.go files.
package shippedtest

import (
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/shippedfiles"
)

// UseCheckout sets shippedfiles.EnvDir to the checkout holding the working
// directory (the nearest ancestor with go.mod), unless it is already set.
func UseCheckout() {
	if os.Getenv(shippedfiles.EnvDir) != "" {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			_ = os.Setenv(shippedfiles.EnvDir, dir)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
