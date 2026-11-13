package stages

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

// Acceptance-criterion verdicts (#2595, #2655). A PR says `Closes #N` only
// when every criterion is ACMet (IssueClosedBy). Any ACUnmet,
// ACNotVerifiable or ACNotJudged criterion, or no criteria at all, gives
// `Refs #N`, so merging the PR leaves the issue open with the remainder named.
//
// ACNotJudged is a criterion of the issue's current body that the run's
// ac-reconcile report never contained (#3228): the run never saw it, which is
// different from seeing it and failing to verify it.
const (
	ACMet           = "met"
	ACUnmet         = "unmet"
	ACNotVerifiable = "not_verifiable"
	ACNotJudged     = "not_judged"
)

// Verdict sources, recorded so a reviewer can tell a mechanical check from a
// judgement.
const (
	ACSourceDeterministic = "deterministic"
	ACSourceDiff          = "diff"
	ACSourceValidate      = "feature-validate"
	ACSourceNone          = "none"
	// ACSourceStale marks a criterion the run's ac-reconcile report did not
	// contain (#3228).
	ACSourceStale = "ac-reconcile"
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
	// Uncommitted lists the uncommitted or untracked paths a diff verdict's
	// evidence rests on (#2669): the commit owner has not committed them yet.
	Uncommitted []string `json:"uncommitted_paths,omitempty"`
	// PRNote is the prose feature-validate wrote for the PR body about this
	// criterion (#3154). pr-create renders it under ACNotesHeading, so a
	// criterion asking for PR-description content is met by what the body
	// then carries.
	PRNote string `json:"pr_note,omitempty"`
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
// An index-matched verdict may only contribute unmet (#2669): a position is
// not evidence a verdict is about this criterion, so it can hold an issue
// open but never close it. Negative evidence counts only in a clause that
// names no other criterion, and never overturns a deterministic met.
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
		detMet := false
		if c, ok := detByIndex[i]; ok && (c.Classification == preflight.ACSatisfied || c.Classification == preflight.ACUnsatisfied) {
			ev := strings.Join(c.Evidence, ", ")
			if ev == "" {
				ev = c.Reason
			}
			cands = append(cands, ACVerdict{Status: NormalizeACStatus(c.Classification), Source: ACSourceDeterministic, Evidence: ev})
			detMet = c.Classification == preflight.ACSatisfied
		}
		if diffCheck != nil {
			if r, ok := diffCheck(text, criteria[:i]); ok {
				ev := r.Evidence
				if len(r.Uncommitted) > 0 {
					ev += " (rests on uncommitted paths: " + strings.Join(r.Uncommitted, ", ") + ")"
				}
				cands = append(cands, ACVerdict{Status: r.Status, Source: ACSourceDiff, Evidence: ev, Step: r.Step, Uncommitted: r.Uncommitted})
			}
		}
		m, hasVal := matched[i]
		var valV ACVerdict
		if hasVal {
			valV = ACVerdict{Status: NormalizeACStatus(m.v.Status), Source: ACSourceValidate,
				Evidence: strings.TrimSpace(m.v.Evidence), PRNote: strings.TrimSpace(m.v.PRNote)}
			if valV.Status == ACNotVerifiable && !detMet && acNegativeEvidence(valV.Evidence, i) {
				valV.Status = ACUnmet
			}
		}
		bodyEvidence := len(validate) > 0 && acAsksForPRBody(text)
		if bodyEvidence {
			valV, hasVal = acPRBodyVerdict(valV, hasVal)
		}
		// Applied last: an index-matched verdict may only add unmet (#2669),
		// including one the PR-body rule upgraded to met.
		if hasVal && m.byIndex && valV.Status != ACUnmet {
			valV, hasVal = ACVerdict{}, false
		}
		note := ""
		if hasVal {
			cands = append(cands, valV)
			note = valV.PRNote
		}

		v := ACVerdict{Status: ACNotVerifiable, Source: ACSourceNone, Evidence: "no rule or verdict could check this criterion"}
		if r, ok := firstACStatus(cands, ACUnmet); ok {
			v = r
		} else if bodyEvidence && valV.Status == ACMet {
			// The criterion asks for its evidence in the PR body: cite the
			// recorded command and result, which the body then carries.
			v = valV
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
		// The note renders whichever source decided the verdict (#3154).
		v.Index, v.Text, v.PRNote = i, text, note
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
// byIndex records a pass 3 or 4 match.
func matchValidateVerdicts(criteria []string, validate []ACVerdict) map[int]acMatchedVerdict {
	out := map[int]acMatchedVerdict{}
	claimed := make([]bool, len(validate))
	vkeys := make([]string, len(validate))
	for j, v := range validate {
		vkeys[j] = acMatchKey(v.Text)
	}
	ckeys := make([]string, len(criteria))
	for i, c := range criteria {
		ckeys[i] = acMatchKey(c)
	}
	claim := func(i, j int, byIndex bool) {
		out[i] = acMatchedVerdict{v: validate[j], byIndex: byIndex}
		claimed[j] = true
	}
	for i, ck := range ckeys {
		for j, vk := range vkeys {
			if ck != "" && !claimed[j] && vk == ck {
				claim(i, j, false)
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
			claim(i, hit, false)
		}
	}
	for _, wantText := range []bool{true, false} {
		for i := range criteria {
			if _, ok := out[i]; ok {
				continue
			}
			for j, v := range validate {
				if !claimed[j] && v.Index == i && (vkeys[j] != "") == wantText {
					claim(i, j, true)
					break
				}
			}
		}
	}
	return out
}

type acMatchedVerdict struct {
	v       ACVerdict
	byIndex bool
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

// acPRBodyRE matches a criterion that asks for content in the PR body: "the
// command and its result are in the PR body", "the PR description lists the
// boundary", "the PR records the cause" (#2941, #3154). It runs on prose with
// code spans stripped.
var acPRBodyRE = regexp.MustCompile(`(?i)\b(?:PR|pull request) (?:body|description)\b` +
	`|\bthe PR (?:records|lists|names)\b`)

// acResultEvidenceRE matches evidence that records a command's result, not
// only that something was looked at: a zero exit status, Go's `ok  <pkg>`
// line, a `--- PASS` line, or a pass count. A bare "pass" or "ok" is not a
// result shape: "tests did not pass" carries both words.
var acResultEvidenceRE = regexp.MustCompile(`(?i)\bexit(?:ed|s)?\s*(?:code\s*|status\s*)?(?:=\s*)?0\b|\bok\s+\S+/\S+|--- PASS:|\b[1-9]\d*\s+passed\b`)

// acFailureEvidenceRE matches evidence that records a failure. Evidence that
// carries one is never promoted, whatever result shape it also holds.
var acFailureEvidenceRE = regexp.MustCompile(`(?i)\bFAIL(?:ED|S|URE)?\b|\bdid not\b|\bnot (?:pass|succeed)|\bexit(?:ed|s)?\s*(?:code\s*|status\s*)?(?:=\s*)?[1-9]\d*\b|\b[1-9]\d*\s+failed\b`)

// ACNoPRNote is the reason a PR-body criterion is unmet when feature-validate
// recorded nothing for the body to carry: no pr_note and no command result
// (#2941, #3154).
const ACNoPRNote = "feature-validate recorded no pr_note or command result for this criterion"

func acAsksForPRBody(text string) bool {
	return acPRBodyRE.MatchString(acStripCodeSpans(text))
}

// acPRBodyVerdict judges feature-validate's verdict on a criterion that asks
// for content in the PR body (#2941, #3154). pr-create writes the body, so the
// part feature-validate could not check itself (what the body will say) is
// done here. A verdict that is not unmet becomes met when it carries a
// pr_note, which the body renders under ACNotesHeading, or when it is
// not_verifiable with evidence recording a command result, which the body
// carries beside the criterion. No verdict, or one with neither, is unmet
// with ACNoPRNote: the body would carry nothing. An unmet verdict stays unmet.
func acPRBodyVerdict(v ACVerdict, ok bool) (ACVerdict, bool) {
	if ok && v.Status == ACUnmet {
		return v, true
	}
	if ok && v.PRNote != "" {
		v.Status = ACMet
		if v.Evidence == "" {
			v.Evidence = "pr_note rendered under " + strings.TrimPrefix(ACNotesHeading, "## ")
		}
		return v, true
	}
	if !ok || v.Evidence == "" || !acResultEvidenceRE.MatchString(v.Evidence) || acFailureEvidenceRE.MatchString(v.Evidence) {
		return ACVerdict{Status: ACUnmet, Source: ACSourceValidate, Evidence: ACNoPRNote}, true
	}
	if v.Status == ACNotVerifiable {
		v.Status = ACMet
	}
	return v, true
}

// negativeEvidenceRE matches validate evidence that says the criterion is
// not met even though its status did not (#2655).
var negativeEvidenceRE = regexp.MustCompile(`(?i)\b(?:not met|unmet|not satisfied|unsatisfied|not implemented)\b`)

// acOtherCriterionRE finds a reference to a criterion by number.
var acOtherCriterionRE = regexp.MustCompile(`(?i)\b(?:AC\s*#?|criteri(?:on|a)\s*#?)(\d+)\b`)

var acEvidenceClauseRE = regexp.MustCompile(`[.;\n]+`)

// acNegativeEvidence reports whether evidence for the criterion at 0-based
// index i says it is not met. A clause counts when it names no criterion or
// names this one; only a clause naming other criteria alone is ignored, so
// "AC3 is not met" in AC1's evidence leaves AC1 alone while "AC1 and AC3 are
// not met" does not (#2669).
func acNegativeEvidence(evidence string, i int) bool {
	for _, clause := range acEvidenceClauseRE.Split(evidence, -1) {
		if !negativeEvidenceRE.MatchString(clause) {
			continue
		}
		refs := acOtherCriterionRE.FindAllStringSubmatch(clause, -1)
		self := len(refs) == 0
		for _, m := range refs {
			if n, err := strconv.Atoi(m[1]); err == nil && n == i+1 {
				self = true
				break
			}
		}
		if self {
			return true
		}
	}
	return false
}

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

// ACNotesHeading heads the section where pr-create renders feature-validate's
// per-criterion pr_note (#3154): the PR-description content a criterion asks
// for.
const ACNotesHeading = "## Notes for acceptance criteria"

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

// closesTarget selects the closing references that close issue n of repo
// ("owner/repo"). An unknown repo ("") selects every reference to number n
// whatever repository it names: the AC backstop must fail closed, demoting a
// closer it cannot place rather than letting it close the issue at merge.
func closesTarget(repo string, n int) func(forge.ClosingRef) bool {
	if repo == "" {
		return func(r forge.ClosingRef) bool { return r.Number == n }
	}
	return func(r forge.ClosingRef) bool { return r.Targets(repo, n) }
}

// BodyClosesIssue reports whether body carries a closing keyword for issue n
// of repo ("owner/repo", or "" when unknown — see closesTarget). forge owns
// the one closing-keyword matcher, shared with the post-merge hook.
func BodyClosesIssue(body, repo string, n int) bool {
	targets := closesTarget(repo, n)
	for _, r := range forge.ClosingRefs(body) {
		if targets(r) {
			return true
		}
	}
	return false
}

// RewriteClosesToRefs replaces every closing keyword that targets issue n of
// repo with `Refs`, so merging the PR leaves the issue open (#2623, the #2595
// rule). References to other issues, including the same number in a named
// other repository, are untouched; an unknown repo ("") rewrites every
// reference to number n.
func RewriteClosesToRefs(body, repo string, n int) string {
	return forge.RewriteClosingRefs(body, closesTarget(repo, n))
}

// rewriteAllClosesToRefs replaces every closing keyword with `Refs`, whatever
// issue it names, including the issue-URL form. It rewrites keywords only:
// untrusted text goes through UntrustedACText, which also collapses line breaks.
func rewriteAllClosesToRefs(s string) string {
	return forge.RewriteClosingRefs(s, func(forge.ClosingRef) bool { return true })
}

// lineBreakRun is a run of whitespace that contains a line break.
var lineBreakRun = regexp.MustCompile(`[ \t\f]*[\r\n]\s*`)

// UntrustedACText renders criterion text or evidence for a PR body. Both come
// from an issue body anyone who can file an issue writes. It puts them on one
// line, so a newline cannot open a bullet or a heading inside the section
// hooks.UnmetCriteria parses (#2662), and rewrites every closing keyword to
// `Refs`, so no issue closes because a criterion named it. The line breaks go
// first, so a keyword split across them is rewritten too.
func UntrustedACText(s string) string {
	return rewriteAllClosesToRefs(lineBreakRun.ReplaceAllString(s, " "))
}

// AppendUnmetACs adds criteria under UnmetACHeading, creating the section
// when the body has none, so post-merge names them as the remainder.
func AppendUnmetACs(body string, issueNumber int, criteria []string) string {
	var items strings.Builder
	for _, c := range criteria {
		items.WriteString("- " + CommentSafeACText(c) + "\n")
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
