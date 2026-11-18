package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

// Check-named criteria at merge (#3424). A criterion naming a CI check
// ("`integration-tests` passes") is not verifiable at pr-create: the check
// runs only once the PR exists. pr-merge waits for that very check, so after
// the wait it re-judges every criterion the PR body left open whose code span
// names a check in the status rollup, by that check's conclusion, and
// promotes `Refs #N` to `Closes #N` when nothing then remains.

// checkCriterionResult is one open criterion's verdict from the rollup.
type checkCriterionResult struct {
	Text     string
	Met      bool
	Evidence string
}

// checkSuccess is the one conclusion that meets a check-named criterion.
const checkSuccess = "SUCCESS"

// unmetItem is one bullet under UnmetACHeading: its text, and the line range
// [start, end] it occupies (the bullet and its indented status line).
type unmetItem struct {
	text       string
	start, end int
}

// judgeCheckCriteria re-judges the PR body's open check-named criteria from
// snap's status rollup, rewrites the body once, and promotes it to close the
// issue when every criterion is then met under the post-merge hook's own
// remainder rule and no issue criterion is uncovered.
// Fails closed toward `Refs`: a client without prBodyClient, a failed PR-body
// read or a failed edit leaves the body as it was; a failed issue-body read
// still records the re-judged verdicts but keeps `Refs`.
func (r *DeterministicRunner) judgeCheckCriteria(ctx context.Context, gh ghClient, workdir string, issueNumber, prNumber int, snap PRViewSnapshot) {
	bc, ok := gh.(prBodyClient)
	if !ok || len(snap.StatusCheckRollup) == 0 {
		return
	}
	body, err := bc.PRBody(ctx, prNumber)
	if err != nil {
		log.Printf("pr-merge: check-criteria judge skipped for PR #%d: read PR body: %v", prNumber, err)
		return
	}
	repo := readPRContextRepo(workdir, issueNumber)
	if BodyClosesIssue(body, repo, issueNumber) {
		return
	}
	newBody, judged := rejudgeCheckCriteria(body, snap.StatusCheckRollup, prNumber)
	if len(judged) == 0 {
		return
	}
	closes := false
	if issueBody, err := bc.IssueBody(ctx, issueNumber); err != nil {
		log.Printf("pr-merge: #%d stays Refs: read issue body: %v", issueNumber, err)
	} else if promoted, ok := promoteRefsToCloses(newBody, issueNumber); ok && checkCriteriaClose(issueBody, newBody, readACReconcile(workdir, issueNumber)) {
		newBody, closes = promoted, true
	}
	if err := bc.EditPRBody(ctx, prNumber, newBody); err != nil {
		log.Printf("pr-merge: could not record check-criteria verdicts on PR #%d: %v", prNumber, err)
		return
	}
	for _, j := range judged {
		status := ACUnmet
		if j.Met {
			status = ACMet
		}
		log.Printf("pr-merge: PR #%d criterion re-judged %s from its check: %s (%s)", prNumber, status, j.Text, j.Evidence)
	}
	if closes {
		log.Printf("pr-merge: every criterion of #%d is met — PR #%d now says Closes #%d", issueNumber, prNumber, issueNumber)
		if err := setPRContextCloses(workdir, issueNumber, true); err != nil {
			log.Printf("pr-merge: could not record closes_issue:true for #%d: %v", issueNumber, err)
		}
	}
}

// checkCriteriaClose reports whether a merge of prBody closes the issue with
// nothing left: the post-merge remainder is empty, no unmet section remains,
// and every unchecked issue criterion was reconciled.
func checkCriteriaClose(issueBody, prBody string, rec *preflight.ACReconcileResult) bool {
	if strings.Contains(prBody, UnmetACHeading) {
		return false
	}
	if len(preflight.UncoveredACs(issueBody, rec)) > 0 {
		return false
	}
	remaining, found := hooks.RemainingCriteria(issueBody, prBody)
	return found && len(remaining) == 0
}

// rejudgeCheckCriteria judges each criterion under UnmetACHeading whose code
// spans name a check in rollup: met when every named check concluded
// SUCCESS, unmet otherwise. A met criterion moves to `## Acceptance
// criteria`; an unmet one gets its status line rewritten. The unmet section
// is dropped when it empties. A criterion naming no check on the PR is left
// as it was. judged is empty when nothing changed.
func rejudgeCheckCriteria(body string, rollup []PRStatusCheckRow, prNumber int) (string, []checkCriterionResult) {
	lines := strings.Split(body, "\n")
	heading := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == UnmetACHeading {
			heading = i
			break
		}
	}
	if heading < 0 {
		return body, nil
	}
	items := unmetItems(lines, heading)
	conclusions := checkConclusions(rollup)
	var judged []checkCriterionResult
	var metLines []string
	drop := map[int]bool{}
	replace := map[int]string{}
	for _, it := range items {
		if bookedUnmet(lines, it) {
			// pr-create judged this unmet on diff evidence; a green check
			// whose name matches a code span never lifts that.
			continue
		}
		res, ok := judgeByChecks(it.text, conclusions, prNumber)
		if !ok {
			continue
		}
		judged = append(judged, res)
		if res.Met {
			metLines = append(metLines, fmt.Sprintf("- %s: %s (this PR — pr-merge: %s)", acStatusLabel(ACMet), it.text, res.Evidence))
			for i := it.start; i <= it.end; i++ {
				drop[i] = true
			}
			continue
		}
		status := fmt.Sprintf("  _%s — pr-merge: %s_", acStatusLabel(ACUnmet), res.Evidence)
		if it.end > it.start {
			replace[it.end] = status
		} else {
			replace[it.start] = lines[it.start] + "\n" + status
		}
	}
	if len(judged) == 0 {
		return body, nil
	}
	if len(metLines) == len(items) {
		// The section is empty: drop it whole, heading to last item.
		last := items[len(items)-1].end
		for i := heading; i <= last; i++ {
			drop[i] = true
		}
		if last+1 < len(lines) && strings.TrimSpace(lines[last+1]) == "" {
			drop[last+1] = true
		}
	}
	// Met lines join the existing `## Acceptance criteria` section, or open
	// one where the unmet section starts.
	insertAt, prefix, suffix := heading, []string{ACMetHeading, ""}, []string{""}
	for i, l := range lines {
		if strings.TrimSpace(l) == ACMetHeading {
			insertAt, prefix, suffix = acSectionEnd(lines, i), nil, nil
			break
		}
	}
	var out []string
	for i, l := range lines {
		if i == insertAt && len(metLines) > 0 {
			out = append(append(append(out, prefix...), metLines...), suffix...)
		}
		if drop[i] {
			continue
		}
		if s, ok := replace[i]; ok {
			l = s
		}
		out = append(out, l)
	}
	if insertAt >= len(lines) {
		out = append(out, metLines...)
	}
	return strings.Join(out, "\n"), judged
}

// bookedUnmet reports whether it carries a `❌ unmet` status line.
func bookedUnmet(lines []string, it unmetItem) bool {
	for i := it.start + 1; i <= it.end; i++ {
		if strings.Contains(lines[i], acStatusLabel(ACUnmet)) {
			return true
		}
	}
	return false
}

// acSectionEnd is the line after the last bullet of the `## Acceptance
// criteria` section at heading, where new met lines are appended.
func acSectionEnd(lines []string, heading int) int {
	end := heading + 1
	for i := heading + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "#") {
			break
		}
		if strings.HasPrefix(t, "- ") {
			end = i + 1
		}
	}
	return end
}

// unmetItems returns the bullets of the unmet section at heading, each with
// its indented status line, stopping at the next heading.
func unmetItems(lines []string, heading int) []unmetItem {
	var out []unmetItem
	for i := heading + 1; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			break
		}
		indented := l != strings.TrimLeft(l, " \t")
		switch {
		case !indented && strings.HasPrefix(t, "- "):
			out = append(out, unmetItem{text: strings.TrimSpace(t[2:]), start: i, end: i})
		case indented && strings.HasPrefix(t, "_") && len(out) > 0 && out[len(out)-1].end == i-1:
			out[len(out)-1].end = i
		}
	}
	return out
}

// checkConclusions maps each check name (lowercased, trimmed) to its worst
// conclusion: rows sharing a name (re-runs, matrix legs) count as passed only
// when every one concluded SUCCESS.
func checkConclusions(rollup []PRStatusCheckRow) map[string]string {
	out := map[string]string{}
	for _, c := range rollup {
		key := strings.ToLower(strings.TrimSpace(c.Name))
		if key == "" {
			continue
		}
		prev, seen := out[key]
		if !seen || (prev == checkSuccess && c.Conclusion != checkSuccess) ||
			checkGapRank(c.Conclusion) > checkGapRank(prev) {
			out[key] = c.Conclusion
		}
	}
	return out
}

// checkClaimWords are the only words a criterion may hold besides its code
// spans for a green check to satisfy it whole: "`lint` passes", "the `unit`
// and `e2e` checks are green against platform `main`".
var checkClaimWords = map[string]bool{
	"a": true, "all": true, "and": true, "are": true, "both": true, "check": true,
	"checks": true, "green": true, "is": true, "pass": true, "passed": true,
	"passes": true, "succeed": true, "succeeded": true, "succeeds": true, "the": true,
	"against": true, "on": true, "platform": true, "branch": true, "ci": true,
}

// checkRefWords precede a code span that names a ref, not a check.
var checkRefWords = map[string]bool{"against": true, "on": true, "platform": true, "branch": true}

// judgeByChecks judges a criterion by the checks its code spans name. ok is
// false when no span names a check on the PR, when a named check has not
// concluded yet, or when the checks are not the whole claim. A failed named
// check makes the criterion unmet; it is met only when the criterion says
// nothing but that its named checks pass, and every one concluded SUCCESS.
func judgeByChecks(text string, conclusions map[string]string, prNumber int) (checkCriterionResult, bool) {
	var passed, failed []string
	pending, whole := false, true
	seen := map[string]bool{}
	var rest strings.Builder
	prev := 0
	for _, sp := range acCodeSpans(text) {
		before := text[prev:sp[0]]
		rest.WriteString(before + " ")
		prev = sp[1]
		name := strings.TrimSpace(acSpanContent(text[sp[2]:sp[3]]))
		key := strings.ToLower(name)
		concl, ok := conclusions[key]
		if !ok {
			words := strings.Fields(strings.ToLower(before))
			if len(words) == 0 || !checkRefWords[words[len(words)-1]] {
				whole = false
			}
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		switch concl {
		case checkSuccess:
			passed = append(passed, fmt.Sprintf("check `%s` concluded SUCCESS on PR #%d", name, prNumber))
		case "":
			pending = true
		default:
			failed = append(failed, fmt.Sprintf("check `%s` concluded %s on PR #%d", name, concl, prNumber))
		}
	}
	rest.WriteString(text[prev:])
	for _, w := range strings.FieldsFunc(strings.ToLower(rest.String()), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if !checkClaimWords[w] {
			whole = false
		}
	}
	switch {
	case len(failed) > 0:
		return checkCriterionResult{Text: text, Evidence: strings.Join(failed, "; ")}, true
	case pending || !whole || len(passed) == 0:
		return checkCriterionResult{}, false
	}
	return checkCriterionResult{Text: text, Met: true, Evidence: strings.Join(passed, "; ")}, true
}

// promoteRefsToCloses turns the `Refs #N` line pr-create writes into
// `Closes #N`; ok=false when the body has no such line. Only a whole line is
// rewritten, the last one, so a reference inside criterion text never
// becomes a closer.
func promoteRefsToCloses(body string, n int) (string, bool) {
	re := regexp.MustCompile(fmt.Sprintf(`(?m)^Refs #%d[ \t]*$`, n))
	locs := re.FindAllStringIndex(body, -1)
	if len(locs) == 0 {
		return body, false
	}
	last := locs[len(locs)-1]
	return body[:last[0]] + fmt.Sprintf("Closes #%d", n) + body[last[1]:], true
}

// readACReconcile reads ac-reconcile-{N}.json; nil when absent or invalid.
func readACReconcile(workdir string, issueNumber int) *preflight.ACReconcileResult {
	data, err := readFile(pipelineContextPath(workdir, fmt.Sprintf("ac-reconcile-%d.json", issueNumber)))
	if err != nil {
		return nil
	}
	var rec preflight.ACReconcileResult
	if json.Unmarshal(data, &rec) != nil {
		return nil
	}
	return &rec
}
