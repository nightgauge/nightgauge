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

// Self-fork recovery and stranded-tip preservation (#2699).
//
// A re-run whose open PR conflicts with its base used to rebase the PR branch.
// The rebased tip no longer descends from origin's tip, so the push is rejected
// as non-fast-forward and the run ends branch_forked — yet origin carries
// nothing this run lacks: merging origin's tip into the local tip changes
// nothing. That is a self-fork, and it is recoverable without a force push.
//
// The test is content, not `git patch-id`: patch-id hashes each hunk's context
// lines, so a rebase that rewrites the lines around a hunk (a CHANGELOG entry
// landing next to it, the #2669 shape) changes the id of an otherwise
// identical commit. A conflict-free three-way merge whose tree equals the
// local tip's tree proves origin carries no change the local tree lacks.

// SelfForkOutcome classifies one ReconcileSelfFork attempt.
type SelfForkOutcome string

const (
	// SelfForkClean means there was nothing to reconcile: origin has no branch
	// or its head is already reachable from the local tip.
	SelfForkClean SelfForkOutcome = "clean"
	// SelfForkRecovered means the branch was a self-fork and the local branch
	// now points at a commit that descends from origin's tip.
	SelfForkRecovered SelfForkOutcome = "recovered"
	// SelfForkForked means origin carries work the local history does not:
	// a genuine fork that must never be overwritten.
	SelfForkForked SelfForkOutcome = "forked"
	// SelfForkUnknown means the comparison could not be made.
	SelfForkUnknown SelfForkOutcome = "unknown"
)

// SelfForkResult is the outcome of one ReconcileSelfFork call.
type SelfForkResult struct {
	Outcome   SelfForkOutcome
	Branch    string
	LocalSHA  string
	RemoteSHA string
	// NewSHA is the recovery commit when Outcome is SelfForkRecovered.
	NewSHA string
	// Unmatched lists origin-only commits with no local patch equivalent.
	Unmatched []string
	Detail    string
}

// ReconcileSelfFork turns a self-inflicted fork of `branch` into a branch that
// fast-forwards origin, and declines everything else.
//
// The recovery commit has origin's tip as its first parent, the local tip as
// its second, and exactly the local tip's tree — the tree validation already
// verified — so nothing is re-applied and nothing can conflict. The branch ref
// moves by compare-and-swap; when `dir` has the branch checked out the index
// follows with `reset --soft`. Nothing is pushed here and nothing is forced.
func ReconcileSelfFork(ctx context.Context, dir, branch string) SelfForkResult {
	res := SelfForkResult{Outcome: SelfForkUnknown, Branch: branch}
	// The branch name can come from a stage-written context file; a protected
	// branch is never rewritten, whatever names it (same guard as
	// ReclaimOrphanedRemoteBranch).
	if isProtectedBranch(branch) {
		res.Detail = fmt.Sprintf("refusing to reconcile protected branch %q", branch)
		return res
	}
	fork := CheckBranchFork(ctx, dir, branch)
	res.LocalSHA, res.RemoteSHA = fork.LocalSHA, fork.RemoteSHA
	switch fork.State {
	case ForkStateClean:
		res.Outcome, res.Detail = SelfForkClean, fork.Detail
		return res
	case ForkStateUnknown:
		res.Detail = fork.Detail
		return res
	}
	local, remote := fork.LocalSHA, fork.RemoteSHA

	if _, _, err := runGit(ctx, dir, "cat-file", "-e", remote+"^{commit}"); err != nil {
		if _, _, err := runGit(ctx, dir, "fetch", "origin", "refs/heads/"+branch); err != nil {
			res.Detail = fmt.Sprintf("could not fetch origin/%s: %v", branch, err)
			return res
		}
	}
	baseOut, _, err := runGit(ctx, dir, "merge-base", local, remote)
	if err != nil {
		res.Detail = fmt.Sprintf("no merge base between local %s and origin/%s %s: %v",
			shortSHA(local), branch, shortSHA(remote), err)
		return res
	}
	base := strings.TrimSpace(baseOut)

	// A merge commit gets no special case: the freshness check puts them on
	// origin routinely, and merged tree == local tree proves one adds nothing.
	// git picks the base itself: a criss-cross has several, and comparing
	// against just one can pass a genuine fork as a self-fork.
	mergedOut, code, mergeErr := runGit(ctx, dir, "merge-tree", "--write-tree", local, remote)
	if mergeErr != nil && code != 1 {
		// `merge-tree --write-tree` needs git 2.38+; an older git lands here.
		res.Detail = fmt.Sprintf("could not merge origin/%s into local (needs git 2.38+): %v", branch, mergeErr)
		return res
	}
	treeOut, _, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", local+"^{tree}")
	if err != nil {
		res.Detail = fmt.Sprintf("could not read local tree: %v", err)
		return res
	}
	localTree := strings.TrimSpace(treeOut)
	mergedTree := ""
	if code == 0 {
		mergedTree = strings.TrimSpace(strings.SplitN(mergedOut, "\n", 2)[0])
	}
	if mergedTree != localTree {
		res.Outcome = SelfForkForked
		res.Unmatched = originOnlyCommits(ctx, dir, base, local, remote)
		short := make([]string, len(res.Unmatched))
		for i, s := range res.Unmatched {
			short[i] = shortSHA(s)
		}
		res.Detail = fmt.Sprintf("origin/%s carries changes the local tree lacks (%s) — a genuine fork",
			branch, strings.Join(short, ", "))
		return res
	}

	newOut, _, err := runGit(ctx, dir, "commit-tree", local+"^{tree}", "-p", remote, "-p", local,
		"-m", fmt.Sprintf("Merge rebased history of %s onto origin (self-fork recovery, #2699)", branch))
	if err != nil {
		res.Detail = fmt.Sprintf("could not write recovery commit: %v", err)
		return res
	}
	newSHA := strings.TrimSpace(newOut)

	headOut, _, _ := runGit(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	if strings.TrimSpace(headOut) == "refs/heads/"+branch {
		// reset --soft moves the checked-out branch ref and keeps the index;
		// the trees are identical, so the work tree is already consistent.
		cur, err := gitRevParse(ctx, dir, branch)
		if err != nil || cur != local {
			res.Detail = fmt.Sprintf("local %s moved during recovery — not touching it", branch)
			return res
		}
		if _, _, err := runGit(ctx, dir, "reset", "--soft", newSHA); err != nil {
			res.Detail = fmt.Sprintf("could not move %s to recovery commit: %v", branch, err)
			return res
		}
	} else if _, _, err := runGit(ctx, dir, "update-ref", "refs/heads/"+branch, newSHA, local); err != nil {
		res.Detail = fmt.Sprintf("could not move %s to recovery commit (ref moved?): %v", branch, err)
		return res
	}

	res.Outcome = SelfForkRecovered
	res.NewSHA = newSHA
	res.Detail = fmt.Sprintf(
		"origin/%s (%s) carries nothing the local tree lacks; %s now descends from it as %s — push fast-forwards",
		branch, shortSHA(remote), branch, shortSHA(newSHA))
	return res
}

// RollbackSelfFork moves `rec.Branch` back from the recovery commit to the
// local tip it replaced. Callers use it when the recovery push fails, so a
// failed recovery leaves the branch exactly as the stage produced it. It moves
// the ref only if it still points at the recovery commit.
func RollbackSelfFork(ctx context.Context, dir string, rec SelfForkResult) error {
	if rec.Outcome != SelfForkRecovered || rec.NewSHA == "" || rec.LocalSHA == "" {
		return nil
	}
	headOut, _, _ := runGit(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	if strings.TrimSpace(headOut) == "refs/heads/"+rec.Branch {
		cur, err := gitRevParse(ctx, dir, rec.Branch)
		if err != nil {
			return err
		}
		if cur != rec.NewSHA {
			return fmt.Errorf("%s moved since recovery (%s) — not rolling back", rec.Branch, shortSHA(cur))
		}
		_, _, err = runGit(ctx, dir, "reset", "--soft", rec.LocalSHA)
		return err
	}
	_, _, err := runGit(ctx, dir, "update-ref", "refs/heads/"+rec.Branch, rec.LocalSHA, rec.NewSHA)
	return err
}

// originOnlyCommits names the origin-only commits of a genuine fork for the
// operator: each one whose change, applied onto the local tip, is not already
// there (a conflict-free cherry-pick that leaves the local tree unchanged means
// the local history already holds it). Falls back to every origin-only commit
// when no single commit can be singled out.
func originOnlyCommits(ctx context.Context, dir, base, local, remote string) []string {
	out, _, _ := runGit(ctx, dir, "rev-list", "--reverse", base+".."+remote)
	all := strings.Fields(out)
	treeOut, _, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", local+"^{tree}")
	if err != nil {
		return all
	}
	localTree := strings.TrimSpace(treeOut)
	var unmatched []string
	for _, c := range all {
		picked, code, _ := runGit(ctx, dir, "merge-tree", "--write-tree", "--merge-base="+c+"^", local, c)
		if code != 0 || strings.TrimSpace(strings.SplitN(picked, "\n", 2)[0]) != localTree {
			unmatched = append(unmatched, c)
		}
	}
	if len(unmatched) == 0 {
		return all
	}
	return unmatched
}

// StrandedRef names the ref that pins a branch_forked run's tip.
func StrandedRef(issue int) string {
	return fmt.Sprintf("refs/nightgauge/stranded/%d", issue)
}

// PreserveStrandedTip pins the local tip of `branch` at
// refs/nightgauge/stranded/<issue> so worktree and branch cleanup cannot
// orphan the run's verified commit. The ref lives outside refs/heads/, so
// branch cleanup and gc leave it alone. Idempotent: a newer tip overwrites it.
func PreserveStrandedTip(ctx context.Context, repoRoot string, issue int, branch string) (string, string, error) {
	sha, err := gitRevParse(ctx, repoRoot, branch)
	if err != nil {
		return "", "", err
	}
	ref := StrandedRef(issue)
	if _, _, err := runGit(ctx, repoRoot, "update-ref", ref, sha); err != nil {
		return "", "", err
	}
	return ref, sha, nil
}
