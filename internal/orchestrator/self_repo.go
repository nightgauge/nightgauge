package orchestrator

// Self-repo identity (#292). Autonomous must refuse to dispatch an issue
// belonging to the repository that BUILT THE RUNNING BINARY: a stage editing
// that repo can be destroyed by the unfixed version of itself (#289 — the
// running binary's ResetPipeline() hard-reset the worktree of the stage that
// was implementing the guard against exactly that, burning $14.84). The rule
// "work self-repo issues interactively" is only real if it is mechanical;
// autonomous dispatch is precisely the path where nobody is watching.

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	gitsvc "github.com/nightgauge/nightgauge/internal/git"
)

// ResolveSelfRepoSlug resolves the "owner/repo" slug of the repository that
// built the running binary. Preference order:
//
//  1. os.Executable() → walk up to the enclosing git repo → origin remote
//     slug. Correct for dev builds and forks — the acceptance criterion is
//     "self-repo identity derives from the running binary's origin remote",
//     not from the workspace's default repo (the extension may be launched
//     from anywhere).
//  2. debug.ReadBuildInfo().Main.Path (e.g. "github.com/nightgauge/nightgauge")
//     — covers `go install`ed or bundled binaries that live outside any git
//     checkout. Wrong for forks (module path keeps the upstream name), which
//     fails SAFE for this guard: a fork's binary refusing upstream-named
//     issues refuses nothing real.
//
// Returns "" when neither source resolves; callers treat "" as
// "identity unknown — guard disabled" and must not refuse anything.
func ResolveSelfRepoSlug() string {
	if slug := selfRepoFromExecutable(); slug != "" {
		return slug
	}
	return selfRepoFromBuildInfo()
}

// selfRepoFromExecutable walks from the running executable's real path up to
// the first directory containing .git and returns that repo's origin slug.
func selfRepoFromExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			svc, err := gitsvc.NewService(dir)
			if err != nil {
				return ""
			}
			slug, err := svc.RemoteRepoSlug()
			if err != nil {
				return ""
			}
			return slug
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// selfRepoFromBuildInfo derives "owner/repo" from the main module path.
func selfRepoFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Path == "" {
		return ""
	}
	parts := strings.Split(info.Main.Path, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}
