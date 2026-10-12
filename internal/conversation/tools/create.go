package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
	if !Requested(s.turn.Message, a.RequestQuote) {
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

// issueRequest matches words that ask for an issue: a filing verb followed,
// within a few words, by "issue", "ticket" or "bug report" that is not an
// existing issue's number ("open issue #12" reads one); or a filing verb
// aimed at something already proposed ("file it", "create that").
var issueRequest = regexp.MustCompile(`(?i)\b(?:file|create|open|raise|log|submit|make|write|add)\s+` +
	`(?:(?:[\w'-]+\s+){0,4}(?:issues?|tickets?|bug\s+reports?)\b(?:\s*#?\d)?|(?:it|that|this|them|those|one)\b)`)

// issueNumber is the tail issueRequest leaves when the noun was followed by
// a number.
var issueNumber = regexp.MustCompile(`\s*#?\d$`)

// confirmation matches a message that opens by confirming a proposal.
var confirmation = regexp.MustCompile(`(?i)^\W*(?:yes|yeah|yep|yup|ok|okay|sure|confirm(?:ed)?|approved?|go ahead|do it|please do|ship it|lgtm|sounds good)\b`)

// Requested reports whether the poster's message in this turn asks for the
// mutation the model wants to make: quote must be the poster's own words,
// found verbatim in message, and either ask for an issue or open the message
// by confirming a proposal. Text from anyone else — an earlier message, a
// file, an issue body, any tool result — is never in message, so it can
// never satisfy this, whatever it says.
//
// It is a gate behind the system prompt's instruction, not a reading of
// intent: it understands English requests only and refuses whatever it does
// not recognize, so a missed request costs a confirmation, never an
// unrequested write.
func Requested(message, quote string) bool {
	m, q := normalize(message), normalize(quote)
	if q == "" || !strings.Contains(strings.ToLower(m), strings.ToLower(q)) {
		return false
	}
	for _, loc := range issueRequest.FindAllStringIndex(q, -1) {
		if !issueNumber.MatchString(q[loc[0]:loc[1]]) {
			return true
		}
	}
	return confirmation.MatchString(q) && strings.HasPrefix(strings.ToLower(m), strings.ToLower(q))
}

// normalize collapses runs of whitespace and trims, so a quote matches the
// message however the model re-spaced it.
func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
