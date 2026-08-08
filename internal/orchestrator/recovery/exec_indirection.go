package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// execGitToFile streams a git command's stdout straight into destPath instead of
// buffering it, for outputs whose size is bounded by repository content rather
// than by anything this process controls — `cat-file blob` on a conflicting
// file, above all. execGit would read a multi-gigabyte blob entirely into
// memory (#301 review).
//
// destPath's parent must already exist. A non-zero exit leaves the partial file
// in place for the caller to clean up; callers treat any error as "this blob was
// not preserved".
var execGitToFile = func(ctx context.Context, dir, destPath string, args ...string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = f
	runErr := cmd.Run()
	closeErr := f.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}

// gitErrDetail renders a git failure the way an operator needs to read it:
// the exit status PLUS the stderr git actually wrote.
//
// execGit uses cmd.Output(), which returns stdout only, and an *exec.ExitError's
// Error() is the bare string "exit status 1". Git puts its entire diagnosis on
// stderr — `git rebase` refusing a dirty index prints NOTHING on stdout and
// "error: cannot rebase: Your index contains uncommitted changes." on stderr —
// so an escalation built from err.Error() and stdout carries no diagnosis at
// all. That is a contentless escalation replacing a wrong success, which is only
// half a fix for a silent-no-op issue (#301 round-2 advisory).
//
// cmd.Output() captures stderr into ExitError.Stderr precisely so this is
// possible; nothing else needs to change at the call sites.
func gitErrDetail(err error) string {
	if err == nil {
		return ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
			return fmt.Sprintf("%s: %s", err.Error(), stderr)
		}
	}
	return err.Error()
}

// execNightgauge is the indirection point for the local nightgauge
// binary. Used by recovery actions that need to invoke deterministic CLI
// subcommands (e.g. project move-status). Tests stub this with a no-op or
// canned response.
var execNightgauge = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nightgauge", args...)
	return cmd.Output()
}
