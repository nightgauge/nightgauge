package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// Apply-time preconditions for the destructive hygiene remedies (ADR-025 § 8,
// TOCTOU). A finding is a claim about the world at scan time; these re-derive
// the claim immediately before a verb acts, and refuse when it no longer
// holds. They only read: nothing here signals a process or deletes a ref. The
// remedy engine runs them as each verb's Precondition.

// processIdentity is what a terminate precondition re-reads about a pid.
type processIdentity struct {
	Command string
	UID     int
}

// processLookup reads one pid's identity. An error means the pid could not be
// read (it exited, or `ps` failed); either way it must not be signalled.
type processLookup func(pid int) (processIdentity, error)

// psProcessLookup reads a pid's owner and command line from `ps`.
func psProcessLookup(pid int) (processIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "uid=", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return processIdentity{}, fmt.Errorf("pid %d could not be read: %w", pid, err)
	}
	line := strings.TrimSpace(string(out))
	uidField, command, ok := strings.Cut(line, " ")
	if !ok {
		return processIdentity{}, fmt.Errorf("pid %d: unexpected ps output %q", pid, line)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(uidField))
	if err != nil {
		return processIdentity{}, fmt.Errorf("pid %d: unreadable uid %q", pid, uidField)
	}
	return processIdentity{Command: strings.TrimSpace(command), UID: uid}, nil
}

// terminatePrecondition decides, immediately before signalling, whether pid
// may be terminated: it must still be a nightgauge binary (basename of argv[0],
// the same test the scan uses) owned by the current user. The pid may have
// been reused since the scan, so both are re-read here rather than trusted
// from the finding. The verb signals the single pid only, never its process
// group.
func terminatePrecondition(pid int, lookup processLookup) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	if pid == os.Getpid() {
		return fmt.Errorf("refusing to signal this doctor process (pid %d)", pid)
	}
	if lookup == nil {
		lookup = psProcessLookup
	}
	id, err := lookup(pid)
	if err != nil {
		return err
	}
	if !(runningProcess{PID: pid, Command: id.Command}).isNightgauge() {
		return fmt.Errorf("pid %d is no longer a nightgauge process (command %q); the pid may have been reused", pid, id.Command)
	}
	if uid := os.Getuid(); uid < 0 || id.UID != uid {
		return fmt.Errorf("pid %d is owned by uid %d, not the current user", pid, id.UID)
	}
	return nil
}

// branchDeletePrecondition re-derives, at apply time, the proof that branch in
// repoRoot is merged — the proof `scripts/branch-merged-check.sh` applies: its
// content is in the base ref, or a merged PR covers its head. It refuses the
// default branch, main and master, and any branch checked out in a worktree.
func branchDeletePrecondition(repoRoot, branch string, door execution.MergedPRLookup) error {
	if branch == "" || branch == "main" || branch == "master" {
		return fmt.Errorf("refusing to delete branch %q", branch)
	}
	scan, err := execution.ScanStrandedBranches(execution.StrandedBranchOptions{RepoRoot: repoRoot, MergedPRLookup: door})
	if err != nil {
		return fmt.Errorf("could not re-derive the merged proof for %s: %w", branch, err)
	}
	for _, b := range scan.Stranded {
		if b.Name == branch {
			return nil
		}
	}
	for _, k := range scan.Kept {
		if k.Name == branch {
			return fmt.Errorf("branch %s is no longer provably merged and unheld (%s)", branch, k.Reason)
		}
	}
	return fmt.Errorf("branch %s no longer exists in %s", branch, repoRoot)
}
