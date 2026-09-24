package reclaim

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StashSkipReason explains why the sweep left a stash in place. Every skip is
// reported: a leak that is silently skipped is the leak this sweep exists to
// end.
type StashSkipReason string

const (
	// StashSkipUnowned — the message carries no canonical marker, so the
	// pipeline cannot prove it created this stash. Ownership is UNDETERMINED
	// and the sweep never acts on it (#323). An operator's own `git stash` and
	// a pre-marker pipeline stash are indistinguishable, and the conservative
	// answer is the only safe one when the action is destructive.
	StashSkipUnowned StashSkipReason = "unowned"
	// StashSkipOtherIssue — owned, but belongs to a different issue than the
	// one this scoped sweep was asked about.
	StashSkipOtherIssue StashSkipReason = "other-issue"
	// StashSkipDirtyTree — restoring would apply the stash on top of
	// uncommitted changes, which is how a pop turns into a conflict the caller
	// cannot resolve. The stash stays; the operator pops it deliberately.
	StashSkipDirtyTree StashSkipReason = "dirty-tree"
	// StashSkipOtherBranch — the stash was recorded on a branch other than
	// the one checked out, or one of the two cannot be named (detached HEAD).
	// A clean tree is no protection here: the stash stack is shared by every
	// worktree and session, and popping a `feat/…` baseline onto `main`
	// conflicts on a tree nobody touched (#1938).
	StashSkipOtherBranch StashSkipReason = "other-branch"
	// StashSkipRestoreFailed — `git stash pop` refused (conflict, missing
	// parent). Git keeps the stash on failure, but a conflicting pop has
	// already written markers and unmerged entries into the tree; the sweep
	// puts the tree back as it found it before recording this skip, and
	// reports an error if it could not.
	StashSkipRestoreFailed StashSkipReason = "restore-failed"
)

// StashAction is what the sweep does with a stash it owns.
type StashAction string

const (
	// StashRestore pops the stash back into the working tree. The default,
	// because it is the only non-destructive answer: a baseline stash holds
	// work a stage moved out of the way and never moved back.
	StashRestore StashAction = "restore"
	// StashDrop discards the stash. Explicit opt-in only — this destroys
	// whatever the stage stashed.
	StashDrop StashAction = "drop"
)

// ReclaimedStash records one stash the sweep restored or dropped.
type ReclaimedStash struct {
	Ref     string       `json:"ref"`
	Message string       `json:"message"`
	Issue   int          `json:"issue"`
	Stage   string       `json:"stage,omitempty"`
	Purpose StashPurpose `json:"purpose,omitempty"`
	Action  StashAction  `json:"action"`
	AgeDays int          `json:"ageDays"`
}

// SkippedStash records one stash the sweep deliberately left alone.
type SkippedStash struct {
	Ref     string          `json:"ref"`
	Message string          `json:"message"`
	Reason  StashSkipReason `json:"reason"`
	AgeDays int             `json:"ageDays"`
}

// StashSweepResult summarises one pass over a repo's stash stack.
type StashSweepResult struct {
	RepoRoot  string           `json:"repoRoot"`
	Action    StashAction      `json:"action"`
	Scanned   int              `json:"scanned"`
	Reclaimed []ReclaimedStash `json:"reclaimed"`
	Skipped   []SkippedStash   `json:"skipped"`
	Errors    []string         `json:"errors,omitempty"`
	DryRun    bool             `json:"dryRun"`
}

// StashSweepOptions configures one pass.
type StashSweepOptions struct {
	// RepoRoot is the repository whose stash stack is swept.
	RepoRoot string
	// Issue narrows the sweep to one issue's stashes. Zero sweeps every
	// pipeline-owned stash in the repo.
	Issue int
	// Action selects restore (default) or drop.
	Action StashAction
	// DryRun classifies without touching the stash stack.
	DryRun bool
	// Now overrides the clock for age reporting. Zero means time.Now.
	Now time.Time
}

// SweepPipelineStashes reclaims the pipeline-owned stashes in RepoRoot.
//
// The reconcile half of stash lifecycle, mirroring SweepMergedWorktrees. The
// inline half (ResetPipeline popping the baseline stash, #289 AC5) runs on the
// path a finished stage walks; a stage killed mid-run never reaches it, and
// this pass is what catches those. It cannot be replaced by "reclaim harder at
// exit" — a SIGKILL runs no deferred code at all, which is exactly how the
// five leaked stashes in the audit were created.
//
// Refs shift as stashes are removed: dropping stash@{1} renumbers stash@{2} to
// stash@{1}. Acting on a captured list would therefore reclaim the WRONG stash
// from the second removal onward — the loop re-lists after every mutation and
// matches by message so it always acts on the stash it classified.
func SweepPipelineStashes(opts StashSweepOptions) (StashSweepResult, error) {
	action := opts.Action
	if action == "" {
		action = StashRestore
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	res := StashSweepResult{RepoRoot: opts.RepoRoot, Action: action, DryRun: opts.DryRun}
	if opts.RepoRoot == "" {
		return res, fmt.Errorf("stash sweep: repo root is required")
	}

	entries, err := ListStashes(opts.RepoRoot)
	if err != nil {
		// "I could not look" is never "there are none" — the caller must not
		// report a clean stash stack it never read.
		return res, fmt.Errorf("stash sweep: %w", err)
	}
	res.Scanned = len(entries)

	// Only a restore touches the tree, so only a restore asks whose branch
	// the stash came from. Drop is branch-blind by design.
	var head string
	if action == StashRestore {
		head, err = currentBranch(opts.RepoRoot)
		if err != nil {
			return res, fmt.Errorf("stash sweep: %w", err)
		}
	}

	// Classify everything up front against the ORIGINAL listing so the report
	// describes the stack as it was found, then act one stash at a time.
	var targets []StashEntry
	for _, e := range entries {
		age := ageDays(e, now)
		switch {
		case !e.Owned:
			res.Skipped = append(res.Skipped, SkippedStash{Ref: e.Ref, Message: e.Message, Reason: StashSkipUnowned, AgeDays: age})
		case opts.Issue > 0 && e.Issue != opts.Issue:
			res.Skipped = append(res.Skipped, SkippedStash{Ref: e.Ref, Message: e.Message, Reason: StashSkipOtherIssue, AgeDays: age})
		case action == StashRestore && (head == "" || e.Branch != head):
			res.Skipped = append(res.Skipped, SkippedStash{Ref: e.Ref, Message: e.Message, Reason: StashSkipOtherBranch, AgeDays: age})
		default:
			targets = append(targets, e)
		}
	}

	for _, e := range targets {
		age := ageDays(e, now)
		if opts.DryRun {
			res.Reclaimed = append(res.Reclaimed, reclaimedFrom(e, action, age))
			continue
		}
		var before string
		if action == StashRestore {
			var dirty bool
			var statusErr error
			before, dirty, statusErr = treeIsDirty(opts.RepoRoot)
			if statusErr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: read working tree: %v", e.Ref, statusErr))
				res.Skipped = append(res.Skipped, SkippedStash{Ref: e.Ref, Message: e.Message, Reason: StashSkipRestoreFailed, AgeDays: age})
				continue
			}
			if dirty {
				res.Skipped = append(res.Skipped, SkippedStash{Ref: e.Ref, Message: e.Message, Reason: StashSkipDirtyTree, AgeDays: age})
				continue
			}
		}
		ref, found := currentRef(opts.RepoRoot, e.Message)
		if !found {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: vanished from the stash stack before it could be reclaimed", e.Ref))
			continue
		}
		if action == StashRestore {
			err = popRestoringTree(opts.RepoRoot, ref, before)
		} else {
			err = runStashAction(opts.RepoRoot, action, ref)
		}
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			res.Skipped = append(res.Skipped, SkippedStash{Ref: ref, Message: e.Message, Reason: StashSkipRestoreFailed, AgeDays: age})
			continue
		}
		res.Reclaimed = append(res.Reclaimed, reclaimedFrom(e, action, age))
	}

	return res, nil
}

func reclaimedFrom(e StashEntry, action StashAction, age int) ReclaimedStash {
	return ReclaimedStash{
		Ref: e.Ref, Message: e.Message, Issue: e.Issue,
		Stage: e.Stage, Purpose: e.Purpose, Action: action, AgeDays: age,
	}
}

func ageDays(e StashEntry, now time.Time) int {
	return int(e.Age(now).Hours() / 24)
}

// currentRef re-resolves a stash's selector by its message. Refs renumber on
// every removal, so the selector captured at classification time is stale the
// moment the first stash is reclaimed.
func currentRef(repoRoot, message string) (string, bool) {
	entries, err := ListStashes(repoRoot)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Message == message {
			return e.Ref, true
		}
	}
	return "", false
}

func runStashAction(repoRoot string, action StashAction, ref string) error {
	verb := "pop"
	if action == StashDrop {
		verb = "drop"
	}
	cmd := exec.Command("git", "stash", verb, ref)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git stash %s %s: %w: %s", verb, ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// treeIsDirty reports whether the working tree holds anything a `git stash
// pop` could collide with. Pipeline exhaust does not count: a scaffolded
// knowledge README has no business blocking the restore of a stage's real
// work, and treating it as a collision is the same mistake `worktree sweep`
// made (#332).
//
// The porcelain is returned too: it is the tree a failed pop must be put back
// to, and re-reading it later would read whatever the pop left behind.
func treeIsDirty(repoRoot string) (string, bool, error) {
	porcelain, err := statusPorcelain(repoRoot)
	if err != nil {
		return "", false, err
	}
	return porcelain, ClassifyStatus(porcelain).Blocked(), nil
}

func statusPorcelain(repoRoot string) (string, error) {
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	return string(out), err
}

// currentBranch returns the short name of the checked-out branch, or "" for a
// detached HEAD.
func currentBranch(repoRoot string) (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil // detached
	}
	if err != nil {
		return "", fmt.Errorf("read current branch in %s: %w", repoRoot, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// popRestoringTree pops ref and, if the pop fails, puts the tree back to
// before — the porcelain read just ahead of it.
//
// A conflicting `git stash pop` keeps the stash but has already written
// conflict markers, left the index unmerged, and (for a stash taken with
// --include-untracked) written the stash's untracked files. Returning at that
// point hands the operator a broken checkout they did not ask for (#1938).
//
// The rollback is only sound because the caller has proved there are no
// tracked changes to lose: HEAD is the tracked state to return to, and the
// untracked files to remove are exactly the stash's own that were not there
// before. Both stay recoverable from the stash git kept.
func popRestoringTree(repoRoot, ref, before string) error {
	if ClassifyStatus(before).Blocked() {
		return fmt.Errorf("git stash pop %s: refused, the tree has changes a failed pop could not be rolled back over", ref)
	}
	sha, err := gitOutput(repoRoot, "rev-parse", "--verify", "-q", ref)
	if err != nil {
		return fmt.Errorf("git stash pop %s: resolve stash commit: %w", ref, err)
	}
	sha = strings.TrimSpace(sha)
	popErr := runStashAction(repoRoot, StashRestore, ref)
	if popErr == nil {
		return nil
	}
	if rbErr := rollbackFailedPop(repoRoot, sha, before); rbErr != nil {
		return fmt.Errorf("%w; the working tree was NOT restored: %v", popErr, rbErr)
	}
	return popErr
}

func rollbackFailedPop(repoRoot, sha, before string) error {
	// One-way reset of index and tracked files to HEAD, the same operation as
	// `reset --hard` without touching untracked files.
	if _, err := gitOutput(repoRoot, "read-tree", "--reset", "-u", "HEAD"); err != nil {
		return fmt.Errorf("reset tracked files: %w", err)
	}
	present := map[string]bool{}
	for _, p := range StatusPaths(before) {
		present[p] = true
	}
	if _, err := gitOutput(repoRoot, "rev-parse", "--verify", "-q", sha+"^3"); err == nil {
		names, lsErr := gitOutput(repoRoot, "ls-tree", "-r", "-z", "--name-only", sha+"^3")
		if lsErr != nil {
			return fmt.Errorf("list the stash's untracked files: %w", lsErr)
		}
		for _, p := range strings.Split(names, "\x00") {
			if p == "" || present[p] {
				continue
			}
			if rmErr := os.Remove(filepath.Join(repoRoot, filepath.FromSlash(p))); rmErr != nil && !os.IsNotExist(rmErr) {
				return fmt.Errorf("remove %s: %w", p, rmErr)
			}
		}
	}
	after, err := statusPorcelain(repoRoot)
	if err != nil {
		return fmt.Errorf("re-read working tree: %w", err)
	}
	if after != before {
		return fmt.Errorf("status differs after rollback:\n%s", strings.TrimSpace(after))
	}
	return nil
}

func gitOutput(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	return string(out), err
}
