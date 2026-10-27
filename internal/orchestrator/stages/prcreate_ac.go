package stages

import (
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
