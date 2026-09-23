package github

// Post-merge verification when the PR run is the gate (#2055).
//
// main's ruleset sets strict_required_status_checks_policy: a pull request
// merges only once it is up to date with main, so its squash commit's tree is
// the tree the PR head's required checks already passed on. The full suites
// therefore no longer re-run on push to main, and "did the merge commit's own
// CI go green?" stops being the right question. After a merge, what is still
// worth verifying is:
//
//  1. the merge commit's tree equals the merged PR head's tree — proof that
//     the PR run tested exactly what landed;
//  2. every required check on that PR head concluded successfully — the gate
//     actually passed (skipped and neutral count as passing, as everywhere);
//  3. whatever still runs on the merge commit itself (CodeQL's default-branch
//     baseline, the cache-warm job) is green, or still running.
//
// When the trees differ — which the strict policy prevents, so it means a
// ruleset bypass such as `--admin` or a branch without the policy — the PR run
// is not evidence about the landed tree. The verdict then falls back to the
// pre-#2055 rule on the merge commit alone: every check that ran there must
// pass AND every required check must be present there. In nightgauge/nightgauge
// the required suites no longer run on push, so that fallback reads NOT-YET
// ("not observable") rather than green: nothing has tested the landed tree,
// and saying so is the honest answer. In a repository whose suites still run
// on push, the same fallback produces a real verdict. The fallback is chosen
// over "red" because red means a check failed, and none has.
//
// A commit with no merged pull request (a direct push, or a PR head polled
// before its merge) takes the same pre-#2055 rule, so callers that poll a PR
// head SHA see no change.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MergeCommitCheckGrace is how long after a merge an EMPTY check list on the
// merge commit is still read as "the push-triggered workflows have not been
// created yet" (NOT-YET) rather than "nothing runs on push" (nothing to wait
// for). GitHub creates push check suites within seconds; five minutes leaves a
// wide margin for a degraded Actions queue.
const MergeCommitCheckGrace = 5 * time.Minute

// MergeProvenance ties a merge commit to the pull request it landed and the
// tree that pull request's head tested.
type MergeProvenance struct {
	PRNumber  int
	MergeSHA  string
	MergeTree string
	HeadSHA   string
	HeadTree  string
	BaseRef   string
	MergedAt  time.Time
}

// TreesMatch reports whether the merge commit's tree is the PR head's tree.
// Empty trees never match: an unread tree is not evidence of equality.
func (p *MergeProvenance) TreesMatch() bool {
	return p != nil && p.MergeTree != "" && p.MergeTree == p.HeadTree
}

// MergeProvenanceReader is implemented by *CIService. Callers type-assert for
// it, so a reader without it (a test fake, another forge) keeps the pre-#2055
// merge-commit-only rule.
type MergeProvenanceReader interface {
	GetMergeProvenance(ctx context.Context, owner, repo, sha string) (*MergeProvenance, error)
}

// notFoundEndsRead treats a 404 as "nothing there" and every other non-200
// as checkRunsStatusError's classified error.
func notFoundEndsRead(resp *http.Response, body []byte) error {
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return checkRunsStatusError(resp, body)
}

// getCommitTree resolves sha (full or abbreviated) to its full SHA and tree.
// Both are empty when the forge does not know the commit (404).
func (s *CIService) getCommitTree(ctx context.Context, owner, repo, sha string) (fullSHA, tree string, err error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", owner, repo, sha)
	err = s.getAllPages(ctx, url, notFoundEndsRead, func(body io.Reader) error {
		var c struct {
			SHA    string `json:"sha"`
			Commit struct {
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
			} `json:"commit"`
		}
		if err := json.NewDecoder(body).Decode(&c); err != nil {
			return fmt.Errorf("decode commit: %w", err)
		}
		fullSHA, tree = c.SHA, c.Commit.Tree.SHA
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("fetch commit %s: %w", sha, err)
	}
	return fullSHA, tree, nil
}

// GetMergeProvenance returns the merged pull request whose merge commit is
// sha, with both trees resolved. It returns (nil, nil) when sha is unknown or
// no merged pull request names it as its merge commit (a direct push, or a PR
// head that has not merged). Any other read failure is an error: the caller
// cannot tell which rule applies, so it must not guess.
func (s *CIService) GetMergeProvenance(ctx context.Context, owner, repo, sha string) (*MergeProvenance, error) {
	fullSHA, mergeTree, err := s.getCommitTree(ctx, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	if fullSHA == "" {
		return nil, nil
	}

	var prov *MergeProvenance
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s/pulls", owner, repo, fullSHA)
	err = s.getAllPages(ctx, url, notFoundEndsRead, func(body io.Reader) error {
		var pulls []struct {
			Number         int        `json:"number"`
			MergeCommitSHA string     `json:"merge_commit_sha"`
			MergedAt       *time.Time `json:"merged_at"`
			Head           struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		}
		if err := json.NewDecoder(body).Decode(&pulls); err != nil {
			return fmt.Errorf("decode pull requests for commit: %w", err)
		}
		for _, p := range pulls {
			if prov != nil || p.MergedAt == nil || !strings.EqualFold(p.MergeCommitSHA, fullSHA) {
				continue
			}
			prov = &MergeProvenance{
				PRNumber:  p.Number,
				MergeSHA:  fullSHA,
				MergeTree: mergeTree,
				HeadSHA:   p.Head.SHA,
				BaseRef:   p.Base.Ref,
				MergedAt:  *p.MergedAt,
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetch pull requests for %s: %w", shortRef(fullSHA), err)
	}
	if prov == nil || prov.HeadSHA == "" {
		return nil, nil
	}

	_, prov.HeadTree, err = s.getCommitTree(ctx, owner, repo, prov.HeadSHA)
	if err != nil {
		return nil, err
	}
	return prov, nil
}

// MergeEvidence is everything EvaluateMergedCommit judges.
type MergeEvidence struct {
	// Provenance is nil when the commit has no merged pull request.
	Provenance *MergeProvenance
	// MergeChecks are every check run and commit status on the merge commit.
	MergeChecks []CheckDetail
	// HeadChecks are every check run and commit status on the PR head. Read
	// only when the trees match.
	HeadChecks []CheckDetail
	// RequiredNames is the base branch's required-check set; nil when it
	// could not be resolved.
	RequiredNames []string
	// Runs is the per-run cross-check for the merge commit; nil skips it.
	Runs []WorkflowRunSummary
	// Now is the evaluation time, for MergeCommitCheckGrace.
	Now time.Time
}

// PRHeadIsEvidence reports whether the PR head's checks are the gate for this
// merge: a merged PR exists and its head's tree is the merge commit's tree.
func (e MergeEvidence) PRHeadIsEvidence() bool {
	return e.Provenance.TreesMatch()
}

// EvaluateMergedCommit is the single post-merge verdict behind `nightgauge ci
// checks-complete`, scripts/post-merge-check.sh (which delegates to it) and
// the post-merge hook. See the file comment for the rule.
func EvaluateMergedCommit(e MergeEvidence) (ChecksCompleteVerdict, []string) {
	p := e.Provenance
	if p == nil {
		return EvaluateCommitChecksCrossChecked(e.MergeChecks, e.RequiredNames, e.Runs)
	}
	if !p.TreesMatch() {
		verdict, reasons := EvaluateCommitChecksCrossChecked(e.MergeChecks, e.RequiredNames, e.Runs)
		lead := fmt.Sprintf("merge commit %s's tree %s differs from PR #%d head %s's tree %s: "+
			"the PR run did not test the landed tree (a ruleset bypass?), so the merge commit's own checks must carry every required check",
			shortRef(p.MergeSHA), shortRef(p.MergeTree), p.PRNumber, shortRef(p.HeadSHA), shortRef(p.HeadTree))
		return verdict, append([]string{lead}, reasons...)
	}

	headVerdict, headReasons := EvaluateChecksComplete(e.HeadChecks, e.RequiredNames)
	mergeVerdict, mergeReasons := evaluatePushChecks(e.MergeChecks, p.MergedAt, e.Now)
	if mergeVerdict != ChecksNotYet {
		if agree, cross := CrossCheckWorkflowRuns(e.MergeChecks, e.Runs); !agree {
			mergeVerdict, mergeReasons = ChecksNotYet, append(mergeReasons, cross...)
		}
	}

	var reasons []string
	for _, r := range headReasons {
		reasons = append(reasons, fmt.Sprintf("PR #%d head %s: %s", p.PRNumber, shortRef(p.HeadSHA), r))
	}
	for _, r := range mergeReasons {
		reasons = append(reasons, fmt.Sprintf("merge commit %s: %s", shortRef(p.MergeSHA), r))
	}
	switch {
	case headVerdict == ChecksNotYet || mergeVerdict == ChecksNotYet:
		return ChecksNotYet, reasons
	case headVerdict == ChecksIncomplete || mergeVerdict == ChecksIncomplete:
		return ChecksIncomplete, reasons
	default:
		return ChecksComplete, nil
	}
}

// evaluatePushChecks judges what runs on the merge commit when the PR head is
// the gate: every check present must conclude and pass, and none is required
// to exist. An empty list is NOT-YET inside MergeCommitCheckGrace of the merge
// (the push workflows may not have been created yet) and passes after it (a
// repository that runs nothing on push has nothing to wait for).
func evaluatePushChecks(checks []CheckDetail, mergedAt, now time.Time) (ChecksCompleteVerdict, []string) {
	if len(checks) == 0 {
		if now.Sub(mergedAt) < MergeCommitCheckGrace {
			return ChecksNotYet, []string{fmt.Sprintf("no checks on the merge commit yet (within %s of the merge)", MergeCommitCheckGrace)}
		}
		return ChecksComplete, nil
	}
	return evaluateAllChecksComplete(checks)
}

func shortRef(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
