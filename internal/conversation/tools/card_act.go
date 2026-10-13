package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// A card act (#1587) reaches the hosted service by exactly one path:
//
//	MCP tools/call (mcp.go)
//	  → Toolset.Call, case act_on_attention_card (toolset.go)
//	    → actOnAttentionCard: parseCardAct, then authorizeCardAct
//	      → Attention.ActOnCard, its only call site in the daemon
//	        → the hosted service's turn-scoped act route, on the turn's lease
//
// There is no default branch, fallback, retry or local-store path to an act.
// The model's arguments are parsed once, into a cardActRequest; the one
// authorization, authorizeCardAct, judges those exact values and builds the
// exact act that is sent, so nothing is checked in one form and sent in
// another. Who acts comes from the turn command, never from an argument: the
// hosted service records the turn's poster as the actor, checks the poster's
// and the operator's roles on the lease (a role it does not know refuses),
// and refuses a card outside the turn's target workspace.

// cardActRequest is one act as the model asked for it, parsed and
// shape-checked once.
type cardActRequest struct {
	CardID    string `json:"card_id"`
	Verb      string `json:"verb"`
	OptionID  string `json:"option_id"`
	SteerText string `json:"steer_text"`
	Body      string `json:"body"`
	Quote     string `json:"request_quote"`
}

// parseCardAct decodes the model's arguments strictly (no unknown, duplicate
// or differently-cased key) and checks every value's shape.
func parseCardAct(raw json.RawMessage) (cardActRequest, *Error) {
	var req cardActRequest
	if err := decode(raw, &req); err != nil {
		return cardActRequest{}, err
	}
	if err := req.validate(); err != nil {
		return cardActRequest{}, err
	}
	return req, nil
}

// validate checks the shape of every value: a card id that is one path
// segment of letters, digits, "_" and "-"; a known verb with exactly its own
// arguments; an option id of the same alphabet (and "."); text within the
// hosted service's bound, with no control or format character.
func (r cardActRequest) validate() *Error {
	if !cardIDPattern.MatchString(r.CardID) {
		return fail(CodeInvalidArguments, "card_id must be a card id list_attention_cards returned")
	}
	for _, text := range []string{r.SteerText, r.Body} {
		if utf8.RuneCountInString(text) > maxCardTextChars || hasControl(text) || !utf8.ValidString(text) {
			return fail(CodeInvalidArguments,
				fmt.Sprintf("steer_text and body are at most %d characters, with no control or format characters", maxCardTextChars))
		}
	}
	switch r.Verb {
	case platform.TurnCardResolve:
		if !optionIDPattern.MatchString(r.OptionID) || r.Body != "" {
			return fail(CodeInvalidArguments, "resolve takes option_id, one of the card's options, and an optional steer_text")
		}
	case platform.TurnCardComment:
		if strings.TrimSpace(r.Body) == "" || r.OptionID != "" || r.SteerText != "" {
			return fail(CodeInvalidArguments, "comment takes body and nothing else")
		}
	case platform.TurnCardAcknowledge, platform.TurnCardMute:
		if r.OptionID != "" || r.SteerText != "" || r.Body != "" {
			return fail(CodeInvalidArguments, r.Verb+" takes no option, steer or body")
		}
	default:
		return fail(CodeInvalidArguments, "verb must be one of "+strings.Join(cardVerbs, ", "))
	}
	return nil
}

// wire is the act the hosted service receives: built from the request's own
// values and nothing else. It has no actor.
func (r cardActRequest) wire() platform.TurnCardAct {
	return platform.TurnCardAct{Verb: r.Verb, OptionID: r.OptionID, SteerText: r.SteerText, Body: r.Body}
}

// authorizeCardAct is the one authorization of a card act, and it runs at
// the one place an act reaches the hosted service, on the values that are
// sent. It fails closed: no hosted route, no posting account, no message of
// the poster's, a request whose shape is not valid, an act the poster's own
// words do not ask for or confirm, an act already made this turn, or one
// past the turn's bound, each refuses. On success it consumes the act's
// place in the turn's bound and returns the act to send.
func (s *Toolset) authorizeCardAct(req cardActRequest) (platform.TurnCardAct, *Error) {
	if s.env.Attention == nil {
		return platform.TurnCardAct{}, fail(CodeHostedUnavailable, "the hosted service's attention routes are not available to this turn")
	}
	if !idPattern.MatchString(s.turn.PosterAccountID) {
		return platform.TurnCardAct{}, fail(CodePosterUnknown, "this turn names no posting account to act for, and a card is never acted on anonymously")
	}
	if err := req.validate(); err != nil {
		return platform.TurnCardAct{}, err
	}
	act := CardAct{Verb: req.Verb, CardID: req.CardID, Option: req.OptionID, Text: req.SteerText + req.Body}
	if !ActRequested(s.turn.Message, req.Quote, s.turn.Proposal, act) {
		return platform.TurnCardAct{}, fail(CodeNotRequested,
			"the poster's own message in this turn does not ask for this act on this card, naming the card (and to "+
				"resolve it, the option) in the same clause and giving any steer or comment text in their own words, "+
				"or confirm a reply that proposed exactly this one act, so nothing is done; propose it that way and "+
				"ask the poster to confirm")
	}
	key := req.CardID + "\x00" + req.Verb
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actsMade[key] {
		return platform.TurnCardAct{}, fail(CodeDuplicateAct, "this turn already made or tried this act on this card; it is never repeated")
	}
	if s.acts >= maxActsPerTurn {
		return platform.TurnCardAct{}, fail(CodeLimitReached, fmt.Sprintf("one turn acts on at most %d cards", maxActsPerTurn))
	}
	// Consumed before the request: an act that timed out may still have
	// landed, so it is neither retried nor uncounted.
	s.acts++
	if s.actsMade == nil {
		s.actsMade = map[string]bool{}
	}
	s.actsMade[key] = true
	return req.wire(), nil
}

// actOnAttentionCard makes one act the poster asked for in their own
// message: parsed once, authorized once, then sent as authorized.
func (s *Toolset) actOnAttentionCard(ctx context.Context, raw json.RawMessage) (string, string, *Error) {
	req, perr := parseCardAct(raw)
	if perr != nil {
		return "", "", perr
	}
	act, aerr := s.authorizeCardAct(req)
	if aerr != nil {
		return "", "", aerr
	}
	ctx, cancel := context.WithTimeout(ctx, attentionTimeout)
	defer cancel()
	res, err := s.env.Attention.ActOnCard(ctx, req.CardID, act)
	if err != nil {
		return "", "", hostedErr(err)
	}
	what := req.Verb + " card " + req.CardID
	if req.Verb == platform.TurnCardResolve {
		what += " with option " + req.OptionID
	}
	detail := fmt.Sprintf("%s: %s at the request of account %s; the hosted service recorded actor %s",
		what, res.Outcome, s.turn.PosterAccountID, res.ActorAccountID)
	if res.ActorAccountID != s.turn.PosterAccountID {
		detail += " (not this turn's poster)"
	}
	if res.ExecutorAccountID != "" {
		detail += ", executor " + res.ExecutorAccountID
	}
	out, _ := json.Marshal(map[string]any{
		"card_id": res.RequestID, "verb": res.Verb, "outcome": res.Outcome, "state": res.Card.Lifecycle.State,
		"actor_account_id": res.ActorAccountID, "executor_account_id": res.ExecutorAccountID,
	})
	return clean(string(out)), clean(detail), nil
}

// The card-act gate. ActRequested decides, on the poster's own message, that
// it asks for this act on this card. As Requested does for an issue, the
// model's quote only locates the words; the act is bound to what the poster
// named, matched exactly as it is sent.

// CardAct is the act the gate judges: the verb, the card, for resolve the
// option, and the steer or comment text the act would write as the poster.
type CardAct struct {
	Verb, CardID, Option, Text string
}

// actRequests are the words that ask for each verb, matched on text whose
// ASCII letters are lower-cased (gateLower). Only the imperative, base form
// asks: "approved", "muting" or "dismisses" report or describe an act, and
// never request one.
var actRequests = map[string]*regexp.Regexp{
	platform.TurnCardResolve: regexp.MustCompile(`\b(?:approve|reject|resolve|answer|choose|pick|select|accept|` +
		`decline|deny|unblock|steer|go with|retry|skip)\b`),
	platform.TurnCardComment: regexp.MustCompile(`\b(?:comment|reply|respond|steer|` +
		`(?:add|leave|post|write) (?:a |the )?(?:comment|note|reply))\b`),
	platform.TurnCardAcknowledge: regexp.MustCompile(`\b(?:acknowledge|ack|` +
		`mark (?:it|them|this|that|the card|those)? ?(?:as )?(?:seen|read))\b`),
	platform.TurnCardMute: regexp.MustCompile(`\b(?:mute|silence|snooze|dismiss)\b`),
}

// requestLeadIn is what may stand before a request's verb in its clause: a
// request is an imperative ("approve dr_1 with wait", "please mute dr_2"),
// so "Alice will approve dr_1" and "what if I approve dr_1" ask for nothing.
var requestLeadIn = regexp.MustCompile(`^(?:(?:please|pls|kindly|now|just|ok|okay|yes|yeah|sure|so|and|then|also|` +
	`go ahead and|you can|let's|lets)[\s,]*)*$`)

// actNegation turns an act request around. It is the issue gate's
// negation without "wait", which names a card option as often as it
// retracts; a sentence that opens with "wait" still retracts (retraction).
var actNegation = regexp.MustCompile(`\b(?:not|no|nope|never|don't|dont|doesn't|didn't|won't|wouldn't|shouldn't|` +
	`can't|cannot|isn't|aren't|without|stop|avoid|instead|rather|nor|neither|hold off|cancel|unless|later)\b|n't\b`)

// clauseEnd separates the clauses an act's card and option must share with
// its request: "approve dr_1 with wait and mute dr_2" is two acts, and
// neither lends its card to the other.
var clauseEnd = regexp.MustCompile(`\.(?:\s|$)|[!?;:\n]|\b(?:and|or|but|then|also|plus)\b`)

// actSentenceEnd separates the sentences a negation is looked for in. A "."
// ends a sentence only before a space or the end, so an id such as
// "queue.add" is never split and a negation after it is still in its
// sentence.
var actSentenceEnd = regexp.MustCompile(`\.(?:\s|$)|[!?;:\n]|\bbut\b`)

// unnameableOptions are words a message holds for their own sake, so an
// option with one of them as its id is never taken as named.
var unnameableOptions = map[string]bool{"a": true, "an": true, "the": true, "it": true, "its": true, "this": true,
	"that": true, "them": true, "those": true, "with": true, "and": true, "or": true, "but": true, "to": true,
	"on": true, "in": true, "of": true, "for": true, "by": true, "as": true, "at": true, "card": true,
	"option": true, "please": true, "yes": true, "no": true}

// ActRequested reports whether the poster's message in this turn asks for
// act: the conjunction of actRequestedFloor, a simple check of what any act
// needs, and actRequestedOnMessage, the decision on the poster's message, so
// a defect in either refuses rather than acts. It reuses the issue gate's
// pieces (#1570): its retraction, its confirmations and its word
// boundaries.
func ActRequested(message, quote, proposal string, act CardAct) bool {
	return actRequestedFloor(message, quote, proposal, act) && actRequestedOnMessage(message, quote, proposal, act)
}

// actRequestedFloor is what every act needs, checked plainly: a known verb, a
// well-formed card id, a quote that occurs in the poster's message, and
// either a message that holds the exact card id (and, to resolve, the exact
// option id) and a request word of the verb's kind, or a message that is
// wholly a confirmation and a proposal that holds them.
func actRequestedFloor(message, quote, proposal string, act CardAct) bool {
	request, ok := actRequests[act.Verb]
	if !ok || !cardIDPattern.MatchString(act.CardID) {
		return false
	}
	m, q := gateLower(gateNormalize(message)), gateLower(gateNormalize(quote))
	if m == "" || q == "" || !strings.Contains(m, q) {
		return false
	}
	holds := func(text string) bool {
		return namesID(text, act.CardID) && (act.Verb != platform.TurnCardResolve || namesID(text, act.Option)) &&
			request.MatchString(gateLower(gateNormalize(text)))
	}
	return holds(message) || (confirmationOnly.MatchString(m) && holds(proposal))
}

// actRequestedOnMessage is the decision on the poster's message. message is the poster's own message, from the hosted service's record
// and bound to the poster's account; an empty message refuses everything.
// quote is the model's pointer to the words. proposal is the completed reply
// just before this turn, or "".
//
// Either a request of the verb's kind lies inside the quoted words, on word
// boundaries, with no negation in its sentence and no later sentence taking
// it back, and its own clause names the card by its exact id and, for
// resolve, the option by its exact id; or the whole message is a
// confirmation, and the proposal it confirms proposes exactly one act, this
// one, named the same way. Steer or comment text must occur exactly, byte
// for byte, in the poster's message (or, when they confirm, in the proposal
// they confirm). Nothing a tool returned takes part, so text from anyone
// else (an earlier message, a card, any tool result) can never satisfy it.
// A request is an imperative: its verb opens its clause, after at most a
// courtesy ("please"), and its sentence is not a question. It understands
// English requests only and refuses what it does not recognize.
func actRequestedOnMessage(message, quote, proposal string, act CardAct) bool {
	request, ok := actRequests[act.Verb]
	if !ok || !cardIDPattern.MatchString(act.CardID) {
		return false
	}
	if act.Verb == platform.TurnCardResolve &&
		(!optionIDPattern.MatchString(act.Option) || unnameableOptions[strings.ToLower(act.Option)]) {
		return false
	}
	n := gateNormalize(message)
	low, q := gateLower(n), gateLower(gateNormalize(quote))
	if q == "" || low == "" {
		return false
	}
	for from := 0; from <= len(low)-len(q); {
		i := strings.Index(low[from:], q)
		if i < 0 {
			break
		}
		start, end := from+i, from+i+len(q)
		from = start + 1
		if !wordBoundary(low, start) || !wordBoundary(low, end) {
			continue
		}
		for _, loc := range request.FindAllStringIndex(low, -1) {
			if loc[0] < start || loc[1] > end {
				continue
			}
			if actNegation.MatchString(actSentence(low, loc[0], loc[1])) || actRetractedAfter(low, loc[1]) ||
				isQuestion(low, loc[1]) {
				continue
			}
			cf, ct := clauseBounds(low, loc[0], loc[1])
			if !requestLeadIn.MatchString(strings.TrimSpace(low[cf:loc[0]])) || reported(low, loc[0]) {
				continue
			}
			if namesAct(n[cf:ct], act) && (act.Text == "" || containsExact(message, act.Text)) {
				return true
			}
		}
		if start == 0 && confirmationOnly.MatchString(low) &&
			proposesOnly(gateNormalize(proposal), act) && (act.Text == "" || containsExact(proposal, act.Text)) {
			return true
		}
	}
	return false
}

// proposesOnly reports whether proposal, normalized, proposes exactly one
// act on a card, and that act: one clause holds any act request at all, it
// is not negated, and it asks for act's verb and names act's card and
// option. A proposal of two acts, or of none, confirms nothing.
func proposesOnly(p string, act CardAct) bool {
	low := gateLower(p)
	if low == "" {
		return false
	}
	clauses := map[[2]int]bool{}
	for _, re := range actRequests {
		for _, loc := range re.FindAllStringIndex(low, -1) {
			from, to := clauseBounds(low, loc[0], loc[1])
			clauses[[2]int{from, to}] = true
		}
	}
	if len(clauses) != 1 {
		return false
	}
	for c := range clauses {
		if actNegation.MatchString(actSentence(low, c[0], c[1])) || !actRequests[act.Verb].MatchString(low[c[0]:c[1]]) {
			return false
		}
		return namesAct(p[c[0]:c[1]], act)
	}
	return false
}

// namesAct reports whether text names act's card and, for resolve, its
// option, each by its exact id.
func namesAct(text string, act CardAct) bool {
	if !namesID(text, act.CardID) {
		return false
	}
	return act.Verb != platform.TurnCardResolve || namesID(text, act.Option)
}

// namesID reports whether text holds id exactly, case included, with no id
// character on either side: "dr_1" is not named by "dr_12", "dr_1-b" or
// "DR_1".
func namesID(text, id string) bool {
	if id == "" {
		return false
	}
	for from := 0; from <= len(text)-len(id); {
		i := strings.Index(text[from:], id)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(id)
		if !idContinues(text, start-1, -1) && !idContinues(text, end, 1) {
			return true
		}
		from = start + 1
	}
	return false
}

// idContinues reports whether the byte at i, read in direction dir (-1
// before an id, 1 after it), carries an id on: a letter, a digit, "_" or
// "-", or a "." with one of those beyond it ("queue.add", but not the full
// stop that ends a sentence).
func idContinues(text string, i, dir int) bool {
	if i < 0 || i >= len(text) {
		return false
	}
	if c := text[i]; c == '.' {
		j := i + dir
		return j >= 0 && j < len(text) && isIDByte(text[j])
	}
	return isIDByte(text[i])
}

func isIDByte(b byte) bool {
	return b == '_' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// containsExact reports whether text occurs in s byte for byte, on word
// boundaries: the words an act writes as the poster are the poster's own.
func containsExact(s, text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for from := 0; from <= len(s)-len(text); {
		i := strings.Index(s[from:], text)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(text)
		if wordBoundary(s, start) && wordBoundary(s, end) {
			return true
		}
		from = start + 1
	}
	return false
}

// gateNormalize is the act gate's form of a text: invisible format
// characters dropped, typographic apostrophes made plain and whitespace
// collapsed, and nothing else, so an id keeps its exact case and every byte
// of the result maps to one byte of gateLower's.
func gateNormalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r):
			continue
		case r == '’' || r == '‘' || r == 'ʼ' || r == '`':
			r = '\''
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// gateLower lower-cases s's ASCII letters only, so its bytes stay aligned
// with s's: the request words are matched on it, and an id is read from s
// at the same offsets.
func gateLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// reportingWord marks someone else's words: "the card says: approve dr_1"
// relays a request, and makes none.
var reportingWord = regexp.MustCompile(`\b(?:says?|said|asks?|asked|wants?|wanted|suggests?|suggested|tells?|told|` +
	`writes?|wrote|reads|claims?|claimed|thinks?|thought|recommends?|recommended|proposes?|proposed|quote[sd]?)\b`)

// reported reports whether the request at start is someone else's words:
// its sentence names a reporting word before it, or it stands inside
// quotation marks.
func reported(s string, start int) bool {
	from := 0
	for _, loc := range actSentenceEnd.FindAllStringIndex(s[:start], -1) {
		if s[loc[0]] != ':' {
			from = loc[1]
		}
	}
	before := s[from:start]
	return reportingWord.MatchString(before) || strings.Count(before, `"`)%2 == 1 ||
		strings.Count(before, "\u201c") > strings.Count(before, "\u201d")
}

// isQuestion reports whether the sentence of s that goes on from end is a
// question: a question about an act ("should I approve dr_1?") never asks
// for it.
func isQuestion(s string, end int) bool {
	loc := actSentenceEnd.FindStringIndex(s[end:])
	return loc != nil && s[end+loc[0]] == '?'
}

// actSentence is the sentence of s that holds [start, end).
func actSentence(s string, start, end int) string {
	from := 0
	for _, loc := range actSentenceEnd.FindAllStringIndex(s[:start], -1) {
		from = loc[1]
	}
	to := len(s)
	if loc := actSentenceEnd.FindStringIndex(s[end:]); loc != nil {
		to = end + loc[0]
	}
	return s[from:to]
}

// actRetractedAfter reports whether a sentence after i opens by taking a
// request back.
func actRetractedAfter(s string, i int) bool {
	for _, loc := range actSentenceEnd.FindAllStringIndex(s[i:], -1) {
		if retraction.MatchString(s[i+loc[1]:]) {
			return true
		}
	}
	return false
}

// clauseBounds are the bounds of the clause of s that holds [start, end).
func clauseBounds(s string, start, end int) (int, int) {
	from := 0
	for _, loc := range clauseEnd.FindAllStringIndex(s[:start], -1) {
		from = loc[1]
	}
	to := len(s)
	if loc := clauseEnd.FindStringIndex(s[end:]); loc != nil {
		to = end + loc[0]
	}
	return from, to
}

// hasControl reports whether s holds a control character other than a
// newline or a tab, or an invisible format character (a bidirectional
// override, a zero-width joiner), which could make the text the poster sees
// differ from the text that is written.
func hasControl(s string) bool {
	for _, r := range s {
		if (r != '\n' && r != '\t' && unicode.IsControl(r)) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}
