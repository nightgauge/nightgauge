package gitworktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BaseCommitFile is the name of the file, inside a linked worktree's own git
// admin directory ($GIT_COMMON_DIR/worktrees/<id>/), that records the commit
// the worktree was created at (#1825).
//
// The OpenCode project-config tamper gate diffs opencode.json and .opencode/
// against this commit. Comparing against the repository's live default-branch
// ref instead was wrong both ways: a worktree created from a primary checkout
// whose HEAD already carries an unmerged config change was refused before any
// stage ran, and a stage able to move refs/remotes/origin/<default> could hide
// a committed tamper from the next dispatch.
//
// The admin directory is the right home: it sits outside the worktree's tree,
// so no file tool scoped to the worktree reaches it, no ref update moves it,
// and git deletes it with the worktree's registration (remove or prune), so
// the record never outlives the worktree it describes.
const BaseCommitFile = "nightgauge-base-commit"

// RecordBaseCommit writes sha as worktreeDir's creation commit. worktreeDir
// must be a linked worktree: the primary checkout's admin directory is shared
// by every worktree of the repository, so recording there would describe none
// of them, and it is refused.
func RecordBaseCommit(worktreeDir, sha string) error {
	cmd := exec.Command("git", "rev-parse", "--absolute-git-dir", "--git-common-dir")
	cmd.Dir = worktreeDir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve git admin dir of %s: %w", worktreeDir, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return fmt.Errorf("resolve git admin dir of %s: unexpected rev-parse output %q", worktreeDir, out)
	}
	gitDir, commonDir := lines[0], lines[1]
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreeDir, commonDir)
	}
	if sameDir(gitDir, commonDir) {
		return fmt.Errorf("record base commit: %s is not a linked worktree", worktreeDir)
	}
	return os.WriteFile(filepath.Join(gitDir, BaseCommitFile), []byte(strings.TrimSpace(sha)+"\n"), 0o600)
}

func sameDir(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ai, bi)
}
