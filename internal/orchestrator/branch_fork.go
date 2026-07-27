package orchestrator

// Branch-fork detection and orphaned-push reclamation (#163).
//
// A stage killed mid-`git push` may have ALREADY pushed when the signal lands.
// The kill discards the local run; it does not discard the remote commit. The
// next attempt branches from the base again, regenerates the implementation,
// and only discovers the problem when ITS push is rejected as non-fast-forward
// — after a full pipeline's tokens are spent on a guaranteed rejection. The
// same end state arrives from a second, unrelated cause: an operator pushing to
// a pipeline-owned branch. Two causes, one unrecoverable shape.
//
// Two deterministic primitives address it, and they share their evidence so
// they can never disagree about who owns a commit:
//
//   - CheckBranchFork — one `git ls-remote` before a stage does work. If the
//     remote branch head is not reachable from the local branch tip, the branch
//     has forked and every downstream push is already doomed. Diagnose it at
//     the stage boundary for ~0 tokens instead of at push time for a full run.
//
//   - ReclaimOrphanedRemoteBranch — after a failed run, drop the remote branch
//     the pipeline itself pushed so the next attempt starts clean. It deletes
//     ONLY a remote head that is contained in the local branch's history, i.e.
//     a commit this run demonstrably authored. An operator's commit is not
//     contained locally, so it is never deleted — that case is left standing
//     for CheckBranchFork to report on the next attempt.
//
// Both are pure git shell-outs with no LLM involvement, per
// .claude/rules/scripts.md.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// branchForkGitTimeout bounds every git invocation these primitives make. The
// only networked call is `ls-remote`; a hung remote must degrade to "unknown"
// rather than stall the stage loop it was added to protect.
const branchForkGitTimeout = 30 * time.Second

// ForkState classifies the relationship between a worktree's local branch tip
// and its remote counterpart.
type ForkState string

const (
	// ForkStateClean means a push can fast-forward: either the remote branch
	// does not exist, or its head is reachable from the local tip.
	ForkStateClean ForkState = "clean"
	// ForkStateForked means the remote branch head is NOT reachable from the
	// local tip. Any push of the local tip will be rejected as non-fast-forward.
	ForkStateForked ForkState = "forked"
	// ForkStateUnknown means the comparison could not be made (no remote, no
	// network, not a git worktree). Never blocks a run — an unreachable remote
	// is an environmental condition, not a forked branch.
	ForkStateUnknown ForkState = "unknown"
)

// BranchFork is the result of one fork comparison.
type BranchFork struct {
	State     ForkState
	Branch    string
	LocalSHA  string
	RemoteSHA string
	// Detail is a single human-readable sentence naming the evidence. It is
	// embedded verbatim in the stage error, so it must be self-contained: the
	// reader of a failed run has no other record of what was compared.
	Detail string
}

// Forked reports whether the comparison confirmed a fork. Unknown is not a
// fork — the check is fail-open by construction.
func (f BranchFork) Forked() bool { return f.State == ForkStateForked }

// CheckBranchFork compares the local tip of `branch` in `dir` against the
// remote branch of the same name on origin.
//
// Cost is one `git ls-remote` (a single network round trip) plus two local
// object reads. It is deliberately fail-open: every failure to *establish* the
// comparison returns ForkStateUnknown, so an offline laptop or a repo without
// an origin never manufactures a blocking failure. Only a positive, evidenced
// answer returns ForkStateForked.
func CheckBranchFork(ctx context.Context, dir, branch string) BranchFork {
	res := BranchFork{State: ForkStateUnknown, Branch: branch}
	if dir == "" || branch == "" {
		res.Detail = "no worktree or branch to compare"
		return res
	}

	localSHA, err := gitRevParse(ctx, dir, branch)
	if err != nil {
		res.Detail = fmt.Sprintf("could not resolve local tip of %q: %v", branch, err)
		return res
	}
	res.LocalSHA = localSHA

	remoteSHA, err := remoteBranchHead(ctx, dir, branch)
	if err != nil {
		res.Detail = fmt.Sprintf("could not read origin/%s: %v", branch, err)
		return res
	}
	if remoteSHA == "" {
		res.State = ForkStateClean
		res.Detail = fmt.Sprintf("origin has no branch %q — nothing to fork from", branch)
		return res
	}
	res.RemoteSHA = remoteSHA

	contained, err := localHistoryContains(ctx, dir, remoteSHA, localSHA)
	if err != nil {
		res.Detail = fmt.Sprintf("could not compare origin/%s (%s) with local %s: %v",
			branch, shortSHA(remoteSHA), shortSHA(localSHA), err)
		return res
	}
	if contained {
		res.State = ForkStateClean
		res.Detail = fmt.Sprintf("origin/%s (%s) is reachable from local %s — push fast-forwards",
			branch, shortSHA(remoteSHA), shortSHA(localSHA))
		return res
	}

	res.State = ForkStateForked
	res.Detail = fmt.Sprintf(
		"origin/%s is at %s, which is NOT an ancestor of the local tip %s — "+
			"the remote carries a commit this worktree never saw, so every push from here is rejected as non-fast-forward",
		branch, shortSHA(remoteSHA), shortSHA(localSHA))
	return res
}

// ReclaimResult is the outcome of one orphaned-push reclamation attempt.
type ReclaimResult struct {
	Deleted   bool
	Branch    string
	RemoteSHA string
	// Reason names what happened, whether or not anything was deleted. A
	// declined reclamation is as load-bearing as a performed one: it is the
	// signal that the remote head belongs to someone else.
	Reason string
}

// ReclaimOrphanedRemoteBranch drops origin's copy of `branch` when — and only
// when — the remote head is contained in the local branch's history.
//
// Containment is the ownership proof. A stage that was killed mid-push had
// already committed locally, so the commit it managed to push is by definition
// an ancestor of (or equal to) the local tip: that is the pipeline's own
// orphan, safe to drop because the run that authored it is dead, its work was
// never validated, and the retry regenerates it from scratch. A commit that is
// NOT contained locally came from somewhere else (an operator, another
// machine); deleting it would destroy work the pipeline cannot recreate, so
// this declines and leaves it for CheckBranchFork to report.
//
// Called on failed runs only. A run that produced a PR keeps its remote branch:
// deleting it would close the PR that holds the work.
func ReclaimOrphanedRemoteBranch(ctx context.Context, dir, branch string) ReclaimResult {
	res := ReclaimResult{Branch: branch}
	if dir == "" || branch == "" {
		res.Reason = "no worktree or branch to reclaim"
		return res
	}
	if isProtectedBranch(branch) {
		res.Reason = fmt.Sprintf("refusing to delete protected branch %q", branch)
		return res
	}

	remoteSHA, err := remoteBranchHead(ctx, dir, branch)
	if err != nil {
		res.Reason = fmt.Sprintf("could not read origin/%s: %v", branch, err)
		return res
	}
	if remoteSHA == "" {
		res.Reason = fmt.Sprintf("origin has no branch %q — nothing orphaned", branch)
		return res
	}
	res.RemoteSHA = remoteSHA

	localSHA, err := gitRevParse(ctx, dir, branch)
	if err != nil {
		res.Reason = fmt.Sprintf(
			"origin/%s is at %s but the local branch is gone — cannot prove the pipeline pushed it, leaving it",
			branch, shortSHA(remoteSHA))
		return res
	}

	contained, err := localHistoryContains(ctx, dir, remoteSHA, localSHA)
	if err != nil {
		res.Reason = fmt.Sprintf("could not compare origin/%s (%s) with local %s: %v — leaving it",
			branch, shortSHA(remoteSHA), shortSHA(localSHA), err)
		return res
	}
	if !contained {
		res.Reason = fmt.Sprintf(
			"origin/%s is at %s, which this run never authored (not contained in local %s) — leaving it for fork pre-flight",
			branch, shortSHA(remoteSHA), shortSHA(localSHA))
		return res
	}

	if _, _, err := runGit(ctx, dir, "push", "origin", "--delete", branch); err != nil {
		res.Reason = fmt.Sprintf("deleting origin/%s (%s) failed: %v", branch, shortSHA(remoteSHA), err)
		return res
	}

	res.Deleted = true
	res.Reason = fmt.Sprintf(
		"dropped origin/%s (%s) — the pipeline's own orphaned push; the next attempt starts from a clean base",
		branch, shortSHA(remoteSHA))
	return res
}

// isProtectedBranch reports whether a branch must never be deleted by the
// pipeline. Epic branches are included: they are the shared base for every
// sub-issue in the epic, so one sub-issue's failed run must not remove the
// ground the others are standing on.
func isProtectedBranch(branch string) bool {
	switch branch {
	case "", "main", "master", "HEAD", "develop":
		return true
	}
	return strings.HasPrefix(branch, "epic/")
}

// remoteBranchHead returns origin's SHA for `branch`, or "" when origin has no
// such branch. One network round trip.
func remoteBranchHead(ctx context.Context, dir, branch string) (string, error) {
	out, _, err := runGit(ctx, dir, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "refs/heads/"+branch {
			return fields[0], nil
		}
	}
	return "", nil
}

// gitRevParse resolves a local branch name to a commit SHA. It resolves
// refs/heads/<branch> explicitly rather than HEAD: the caller may be running in
// the main workspace root while the branch is checked out in a worktree, and
// worktrees share the ref store, so the branch ref is the reliable handle where
// HEAD is not.
func gitRevParse(ctx context.Context, dir, branch string) (string, error) {
	out, _, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", fmt.Errorf("no local branch %q", branch)
	}
	return sha, nil
}

// localHistoryContains reports whether `candidate` is reachable from `tip` in
// the local object store.
//
// A candidate the local store does not have at all is definitively NOT
// reachable — every ancestor of a local tip is a local object — so an absent
// object is a negative answer, not an error. Without that step
// `merge-base --is-ancestor` fails with exit 128 on an unfetched remote head,
// which is precisely the shape a fork produces, and the check would degrade to
// "unknown" in exactly the case it exists to catch.
func localHistoryContains(ctx context.Context, dir, candidate, tip string) (bool, error) {
	if _, _, err := runGit(ctx, dir, "cat-file", "-e", candidate+"^{commit}"); err != nil {
		return false, nil
	}
	_, code, err := runGit(ctx, dir, "merge-base", "--is-ancestor", candidate, tip)
	if err == nil {
		return true, nil
	}
	if code == 1 {
		return false, nil
	}
	return false, err
}

// runGit executes a git command in dir and returns its combined output and
// exit code. The exit code is returned separately because git's plumbing
// distinguishes "the answer is no" (1) from "the question was invalid" (128),
// and collapsing the two is how an ancestry check silently stops answering.
func runGit(ctx context.Context, dir string, args ...string) (string, int, error) {
	cctx, cancel := context.WithTimeout(ctx, branchForkGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return string(out), exitErr.ExitCode(), fmt.Errorf("git %s: %s",
				strings.Join(args, " "), strings.TrimSpace(truncateForLog(string(out), 200)))
		}
		return string(out), -1, err
	}
	return string(out), 0, nil
}

// shortSHA abbreviates a commit SHA for log/error text.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// truncateForLog bounds a git output excerpt embedded in an error.
func truncateForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
