package reclaim

import (
	"fmt"
	"os/exec"
	"strings"
)

// Pruning preserved WIP refs — the lifecycle half of #1105.
//
// A ref under refs/nightgauge/wip/ is a claim on the object graph that never
// expires: `git gc` cannot reclaim what a ref reaches, so every guard kill
// leaves a permanent tail. But the whole point of the namespace is that it is
// the LAST copy of work the pipeline destroyed, so a prune that guesses wrong
// destroys exactly the thing the feature exists to protect.
//
// That asymmetry decides the design. Automatic pruning removes a ref on
// exactly one proof: the preserved commit's content is already present in the
// base ref, so deleting the anchor loses nothing that is not also in `main`.
// Everything else is KEPT and reported, and can only be removed by an operator
// naming it — `--discard` with an explicit `--ref` or `--issue`. There is no
// age-based expiry and there will not be one: a WIP commit does not become
// less valuable by being old, and "it was three weeks ago" is not evidence
// that the work landed.
//
// Merged-ness is CONTENT, never ancestry, for the same reason
// execution.ScanStrandedBranches gives: squash merge is this workspace's only
// merge shape, and an ancestry test would report "nothing has ever landed",
// forever, while looking correct.

// WipKeepReason names why a preserved ref was not pruned. Every scanned ref
// lands on exactly one reason or in Pruned.
type WipKeepReason string

const (
	// WipKeepNotLanded is the case that matters: the preserved commit carries
	// content the base ref does not have. This is unsalvaged work and the ref
	// is the only thing holding it.
	WipKeepNotLanded WipKeepReason = "not-landed"
	// WipKeepUnknown is a classification that could not be completed — an
	// unreadable commit, an empty diff-tree, a failing git. "I could not look"
	// is never "safe to delete" (#296).
	WipKeepUnknown WipKeepReason = "unknown"
)

// WipPruneReason names why a ref WAS pruned, so a report says which of the two
// doors it went through rather than only that it is gone.
type WipPruneReason string

const (
	// WipPrunedLanded is the automatic door: the content is already in base.
	WipPrunedLanded WipPruneReason = "landed"
	// WipPrunedDiscarded is the explicit door: an operator named this ref (or
	// its issue) with --discard, accepting the loss.
	WipPrunedDiscarded WipPruneReason = "discarded"
)

// PrunedWipRef is one removed anchor, with enough of the commit preserved in
// the report to `git show` it from the reflog if the deletion was a mistake.
type PrunedWipRef struct {
	WipRef
	Reason WipPruneReason `json:"reason"`
}

// KeptWipRef is one anchor the prune deliberately left in place.
type KeptWipRef struct {
	WipRef
	Reason WipKeepReason `json:"reason"`
}

// WipPruneResult is one pass over a repository's preserved-WIP namespace.
type WipPruneResult struct {
	// BaseRef is what "landed" was measured against, echoed so a report read
	// later carries its own premise.
	BaseRef string         `json:"baseRef"`
	DryRun  bool           `json:"dryRun"`
	Scanned int            `json:"scanned"`
	Pruned  []PrunedWipRef `json:"pruned,omitempty"`
	Kept    []KeptWipRef   `json:"kept,omitempty"`
	Errors  []string       `json:"errors,omitempty"`
}

// WipPruneOptions configures one prune pass.
type WipPruneOptions struct {
	// RepoRoot is the repository whose WIP namespace is scanned.
	RepoRoot string
	// BaseRef is the ref preserved content is compared against. Empty
	// resolves origin/<default-branch>, falling back to the local branch.
	//
	// This never fetches, for the same reason the worktree and stranded-branch
	// scans do not: a stale base makes a landed commit read as not-landed, so
	// the ref is KEPT. Staleness costs timeliness, never work.
	BaseRef string
	// Issue narrows the pass to one issue's refs. 0 means every issue.
	Issue int
	// Ref narrows the pass to one exact ref name.
	Ref string
	// Discard removes the selected refs regardless of whether their content
	// landed. Requires Issue or Ref: a bare discard would delete every
	// preserved commit in the repository, which is the outcome this whole
	// namespace exists to prevent.
	Discard bool
	// DryRun classifies without deleting anything.
	DryRun bool
}

// PruneWipRefs removes preserved-WIP anchors whose work is landed or which the
// operator has explicitly discarded.
func PruneWipRefs(opts WipPruneOptions) (WipPruneResult, error) {
	var res WipPruneResult
	if opts.RepoRoot == "" {
		return res, fmt.Errorf("prune wip refs: repo root is required")
	}
	if opts.Discard && opts.Issue <= 0 && opts.Ref == "" {
		return res, fmt.Errorf("prune wip refs: --discard destroys preserved work and requires an explicit --issue or --ref")
	}
	res.DryRun = opts.DryRun

	baseRef := opts.BaseRef
	if baseRef == "" {
		var err error
		baseRef, err = resolveWipBaseRef(opts.RepoRoot)
		if err != nil {
			return res, fmt.Errorf("prune wip refs: %w", err)
		}
	}
	res.BaseRef = baseRef

	refs, err := ListWipRefs(opts.RepoRoot)
	if err != nil {
		return res, fmt.Errorf("prune wip refs: %w", err)
	}

	for _, ref := range refs {
		if opts.Ref != "" && ref.Ref != opts.Ref {
			continue
		}
		if opts.Issue > 0 && ref.Issue != opts.Issue {
			continue
		}
		res.Scanned++

		reason := WipPrunedLanded
		if opts.Discard {
			reason = WipPrunedDiscarded
		} else {
			landed, err := wipContentLanded(opts.RepoRoot, baseRef, ref.Commit)
			if err != nil {
				res.Kept = append(res.Kept, KeptWipRef{WipRef: ref, Reason: WipKeepUnknown})
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", ref.Ref, err))
				continue
			}
			if !landed {
				res.Kept = append(res.Kept, KeptWipRef{WipRef: ref, Reason: WipKeepNotLanded})
				continue
			}
		}

		if !opts.DryRun {
			if err := deleteWipRef(opts.RepoRoot, ref.Ref, ref.Commit); err != nil {
				res.Kept = append(res.Kept, KeptWipRef{WipRef: ref, Reason: WipKeepUnknown})
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", ref.Ref, err))
				continue
			}
		}
		res.Pruned = append(res.Pruned, PrunedWipRef{WipRef: ref, Reason: reason})
	}
	return res, nil
}

// wipContentLanded reports whether every path the preserved commit touches
// already reads identically in the base ref.
//
// Restricted to the commit's OWN paths on purpose: an unrestricted
// `git diff base commit` also reports everything the base gained afterwards,
// so a landed WIP commit would read as not-landed the moment `main` moved —
// the exact decay `scripts/branch-merged-check.sh` exists to avoid.
//
// A commit that touches nothing returns an error rather than "landed": an
// empty path set makes the diff trivially empty, and a vacuous proof must not
// authorise a deletion.
func wipContentLanded(repoRoot, baseRef, commit string) (bool, error) {
	out, err := gitWipOutput(repoRoot, "diff-tree", "--no-commit-id", "--name-only", "-r", commit)
	if err != nil {
		return false, fmt.Errorf("diff-tree %s: %w", commit, err)
	}
	files := nonEmptyLines(out)
	if len(files) == 0 {
		return false, fmt.Errorf("commit %s touches no paths — cannot prove its content landed", commit)
	}

	args := append([]string{"diff", "--name-only", baseRef, commit, "--"}, files...)
	diff, err := gitWipOutput(repoRoot, args...)
	if err != nil {
		return false, fmt.Errorf("diff %s..%s: %w", baseRef, commit, err)
	}
	return len(nonEmptyLines(diff)) == 0, nil
}

// deleteWipRef removes one ref, compare-and-delete against the SHA the scan
// observed. If a concurrent kill re-anchored the ref at newer work between the
// listing and the delete, git refuses and the newer work survives.
func deleteWipRef(repoRoot, ref, expectedSHA string) error {
	args := []string{"update-ref", "-d", ref}
	if expectedSHA != "" {
		args = append(args, expectedSHA)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update-ref -d %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveWipBaseRef picks the ref to measure "landed" against: the remote
// tracking branch when it exists (it is what merges actually land on), the
// local default branch otherwise.
func resolveWipBaseRef(repoRoot string) (string, error) {
	def := "main"
	if out, err := gitWipOutput(repoRoot, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); name != "" {
			def = name
		}
	}
	if _, err := gitWipOutput(repoRoot, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+def); err == nil {
		return "origin/" + def, nil
	}
	if _, err := gitWipOutput(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+def); err == nil {
		return def, nil
	}
	return "", fmt.Errorf("could not resolve a base ref in %s (no origin/%s and no local %s)", repoRoot, def, def)
}
