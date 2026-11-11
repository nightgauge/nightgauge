package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// skillSnapshotsDir is the checkout-state subdirectory holding one frozen
// copy of the worktree's skills/ tree per run (#2813).
const skillSnapshotsDir = "skillsnap"

// skillSnapshotFallbackBase, when set, roots snapshots for a workspace that
// has no git checkout state. It is nil in production, where a workspace without
// checkout state fails closed; package tests set it so scheduler harnesses on
// plain temp directories still render from a frozen copy.
var skillSnapshotFallbackBase func() (string, error)

func fallbackSnapshotBase(workspaceRoot string, issueNumber int) (string, error) {
	root, err := skillSnapshotFallbackBase()
	if err != nil {
		return "", err
	}
	base := filepath.Join(root, snapshotKey(workspaceRoot), strconv.Itoa(issueNumber))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return base, nil
}

// snapshotKey is a short, path-safe key for runID. Rendered skills embed
// absolute include paths, so every character of the snapshot path is paid in
// tokens on every dispatch; a 12-hex digest keeps that cost small.
func snapshotKey(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return hex.EncodeToString(sum[:6])
}

// runSkillSnapshot returns a workspace root whose skills/ directory is a copy
// of liveRoot/skills taken the first time it is asked for during runID, so
// every stage of the run renders the instructions that existed at run start.
//
// Without it an earlier stage, which has write access to the worktree, could
// rewrite a later stage's SKILL.md (or an include, or skills/_shared) and change
// what that stage executes: a privilege boundary between stages that held only
// by convention. The snapshot lives in git-dir checkout state, outside the
// worktree, so no stage's tools touch it, and its tree (files and directories) is read-only on disk.
//
// The snapshot is keyed by the persisted run ID, so a resumed run in a new
// process reuses the copy taken at run start instead of copying an edited tree.
// Creation is atomic (copy to a temp sibling, then rename) and losing a rename
// race keeps the winner. Symlinks are skipped: one would point back into the
// live worktree and reopen the hole. Snapshots of the same issue's earlier
// runs are pruned; other issues' snapshots are never touched.
//
// When liveRoot has no skills/ directory the snapshot is an empty skills/, so
// the absence is frozen too and a later stage cannot introduce skills mid-run.
func runSkillSnapshot(workspaceRoot, liveRoot string, issueNumber int, runID string) (string, error) {
	if runID == "" {
		return "", fmt.Errorf("empty run id")
	}
	key := snapshotKey(runID)
	// Resolve a symlinked skills/ root itself (WalkDir would not descend it);
	// it is still copied once, so the content is frozen. Symlinks inside the
	// tree are skipped by copySkillTree.
	// A worktree with no skills/ at run start is frozen as an empty copy, not
	// skipped: skipping would let the first render after an earlier stage
	// created skills/ copy that stage's files into the run's snapshot.
	src, err := filepath.EvalSymlinks(filepath.Join(liveRoot, "skills"))
	if err == nil {
		if info, statErr := os.Stat(src); statErr != nil || !info.IsDir() {
			src = ""
		}
	} else {
		src = ""
	}
	if issueNumber <= 0 {
		return "", fmt.Errorf("invalid issue number %d", issueNumber)
	}
	// One directory per issue: concurrent runs of different issues share the
	// checkout, so neither the copy nor the prune may cross issues (#2813).
	base, err := layout.CheckoutSubdir(workspaceRoot,
		filepath.Join(skillSnapshotsDir, strconv.Itoa(issueNumber)))
	if err != nil && skillSnapshotFallbackBase != nil {
		base, err = fallbackSnapshotBase(workspaceRoot, issueNumber)
	}
	if err != nil {
		return "", err
	}
	dst := filepath.Join(base, key)
	if info, err := os.Stat(filepath.Join(dst, "skills")); err == nil && info.IsDir() {
		return dst, nil
	}

	tmp, err := os.MkdirTemp(base, key+".tmp-")
	if err != nil {
		return "", err
	}
	if src == "" {
		if err := os.Mkdir(filepath.Join(tmp, "skills"), 0o755); err != nil {
			_ = removeSnapshotTree(tmp)
			return "", err
		}
	} else if err := copySkillTree(src, filepath.Join(tmp, "skills")); err != nil {
		_ = removeSnapshotTree(tmp)
		return "", fmt.Errorf("copy %s: %w", src, err)
	}
	if err := freezeTree(tmp); err != nil {
		_ = removeSnapshotTree(tmp)
		return "", err
	}
	if err := renameFrozen(tmp, dst); err != nil {
		_ = removeSnapshotTree(tmp)
		// A concurrent stage of the same run won the race: keep its copy.
		if info, statErr := os.Stat(filepath.Join(dst, "skills")); statErr == nil && info.IsDir() {
			return dst, nil
		}
		return "", err
	}
	pruneSkillSnapshots(base, key)
	return dst, nil
}

// copySkillTree copies regular files and directories from src to dst,
// skipping symlinks and any other non-regular entry.
func copySkillTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			return copyRegularFile(path, target)
		default:
			return nil // symlinks, devices, sockets: never followed or copied
		}
	})
}

func copyRegularFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// freezeTree makes every regular file 0444 and every directory, including
// root itself, 0555 (#3088). Unlinking or renaming an entry needs write
// permission on its parent directory, so frozen files alone would still let a
// stage remove or rename-replace SKILL.md. Directories are chmodded after
// their contents (post-order) because the walk must still read them. The
// protection is the location outside the worktree; the mode is a backstop.
// Remove a frozen tree with removeSnapshotTree and rename it with renameFrozen.
func freezeTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return os.Chmod(path, 0o444)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

// thawDirs makes every directory under root (root included) owner-writable so
// its entries can be removed. WalkDir visits a directory before reading it, so
// a pre-order chmod reaches every level. Files need no chmod: unlinking needs
// only write permission on the parent directory.
func thawDirs(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if err := os.Chmod(path, 0o700); err != nil && path == root {
				return err
			}
		}
		return nil
	})
}

// removeSnapshotTree removes path, a snapshot or a directory of snapshots,
// whose directories freezeTree made read-only.
func removeSnapshotTree(path string) error {
	thawDirs(path)
	return os.RemoveAll(path)
}

// renameFrozen renames a frozen snapshot root. Some platforms (darwin)
// require write permission on a directory being moved to a new parent entry,
// so the root is thawed for the rename and re-frozen afterwards.
func renameFrozen(oldPath, newPath string) error {
	if err := os.Chmod(oldPath, 0o700); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		_ = os.Chmod(oldPath, 0o555)
		return err
	}
	return os.Chmod(newPath, 0o555)
}

// pruneSkillSnapshots removes every snapshot in one issue's directory that
// does not belong to keep. An issue has one run in flight at a time, so an
// earlier run's copy of that issue is never read again; base is per issue, so
// a concurrent run of another issue keeps its copy. Best-effort: a failure leaves a stale copy, never a wrong one.
func pruneSkillSnapshots(base, keep string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == keep || strings.HasPrefix(e.Name(), keep+".tmp-") {
			continue
		}
		_ = removeSnapshotTree(filepath.Join(base, e.Name()))
	}
}

// resetSkillSnapshotForRun prepares issue's snapshot directory for runID
// before the run's first render. resumed is beginRunLifecycle's decision: true
// exactly when this run moved a paused or interrupted record of THIS issue back
// to running, which is also when it skips the stages that already ran. It never
// reads run state itself: the checkout's record names whichever issue wrote it
// last.
//
// A resumed run adopts the issue's existing snapshot whatever its key, because
// the resuming run can carry a different ID from the one the run started under
// (a platform re-dispatch brings a new one; MarkRunning mints its own).
// Re-copying on resume would freeze the worktree as an earlier stage left it,
// which is the hole this closes (#2813). A run that resumed nothing starts at
// its first stage, so it discards the issue's snapshots and renders current
// skills. Best-effort: a failure leaves frozen content, never live content.
func resetSkillSnapshotForRun(workspaceRoot string, issueNumber int, runID string, resumed bool) {
	if runID == "" || issueNumber <= 0 {
		return
	}
	base, ok := skillSnapshotIssueDir(workspaceRoot, issueNumber)
	if !ok {
		return
	}
	if !resumed {
		_ = removeSnapshotTree(base)
		return
	}
	adoptSkillSnapshot(base, snapshotKey(runID))
}

// adoptSkillSnapshot renames the issue's one complete snapshot to key when no
// snapshot already carries it. Pruning keeps at most one per issue; with
// several (a failed prune), the newest is adopted and the rest are removed.
func adoptSkillSnapshot(base, key string) {
	if info, err := os.Stat(filepath.Join(base, key, "skills")); err == nil && info.IsDir() {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	var pick string
	var pickTime int64
	for _, e := range entries {
		if !e.IsDir() || strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		info, err := os.Stat(filepath.Join(base, e.Name(), "skills"))
		if err != nil || !info.IsDir() {
			continue
		}
		if t := info.ModTime().UnixNano(); pick == "" || t > pickTime {
			pick, pickTime = e.Name(), t
		}
	}
	if pick == "" {
		return
	}
	if err := renameFrozen(filepath.Join(base, pick), filepath.Join(base, key)); err != nil {
		return
	}
	pruneSkillSnapshots(base, key)
}

// dropSkillSnapshot removes issue's snapshots when its run ends in a state that
// is never resumed (completed or aborted), so checkout state does not keep a
// full skills/ copy for every issue it has run. A paused run keeps its copy.
func dropSkillSnapshot(workspaceRoot string, issueNumber int) {
	if issueNumber <= 0 {
		return
	}
	if base, ok := skillSnapshotIssueDir(workspaceRoot, issueNumber); ok {
		_ = removeSnapshotTree(base)
	}
}

// skillSnapshotIssueDir is issue's snapshot directory, without creating it.
func skillSnapshotIssueDir(workspaceRoot string, issueNumber int) (string, bool) {
	sub := filepath.Join(skillSnapshotsDir, strconv.Itoa(issueNumber))
	base, err := layout.CheckoutPath(workspaceRoot, sub)
	if err != nil {
		if skillSnapshotFallbackBase == nil {
			return "", false
		}
		if base, err = fallbackSnapshotBase(workspaceRoot, issueNumber); err != nil {
			return "", false
		}
	}
	return base, true
}
