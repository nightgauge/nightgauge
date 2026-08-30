// The pipeline's commit owner (#1179).
//
// `feature-dev` deliberately does not commit — it verifies what it changed and
// hands off (#1608) — and `feature-validate` is the stage that commits and
// pushes. That is a CONVENTION written in a skill file, and routing may skip
// `feature-validate`: the fast-track change_rules path
// (docs/GATE_RELAXATION.md) emits `skip_stages = [feature-planning,
// feature-validate]` for a trivial change. With the only documented commit
// owner skipped, the working tree reached `pr-create` uncommitted, the branch
// had zero commits ahead of base, and the PR opened empty. Nothing detected it
// except the stage's own prose self-assessment, which nothing consumes.
//
// The fix is structural, not another sentence in a skill: the commit owner is
// this compiled step, and it runs at the head of the pr-create deterministic
// runner — BEFORE the create/punt decision, so it owns the commit on both the
// deterministic path and the LLM-skill fallback, and before the client-wiring
// check, so an unwired GitHub client cannot silently disable it. `pr-create`
// is never skippable (schedulerSkippableStages honours only feature-planning
// and feature-validate), so the owner is always present in the chain.
//
// It is a no-op on the normal path: when feature-validate ran, the branch has
// commits ahead of base and the tree is clean, and DecideCommit answers
// "commits-ahead" / "clean-tree" without touching git's object store.
package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// Reason codes recorded on PRCreateResult.CommitReason. Distinct from the
// create-path reasons so telemetry can group "what did the commit owner do"
// independently of "did the deterministic create path win".
const (
	ReasonCommitted                  = "committed"
	ReasonCommitNotNeededClean       = "commit-not-needed: clean-tree"
	ReasonCommitNotNeededAhead       = "commit-not-needed: commits-ahead"
	ReasonCommitNotNeededExhaustOnly = "commit-not-needed: exhaust-only"
	ReasonCommitRefusedBranchIsBase  = "commit-refused: branch-is-base"
	ReasonCommitRefusedDetached      = "commit-refused: detached-head"
	ReasonCommitRefusedUnmergedIndex = "commit-refused: unmerged-index"
	ReasonCommitRefusedAheadUnknown  = "commit-refused: ahead-unknown"
	ReasonCommitBranchUnknown        = "commit-refused: branch-unknown"
	ReasonCommitFailed               = "commit-failed"
	ReasonCommitClientUnavailable    = "commit-skipped: git-client-unavailable"
)

// defaultBaseBranch is used when the issue context recorded no base branch.
// Committing is refused on it, so a wrong guess costs a missed commit, never a
// commit onto the default branch.
const defaultBaseBranch = "main"

// ErrNothingStaged is returned by a gitClient's CommitAll when, after the
// pipeline's own untracked exhaust is unstaged, nothing is left to commit.
var ErrNothingStaged = errors.New("nothing staged after excluding pipeline exhaust")

// CommitDecision is the output of the pure commit-owner rule.
type CommitDecision struct {
	// ShouldCommit is true when the working tree carries work that no commit
	// on this branch holds yet.
	ShouldCommit bool
	// Reason is the recorded outcome code (one of the Reason* constants above,
	// pre-commit — the caller replaces it with ReasonCommitted or
	// ReasonCommitFailed once the commit is attempted).
	Reason string
	// Blocking is every path that will land in the commit: deliverables and
	// TRACKED bookkeeping changes.
	Blocking []string
	// Exhaust is the pipeline's own UNTRACKED bookkeeping (stage context files,
	// knowledge scaffolds). It is deliberately kept OUT of the commit — the same
	// rule RecoverUncommittedWork uses, so a rescue and this owner never
	// disagree about what counts as work.
	Exhaust []string
}

// DecideCommit is the pure decision rule for the commit owner. It reads a
// `git status --porcelain --untracked-files=all` dump plus three facts about
// the branch, and returns whether pr-create must create the run's commit.
//
// Decision matrix (first match wins):
//
//	currentBranch == "" / "HEAD"       → refuse (detached-head / branch-unknown)
//	currentBranch == baseBranch        → refuse (branch-is-base)
//	!aheadKnown                        → refuse (ahead-unknown)
//	aheadCount > 0                     → no-op  (commits-ahead — normal path)
//	unmerged index                     → refuse (unmerged-index)
//	no blocking changes, no entries    → no-op  (clean-tree)
//	no blocking changes, exhaust only  → no-op  (exhaust-only)
//	otherwise                          → COMMIT
//
// The `aheadCount > 0` arm is what keeps the normal (non-skipped) path
// byte-identical: feature-validate already committed, so the owner declines.
func DecideCommit(currentBranch, baseBranch, porcelain string, aheadCount int, aheadKnown bool) CommitDecision {
	branch := strings.TrimSpace(currentBranch)
	if branch == "" {
		return CommitDecision{Reason: ReasonCommitBranchUnknown}
	}
	if branch == "HEAD" {
		return CommitDecision{Reason: ReasonCommitRefusedDetached}
	}
	base := strings.TrimSpace(baseBranch)
	if base == "" {
		base = defaultBaseBranch
	}
	if branch == base {
		return CommitDecision{Reason: ReasonCommitRefusedBranchIsBase}
	}
	if !aheadKnown {
		return CommitDecision{Reason: ReasonCommitRefusedAheadUnknown}
	}
	if aheadCount > 0 {
		return CommitDecision{Reason: ReasonCommitNotNeededAhead}
	}
	// An unmerged index is not uncommitted work and this owner cannot handle it
	// (the #301 reasoning RecoverUncommittedWork records): `git add -A`
	// collapses the conflict stages and commits files full of conflict markers.
	if HasUnmergedIndex(porcelain) {
		return CommitDecision{Reason: ReasonCommitRefusedUnmergedIndex}
	}
	report := reclaim.ClassifyStatus(porcelain)
	if len(report.Blocking) == 0 {
		if len(report.Exhaust) == 0 {
			return CommitDecision{Reason: ReasonCommitNotNeededClean}
		}
		return CommitDecision{Reason: ReasonCommitNotNeededExhaustOnly, Exhaust: report.Exhaust}
	}
	return CommitDecision{
		ShouldCommit: true,
		Reason:       ReasonCommitted,
		Blocking:     report.Blocking,
		Exhaust:      report.Exhaust,
	}
}

// HasUnmergedIndex reports whether any porcelain entry is a merge conflict.
// Porcelain v1 renders a conflict as one of DD/AU/UD/UA/DU/AA/UU.
func HasUnmergedIndex(porcelain string) bool {
	for _, e := range reclaim.ParseStatus(porcelain) {
		switch e.XY {
		case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
			return true
		}
	}
	return false
}

// CommitMessage renders the commit-owner's message from the snapshot. Pure and
// deterministic — no timestamps, no environment reads — so the same tree
// produces the same message on a retry.
func CommitMessage(snap PRCreateSnapshot) string {
	subject := RenderTitle(snap)
	if strings.TrimSpace(stripTitlePrefix(snap.IssueTitle)) == "" {
		// Issue title unknown (no issue context) — do not emit a dangling
		// "fix(#N):" subject.
		subject = fmt.Sprintf("%s(#%d): implement issue #%d",
			titlePrefix(snap.IssueType), snap.IssueNumber, snap.IssueNumber)
	}
	base := snap.BaseBranch
	if base == "" {
		base = defaultBaseBranch
	}
	return fmt.Sprintf(
		"%s\n\nCommitted by pr-create, the pipeline's commit owner (#1179): the working\n"+
			"tree carried changes and the branch had no commits ahead of %s.\n",
		subject, base)
}

// commitOutcome is what ensureWorkCommitted reports back to Run.
type commitOutcome struct {
	Committed bool
	SHA       string
	Reason    string
}

// ensureWorkCommitted is the commit owner's imperative half: read the three
// git facts, run the pure rule, and — only when the rule says so — stage the
// blocking changes and commit them. Never fatal: a refusal or a git failure is
// recorded on the result and the stage continues, because the failure mode it
// guards against (an empty PR) is not made worse by a commit that could not be
// created.
func (r *DeterministicPRCreateRunner) ensureWorkCommitted(ctx context.Context, workdir string, snap PRCreateSnapshot) commitOutcome {
	if r.git == nil {
		return commitOutcome{Reason: ReasonCommitClientUnavailable}
	}
	base := snap.BaseBranch
	if base == "" {
		base = defaultBaseBranch
	}

	branch, branchErr := r.git.CurrentBranch(ctx, workdir)
	if branchErr != nil {
		return commitOutcome{Reason: fmt.Sprintf("%s: %s", ReasonCommitBranchUnknown, truncateErr(branchErr, 120))}
	}
	porcelain, statusErr := r.git.WorkingTreeStatus(ctx, workdir)
	if statusErr != nil {
		return commitOutcome{Reason: fmt.Sprintf("%s: %s", ReasonCommitFailed, truncateErr(statusErr, 120))}
	}
	ahead, aheadErr := r.git.CommitsAhead(ctx, workdir, base)
	decision := DecideCommit(branch, base, porcelain, ahead, aheadErr == nil)
	if !decision.ShouldCommit {
		return commitOutcome{Reason: decision.Reason}
	}

	sha, commitErr := r.git.CommitAll(ctx, workdir, CommitMessage(snap), decision.Exhaust)
	if errors.Is(commitErr, ErrNothingStaged) {
		return commitOutcome{Reason: ReasonCommitNotNeededExhaustOnly}
	}
	if commitErr != nil {
		return commitOutcome{Reason: fmt.Sprintf("%s: %s", ReasonCommitFailed, truncateErr(commitErr, 200))}
	}
	return commitOutcome{Committed: true, SHA: sha, Reason: ReasonCommitted}
}
