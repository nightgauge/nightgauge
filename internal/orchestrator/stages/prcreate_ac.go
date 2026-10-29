package stages

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/preflight"
)

// Acceptance-criterion verdicts (#2595, #2655). A PR says `Closes #N` only
// when every criterion is ACMet (IssueClosedBy). Any ACUnmet or
// ACNotVerifiable criterion, or no criteria at all, gives `Refs #N`, so
// merging the PR leaves the issue open with the remainder named.
const (
	ACMet           = "met"
	ACUnmet         = "unmet"
	ACNotVerifiable = "not_verifiable"
)

// Verdict sources, recorded so a reviewer can tell a mechanical check from a
// judgement.
const (
	ACSourceDeterministic = "deterministic"
	ACSourceDiff          = "diff"
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
	// Step names the runtime or operator step a not_verifiable criterion
	// needs (#2665), so the PR says what would verify it.
	Step string `json:"step,omitempty"`
}

// NormalizeACStatus maps the verdict vocabulary feature-validate writes, and
// the ac-reconcile classification names, onto the three statuses. Anything
// unrecognised is not verifiable: an unreadable verdict proves nothing.
func NormalizeACStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "met", "satisfied", "pass", "passed":
		return ACMet
	case "unmet", "unsatisfied", "not_met", "not met", "not-met", "fail", "failed",
		"partial", "partially met", "partially_met", "partially-met", "missing":
		return ACUnmet
	default:
		return ACNotVerifiable
	}
}

// ResolveACVerdicts produces one verdict per criterion, in index order.
//
// Each criterion gathers up to three answers: the deterministic tree
// reconcile (preflight.ReconcileACs), the diff verifier (diffCheck, nil when
// no diff was collected), and feature-validate's verdict. An unmet from any
// source wins, so a heuristic met can never close an issue another source
// judged unmet (#2665). Otherwise any met wins, cited from the first of
// deterministic, diff, validate. Otherwise the criterion is not verifiable,
// with the runtime step named when the diff verifier found one.
//
// Validate verdicts are matched to criteria by matchValidateVerdicts: text
// first, index only as a fallback, so a reordered list cannot misfile one.
func ResolveACVerdicts(criteria []string, validate []ACVerdict, det *preflight.ACReconcileResult, diffCheck ACDiffChecker) []ACVerdict {
	if len(criteria) == 0 {
		return nil
	}
	matched := matchValidateVerdicts(criteria, validate)
	detByIndex := map[int]preflight.ReconciledCriterion{}
	if det != nil {
		for _, c := range det.AcceptanceCriteria {
			detByIndex[c.Index] = c
		}
	}

	out := make([]ACVerdict, 0, len(criteria))
	for i, text := range criteria {
		var cands []ACVerdict
		if c, ok := detByIndex[i]; ok && (c.Classification == preflight.ACSatisfied || c.Classification == preflight.ACUnsatisfied) {
			ev := strings.Join(c.Evidence, ", ")
			if ev == "" {
				ev = c.Reason
			}
			cands = append(cands, ACVerdict{Status: NormalizeACStatus(c.Classification), Source: ACSourceDeterministic, Evidence: ev})
		}
		if diffCheck != nil {
			if r, ok := diffCheck(text); ok {
				cands = append(cands, ACVerdict{Status: r.Status, Source: ACSourceDiff, Evidence: r.Evidence, Step: r.Step})
			}
		}
		if val, ok := matched[i]; ok {
			v := ACVerdict{Status: NormalizeACStatus(val.Status), Source: ACSourceValidate, Evidence: strings.TrimSpace(val.Evidence)}
			if v.Status == ACNotVerifiable && negativeEvidenceRE.MatchString(v.Evidence) {
				v.Status = ACUnmet
			}
			cands = append(cands, v)
		}

		v := ACVerdict{Status: ACNotVerifiable, Source: ACSourceNone, Evidence: "no rule or verdict could check this criterion"}
		if r, ok := firstACStatus(cands, ACUnmet); ok {
			v = r
		} else if r, ok := firstACStatus(cands, ACMet); ok {
			v = r
		} else if len(cands) > 0 {
			// Every answer is not_verifiable: prefer the one naming a step.
			v = cands[len(cands)-1]
			for _, c := range cands {
				if c.Step != "" {
					v = c
					break
				}
			}
		}
		v.Index, v.Text = i, text
		out = append(out, v)
	}
	return out
}

// matchValidateVerdicts assigns each criterion at most one validate verdict,
// claiming each verdict at most once, in four passes (#2679):
//
//  1. equal acMatchKey;
//  2. a unique prefix match, either way round, so a criterion stored
//     truncated still finds the verdict that carries its full text;
//  3. a verdict at the criterion's index whose text matched no criterion;
//  4. a verdict with no text at the criterion's index.
//
// Text passes run first, so a reordered list still lands on its own text.
func matchValidateVerdicts(criteria []string, validate []ACVerdict) map[int]ACVerdict {
	out := map[int]ACVerdict{}
	claimed := make([]bool, len(validate))
	vkeys := make([]string, len(validate))
	for j, v := range validate {
		vkeys[j] = acMatchKey(v.Text)
	}
	ckeys := make([]string, len(criteria))
	for i, c := range criteria {
		ckeys[i] = acMatchKey(c)
	}
	claim := func(i, j int) {
		out[i] = validate[j]
		claimed[j] = true
	}
	for i, ck := range ckeys {
		for j, vk := range vkeys {
			if ck != "" && !claimed[j] && vk == ck {
				claim(i, j)
				break
			}
		}
	}
	for i, ck := range ckeys {
		if _, ok := out[i]; ok || ck == "" {
			continue
		}
		hit := -1
		for j, vk := range vkeys {
			if claimed[j] || vk == "" || !(strings.HasPrefix(vk, ck) || strings.HasPrefix(ck, vk)) {
				continue
			}
			if hit >= 0 {
				hit = -2
				break
			}
			hit = j
		}
		if hit >= 0 {
			claim(i, hit)
		}
	}
	for _, wantText := range []bool{true, false} {
		for i := range criteria {
			if _, ok := out[i]; ok {
				continue
			}
			for j, v := range validate {
				if !claimed[j] && v.Index == i && (vkeys[j] != "") == wantText {
					claim(i, j)
					break
				}
			}
		}
	}
	return out
}

// acMatchKey is the markdown-insensitive key used only to match validate
// verdicts to criteria: whitespace-collapsed, lowercased, without backticks
// or `*`/`_` emphasis, and without trailing `.`, `;` or `:`. It is not
// NormalizeACText, which stays whitespace-only for fingerprints (#2623).
func acMatchKey(s string) string {
	s = strings.ToLower(preflight.NormalizeACText(s))
	s = strings.NewReplacer("`", "", "*", "", "_", "").Replace(s)
	return strings.TrimRight(strings.Join(strings.Fields(s), " "), ".;: ")
}

func firstACStatus(cands []ACVerdict, status string) (ACVerdict, bool) {
	for _, c := range cands {
		if c.Status == status {
			return c, true
		}
	}
	return ACVerdict{}, false
}

// negativeEvidenceRE matches validate evidence that says the criterion is
// not met even though its status did not (#2655).
var negativeEvidenceRE = regexp.MustCompile(`(?i)\b(?:not met|unmet|not satisfied|unsatisfied|not implemented)\b`)

// IssueClosedBy reports whether a PR with these verdicts closes its issue:
// only when there is at least one criterion and every one is met.
func IssueClosedBy(verdicts []ACVerdict) bool {
	if len(verdicts) == 0 {
		return false
	}
	for _, v := range verdicts {
		if v.Status != ACMet {
			return false
		}
	}
	return true
}

// OpenACs returns every verdict that is not met, in order.
func OpenACs(verdicts []ACVerdict) []ACVerdict {
	var out []ACVerdict
	for _, v := range verdicts {
		if v.Status != ACMet {
			out = append(out, v)
		}
	}
	return out
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

// RewriteAllClosesToRefs replaces every closing keyword with `Refs`,
// whatever issue it names. It is for untrusted text — acceptance-criterion
// text and evidence come from an issue body anyone who can file an issue
// writes — so no issue closes because a criterion named it.
func RewriteAllClosesToRefs(s string) string {
	return closingKeywordRE.ReplaceAllStringFunc(s, func(match string) string {
		return "Refs " + closingKeywordRE.FindStringSubmatch(match)[1]
	})
}

// AppendUnmetACs adds criteria under UnmetACHeading, creating the section
// when the body has none, so post-merge names them as the remainder.
func AppendUnmetACs(body string, issueNumber int, criteria []string) string {
	var items strings.Builder
	for _, c := range criteria {
		items.WriteString("- " + RewriteAllClosesToRefs(c) + "\n")
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
