package hooks

import (
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/config"
)

// RepoOptedIn reports whether the repository holding workdir opted in to
// Nightgauge: its committed project config (config.ProjectConfigPath) exists.
//
// The Claude plugin is user-scoped, so its hooks run in every repository the
// user opens. Hooks that change behaviour rather than guard against damage
// (stop-verify, format-on-save, the sanitization default) act only where this
// returns true (#2765). The safety gates stay global and never consult it.
//
// The roots checked are the git toplevel of workdir (workdir itself when it is
// not inside a repository) and, for a linked worktree, the canonical checkout
// (the parent of `git rev-parse --git-common-dir`). A local-tier file alone
// does not opt a repository in. Any git failure degrades to "not opted in",
// which is the side that never blocks.
//
// claude-plugins/nightgauge/hooks/lib/guard.sh `_ng_repo_opted_in` applies
// the same rule for hooks that must decide without the binary; change both
// together.
func RepoOptedIn(workdir string) bool {
	if workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return false
		}
		workdir = wd
	}
	for _, root := range optInRoots(workdir) {
		info, err := os.Stat(config.ProjectConfigPath(root))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// optInRoots returns the directories whose .nightgauge/config.yaml opts
// workdir in, in lookup order.
func optInRoots(workdir string) []string {
	toplevel := gitOutput(workdir, "rev-parse", "--show-toplevel")
	if toplevel == "" {
		return []string{workdir}
	}
	roots := []string{toplevel}
	commonDir := gitOutput(workdir, "rev-parse", "--git-common-dir")
	if commonDir == "" {
		return roots
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(workdir, commonDir)
	}
	canonical := filepath.Dir(filepath.Clean(commonDir))
	if canonical != filepath.Clean(toplevel) {
		roots = append(roots, canonical)
	}
	return roots
}
