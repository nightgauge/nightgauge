package main

import (
	"fmt"
	"path/filepath"
)

// cloneDir resolves a per-clone class directory through one of the
// internal/layout resolvers (layout.PipelineStateDir, layout.CloneLogsDir, ...)
// for a root that may be empty or relative, as a --workdir flag or the working
// directory often is. The resolvers refuse such a root, so it is made absolute
// first; the result is then expressed relative to root again, so the path is
// the one filepath.Join(root, ...) produced before the resolvers existed ("" is
// the working directory). An absolute root gets the resolver's result as is.
// The only new failure is an unreadable working directory.
func cloneDir(resolve func(string) (string, error), root string) (string, error) {
	if filepath.IsAbs(root) {
		return resolve(root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve %q against the working directory: %w", root, err)
	}
	dir, err := resolve(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(abs, dir)
	if err != nil {
		return dir, nil
	}
	return filepath.Join(root, rel), nil
}
