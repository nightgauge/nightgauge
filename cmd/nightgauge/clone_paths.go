package main

import (
	"fmt"
	"path/filepath"
)

// cloneDir resolves a per-clone class directory through one of the
// internal/layout resolvers (layout.PipelineStateDir, layout.CloneLogsDir, ...)
// for a root that may be empty or relative, as a --workdir flag or the working
// directory often is. The resolvers refuse such a root, so it is made absolute
// first. The result is absolute: the class directories live under the git
// common dir (ADR-024 § 7), not under root. Outside a git repository the
// resolver's "not a git repository" error is returned.
func cloneDir(resolve func(string) (string, error), root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve %q against the working directory: %w", root, err)
	}
	return resolve(abs)
}
