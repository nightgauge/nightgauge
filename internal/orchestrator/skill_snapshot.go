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
	"github.com/nightgauge/nightgauge/internal/runstate"
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
// worktree, so no stage's tools touch it, and its files are read-only on disk.
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
			_ = os.RemoveAll(tmp)
			return "", err
		}
	} else if err := copySkillTree(src, filepath.Join(tmp, "skills")); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("copy %s: %w", src, err)
	}
	if err := freezeTree(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
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

// freezeTree makes every file 0444. Directories stay owner-writable so any
// cleanup of checkout state (RemoveAll) can still delete the snapshot; the
// protection is the location outside the worktree, the mode is a backstop.
func freezeTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chmod(path, 0o444)
	})
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
		_ = os.RemoveAll(filepath.Join(base, e.Name()))
	}
}

// skillSnapshotResumes reports whether runID continues an earlier run of
// issue, judged from where the scheduler took runID (#2813). A platform-supplied
// ID names this issue's run, and a new platform run carries a new ID. rs is the
// run-state record that supplied runID, nil unless it did; it is a resume only
// when that record names issue and is still running or paused. A record for
// another issue, or a finished one, was inherited, not resumed.
func skillSnapshotResumes(fromRemote bool, rs *runstate.RunState, issue int) bool {
	if fromRemote {
		return true
	}
	return rs != nil && rs.IssueNumber == issue &&
		(rs.State == runstate.StateRunning || rs.State == runstate.StatePaused)
}

// resetSkillSnapshotForRun prepares issue's snapshot directory for runID
// before the run's first render; the caller decides resumed with
// skillSnapshotResumes from the run ID's provenance. It never reads run state
// itself: the checkout's record names whichever issue wrote it last.
//
// A resumed run adopts the issue's existing snapshot whatever its key, because
// the run-state record can carry a different ID from the one the run started
// under (MarkRunning mints its own). Re-copying on resume would freeze the
// worktree as an earlier stage left it, which is the hole this closes (#2813).
// A run that is not a resume discards the issue's snapshots, so an inherited ID
// never reuses an old copy and the run sees updated skills.
// Best-effort: a failure leaves frozen content, never live content.
func resetSkillSnapshotForRun(workspaceRoot string, issueNumber int, runID string, resumed bool) {
	if runID == "" || issueNumber <= 0 {
		return
	}
	base, ok := skillSnapshotIssueDir(workspaceRoot, issueNumber)
	if !ok {
		return
	}
	if !resumed {
		_ = os.RemoveAll(base)
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
	if err := os.Rename(filepath.Join(base, pick), filepath.Join(base, key)); err != nil {
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
		_ = os.RemoveAll(base)
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
