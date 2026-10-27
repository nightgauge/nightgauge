package stages

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/preflight"
)

// Acceptance-criterion verdicts (#2595). Only ACUnmet changes how the PR
// refers to its issue: a PR with any unmet criterion says `Refs #N`, so
// merging it leaves the issue open. ACNotVerifiable is listed in the body but
// keeps `Closes #N`, because most criteria cannot be checked mechanically and
// treating that as failure would leave nearly every issue open.
const (
	ACMet           = "met"
	ACUnmet         = "unmet"
	ACNotVerifiable = "not_verifiable"
)

// Verdict sources, recorded so a reviewer can tell a mechanical check from a
// judgement.
const (
	ACSourceDeterministic = "deterministic"
	ACSourceValidate      = "feature-validate"
	ACSourceNone          = "none"
)

// ACVerdict is the resolved verdict for one acceptance criterion.
type ACVerdict struct {
	Index    int    `json:"index"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Source   string `json:"source"`
	Evidence string `json:"evidence,omitempty"`
}

// NormalizeACStatus maps the verdict vocabulary feature-validate writes, and
// the ac-reconcile classification names, onto the three statuses. Anything
// unrecognised is not verifiable: an unreadable verdict proves nothing.
func NormalizeACStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "met", "satisfied", "pass", "passed":
		return ACMet
	case "unmet", "unsatisfied", "not_met", "fail", "failed":
		return ACUnmet
	default:
		return ACNotVerifiable
	}
}

// ResolveACVerdicts produces one verdict per criterion, in index order.
//
// Precedence per criterion: a deterministic `satisfied` or `unsatisfied`
// from preflight.ReconcileACs on the branch tree wins, because it was
// checked rather than judged. Otherwise feature-validate's verdict decides.
// With neither, the criterion is not verifiable.
func ResolveACVerdicts(criteria []string, validate []ACVerdict, det *preflight.ACReconcileResult) []ACVerdict {
	if len(criteria) == 0 {
		return nil
	}
	byIndex := make(map[int]ACVerdict, len(validate))
	for _, v := range validate {
		byIndex[v.Index] = v
	}
	detByIndex := map[int]preflight.ReconciledCriterion{}
	if det != nil {
		for _, c := range det.AcceptanceCriteria {
			detByIndex[c.Index] = c
		}
	}

	out := make([]ACVerdict, 0, len(criteria))
	for i, text := range criteria {
		v := ACVerdict{Index: i, Text: text, Status: ACNotVerifiable, Source: ACSourceNone}
		if c, ok := detByIndex[i]; ok && (c.Classification == preflight.ACSatisfied || c.Classification == preflight.ACUnsatisfied) {
			v.Status = NormalizeACStatus(c.Classification)
			v.Source = ACSourceDeterministic
			v.Evidence = strings.Join(c.Evidence, ", ")
			if v.Evidence == "" {
				v.Evidence = c.Reason
			}
		} else if val, ok := byIndex[i]; ok {
			v.Status = NormalizeACStatus(val.Status)
			v.Source = ACSourceValidate
			v.Evidence = strings.TrimSpace(val.Evidence)
		}
		out = append(out, v)
	}
	return out
}

// HasUnmet reports whether any verdict is unmet.
func HasUnmet(verdicts []ACVerdict) bool {
	for _, v := range verdicts {
		if v.Status == ACUnmet {
			return true
		}
	}
	return false
}

// UnmetACHeading is the PR-body heading that lists unmet criteria. The
// post-merge hook parses the section under it to name the remainder on the
// issue, so the two must agree.
const UnmetACHeading = "## ⚠️ Unmet acceptance criteria"

// acCheckboxBody rebuilds an issue-body fragment from criteria text so the
// deterministic reconciler can re-run against the branch tree.
func acCheckboxBody(criteria []string) string {
	var b strings.Builder
	for _, c := range criteria {
		b.WriteString("- [ ] ")
		b.WriteString(c)
		b.WriteString("\n")
	}
	return b.String()
}

// closingKeywordRE matches GitHub's closing keywords followed by an issue
// reference, optionally repo-qualified. Twin of hooks.closingKeywordRE, which
// stages must not import; the two must agree on what closes an issue.
var closingKeywordRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[ \t]*:?[ \t]*((?:[\w.-]+/[\w.-]+)?#(\d+))\b`)

// BodyClosesIssue reports whether body carries a closing keyword for issue n.
func BodyClosesIssue(body string, n int) bool {
	want := strconv.Itoa(n)
	for _, m := range closingKeywordRE.FindAllStringSubmatch(body, -1) {
		if m[2] == want {
			return true
		}
	}
	return false
}

// RewriteClosesToRefs replaces every closing keyword that targets issue n
// with `Refs`, so merging the PR leaves the issue open (#2623, the #2595
// rule). References to other issues are untouched.
func RewriteClosesToRefs(body string, n int) string {
	want := strconv.Itoa(n)
	return closingKeywordRE.ReplaceAllStringFunc(body, func(match string) string {
		m := closingKeywordRE.FindStringSubmatch(match)
		if m[2] != want {
			return match
		}
		return "Refs " + m[1]
	})
}

// AppendUnmetACs adds criteria under UnmetACHeading, creating the section
// when the body has none, so post-merge names them as the remainder.
func AppendUnmetACs(body string, issueNumber int, criteria []string) string {
	var items strings.Builder
	for _, c := range criteria {
		items.WriteString("- " + c + "\n")
	}
	if i := strings.Index(body, UnmetACHeading); i >= 0 {
		// Insert at the end of the existing section (next heading or EOF).
		start := i + len(UnmetACHeading)
		end := len(body)
		if j := strings.Index(body[start:], "\n## "); j >= 0 {
			end = start + j + 1
		}
		section := strings.TrimRight(body[:end], "\n") + "\n"
		return section + items.String() + "\n" + body[end:]
	}
	return strings.TrimRight(body, "\n") + "\n\n" + UnmetACHeading + "\n\n" +
		fmt.Sprintf("This PR leaves #%d open. These criteria remain:\n\n", issueNumber) +
		items.String()
}
