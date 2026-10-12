package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxTitleChars is GitHub's own bound on an issue title.
	maxTitleChars = 256
	// maxBodyChars leaves room under GitHub's 65,536 for the provenance.
	maxBodyChars = 60000
)

// createIssue files an issue the poster asked for in their own message, as
// the posting account's request, through the App for a teammate. Every check
// runs before the forge is reached: the poster's identity, their read access
// to the repository, their request in this turn's message, the per-turn
// bound and the arguments. It returns the tool's text and the detail a
// transcript receipt shows.
func (s *Toolset) createIssue(ctx context.Context, raw json.RawMessage) (string, string, *Error) {
	var a struct {
		repoArgs
		Title        string `json:"title"`
		Body         string `json:"body"`
		RequestQuote string `json:"request_quote"`
	}
	if err := decode(raw, &a); err != nil {
		return "", "", err
	}
	if !idPattern.MatchString(s.turn.PosterAccountID) ||
		(s.turn.PosterLogin != "" && !ownerPattern.MatchString(s.turn.PosterLogin)) {
		return "", "", fail(CodePosterUnknown, "this turn names no posting account to file the issue for, and an issue is never filed anonymously")
	}
	if err := s.checkRepo(a.repoArgs); err != nil {
		return "", "", err
	}
	if !Requested(s.turn.Message, a.RequestQuote, s.turn.IssueProposed) {
		return "", "", fail(CodeNotRequested,
			"the poster's own message in this turn does not ask for an issue or confirm one, so none is filed; "+
				"propose it and ask the poster to confirm")
	}
	title := strings.TrimSpace(a.Title)
	if title == "" || strings.ContainsAny(title, "\r\n") || utf8.RuneCountInString(title) > maxTitleChars {
		return "", "", fail(CodeInvalidArguments, fmt.Sprintf("title must be one line of 1 to %d characters", maxTitleChars))
	}
	if utf8.RuneCountInString(a.Body) > maxBodyChars {
		return "", "", fail(CodeInvalidArguments, fmt.Sprintf("body must be at most %d characters", maxBodyChars))
	}
	f, ferr := s.forge()
	if ferr != nil {
		return "", "", ferr
	}
	s.mu.Lock()
	if s.issues >= maxIssuesPerTurn {
		s.mu.Unlock()
		return "", "", fail(CodeLimitReached, fmt.Sprintf("one turn files at most %d issues", maxIssuesPerTurn))
	}
	// Counted before the request: a write that timed out may still have
	// landed.
	s.issues++
	s.mu.Unlock()

	body := strings.TrimRight(neutralizeProvenance(clean(a.Body)), "\n") + RenderProvenance(Provenance{
		Origin:           ProvenanceOrigin,
		ConversationID:   s.turn.ConversationID,
		TurnID:           s.turn.TurnID,
		ActorAccountID:   s.turn.PosterAccountID,
		ActorGitHubLogin: s.turn.PosterLogin,
	})
	ctx, cancel := context.WithTimeout(ctx, forgeTimeout)
	defer cancel()
	issue, err := f.CreateIssue(ctx, a.Owner, a.Repo, clean(title), body, s.turn.PosterIsOperator)
	if err != nil {
		return "", "", forgeErr(err)
	}
	ref := fmt.Sprintf("%s/%s#%d", a.Owner, a.Repo, issue.Number)
	detail := "filed " + ref
	if issue.URL != "" {
		detail += " (" + issue.URL + ")"
	}
	detail += " at the request of account " + s.turn.PosterAccountID
	out, _ := json.Marshal(map[string]any{
		"filed": ref, "url": issue.URL, "requested_by_account": s.turn.PosterAccountID,
	})
	return string(out), detail, nil
}

// The request gate. Requested is the conjunction of two gates, and accepts
// only what both accept:
//
//   - requestedV1 is the gate as #1570 first shipped it, kept verbatim as a
//     floor, so no later change to the gate can accept anything it refused;
//   - requestedOnMessage decides on the poster's message itself, closing the
//     differentials a security review found in requestedV1 (#1570).

// issueRequestV1 and confirmationV1 are requestedV1's patterns, unchanged.
var (
	issueRequestV1 = regexp.MustCompile(`(?i)\b(?:file|create|open|raise|log|submit|make|write|add)\s+` +
		`(?:(?:[\w'-]+\s+){0,4}(?:issues?|tickets?|bug\s+reports?)\b(?:\s*#?\d)?|(?:it|that|this|them|those|one)\b)`)
	confirmationV1 = regexp.MustCompile(`(?i)^\W*(?:yes|yeah|yep|yup|ok|okay|sure|confirm(?:ed)?|approved?|go ahead|do it|please do|ship it|lgtm|sounds good)\b`)
)

// requestedV1 is #1570's first gate: the quote, whitespace-collapsed, must
// occur in the message ignoring case, and either carry request words or be
// a confirmation the message opens with.
func requestedV1(message, quote string) bool {
	m, q := normalize(message), normalize(quote)
	if q == "" || !strings.Contains(strings.ToLower(m), strings.ToLower(q)) {
		return false
	}
	for _, loc := range issueRequestV1.FindAllStringIndex(q, -1) {
		if !issueNumber.MatchString(q[loc[0]:loc[1]]) {
			return true
		}
	}
	return confirmationV1.MatchString(q) && strings.HasPrefix(strings.ToLower(m), strings.ToLower(q))
}

// normalize collapses runs of whitespace and trims.
func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// nounRequest asks for an issue by name: a filing verb followed, within a
// few words, by "issue", "ticket" or "bug report" that is not an existing
// issue's number ("open issue #12" reads one).
var nounRequest = regexp.MustCompile(`\b(?:file|create|open|raise|log|submit|make|write|add)\s+` +
	`(?:[\w'-]+\s+){0,4}(?:issues?|tickets?|bug\s+reports?)\b(?:\s*#?\d)?`)

// pronounRequest asks for something already proposed: "file it", "create
// that". It names no issue, so it counts only when the reply just before
// this turn proposed one.
var pronounRequest = regexp.MustCompile(`\b(?:file|create|open|raise|log|submit|make|write|add)\s+` +
	`(?:it|that|this|them|those|one)\b`)

// issueNumber is the tail a request leaves when the noun was followed by a
// number.
var issueNumber = regexp.MustCompile(`\s*#?\d$`)

// negation is a word that turns a request around: "do not file an issue",
// "no need to open a ticket". Any one in the request's sentence refuses it.
var negation = regexp.MustCompile(`\b(?:not|no|nope|never|don't|dont|doesn't|didn't|won't|wouldn't|shouldn't|` +
	`can't|cannot|isn't|aren't|without|stop|avoid|instead|rather|nor|neither|hold off|cancel|wait|unless|later)\b|n't\b`)

// retraction opens a later sentence that takes a request back: "File an
// issue? No, wait." Any later sentence of the message that opens with one
// refuses every request before it.
var retraction = regexp.MustCompile(`^[\s\p{P}]*(?:no|nope|not|never|don't|do not|wait|hold on|cancel|actually|` +
	`never ?mind|scratch that|forget (?:it|that)|ignore (?:it|that))\b`)

// sentenceEnd separates the sentences a negation is looked for in.
var sentenceEnd = regexp.MustCompile(`[.!?;:\n]|\bbut\b`)

// confirmationOnly is a message that is nothing but a confirmation: it opens
// with one of requestedV1's confirming phrases, and every word after is
// another, or a courtesy ("please", "thanks") or a pronoun request ("file
// it"). "Yes, file it" and "Yes, thanks" are confirmations; "Thanks",
// "Please", "Yes. What is the status?" and "Yes, but don't" are not.
var confirmationOnly = regexp.MustCompile(`^[\s\p{P}]*(?:` + confirmPhrases + `)(?:[\s\p{P}]+(?:` + confirmPhrases +
	`|please|thanks|thank you|go for it|(?:file|create|open) (?:it|that)))*[\s\p{P}]*$`)

// confirmPhrases are requestedV1's confirming phrases.
const confirmPhrases = `yes|yeah|yep|yup|ok|okay|sure|confirm|confirmed|approve|approved|go ahead|do it|please do|ship it|lgtm|sounds good`

// Requested reports whether the poster's message in this turn asks for the
// mutation the model wants to make. message is the poster's own message in
// this turn, taken from the hosted service's record and bound to the
// poster's account (an empty message refuses everything). quote is the
// model's pointer to the words. proposed reports that the reply just before
// this turn proposed an issue, which a bare confirmation, or a request that
// names no issue ("file it"), needs.
//
// It is the conjunction of requestedV1, the first gate, and
// requestedOnMessage, so it never accepts what requestedV1 refused. Text from
// anyone else (an earlier message, a file, an issue body, any tool result)
// is never in the poster's message, so it can never satisfy it, whatever it
// says. It understands English requests only and refuses whatever it does
// not recognize, so a missed request costs a confirmation, never an
// unrequested write.
func Requested(message, quote string, proposed bool) bool {
	return requestedV1(message, quote) && requestedOnMessage(message, quote, proposed)
}

// requestedOnMessage decides on the poster's message: the model's quote must
// occur in it on word boundaries, both folded the same way (gateFold); then
// either a request that names an issue lies inside the quoted words, with no
// negation in its sentence and no later sentence taking it back, or, when
// the reply before proposed an issue, the request names none ("file it") or
// the whole message is a confirmation.
func requestedOnMessage(message, quote string, proposed bool) bool {
	m, q := gateFold(message), gateFold(quote)
	if q == "" || m == "" {
		return false
	}
	for from := 0; from <= len(m)-len(q); {
		i := strings.Index(m[from:], q)
		if i < 0 {
			break
		}
		start, end := from+i, from+i+len(q)
		from = start + 1
		if !wordBoundary(m, start) || !wordBoundary(m, end) {
			continue // a fragment of a word: "profile an issue" holds "file an issue"
		}
		patterns := []*regexp.Regexp{nounRequest}
		if proposed {
			patterns = append(patterns, pronounRequest)
		}
		for _, re := range patterns {
			for _, loc := range re.FindAllStringIndex(m, -1) {
				if loc[0] < start || loc[1] > end || issueNumber.MatchString(m[loc[0]:loc[1]]) {
					continue
				}
				if !negation.MatchString(sentenceAround(m, loc[0], loc[1])) && !retractedAfter(m, loc[1]) {
					return true
				}
			}
		}
		if proposed && start == 0 && confirmationOnly.MatchString(m) {
			return true
		}
	}
	return false
}

// gateFold is the one form the gate compares and reads both texts in:
// invisible format characters dropped, typographic apostrophes made plain,
// lower case, and whitespace collapsed. Applying it to both sides keeps a
// character the model or the poster added from making the quote match where
// the words do not, or not match where they do.
func gateFold(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r):
			continue
		case r == '\u2019' || r == '\u2018' || r == '\u02bc' || r == '`':
			r = '\''
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// wordBoundary reports whether i in s is not inside a word.
func wordBoundary(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return true
	}
	before, _ := utf8.DecodeLastRuneInString(s[:i])
	after, _ := utf8.DecodeRuneInString(s[i:])
	return !(isWordRune(before) && isWordRune(after))
}

func isWordRune(r rune) bool {
	return r == '_' || r == '\'' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// retractedAfter reports whether a sentence after i opens by taking a
// request back.
func retractedAfter(s string, i int) bool {
	for _, loc := range sentenceEnd.FindAllStringIndex(s[i:], -1) {
		if retraction.MatchString(s[i+loc[1]:]) {
			return true
		}
	}
	return false
}

// sentenceAround is the sentence of s that holds [start, end).
func sentenceAround(s string, start, end int) string {
	from := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(s[:start], -1) {
		from = loc[1]
	}
	to := len(s)
	if loc := sentenceEnd.FindStringIndex(s[end:]); loc != nil {
		to = end + loc[0]
	}
	return s[from:to]
}
