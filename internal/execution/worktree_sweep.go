package execution

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree reclamation — the reconcile half of worktree lifecycle.
//
// CleanupWorktree (worktree.go) is the inline half: it runs on the path a
// finished run walks. Inline cleanup structurally cannot reclaim everything —
// a run swept mid-flight (window reload, crash, kill) never reaches its
// cleanup step, so its worktree survives the merge that should have retired
// it. This file is the reconcile pass that catches those: it walks the
// worktrees git actually has registered and reclaims the ones whose work is
// already on the default branch. See Issue #110.

// SkipReason explains why the sweep left a worktree in place. Every skip is
// reported rather than silently dropped so a leak is diagnosable from the
// sweep's own output instead of inferred days later from disk usage.
type SkipReason string

const (
	// SkipNotPipelineManaged — the directory name carries no issue number, so
	// the pipeline did not create it. The pipeline only garbage-collects its
	// own state; a developer's hand-made worktree is never touched.
	SkipNotPipelineManaged SkipReason = "not-pipeline-managed"
	// SkipPrimary — the repository's main checkout.
	SkipPrimary SkipReason = "primary-checkout"
	// SkipLocked — `git worktree lock` marks it as deliberately pinned.
	SkipLocked SkipReason = "locked"
	// SkipDetached — no branch to compare against the default branch.
	SkipDetached SkipReason = "detached-head"
	// SkipProtectedBranch — main/master/the default branch itself.
	SkipProtectedBranch SkipReason = "protected-branch"
	// SkipActiveRun — the issue is currently executing.
	SkipActiveRun SkipReason = "active-run"
	// SkipDirty — uncommitted or untracked changes would be destroyed.
	SkipDirty SkipReason = "uncommitted-changes"
	// SkipNoOwnCommits — the branch has produced no commits of its own, which
	// is indistinguishable from a worktree that was just created for a run
	// about to start. See mergedIntoBase for why this guard exists.
	SkipNoOwnCommits SkipReason = "no-commits-of-its-own"
	// SkipUnmergedContent — the branch carries work not yet on the default
	// branch. This is the load-bearing safety check.
	SkipUnmergedContent SkipReason = "unmerged-content"
)

// ReclaimedWorktree records one worktree the sweep removed (or, under DryRun,
// would have removed) together with the branch it held.
type ReclaimedWorktree struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	IssueNumber int    `json:"issueNumber"`
}

// SkippedWorktree records one worktree the sweep deliberately left in place.
type SkippedWorktree struct {
	Path   string     `json:"path"`
	Branch string     `json:"branch,omitempty"`
	Reason SkipReason `json:"reason"`
}

// WorktreeSweepResult summarizes one reconcile pass over a single repo.
type WorktreeSweepResult struct {
	RepoRoot string `json:"repoRoot"`
	// DefaultBranch is the resolved default branch name; BaseRef is the ref
	// branches were actually compared against (origin/<default> when the
	// remote-tracking ref exists, the local branch otherwise).
	DefaultBranch string              `json:"defaultBranch"`
	BaseRef       string              `json:"baseRef"`
	Scanned       int                 `json:"scanned"`
	Reclaimed     []ReclaimedWorktree `json:"reclaimed"`
	Skipped       []SkippedWorktree   `json:"skipped"`
	Errors        []string            `json:"errors,omitempty"`
	DryRun        bool                `json:"dryRun"`
}

// WorktreeSweepOptions configures one reconcile pass.
type WorktreeSweepOptions struct {
	// RepoRoot is the repository whose registered worktrees are swept.
	RepoRoot string
	// DefaultBranch overrides local default-branch detection ("main",
	// "master", …). Empty resolves it from origin/HEAD.
	DefaultBranch string
	// ActiveIssues holds issue numbers with a run in flight. Their worktrees
	// are never reclaimed, however merged their branch looks — a run whose PR
	// has just landed may still have stages to execute in that directory.
	ActiveIssues map[int]bool
	// DryRun classifies without removing anything.
	DryRun bool
}

// SweepMergedWorktrees reclaims every pipeline-created worktree in RepoRoot
// whose branch carries no content the default branch is missing.
//
// It is best-effort and non-fatal by design: a per-worktree failure is
// recorded in Errors and logged at WARN, and the remaining worktrees are still
// swept. An error is returned only for a caller mistake (no repo root, git
// unusable) — the conditions under which no meaningful classification is
// possible at all.
func SweepMergedWorktrees(opts WorktreeSweepOptions) (WorktreeSweepResult, error) {
	res := WorktreeSweepResult{RepoRoot: opts.RepoRoot, DryRun: opts.DryRun}
	if opts.RepoRoot == "" {
		return res, fmt.Errorf("worktree sweep: repo root is required")
	}

	defaultBranch := opts.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = detectDefaultBranch(opts.RepoRoot)
	}
	baseRef, err := resolveBaseRef(opts.RepoRoot, defaultBranch)
	if err != nil {
		return res, fmt.Errorf("worktree sweep: %w", err)
	}
	res.BaseRef = baseRef
	res.DefaultBranch = defaultBranch

	listing, err := gitOutput(opts.RepoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return res, fmt.Errorf("worktree sweep: git worktree list: %w", err)
	}
	records := parseWorktreeList(listing)

	// `git worktree list` always reports the main checkout first; nothing else
	// distinguishes it in the porcelain format.
	for i, wt := range records {
		res.Scanned++
		skip, num := classifyWorktree(wt, i == 0, defaultBranch, baseRef, opts)
		if skip != "" {
			res.Skipped = append(res.Skipped, SkippedWorktree{Path: wt.Path, Branch: wt.Branch, Reason: skip})
			continue
		}
		if opts.DryRun {
			res.Reclaimed = append(res.Reclaimed, ReclaimedWorktree{Path: wt.Path, Branch: wt.Branch, IssueNumber: num})
			continue
		}
		if err := reclaimWorktree(opts.RepoRoot, wt); err != nil {
			log.Printf("[WARN] worktree sweep: %v", err)
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		res.Reclaimed = append(res.Reclaimed, ReclaimedWorktree{Path: wt.Path, Branch: wt.Branch, IssueNumber: num})
	}

	return res, nil
}

// classifyWorktree returns the reason a worktree must be left alone, or an
// empty reason plus its issue number when it is safe to reclaim. Ordered
// cheapest-check-first so the expensive git calls only run on candidates that
// have already cleared every structural guard.
func classifyWorktree(wt worktreeRecord, isPrimary bool, defaultBranch, baseRef string, opts WorktreeSweepOptions) (SkipReason, int) {
	if isPrimary || wt.Bare {
		return SkipPrimary, 0
	}
	if wt.Locked {
		return SkipLocked, 0
	}
	if wt.Detached || wt.Branch == "" {
		return SkipDetached, 0
	}
	if wt.Branch == "main" || wt.Branch == "master" || wt.Branch == defaultBranch {
		return SkipProtectedBranch, 0
	}

	num, ok := IssueNumberFromWorktreeDir(filepath.Base(wt.Path))
	if !ok {
		return SkipNotPipelineManaged, 0
	}
	if opts.ActiveIssues[num] {
		return SkipActiveRun, num
	}

	// A prunable entry's directory is already gone — `git worktree remove`
	// still needs to run to drop the registration, and there is nothing on
	// disk left to protect.
	if !wt.Prunable {
		dirty, err := hasUncommittedChanges(wt.Path)
		if err != nil || dirty {
			return SkipDirty, num
		}
	}

	merged, hasOwnCommits, err := mergedIntoBase(opts.RepoRoot, baseRef, wt.Branch)
	if err != nil {
		return SkipUnmergedContent, num
	}
	if !hasOwnCommits {
		return SkipNoOwnCommits, num
	}
	if !merged {
		return SkipUnmergedContent, num
	}
	return "", num
}

// BranchAheadInfo reports whether a branch carries committed, unmerged work
// with no uncommitted changes on top — the state a killed stage can leave
// behind after it committed but before pr-create ran (#191).
type BranchAheadInfo struct {
	HasOwnCommits bool
	Clean         bool // no uncommitted/untracked changes
	AheadOfBase   bool // mergedIntoBase reports content not yet on base
}

// DetectBranchAhead inspects worktreePath (a live git checkout) against
// baseRef using the same content-diff logic worktree reclamation relies on
// (mergedIntoBase) — see docs/GO_BINARY.md#worktree-reclamation-issue-110 for
// why ancestry checks are unsafe here (squash merges).
func DetectBranchAhead(worktreePath, branch, baseRef string) (BranchAheadInfo, error) {
	merged, hasOwnCommits, err := mergedIntoBase(worktreePath, baseRef, branch)
	if err != nil {
		return BranchAheadInfo{}, err
	}
	dirty, err := hasUncommittedChanges(worktreePath)
	if err != nil {
		return BranchAheadInfo{}, err
	}
	return BranchAheadInfo{
		HasOwnCommits: hasOwnCommits,
		Clean:         !dirty,
		AheadOfBase:   hasOwnCommits && !merged,
	}, nil
}

// ResolveBaseRef exposes resolveBaseRef for callers outside this package (the
// abandoned-commit recovery action) that need to resolve origin/<default>
// themselves when the issue context carries no base ref (#191).
func ResolveBaseRef(repoRoot, defaultBranch string) (string, error) {
	return resolveBaseRef(repoRoot, defaultBranch)
}

// DetectDefaultBranch exposes detectDefaultBranch for the same callers as
// ResolveBaseRef (#191).
func DetectDefaultBranch(repoRoot string) string {
	return detectDefaultBranch(repoRoot)
}

// mergedIntoBase reports whether branch has commits of its own (hasOwnCommits)
// and whether the content of those commits is already fully represented in
// baseRef (merged).
//
// The merge test is deliberately a CONTENT diff, not an ancestry check. This
// project squash-merges, so the branch tip is never an ancestor of the default
// branch and `git merge-base --is-ancestor` reports a false negative for every
// merged branch — the exact reason a squash-merged branch cannot be told apart
// from one that was never pushed after the fact (AGENTS.md § Clean up on
// merge; docs/GIT_WORKFLOW.md § After Merge).
//
// hasOwnCommits is a safety guard, not part of the merge test: a worktree
// created at the tip of the default branch that has committed nothing yet also
// has an empty content diff, and is indistinguishable from a run that is about
// to start writing. Requiring at least one commit of its own means the sweep
// only ever reclaims a worktree that demonstrably did work and whose work
// landed.
func mergedIntoBase(repoRoot, baseRef, branch string) (merged bool, hasOwnCommits bool, err error) {
	countOut, err := gitOutput(repoRoot, "rev-list", "--count", baseRef+".."+branch)
	if err != nil {
		return false, false, err
	}
	hasOwnCommits = strings.TrimSpace(countOut) != "0"

	diffOut, err := gitOutput(repoRoot, "diff", "--stat", baseRef+".."+branch)
	if err != nil {
		return false, hasOwnCommits, err
	}
	return strings.TrimSpace(diffOut) == "", hasOwnCommits, nil
}

// hasUncommittedChanges reports whether the worktree holds modified, staged,
// or untracked files. Gitignored files (node_modules, build output, the copied
// config.local.yaml) are excluded, so a normal post-run worktree reads clean.
func hasUncommittedChanges(worktreePath string) (bool, error) {
	out, err := gitOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// reclaimWorktree removes a worktree and the branch it held. Failures are
// logged at WARN rather than swallowed — a silent failure here is exactly how
// the leak this sweep exists to fix stayed invisible.
//
// The branch is deleted with -D, not -d: its content is already on the default
// branch, but a squash merge leaves the tip a non-ancestor, so -d refuses it.
func reclaimWorktree(repoRoot string, wt worktreeRecord) error {
	cmd := exec.Command("git", "worktree", "remove", wt.Path, "--force")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[WARN] worktree sweep: git worktree remove %s failed (%v): %s — falling back to manual removal",
			wt.Path, err, strings.TrimSpace(string(out)))
		if rmErr := os.RemoveAll(wt.Path); rmErr != nil {
			return fmt.Errorf("remove worktree %s: %v (manual cleanup also failed: %v)", wt.Path, err, rmErr)
		}
		prune := exec.Command("git", "worktree", "prune")
		prune.Dir = repoRoot
		if pruneOut, pruneErr := prune.CombinedOutput(); pruneErr != nil {
			log.Printf("[WARN] worktree sweep: git worktree prune after manual removal of %s failed (%v): %s",
				wt.Path, pruneErr, strings.TrimSpace(string(pruneOut)))
		}
	}

	del := exec.Command("git", "branch", "-D", wt.Branch)
	del.Dir = repoRoot
	if out, err := del.CombinedOutput(); err != nil {
		log.Printf("[WARN] worktree sweep: git branch -D %s failed (%v): %s",
			wt.Branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// branchAheadOfBase reports whether branchName carries commits (with content
// not already represented on repo's default branch) — the state a killed
// stage can leave behind: a validated commit that never reached pr-create, or
// a commit that landed on a stray branch (e.g. `temp-pre-push-<n>`, left
// behind when a SIGKILL bypasses pre_push.go's restore-defer) after the
// pipeline's own cleanup lost track of it. Shared by CleanupWorktree (checked
// against the worktree's current HEAD, whatever branch that is) and
// CleanupLocalBranch (checked against a named branch ref) — both must refuse
// to destroy content the remote doesn't have, independent of working-tree
// dirtiness (#266).
//
// dir may be a linked worktree or the main checkout; git resolves refs
// against the shared object store either way. Soft-fail: an unresolvable
// default branch or a failed content-diff both return (false, err) so the
// caller logs and falls through to its existing behavior rather than
// blocking cleanup on this check's own fragility.
func branchAheadOfBase(dir, branchName string) (bool, error) {
	if branchName == "" {
		return false, nil
	}
	defaultBranch := detectDefaultBranch(dir)
	if branchName == "main" || branchName == "master" || branchName == defaultBranch {
		return false, nil
	}
	if !refExists(dir, "refs/heads/"+branchName) {
		return false, nil
	}
	baseRef, err := resolveBaseRef(dir, defaultBranch)
	if err != nil {
		return false, err
	}
	merged, hasOwnCommits, err := mergedIntoBase(dir, baseRef, branchName)
	if err != nil {
		return false, err
	}
	return hasOwnCommits && !merged, nil
}

// resolveBaseRef picks the ref every branch is compared against. The remote
// ref is preferred (origin/main is what "already merged" actually means); a
// repo with no remote falls back to the local branch so the check still works
// offline and in tests.
func resolveBaseRef(repoRoot, defaultBranch string) (string, error) {
	if refExists(repoRoot, "refs/remotes/origin/"+defaultBranch) {
		return "origin/" + defaultBranch, nil
	}
	if refExists(repoRoot, "refs/heads/"+defaultBranch) {
		return defaultBranch, nil
	}
	return "", fmt.Errorf("default branch %q resolves to no ref in %s", defaultBranch, repoRoot)
}

// detectDefaultBranch reads origin/HEAD, then falls back to whichever of
// main/master exists. Purely local — no network call.
func detectDefaultBranch(repoRoot string) string {
	if out, err := gitOutput(repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); name != "" {
			return name
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if refExists(repoRoot, "refs/remotes/origin/"+candidate) || refExists(repoRoot, "refs/heads/"+candidate) {
			return candidate
		}
	}
	return "main"
}

func refExists(repoRoot, ref string) bool {
	_, err := gitOutput(repoRoot, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// worktreeRecord is one entry from `git worktree list --porcelain`.
type worktreeRecord struct {
	Path     string
	Branch   string // short name; empty when detached
	Detached bool
	Bare     bool
	Locked   bool
	Prunable bool
}

// parseWorktreeList parses `git worktree list --porcelain`. Records are
// blank-line separated; each opens with a `worktree <path>` line followed by
// zero or more attribute lines (`branch`, `detached`, `bare`, `locked`,
// `prunable`), some of which carry a trailing reason we do not need.
func parseWorktreeList(out string) []worktreeRecord {
	var records []worktreeRecord
	var cur *worktreeRecord
	flush := func() {
		if cur != nil {
			records = append(records, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		field, value, _ := strings.Cut(line, " ")
		switch field {
		case "worktree":
			flush()
			cur = &worktreeRecord{Path: strings.TrimSpace(value)}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(strings.TrimSpace(value), "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	flush()
	return records
}

// IssueNumberFromWorktreeDir returns the issue number encoded in a worktree
// directory's base name. Both dispatch paths are accepted: "issue-NNN" (the
// extension's WorktreeManager) and "<repo>-issue-NNN" (the Go
// execution.Manager). A name that encodes no issue number was not created by
// the pipeline.
func IssueNumberFromWorktreeDir(base string) (int, bool) {
	idx := strings.LastIndex(base, "issue-")
	if idx < 0 {
		return 0, false
	}
	tail := base[idx+len("issue-"):]
	if tail == "" {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(tail, "%d", &n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
