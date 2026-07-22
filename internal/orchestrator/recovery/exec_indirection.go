package recovery

import (
	"context"
	"os/exec"
)

// execGh is the indirection point for `gh`-backed recovery actions so tests
// can stub GitHub CLI calls. Mirrors gates.execGh's pattern (Issue #3266).
// Default implementation runs the real `gh` binary.
//
// Tests assign a replacement that returns canned stdout/stderr.
//
// Cross-repo invocations (Issue #3683): callers needing to act against a
// non-current repository pass `--repo <owner/repo>` as part of args. No
// separate indirection is needed — the existing variadic signature covers
// arbitrary gh flag combinations.
var execGh = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}

// execGit is the indirection point for `git`-backed recovery actions. Mirrors
// gates.execGitForGate's pattern. The dir argument is the workdir for the
// command; tests typically pass an empty string and ignore it.
var execGit = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// execNightgauge is the indirection point for the local nightgauge
// binary. Used by recovery actions that need to invoke deterministic CLI
// subcommands (e.g. project move-status). Tests stub this with a no-op or
// canned response.
var execNightgauge = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nightgauge", args...)
	return cmd.Output()
}
