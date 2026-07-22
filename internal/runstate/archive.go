package runstate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ArchiveRun moves every live context file for this run's issue into
// .nightgauge/pipeline/history/<runId>/ and writes a final
// run-state.json snapshot inside the archive directory for forensics.
//
// Idempotent: if a file is missing it is skipped. Returns the absolute
// archive directory path on success.
func ArchiveRun(baseDir string, rs *RunState) (string, error) {
	if rs == nil {
		return "", fmt.Errorf("nil run state")
	}
	archiveDir := filepath.Join(baseDir, "history", rs.RunID)
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return "", fmt.Errorf("create archive dir: %w", err)
	}

	suffix := fmt.Sprintf("%d.json", rs.IssueNumber)
	entries, err := os.ReadDir(baseDir)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("readdir base: %w", err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if name == FileName {
			// run-state.json itself stays in place (final snapshot is also
			// written into the archive dir below).
			continue
		}
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		src := filepath.Join(baseDir, name)
		dst := filepath.Join(archiveDir, name)
		if err := os.Rename(src, dst); err != nil {
			// Cross-device rename on some filesystems — fall back to copy + unlink.
			data, readErr := os.ReadFile(src)
			if readErr != nil {
				return "", fmt.Errorf("rename %s: %w (fallback read also failed: %v)", name, err, readErr)
			}
			if writeErr := os.WriteFile(dst, data, 0644); writeErr != nil {
				return "", fmt.Errorf("rename %s: %w (fallback write failed: %v)", name, err, writeErr)
			}
			_ = os.Remove(src)
		}
	}

	// Snapshot final run-state inside the archive directory.
	data, err := jsonMarshalIndent(rs)
	if err != nil {
		return "", fmt.Errorf("marshal run-state snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := AtomicWriteFile(filepath.Join(archiveDir, FileName), data, 0644); err != nil {
		return "", err
	}
	return archiveDir, nil
}

// CleanupBranchAndWorktree handles the destructive parts of a discard:
// remove worktree (if recorded), delete local branch, attempt to delete the
// remote tracking branch. All steps are best-effort — one failure does not
// block the others. Returns the first error encountered or nil.
//
// Protected branches (main, master) are never deleted.
func CleanupBranchAndWorktree(repoRoot string, rs *RunState) error {
	if rs == nil {
		return fmt.Errorf("nil run state")
	}
	var firstErr error

	if rs.WorktreePath != nil && *rs.WorktreePath != "" {
		// Best-effort git worktree remove — fall back to rm -rf if needed.
		cmd := exec.Command("git", "worktree", "remove", *rs.WorktreePath, "--force")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			// Manual cleanup if git refuses.
			if rmErr := os.RemoveAll(*rs.WorktreePath); rmErr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("worktree remove: %s: %w (manual cleanup failed: %v)",
						strings.TrimSpace(string(out)), err, rmErr)
				}
			}
			// Prune dangling refs.
			pruneCmd := exec.Command("git", "worktree", "prune")
			pruneCmd.Dir = repoRoot
			_ = pruneCmd.Run()
		}
	}

	if rs.Branch != "" && rs.Branch != "main" && rs.Branch != "master" {
		// Local branch
		delLocal := exec.Command("git", "branch", "-D", rs.Branch)
		delLocal.Dir = repoRoot
		if err := delLocal.Run(); err != nil && firstErr == nil {
			// Not fatal — branch may not exist locally.
		}
		// Remote branch (best-effort)
		delRemote := exec.Command("git", "push", "origin", "--delete", rs.Branch)
		delRemote.Dir = repoRoot
		_ = delRemote.Run()
	}

	return firstErr
}
