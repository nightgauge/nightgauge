// Package detect provides shared project-framework detection for the
// deterministic layer. internal/ci and internal/build previously each had
// their own detection and disagreed about the same project — ci knew
// flutter, build returned skipped (#195). Both now route through here so
// they can't diverge again.
package detect

import (
	"os"
	"path/filepath"
)

// Framework identifiers returned by Framework.
const (
	FrameworkFlutter = "flutter"
	FrameworkGo      = "go"
	FrameworkNode    = "node"
	FrameworkUnknown = "unknown"
)

// Framework returns the project framework for workdir. Detection order
// matches internal/ci's historical behavior: pubspec.yaml → go.mod →
// package.json → unknown.
func Framework(workdir string) string {
	switch {
	case fileExists(filepath.Join(workdir, "pubspec.yaml")):
		return FrameworkFlutter
	case fileExists(filepath.Join(workdir, "go.mod")):
		return FrameworkGo
	case fileExists(filepath.Join(workdir, "package.json")):
		return FrameworkNode
	default:
		return FrameworkUnknown
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
