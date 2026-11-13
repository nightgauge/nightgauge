package stages

import (
	"fmt"
	"strings"
)

// NoDiffOutcome settles a run whose branch has no diff against its base
// (#2941). The #2942 specimen re-ran a `Refs` partial delivery whose code was
// already on main and opened a +0/-0 PR of nothing but merges of main; the
// next run would do the same. With nothing to open a PR for, the criteria are
// all that is left to report: closeIssue is true only when every one is met,
// and comment is the issue comment carrying the evidence for each, or naming
// exactly which remain unmet.
func NoDiffOutcome(issueNumber int, branch, base string, verdicts []ACVerdict) (closeIssue bool, comment string) {
	if base == "" {
		base = defaultBaseBranch
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("pr-create found no diff between `%s` and `%s`: this issue's code is already on `%s`, so no pull request was opened (#2941).\n\n",
		CommentSafeACText(branch), CommentSafeACText(base), CommentSafeACText(base)))
	closeIssue = IssueClosedBy(verdicts)
	var met []ACVerdict
	for _, v := range verdicts {
		if v.Status == ACMet {
			met = append(met, v)
		}
	}
	if len(met) > 0 {
		b.WriteString("Met, with the evidence recorded for each:\n\n")
		for _, v := range met {
			b.WriteString("- " + CommentSafeACText(v.Text))
			if v.Evidence != "" {
				b.WriteString(fmt.Sprintf(" (%s: %s)", v.Source, CommentSafeACText(v.Evidence)))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if closeIssue {
		b.WriteString("Every acceptance criterion is met, so the issue is closed.\n")
		return true, b.String()
	}
	open := OpenACs(verdicts)
	if len(open) == 0 {
		b.WriteString(fmt.Sprintf("No acceptance criteria were found to verify, so #%d stays open.\n\n", issueNumber))
	} else {
		b.WriteString("Still unmet or not verifiable:\n\n")
		for _, v := range open {
			b.WriteString(fmt.Sprintf("- %s — _%s", CommentSafeACText(v.Text), acStatusLabel(v.Status)))
			if v.Status == ACNotVerifiable && v.Step != "" {
				b.WriteString(" — needs: " + CommentSafeACText(v.Step))
			} else if v.Evidence != "" {
				b.WriteString(fmt.Sprintf(" — %s: %s", v.Source, CommentSafeACText(v.Evidence)))
			}
			b.WriteString("_\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("The issue is moved to Backlog so the pipeline does not dispatch it again. Re-open the work with a change that addresses what is left, or close the issue by hand.\n")
	return false, b.String()
}

// NoDiffTerminalDetail is the stage failure text for a settled no-diff run.
// Each form carries the marker the terminal-kind table routes on: a closed
// issue is issue_closed (board to Done, no failure), an open one is
// not_pipeline_actionable (parked in Backlog, no retry), because no further
// pipeline lap can change a branch that is level with its base.
func NoDiffTerminalDetail(issueNumber int, branch, base string, closed bool, unmet int) string {
	if closed {
		return fmt.Sprintf("issue_closed: pr-create found branch %s level with %s and every acceptance criterion met; #%d was closed with the evidence [#2941]",
			branch, base, issueNumber)
	}
	return fmt.Sprintf("[not-pipeline-actionable] pr-create: branch %s has no diff against %s, so there is nothing to open a PR for; %d acceptance criteria remain unmet or unverifiable and #%d waits in Backlog for a human [#2941]",
		branch, base, unmet, issueNumber)
}
