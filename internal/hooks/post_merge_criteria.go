package hooks

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/intelligence/acparse"
)

// PR verdict statuses, parsed from the body stages.renderPRBody writes (#3318).
const (
	verdictMet           = "met"
	verdictUnmet         = "unmet"
	verdictNotVerifiable = "not_verifiable"
	verdictNotVerified   = "not_verified"
)

// These labels must equal stages.acStatusLabel's; they are restated here
// because hooks must not import the orchestrator.
const (
	metLinePrefix         = "✅ met: "
	notVerifiableLabel    = "❔ not verifiable"
	unmetLabel            = "❌ unmet"
	notVerifiedLabel      = "❔ not verified"
	prAcceptanceHeading   = "## Acceptance criteria"
	minPrefixMatchRunes   = 16
	acceptanceHeadingWord = "acceptance criteria"
)

// prVerdict is one criterion the merged PR body reported on.
type prVerdict struct {
	Text   string
	Status string
}

// prCriteriaVerdicts returns the per-criterion verdicts in a PR body: met
// lines under "## Acceptance criteria" and bullets under the unmet heading,
// each classified by the indented status line that follows it.
func prCriteriaVerdicts(prBody string) []prVerdict {
	var out []prVerdict
	section := ""
	lastUnmet := -1
	for _, line := range strings.Split(prBody, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			switch t {
			case prAcceptanceHeading:
				section = "met"
			case unmetCriteriaHeading:
				section = "unmet"
			default:
				section = ""
			}
			lastUnmet = -1
			continue
		}
		switch section {
		case "met":
			if strings.HasPrefix(t, "- "+metLinePrefix) {
				out = append(out, prVerdict{Text: strings.TrimSpace(strings.TrimPrefix(t, "- "+metLinePrefix)), Status: verdictMet})
			}
		case "unmet":
			indented := line != strings.TrimLeft(line, " \t")
			if !indented && strings.HasPrefix(t, "- ") {
				out = append(out, prVerdict{Text: strings.TrimSpace(t[2:]), Status: verdictUnmet})
				lastUnmet = len(out) - 1
				continue
			}
			if indented && lastUnmet >= 0 && strings.HasPrefix(t, "_") {
				status := strings.TrimPrefix(t, "_")
				switch {
				case strings.HasPrefix(status, notVerifiableLabel):
					out[lastUnmet].Status = verdictNotVerifiable
				case strings.HasPrefix(status, notVerifiedLabel):
					out[lastUnmet].Status = verdictNotVerified
				case strings.HasPrefix(status, unmetLabel):
					out[lastUnmet].Status = verdictUnmet
				}
				lastUnmet = -1
			}
		}
	}
	return out
}

// issueCriterion is one acceptance criterion in the issue body. Index is the
// acparse index of a checkbox; Box is false for a numbered or plain item.
type issueCriterion struct {
	Text    string
	Checked bool
	Box     bool
	Index   int
}

var (
	headingRe      = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	fenceLineRe    = regexp.MustCompile("^\\s*(```|~~~)")
	listItemRe     = regexp.MustCompile(`^(?:\d+[.)]|[-*+])\s+(.*)$`)
	checkboxHeadRe = regexp.MustCompile(`^[-*+]\s+\[[ xX]\]`)
)

// issueCriteria returns every checkbox in the issue body plus the numbered and
// plain list items under an "Acceptance criteria" heading, in document order.
// Items inside code fences are ignored; numbered and plain items inside
// <details> blocks are ignored too (checkboxes there still count, as
// acparse.List counts them).
func issueCriteria(body string) []issueCriterion {
	boxes := acparse.List(body)
	boxAt := make(map[int]acparse.Item, len(boxes))
	for _, b := range boxes {
		boxAt[b.Line] = b
	}
	var out []issueCriterion
	lines := strings.Split(body, "\n")
	inFence, details := false, 0
	acLevel := 0 // heading level of the open Acceptance criteria section; 0 = none
	for i, raw := range lines {
		if b, ok := boxAt[i]; ok {
			out = append(out, issueCriterion{Text: b.Text, Checked: b.Checked, Box: true, Index: b.Index})
			continue
		}
		line := strings.TrimRight(raw, "\r")
		if fenceLineRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lower := strings.ToLower(line)
		details += strings.Count(lower, "<details") - strings.Count(lower, "</details>")
		if details < 0 {
			details = 0
		}
		if m := headingRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			level := len(m[1])
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m[2])), acceptanceHeadingWord) {
				acLevel = level
			} else if acLevel > 0 && level <= acLevel {
				acLevel = 0
			}
			continue
		}
		if acLevel == 0 || details > 0 || line != strings.TrimLeft(line, " \t") {
			continue
		}
		t := strings.TrimSpace(line)
		if checkboxHeadRe.MatchString(t) {
			continue
		}
		if m := listItemRe.FindStringSubmatch(t); m != nil && strings.TrimSpace(m[1]) != "" {
			out = append(out, issueCriterion{Text: strings.TrimSpace(m[1])})
		}
	}
	return out
}

var spaceRe = regexp.MustCompile(`\s+`)

// normaliseCriterion folds a criterion for comparison. Closing keywords are
// rewritten to `Refs` first, as stages.UntrustedACText renders criteria in the
// PR body, so the issue's copy and the PR's compare equal (#3529).
func normaliseCriterion(s string) string {
	s = forge.RewriteClosingRefs(s, func(forge.ClosingRef) bool { return true })
	s = strings.ToLower(s)
	s = strings.NewReplacer("`", "", "*", "").Replace(s)
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	return strings.TrimSpace(strings.TrimRight(s, "."))
}

// criterionMatches reports whether two criterion texts name the same
// criterion: equal after normalising, or one a prefix of the other when the
// shorter is long enough that the prefix is not a coincidence.
func criterionMatches(a, b string) bool {
	na, nb := normaliseCriterion(a), normaliseCriterion(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	short, long := na, nb
	if utf8.RuneCountInString(short) > utf8.RuneCountInString(long) {
		short, long = long, short
	}
	return utf8.RuneCountInString(short) >= minPrefixMatchRunes && strings.HasPrefix(long, short)
}

// tickMatches is the stricter match that licenses writing a tick: the met
// line must name the whole criterion, optionally followed by its evidence
// suffix, so a short claim cannot tick a stronger requirement.
func tickMatches(criterion, metText string) bool {
	nc, nm := normaliseCriterion(criterion), normaliseCriterion(metText)
	return nc != "" && (nm == nc || strings.HasPrefix(nm, nc+" (") || strings.HasPrefix(nm, nc+". ("))
}

// remainderPlan is what a partial delivery leaves, and which boxes it ticks.
type remainderPlan struct {
	// TickIndices are acparse indices of unticked boxes the PR met.
	TickIndices []int
	// Remaining is the criteria left, in issue order, then PR-only ones.
	Remaining []string
	// NotVerifiable is the subset of Remaining the PR marked not verifiable
	// (#3362), in the same order; the hook records it under its own heading.
	NotVerifiable []string
	// NotVerifiableOnly: every remaining criterion is not verifiable by the
	// pipeline or reserved to the supervisor/owner.
	NotVerifiableOnly bool
	// Met holds the issue text of every criterion this PR settled as met,
	// boxed or not (#3318); the hook records it in its marker comment.
	Met []string
	// Found is false when neither body names any criterion; the caller then
	// falls back to UnmetCriteria(prBody).
	Found bool
}

// computeRemainder joins the issue's criteria with the PR's verdicts (#3318).
// A criterion the PR met is excluded and its box ticked; one the PR left open
// stays whatever its box says; one the PR did not mention stays when it is
// an unticked box or a numbered/plain item. PR verdicts the issue never wrote
// are appended. A criterion an earlier partial-delivery marker recorded as met
// (priorMet, the issue's own wording) is dropped unless this PR restates it as
// unmet: the latest evidence wins, and "not verifiable" is no evidence
// against an earlier met record (#3362).
func computeRemainder(issueBody, prBody string, priorMet []string) remainderPlan {
	criteria := issueCriteria(issueBody)
	metBefore := func(text string) bool { return containsNormalised(priorMet, text) }
	verdicts := prCriteriaVerdicts(prBody)
	plan := remainderPlan{Found: len(criteria) > 0 || len(verdicts) > 0, Remaining: []string{}}
	usedVerdict := make([]bool, len(verdicts))
	keptVerdict := make([]bool, len(verdicts))
	var kept []string // normalised, for de-duplication
	allNotVerifiable := true
	keep := func(text string, notVerifiable bool) {
		n := normaliseCriterion(text)
		for _, k := range kept {
			if k == n {
				return
			}
		}
		kept = append(kept, n)
		plan.Remaining = append(plan.Remaining, text)
		if notVerifiable {
			plan.NotVerifiable = append(plan.NotVerifiable, text)
		}
		if !notVerifiable && !isOwnerOnlyRemainder([]string{text}) {
			allNotVerifiable = false
		}
	}
	for _, c := range criteria {
		// Prefer a verdict naming the whole criterion (exact, or a met line
		// with its evidence suffix) over one sharing only a prefix, so a
		// longer unmet criterion listed first cannot shadow this one's met.
		match := -1
		for i, v := range verdicts {
			if normaliseCriterion(c.Text) == normaliseCriterion(v.Text) ||
				(v.Status == verdictMet && tickMatches(c.Text, v.Text)) {
				match = i
				break
			}
		}
		if match < 0 {
			for i, v := range verdicts {
				if criterionMatches(c.Text, v.Text) {
					match = i
					break
				}
			}
		}
		if match < 0 {
			if !c.Checked && !metBefore(c.Text) {
				keep(c.Text, false)
			}
			continue
		}
		usedVerdict[match] = true
		v := verdicts[match]
		if v.Status == verdictMet {
			// Only a met line naming the whole criterion settles it; a
			// shorter claim matched by prefix leaves the criterion as if
			// unmentioned, so it cannot silently drop a stronger requirement.
			if !tickMatches(c.Text, v.Text) {
				if !c.Checked && !metBefore(c.Text) {
					keep(c.Text, false)
				}
				continue
			}
			if !containsNormalised(plan.Met, c.Text) {
				plan.Met = append(plan.Met, c.Text)
			}
			if c.Box && !c.Checked {
				plan.TickIndices = append(plan.TickIndices, c.Index)
			}
			continue
		}
		if v.Status == verdictNotVerifiable && metBefore(c.Text) {
			continue
		}
		// The issue's wording, not the PR's rendering of it, listed once per
		// verdict however many criteria it answers (#3529).
		if !keptVerdict[match] {
			keptVerdict[match] = true
			keep(c.Text, v.Status == verdictNotVerifiable)
		}
	}
	for i, v := range verdicts {
		if !usedVerdict[i] && v.Status != verdictMet {
			keep(v.Text, v.Status == verdictNotVerifiable)
		}
	}
	plan.NotVerifiableOnly = len(plan.Remaining) > 0 && allNotVerifiable
	return plan
}

// containsNormalised reports whether list holds text after normalisation.
func containsNormalised(list []string, text string) bool {
	n := normaliseCriterion(text)
	for _, m := range list {
		if normaliseCriterion(m) == n {
			return true
		}
	}
	return false
}

// IssueCriterionTexts returns the issue body's criteria in the wording
// post-merge records them under in a partial-delivery comment (#3362).
func IssueCriterionTexts(body string) []string {
	var out []string
	for _, c := range issueCriteria(body) {
		out = append(out, c.Text)
	}
	return out
}

// NormaliseCriterion is the normalisation the partial-delivery writer
// compares criteria under (#3362).
func NormaliseCriterion(s string) string { return normaliseCriterion(s) }

// RecordedCriterionMatches reports whether a criterion, in the issue's
// wording, is the one a partial-delivery record entry names: equal after
// normalising, or the entry followed by an evidence suffix (#3362). It is
// the rule the writer ticks under, so a shorter entry never matches a
// stronger criterion.
func RecordedCriterionMatches(criterion, recorded string) bool {
	return tickMatches(criterion, recorded)
}

// RemainingCriteria is the criteria a merge of a PR with prBody would leave
// open on an issue with issueBody, under the rule the post-merge hook applies
// (#3424): pr-merge promotes `Refs` to `Closes` only when it is empty, so the
// two never disagree. found=false when neither body names any criterion.
func RemainingCriteria(issueBody, prBody string) (remaining []string, found bool) {
	plan := computeRemainder(issueBody, prBody, nil)
	return plan.Remaining, plan.Found
}
