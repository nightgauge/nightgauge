package execution

import (
	"fmt"
	"sort"
	"strings"
)

// Stranded merged branches — the half of branch lifecycle no scan could see
// (#912).
//
// `SweepMergedWorktrees` deletes a merged branch only as a SIDE-EFFECT of
// reclaiming the worktree that held it: it drives entirely off
// `git worktree list --porcelain`, so a merged branch whose worktree is
// already gone falls out of scope permanently. Nothing else enumerates
// branches, and `doctor` had arms for worktrees and stashes and none for
// branches. On 2026-08-25 three squash-merged branches sat in the core repo,
// each confirmed SAFE-DELETE by `scripts/branch-merged-check.sh`, while
// `worktree sweep --dry-run` reported "no reclaimable worktrees" and `doctor`
// reported "healthy". Two leaked branches and every check green is the state
// this file exists to make visible.
//
// The asymmetry that produced them is the tell: the same session merged three
// PRs identically, and the one worktree left in place was reclaimed completely
// — worktree AND branch — because the sweep could still see it. The two whose
// worktrees a human removed first stranded their branches.
//
// REPORT-ONLY, deliberately and permanently. This scan must never delete.
// Whether a branch is "pipeline-managed" is decided by the worktree DIRECTORY
// name (`issue-NNN`, see IssueNumberFromWorktreeDir) and never by the branch
// name — so at exactly the moment this scan applies, the worktree is gone and
// the provenance signal with it. A deleting version could not tell a pipeline
// branch from a developer's, and merged-ness does not rescue it: a developer
// may keep a merged branch on purpose. Giving branches a real provenance
// marker is a separate design change; until it exists, the honest deliverable
// is to name the condition and leave the delete to a human.

// BranchKeepReason names why a local branch was NOT reported as stranded. Every
// scanned branch lands on exactly one reason or in Stranded — a branch that
// appears in neither is a bug in the classifier, not a branch in limbo.
type BranchKeepReason string

const (
	// KeepDefaultBranch is the base itself. It has no commits of its own
	// relative to the base by construction, but it is excluded by name first
	// so the reason an operator reads is the true one.
	KeepDefaultBranch BranchKeepReason = "default-branch"
	// KeepHeldByWorktree is a branch some live worktree has checked out. The
	// worktree sweep owns those; classifying them here would report a branch
	// as strandable while a run may still be writing to it.
	KeepHeldByWorktree BranchKeepReason = "held-by-worktree"
	// KeepNoOwnCommits is a branch carrying nothing the base lacks — a branch
	// cut and never committed to. Indistinguishable from a checkout about to
	// start work, exactly as in mergedIntoBase's hasOwnCommits guard.
	KeepNoOwnCommits BranchKeepReason = "no-own-commits"
	// KeepUnmergedContent is the case that matters most: the branch carries
	// content the base does not have. Unpushed work and in-flight work both
	// land here, and both must be kept.
	KeepUnmergedContent BranchKeepReason = "unmerged-content"
	// KeepUnknown is a classification that could not be completed. "I could
	// not look" is never "safe to delete" — fail closed (#296).
	KeepUnknown BranchKeepReason = "unknown"
)

// StrandedBranch is one local branch whose content is already fully in the
// base ref and which no worktree holds — a merge that was never finished.
type StrandedBranch struct {
	Name string `json:"name"`
	// Tip is the branch's own commit SHA, so a report is still actionable
	// after the branch has been deleted by hand.
	Tip string `json:"tip"`
}

// KeptBranch is one local branch the scan deliberately did not report, with
// the reason. Kept in the result rather than discarded because "nothing is
// stranded" and "the scan skipped everything" print identically otherwise.
type KeptBranch struct {
	Name   string           `json:"name"`
	Reason BranchKeepReason `json:"reason"`
}

// StrandedBranchScan is one report-only pass over a repo's local branches.
type StrandedBranchScan struct {
	// BaseRef is the ref branches were compared against, echoed so a report
	// read later carries what it was measured against.
	BaseRef string `json:"baseRef"`
	// Scanned counts every local branch considered, including kept ones.
	Scanned  int              `json:"scanned"`
	Stranded []StrandedBranch `json:"stranded"`
	Kept     []KeptBranch     `json:"kept,omitempty"`
	// Errors records per-branch classification failures. A branch that errors
	// is kept with KeepUnknown and also named here; the scan continues.
	Errors []string `json:"errors,omitempty"`
}

// StrandedBranchOptions configures one report-only branch scan.
type StrandedBranchOptions struct {
	// RepoRoot is the repository whose local branches are enumerated.
	RepoRoot string
	// DefaultBranch is the branch name that is the base. Empty resolves it
	// from origin/HEAD.
	DefaultBranch string
	// BaseRef is the ref to compare content against. Empty resolves it the
	// same way the worktree sweep does (origin/<default> when the
	// remote-tracking ref exists, the local branch otherwise).
	//
	// This scan NEVER fetches. Passing a BaseRef the caller has just fetched
	// is how freshness gets in; keeping the fetch out keeps this a pure git
	// function that unit tests can run against fixture refs, for the same
	// reason mergedIntoBase does not fetch either.
	BaseRef string
}

// ScanStrandedBranches reports local branches that are fully merged into the
// base ref and held by no worktree.
//
// Merged-ness is decided by mergedIntoBase — the CONTENT diff, never ancestry.
// Ancestry is a false negative for every squash merge, which is this
// repository's only merge shape, so an ancestry-based version of this scan
// would report nothing, forever, and look correct doing it.
//
// The scan is report-only; see the file comment for why that is a design
// decision and not an unfinished one.
func ScanStrandedBranches(opts StrandedBranchOptions) (StrandedBranchScan, error) {
	var res StrandedBranchScan
	if opts.RepoRoot == "" {
		return res, fmt.Errorf("stranded branch scan: repo root is required")
	}

	defaultBranch := opts.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = detectDefaultBranch(opts.RepoRoot)
	}
	baseRef := opts.BaseRef
	if baseRef == "" {
		var err error
		baseRef, err = resolveBaseRef(opts.RepoRoot, defaultBranch)
		if err != nil {
			return res, fmt.Errorf("stranded branch scan: %w", err)
		}
	}
	res.BaseRef = baseRef

	branches, err := localBranches(opts.RepoRoot)
	if err != nil {
		return res, fmt.Errorf("stranded branch scan: %w", err)
	}
	held, err := branchesHeldByWorktrees(opts.RepoRoot)
	if err != nil {
		return res, fmt.Errorf("stranded branch scan: %w", err)
	}

	for _, branch := range branches {
		res.Scanned++
		if branch == defaultBranch || branch == "main" || branch == "master" {
			res.Kept = append(res.Kept, KeptBranch{Name: branch, Reason: KeepDefaultBranch})
			continue
		}
		if held[branch] {
			res.Kept = append(res.Kept, KeptBranch{Name: branch, Reason: KeepHeldByWorktree})
			continue
		}
		merged, hasOwnCommits, err := mergedIntoBase(opts.RepoRoot, baseRef, branch)
		if err != nil {
			res.Kept = append(res.Kept, KeptBranch{Name: branch, Reason: KeepUnknown})
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", branch, err))
			continue
		}
		switch {
		case !hasOwnCommits:
			res.Kept = append(res.Kept, KeptBranch{Name: branch, Reason: KeepNoOwnCommits})
		case !merged:
			res.Kept = append(res.Kept, KeptBranch{Name: branch, Reason: KeepUnmergedContent})
		default:
			tip, err := gitOutput(opts.RepoRoot, "rev-parse", branch)
			if err != nil {
				// The classification succeeded; only the evidence SHA is
				// missing. Report the branch without it rather than
				// downgrading a real finding to unknown.
				res.Errors = append(res.Errors, fmt.Sprintf("%s: rev-parse: %v", branch, err))
			}
			res.Stranded = append(res.Stranded, StrandedBranch{Name: branch, Tip: strings.TrimSpace(tip)})
		}
	}

	sort.Slice(res.Stranded, func(i, j int) bool { return res.Stranded[i].Name < res.Stranded[j].Name })
	sort.Slice(res.Kept, func(i, j int) bool { return res.Kept[i].Name < res.Kept[j].Name })
	return res, nil
}

// localBranches lists every local branch name. `for-each-ref` rather than
// `git branch`, whose output carries the current-branch marker and is
// explicitly documented as not for scripts.
func localBranches(repoRoot string) ([]string, error) {
	out, err := gitOutput(repoRoot, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("for-each-ref refs/heads: %w", err)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// branchesHeldByWorktrees is the set of branches some registered worktree has
// checked out — the primary checkout included. A branch in this set belongs to
// the worktree sweep, not to this scan.
func branchesHeldByWorktrees(repoRoot string) (map[string]bool, error) {
	listing, err := gitOutput(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	held := make(map[string]bool)
	for _, wt := range parseWorktreeList(listing) {
		if wt.Branch != "" {
			held[wt.Branch] = true
		}
	}
	return held, nil
}
