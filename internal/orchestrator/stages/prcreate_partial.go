package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/preflight"
)

// BranchNoDiff answers pr-create's ReasonNoDiffAgainstBase question for a
// stage workspace outside pr-create (#3160): it reads branch and base from
// issue-{N}.json as pr-create's snapshot does, and reports level=true when the
// branch's tree, committed or not, carries no deliverable change against the
// base. Both callers share CollectACDiff and hasDeliverable, so the
// post-feature-dev stop and pr-create cannot disagree about "no diff". An error
// means the question could not be answered; callers must not treat it as level.
func BranchNoDiff(ctx context.Context, workdir string, issueNumber int) (branch, base string, level bool, err error) {
	if workdir == "" {
		return "", "", false, fmt.Errorf("no workdir")
	}
	if data, readErr := readFile(pipelineContextPath(workdir, fmt.Sprintf("issue-%d.json", issueNumber))); readErr == nil {
		var raw struct {
			Branch     string `json:"branch"`
			BaseBranch string `json:"base_branch"`
		}
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			return "", "", false, fmt.Errorf("parse issue context: %w", jsonErr)
		}
		branch, base = raw.Branch, raw.BaseBranch
	}
	if base == "" {
		base = defaultBaseBranch
	}
	if branch != "" && branch == base {
		// Working on the base itself is pr-create's branch-is-base punt, not a
		// level feature branch.
		return branch, base, false, nil
	}
	d, err := CollectACDiff(ctx, workdir, base)
	if err != nil {
		return branch, base, false, err
	}
	return branch, base, !d.hasDeliverable(), nil
}

// VerifyCriteriaOnTree runs pr-create's deterministic verifier over criteria
// on the tree at workdir (#3160): the same ReconcileACs rules over a synthetic
// checklist, resolved by ResolveACVerdicts with no validate verdicts and no
// diff. It is how the scheduler re-checks a partial-delivery remainder against
// main before re-admitting it.
func VerifyCriteriaOnTree(workdir string, issueNumber int, criteria []string) []ACVerdict {
	if len(criteria) == 0 {
		return nil
	}
	var det *preflight.ACReconcileResult
	if workdir != "" {
		det = preflight.ReconcileACs(workdir, issueNumber, acCheckboxBody(criteria))
	}
	return ResolveACVerdicts(criteria, nil, det, nil)
}

// CommentSafeACText is UntrustedACText for text quoted into a pipeline-authored
// issue comment (#3160). Such a comment passes the trust filter, so a "<!--"
// left in criterion text could plant a marker the pipeline then parses as its
// own, such as a forged partial-delivery remainder.
func CommentSafeACText(s string) string {
	return strings.ReplaceAll(UntrustedACText(s), "<!--", "&lt;!--")
}

// PartialRerunNoDiffComment is the issue comment a partial-delivery re-run
// posts when feature-dev ends with nothing to commit (#3160). It names the
// remainder as still open: no lap of this pipeline produced it, and closing
// stays a human decision (#3089).
func PartialRerunNoDiffComment(issueNumber, pr int, branch, base string, criteria []string) string {
	if base == "" {
		base = defaultBaseBranch
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<!-- nightgauge:partial-rerun-no-diff pr=%d -->\n", pr))
	b.WriteString(fmt.Sprintf("A re-run of #%d targeting the criteria PR #%d left unmet ended after feature-dev with no diff between `%s` and `%s`, so feature-validate and pr-create did not run (#3160).\n\n",
		issueNumber, pr, CommentSafeACText(branch), CommentSafeACText(base)))
	b.WriteString("Still open:\n\n")
	for _, c := range criteria {
		b.WriteString("- " + CommentSafeACText(c) + "\n")
	}
	b.WriteString("\nThe issue waits in Backlog: close it if these are met, or rewrite them so a run can deliver them.\n")
	return b.String()
}

// PartialRerunNoDiffDetail is the terminal text for that stop. The leading
// marker makes the record ladder classify it not_pipeline_actionable, the same
// non-failure pr-create's no-diff park uses (#2941).
func PartialRerunNoDiffDetail(issueNumber, pr int, branch, base string, open int) string {
	return fmt.Sprintf("[not-pipeline-actionable] feature-dev: partial-delivery re-run of #%d (after PR #%d) produced no diff between %s and %s, so feature-validate was skipped; %d remaining criteria stay open and #%d waits in Backlog for a human [#3160]",
		issueNumber, pr, branch, base, open, issueNumber)
}
