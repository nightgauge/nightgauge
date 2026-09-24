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
//  3. whatever else still runs on the merge commit itself is green, or still
//     running.
//
// cache-warm is never part of the verdict (InformationalCheck). It tests
// nothing: it restores and saves caches. A network blip failing it would
// otherwise read as "main is red, fix it now" with nothing to fix.
//
// CodeQL (the "Analyze (<language>)" jobs and the "CodeQL" code-scanning
// check, CodeQLCheck) is informational on the tree-equal path only. It keeps
// running on push to main because it is the default-branch baseline for the
// Security tab and for PR "new alerts" comparisons, but it analyses the same
// tree with the same queries as the PR's own CodeQL run, which is required
// and already passed in (2). Its merge-commit runs are reported as info,
// never RED and never NOT-YET. On the tree-mismatch fallback below they count
// like every other check, because nothing has analysed the landed tree.
//
// When the trees differ — which the strict policy prevents, so it means a
// ruleset bypass such as `--admin` or a branch without the policy — the PR run
// is not evidence about the landed tree. The verdict then falls back to the
// pre-#2055 rule on the merge commit alone: every check that ran there must
// pass AND every required check must be present there. In a repository whose
// suites still run on push that produces a real verdict. Where they do not,
// the required checks are absent: that is NOT-YET inside
// MergeCommitCheckGrace, and RED once the grace has passed with no required
// check running or queued on the merge commit, because the landed tree was
// never tested and waiting will not change that. The remedy is to run the
// suites on main via workflow_dispatch, which puts the required checks on the
// merge commit while it is still main's head.
//
// A commit with no merged pull request (a direct push, or a PR head polled
// before its merge) takes the same pre-#2055 rule, so callers that poll a PR
// head SHA see no change.
//
// An unknown required-check set (the lookup failed) is never GREEN: the gate
// cannot be verified, so the verdict is NOT-YET.

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
	// RequiredNames is the base branch's required-check set.
	RequiredNames []string
	// RequiredKnown is false when the required-check set could not be read.
	// The verdict is then never green.
	RequiredKnown bool
	// Runs is the per-run cross-check for the merge commit; nil skips it.
	Runs []WorkflowRunSummary
	// Now is the evaluation time, for MergeCommitCheckGrace.
	Now time.Time
	// NoPushWorkflows is true only when the workflows at the merge commit were
	// read and none can run on a push to the base branch (#2061). An empty
	// merge-commit list is then final, and the grace does not apply.
	NoPushWorkflows bool
}

// PRHeadIsEvidence reports whether the PR head's checks are the gate for this
// merge: a merged PR exists and its head's tree is the merge commit's tree.
func (e MergeEvidence) PRHeadIsEvidence() bool {
	return e.Provenance.TreesMatch()
}

// InformationalCheck reports whether a check on a merge commit is reported
// but never decides a verdict: cache-warm (#2055) tests nothing, so its
// failure is not main being red.
func InformationalCheck(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "cache-warm")
}

// informationalWorkflow is InformationalCheck for the per-run cross-check:
// the cache-warm job runs in the "Cache warm" workflow.
func informationalWorkflow(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "cache warm")
}

// CodeQLCheck reports whether a check is one of CodeQL's, matched the way
// .github/workflows/codeql.yml names them: the "Analyze (<language>)" matrix
// jobs and the "CodeQL" code-scanning check that the analysis upload creates.
// On a merge commit whose tree equals the PR head's they are informational
// (#2055): the PR's own required CodeQL run already analysed that tree.
func CodeQLCheck(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "codeql" || (strings.HasPrefix(n, "analyze (") && strings.HasSuffix(n, ")"))
}

// codeQLWorkflow is CodeQLCheck for the per-run cross-check: the jobs run in
// the "CodeQL" workflow.
func codeQLWorkflow(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "codeql")
}

// DecidingChecks drops the informational checks from a merge commit's list.
func DecidingChecks(checks []CheckDetail) []CheckDetail {
	out := make([]CheckDetail, 0, len(checks))
	for _, c := range checks {
		if !InformationalCheck(c.Name) {
			out = append(out, c)
		}
	}
	return out
}

func decidingRuns(runs []WorkflowRunSummary) []WorkflowRunSummary {
	if runs == nil {
		return nil
	}
	out := make([]WorkflowRunSummary, 0, len(runs))
	for _, r := range runs {
		if !informationalWorkflow(r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// UntestedTreeReason is the reason EvaluateMergedCommit gives when a merge
// commit's tree was never tested; callers that raise a card name it.
const UntestedTreeReason = "the landed tree was never tested (bypass?): run the suites on main via workflow_dispatch"

const unknownRequiredReason = "the required-check set could not be read, so the gate cannot be verified; not observable"

// EvaluateMergedCommit is the single post-merge verdict behind `nightgauge ci
// checks-complete`, scripts/post-merge-check.sh (which applies the same rule
// when no binary is available) and the post-merge hook. See the file comment.
func EvaluateMergedCommit(e MergeEvidence) (ChecksCompleteVerdict, []string) {
	e.MergeChecks = DecidingChecks(e.MergeChecks)
	e.Runs = decidingRuns(e.Runs)
	p := e.Provenance
	if p == nil {
		verdict, reasons := EvaluateCommitChecksCrossChecked(e.MergeChecks, e.RequiredNames, e.Runs)
		return neverGreenIfUnknown(verdict, reasons, e.RequiredKnown)
	}
	if !p.TreesMatch() {
		verdict, reasons := EvaluateCommitChecksCrossChecked(e.MergeChecks, e.RequiredNames, e.Runs)
		lead := fmt.Sprintf("merge commit %s's tree %s differs from PR #%d head %s's tree %s: "+
			"the PR run did not test the landed tree, so the merge commit's own checks must carry every required check",
			shortRef(p.MergeSHA), shortRef(p.MergeTree), p.PRNumber, shortRef(p.HeadSHA), shortRef(p.HeadTree))
		if verdict == ChecksNotYet && e.RequiredKnown && untestedForGood(e, p) {
			missing := MissingRequiredChecks(e.MergeChecks, e.RequiredNames)
			return ChecksIncomplete, []string{lead, UntestedTreeReason,
				fmt.Sprintf("required check(s) never ran on the merge commit: %s", strings.Join(missing, ", "))}
		}
		verdict, reasons = neverGreenIfUnknown(verdict, reasons, e.RequiredKnown)
		return verdict, append([]string{lead}, reasons...)
	}
	if !e.RequiredKnown {
		// (b) cannot be verified without the required set.
		return ChecksNotYet, []string{unknownRequiredReason}
	}

	// CodeQL is informational here: the PR head's required CodeQL run
	// analysed this same tree. Its presence still proves the push workflows
	// were created, so it ends the empty-list grace.
	anyPushCheck := len(e.MergeChecks) > 0
	pushChecks, codeql := splitCodeQL(e.MergeChecks)
	var pushRuns []WorkflowRunSummary
	if e.Runs != nil {
		pushRuns = make([]WorkflowRunSummary, 0, len(e.Runs))
		for _, r := range e.Runs {
			if !codeQLWorkflow(r.Name) {
				pushRuns = append(pushRuns, r)
			}
		}
	}

	headVerdict, headReasons := EvaluateChecksComplete(e.HeadChecks, e.RequiredNames)
	mergeVerdict, mergeReasons := evaluatePushChecks(pushChecks, anyPushCheck || e.NoPushWorkflows, p.MergedAt, e.Now)
	if mergeVerdict != ChecksNotYet {
		if agree, cross := CrossCheckWorkflowRuns(pushChecks, pushRuns); !agree {
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
	info := codeQLInfo(codeql, p.MergeSHA)
	switch {
	case headVerdict == ChecksNotYet || mergeVerdict == ChecksNotYet:
		return ChecksNotYet, append(reasons, info...)
	case headVerdict == ChecksIncomplete || mergeVerdict == ChecksIncomplete:
		return ChecksIncomplete, append(reasons, info...)
	default:
		return ChecksComplete, info
	}
}

// splitCodeQL separates CodeQL's checks (CodeQLCheck) from the rest.
func splitCodeQL(checks []CheckDetail) (rest, codeql []CheckDetail) {
	for _, c := range checks {
		if CodeQLCheck(c.Name) {
			codeql = append(codeql, c)
		} else {
			rest = append(rest, c)
		}
	}
	return rest, codeql
}

// codeQLInfo reports the CodeQL merge-commit checks that are not a concluded
// pass, as information only: they never decide the tree-equal verdict.
func codeQLInfo(codeql []CheckDetail, mergeSHA string) []string {
	var running, failed []string
	for _, c := range codeql {
		switch {
		case !isChecksCompleteConcluded(c):
			running = append(running, c.Name)
		case !passingCheckConclusions[strings.ToUpper(strings.TrimSpace(c.Conclusion))]:
			failed = append(failed, fmt.Sprintf("%s (%s)", c.Name, strings.ToLower(c.Conclusion)))
		}
	}
	const why = "informational: the PR's required CodeQL run analysed this same tree; this run is the default-branch baseline"
	var out []string
	if len(failed) > 0 {
		out = append(out, fmt.Sprintf("merge commit %s: CodeQL did not pass: %s (%s)", shortRef(mergeSHA), strings.Join(failed, ", "), why))
	}
	if len(running) > 0 {
		out = append(out, fmt.Sprintf("merge commit %s: CodeQL still running: %s (%s)", shortRef(mergeSHA), strings.Join(running, ", "), why))
	}
	return out
}

// untestedForGood reports that waiting cannot make a tree-mismatched merge
// commit observable: the grace since the merge has passed, a required check
// is absent, and no required check is running or queued there.
func untestedForGood(e MergeEvidence, p *MergeProvenance) bool {
	if e.Now.Sub(p.MergedAt) < MergeCommitCheckGrace {
		return false
	}
	if len(MissingRequiredChecks(e.MergeChecks, e.RequiredNames)) == 0 {
		return false
	}
	required := make(map[string]bool, len(e.RequiredNames))
	for _, name := range e.RequiredNames {
		required[strings.ToLower(strings.TrimSpace(name))] = true
	}
	for _, c := range e.MergeChecks {
		if required[strings.ToLower(strings.TrimSpace(c.Name))] && !isChecksCompleteConcluded(c) {
			return false
		}
	}
	return true
}

// neverGreenIfUnknown turns a green verdict into NOT-YET when the required
// set could not be read: absence of a known requirement is not evidence.
func neverGreenIfUnknown(verdict ChecksCompleteVerdict, reasons []string, known bool) (ChecksCompleteVerdict, []string) {
	if verdict == ChecksComplete && !known {
		return ChecksNotYet, append(reasons, unknownRequiredReason)
	}
	return verdict, reasons
}

// evaluatePushChecks judges what runs on the merge commit when the PR head is
// the gate: every check present must conclude and pass, and none is required
// to exist. An empty list is NOT-YET inside MergeCommitCheckGrace of the merge
// (the push workflows may not have been created yet) and passes after it (a
// repository that runs nothing on push has nothing to wait for). anySeen is
// true when an informational check (CodeQL) was on the list before it was
// filtered, or when no workflow can run on push (#2061): either way there is
// nothing to wait for.
func evaluatePushChecks(checks []CheckDetail, anySeen bool, mergedAt, now time.Time) (ChecksCompleteVerdict, []string) {
	if len(checks) == 0 {
		if !anySeen && now.Sub(mergedAt) < MergeCommitCheckGrace {
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
