package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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
				"resolve it, the option) right after the request, with no condition or later retraction, and giving "+
				"any steer or comment text in their own words, or confirm a reply that offered exactly this one act, "+
				"plainly, so nothing is done; propose it that way and ask the poster to confirm")
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
// retracts; a segment that opens with "wait" still retracts (retractionOpens).
var actNegation = regexp.MustCompile(`\b(?:not|no|nope|never|don't|dont|doesn't|didn't|won't|wouldn't|shouldn't|` +
	`can't|cannot|isn't|aren't|without|stop|avoid|instead|rather|nor|neither|hold off|cancel|unless|later)\b|n't\b`)

// clauseEnd separates the clauses a request's lead-in is read in. A comma
// does not end one, so "Alice, approve dr_1" opens its clause with Alice
// and asks her, not this turn.
var clauseEnd = regexp.MustCompile(`\.(?:\s|$)|[!?;:\n]|\b(?:and|or|but|then|also|plus)\b`)

// segmentEnd separates the segments a request binds its card and option in:
// the clauses, and their comma-separated parts. "approve dr_1 with wait and
// mute dr_2" is two acts, and in "approve dr_1 with wait, leave dr_2 alone"
// the request's own segment names dr_1 and wait and lends neither to dr_2.
var segmentEnd = regexp.MustCompile(`\.(?:\s|$)|[!?;:,\n]|\b(?:and|or|but|then|also|plus)\b`)

// actSentenceEnd separates the sentences a negation is looked for in. A "."
// ends a sentence only before a space or the end, so an id such as
// "queue.add" is never split and a negation after it is still in its
// sentence.
var actSentenceEnd = regexp.MustCompile(`\.(?:\s|$)|[!?;:\n]|\bbut\b`)

// bindFillers are the only words that may stand between a request and the
// card and option it binds, or between the two: "resolve the dr_1 card with
// the wait option". Any other word there, another card's id included,
// leaves the act unbound. Matched on lower case; an option whose id is one
// of them is never taken as named.
var bindFillers = map[string]bool{"the": true, "a": true, "an": true, "card": true, "cards": true, "request": true,
	"item": true, "gate": true, "decision": true, "on": true, "for": true, "of": true, "to": true, "with": true,
	"using": true, "via": true, "by": true, "as": true, "option": true, "choice": true, "answer": true, "s": true,
	"please": true, "choosing": true, "picking": true, "selecting": true}

// unnameableOptions are words a message holds for their own sake, so an
// option with one of them as its id is never taken as named.
var unnameableOptions = map[string]bool{"a": true, "an": true, "the": true, "it": true, "its": true, "this": true,
	"that": true, "them": true, "those": true, "with": true, "and": true, "or": true, "but": true, "to": true,
	"on": true, "in": true, "of": true, "for": true, "by": true, "as": true, "at": true, "card": true,
	"option": true, "please": true, "yes": true, "no": true}

// bindToken is one word as binding reads it: a run of letters, marks,
// digits, "_" and "-", with a "." inside it ("queue.add") but not one that
// ends it. Every other character separates words, an apostrophe included
// ("dr_1's" is "dr_1" and "s"), and a word in any script is a word.
var bindToken = regexp.MustCompile(`[\p{L}\p{M}\p{N}_-]+(?:\.[\p{L}\p{M}\p{N}_-]+)*`)

// actCondition makes an act wait on something: "approve dr_1 with wait if
// the build passes" asks for nothing now ("unless" is a negation already).
// It counts before the request in its sentence and in the request's own
// segment, and where it opens a later segment of the sentence ("..., once
// CI is green"); inside a later segment it is a steer's or comment's own
// words ("steering it to after the deploy").
var (
	actCondition   = regexp.MustCompile(`\b(?:` + conditionWords + `)\b`)
	conditionOpens = regexp.MustCompile(`^[\s\p{P}]*(?:(?:only|just|but|right) )?(?:` + conditionWords + `)\b`)
)

const conditionWords = `if|once|when|whenever|provided|providing|assuming|supposing|as soon as|in case|` +
	`depending on|after|before`

// interjections may open a segment before a retraction or a hedge: "Hmm,
// no.", "Oh, wait."
const interjections = `(?:(?:hmm*|hm+|um+|uh+|erm?|oh|ah|well|ok|okay|oops|ugh|so|i mean)\b[\s\p{P}]*)*`

// retractionOpens is a later segment that takes a request back as it
// opens, after at most an interjection: the issue gate's retraction
// vocabulary, "wait" included. "Approve dr_1 with wait. Hmm, no." acts on
// nothing, and "Mute dr_1. It's not urgent." still mutes.
var retractionOpens = regexp.MustCompile(`^[\s\p{P}]*` + interjections +
	`(?:no|nope|not|never|don't|dont|do not|wait|hold on|hold off|cancel|actually|never ?mind|scratch that|forget (?:it|that)|ignore (?:it|that)|` +
	`on second thought)\b`)

// retractionAnywhere takes a request back wherever it stands after it:
// phrases that say nothing else.
var retractionAnywhere = regexp.MustCompile(`\b(?:never ?mind|scratch that|forget (?:it|that)|ignore (?:it|that)|` +
	`hold on|cancel (?:it|that)|on second thought)\b`)

// ActRequested reports whether the poster's message in this turn asks for
// act: the conjunction of actRequestedFloor, a simple check of what any act
// needs, and actRequestedOnMessage, the decision on the poster's message, so
// a defect in either refuses rather than acts. It reuses the issue gate's
// pieces (#1570): its confirmations and its word boundaries.
func ActRequested(message, quote, proposal string, act CardAct) bool {
	return actRequestedFloor(message, quote, proposal, act) && actRequestedOnMessage(message, quote, proposal, act)
}

// actRequestedFloor is what every act needs, checked plainly: a known verb, a
// well-formed card id, a quote that occurs in the poster's message, and
// either a message that holds the exact card id (and, to resolve, the exact
// option id) and a request word of the verb's kind, or a message that is
// wholly a confirmation and a proposal that holds them. It reads the
// message as written, so an id with an invisible character inside it is not
// the id.
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

// actRequestedOnMessage is the decision on the poster's message. message is
// the poster's own message, from the hosted service's record and bound to
// the poster's account; an empty message refuses everything. quote is the
// model's pointer to the words. proposal is the completed reply just before
// this turn, or "".
//
// Either a request of the verb's kind lies inside the quoted words, on word
// boundaries, and stands unqualified (standsAsAsked), and it binds the act
// (requestBinds): the first words after it, fillers aside, are the card's
// exact id and, for resolve, the option's; or a bare list entry joined to it
// by commas names them ("mute dr_1, dr_2"). Or the whole message is a
// confirmation, and the proposal it confirms offers exactly one act, this
// one (proposesOnly). Steer or comment text must occur exactly, byte for
// byte, in the poster's message (or, when they confirm, in the proposal they
// confirm). Nothing a tool returned takes part, so text from anyone else (an
// earlier message, a card, any tool result) can never satisfy it. A request
// is an imperative: its verb opens its clause, after at most a courtesy
// ("please"). It understands English requests only and refuses what it does
// not recognize.
func actRequestedOnMessage(message, quote, proposal string, act CardAct) bool {
	request, ok := actRequests[act.Verb]
	if !ok || !cardIDPattern.MatchString(act.CardID) {
		return false
	}
	if act.Verb == platform.TurnCardResolve && (!optionIDPattern.MatchString(act.Option) ||
		unnameableOptions[strings.ToLower(act.Option)] || bindFillers[strings.ToLower(act.Option)]) {
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
			if loc[0] < start || loc[1] > end || !standsAsAsked(low, n, loc[0], loc[1]) {
				continue
			}
			if requestBinds(n, low, loc[1], act) && (act.Text == "" || containsExact(message, act.Text)) {
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

// standsAsAsked reports whether the request at [start, end) of low (n's
// lower-cased form) is the poster's own, unqualified: its verb opens its
// clause after at most a courtesy; its sentence holds no negation and no
// condition and is not a question; nothing after it takes it back; and it
// is not someone else's words.
func standsAsAsked(low, n string, start, end int) bool {
	cf, _ := clauseBounds(low, start, end)
	return requestLeadIn.MatchString(strings.TrimSpace(low[cf:start])) &&
		!actNegation.MatchString(actSentence(low, start, end)) && !conditional(low, start, end) &&
		!isQuestion(low, end) && !actRetractedAfter(low, end) && !reported(low, n, start)
}

// requestSegments are the segments a request that ends at i governs: its
// own, up to the next segment end, then each bare list entry joined after
// it by a comma ("mute dr_1, dr_2, dr_3"). The first segment that is not a
// bare entry, or a join that is not a comma, ends the list.
func requestSegments(low string, i int) [][2]int {
	var segs [][2]int
	for from := i; ; {
		to, next, comma := len(low), len(low), false
		if loc := segmentEnd.FindStringIndex(low[from:]); loc != nil {
			to, next, comma = from+loc[0], from+loc[1], low[from+loc[0]] == ','
		}
		if len(segs) > 0 && !listEntry(low[from:to]) {
			return segs
		}
		segs = append(segs, [2]int{from, to})
		if !comma {
			return segs
		}
		from = next
	}
}

// listEntry reports whether seg is a bare entry of a list after a request:
// an id-shaped word ("dr_2"), alone or with one word after a filler ("dr_2
// with abort"), and no request of its own. "leave dr_2 alone", "ignore
// dr_2" and "mute dr_3" are not entries, so no act is lent across them.
func listEntry(seg string) bool {
	for _, re := range actRequests {
		if re.MatchString(seg) {
			return false
		}
	}
	words, joined := bindWords(seg)
	if len(words) == 0 || len(words) > 2 || !strings.ContainsAny(words[0], "0123456789_-") {
		return false
	}
	return len(words) == 1 || joined[1]
}

// requestBinds reports whether the request that ends at i of low (n's
// lower-cased form) binds act: its own segment names act's card and option
// first, or one of the bare list entries after it names exactly them.
func requestBinds(n, low string, i int, act CardAct) bool {
	for k, seg := range requestSegments(low, i) {
		if binds(n[seg[0]:seg[1]], act, k > 0) {
			return true
		}
	}
	return false
}

// binds reports whether seg names act's card and, for resolve, its option,
// each by its exact id, as its first words other than fillers: the card,
// or for resolve the card and the option in either order ("pick wait for
// dr_1"), with a filler between them. In a list entry the card comes first
// and nothing follows the act's words.
func binds(seg string, act CardAct, entry bool) bool {
	words, joined := bindWords(seg)
	if act.Verb != platform.TurnCardResolve {
		return len(words) > 0 && (!entry || len(words) == 1) && words[0] == act.CardID
	}
	if len(words) < 2 || (entry && len(words) != 2) || !joined[1] {
		return false
	}
	return (words[0] == act.CardID && words[1] == act.Option) ||
		(!entry && words[0] == act.Option && words[1] == act.CardID)
}

// bindWords are seg's words other than fillers, in order, and for each
// whether a filler stood between it and the word before.
func bindWords(seg string) (words []string, joined []bool) {
	filler := false
	for _, w := range bindToken.FindAllString(seg, -1) {
		if bindFillers[strings.ToLower(w)] {
			filler = true
			continue
		}
		words, joined = append(words, w), append(joined, filler)
		filler = false
	}
	return words, joined
}

// conditional reports whether the request at [start, end) of s waits on
// something (actCondition).
func conditional(s string, start, end int) bool {
	from, to := sentenceBounds(s, start, end)
	head := requestSegments(s, end)[0]
	return actCondition.MatchString(s[from:head[1]]) || opensLater(s[:to], head[1], conditionOpens)
}

// opensLater reports whether a segment of s that starts after i opens with
// re.
func opensLater(s string, i int, re *regexp.Regexp) bool {
	for _, loc := range segmentEnd.FindAllStringIndex(s[i:], -1) {
		if re.MatchString(s[i+loc[1]:]) {
			return true
		}
	}
	return false
}

// The confirmation path. A bare "yes" confirms an act only when the reply it
// answers offers exactly that one act, plainly: as an offer ("Shall I mute
// dr_1?") or a statement of intent ("I will mute dr_1."), with no hedge,
// condition, negation or retraction.
var (
	offerCourtesy = `(?:(?:so|ok|okay|now|next|alright|great|sure)[\s,]*)*`
	offerQuestion = regexp.MustCompile(`^` + offerCourtesy + `(?:shall i|should i|can i|may i|` +
		`do you want me to|would you like me to|want me to)$`)
	offerStatement = regexp.MustCompile(`^` + offerCourtesy +
		`(?:i will|i'll|i can|let me|i am going to|i'm going to)$`)
	// proposalHedge makes an offer tentative in its own sentence ("I would
	// approve dr_1 with wait?"), and hedgeOpens where it opens a later
	// segment ("Not sure.", "Maybe not.").
	proposalHedge = regexp.MustCompile(`\b(?:maybe|perhaps|possibly|probably|might|not sure|unsure|not certain|` +
		`uncertain|i think|i guess|i suppose|i would|i'd)\b`)
	hedgeOpens = regexp.MustCompile(`^[\s\p{P}]*` + interjections + `(?:maybe|perhaps|possibly|probably|unsure|` +
		`uncertain|not sure|i'm not sure|i think|i guess|i suppose)\b`)
	// confirmCondition waits only on the confirmation itself ("if you
	// confirm"), so it is no condition on an offer.
	confirmCondition = regexp.MustCompile(`\bif you(?: would|'d)? (?:like|want|prefer|agree|confirm)\b|` +
		`\bif you say so\b|\bif that works\b|\bif that's (?:ok|okay|fine|alright)\b`)
)

// proposesOnly reports whether proposal, normalized, offers exactly one act
// on a card, and that act. Every request word is an act (overlapping words
// of two verbs are one), and so is every bare list entry after one, so "I
// will approve dr_1 with wait, acknowledge dr_1" and "approve dr_1 with
// wait, dr_2 with abort" offer two and confirm nothing. The one act must be
// act's verb, offered plainly, and bind act's card and option.
func proposesOnly(p string, act CardAct) bool {
	low := gateLower(p)
	acts := statedActs(low)
	if len(acts) != 1 || len(requestSegments(low, acts[0][1])) != 1 {
		return false
	}
	start, end := acts[0][0], acts[0][1]
	if !matchesWithin(actRequests[act.Verb], low, start, end) {
		return false
	}
	cf, _ := clauseBounds(low, start, end)
	lead := strings.TrimSpace(low[cf:start])
	offered := offerQuestion.MatchString(lead) || (offerStatement.MatchString(lead) && !isQuestion(low, end))
	unconditional := !conditional(confirmCondition.ReplaceAllStringFunc(low, blank), start, end)
	sentence := actSentence(low, start, end)
	tentative := proposalHedge.MatchString(sentence) || opensLater(low, end, hedgeOpens)
	return offered && unconditional && !tentative && !actNegation.MatchString(sentence) &&
		!actRetractedAfter(low, end) && requestBinds(p, low, end, act)
}

// statedActs are the request words of every verb in low, as spans; spans
// that overlap (resolve's and comment's "steer") are one.
func statedActs(low string) [][2]int {
	var spans [][2]int
	for _, re := range actRequests {
		for _, loc := range re.FindAllStringIndex(low, -1) {
			spans = append(spans, [2]int{loc[0], loc[1]})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var merged [][2]int
	for _, s := range spans {
		if k := len(merged) - 1; k >= 0 && s[0] < merged[k][1] {
			merged[k][1] = max(merged[k][1], s[1])
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// matchesWithin reports whether re matches somewhere inside [start, end) of s.
func matchesWithin(re *regexp.Regexp, s string, start, end int) bool {
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if loc[0] >= start && loc[1] <= end {
			return true
		}
	}
	return false
}

// blank replaces a match with spaces, keeping every offset of the text.
func blank(m string) string { return strings.Repeat(" ", len(m)) }

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

// speakerLabel is a name and nothing else before a colon, as a chat log or
// a pasted exchange writes it: one word ("Bob", "bob", "@bob") or two
// capitalized ones ("Bob Smith"). "Two things:" is not one.
var speakerLabel = regexp.MustCompile(`^\s*(@?[\p{L}\p{N}_.'-]+|\p{Lu}[\p{L}'.-]*\s+\p{Lu}[\p{L}'.-]*)\s*$`)

// notSpeakers are the one-word labels that head a poster's own words.
var notSpeakers = map[string]bool{"note": true, "notes": true, "ps": true, "fyi": true, "update": true,
	"request": true, "todo": true, "action": true, "next": true, "also": true, "ok": true, "okay": true, "so": true,
	"then": true, "now": true, "please": true, "first": true, "second": true, "third": true, "finally": true,
	"summary": true, "decision": true, "plan": true, "answer": true, "reply": true, "response": true, "task": true,
	"step": true, "steps": true, "hi": true, "hey": true, "hello": true, "thanks": true, "edit": true,
	"context": true, "reminder": true, "important": true, "urgent": true, "question": true, "re": true,
	"status": true, "yes": true, "sure": true, "ask": true}

// reported reports whether the request at start of s (orig's lower-cased
// form) is someone else's words: its sentence names a reporting word before
// it, a speaker's label heads one of the sentence's colon-separated parts
// before it ("Bob: approve dr_1"), or it stands inside quotation marks.
func reported(s, orig string, start int) bool {
	from := 0
	for _, loc := range actSentenceEnd.FindAllStringIndex(s[:start], -1) {
		if s[loc[0]] != ':' {
			from = loc[1]
		}
	}
	before := s[from:start]
	if reportingWord.MatchString(before) || strings.Count(before, `"`)%2 == 1 ||
		strings.Count(before, "“") > strings.Count(before, "”") {
		return true
	}
	parts := strings.Split(orig[from:start], ":")
	for _, label := range parts[:len(parts)-1] {
		if m := speakerLabel.FindStringSubmatch(label); m != nil && !notSpeakers[strings.ToLower(m[1])] {
			return true
		}
	}
	return false
}

// isQuestion reports whether the sentence of s that goes on from end is a
// question: a question about an act ("should I approve dr_1?") never asks
// for it.
func isQuestion(s string, end int) bool {
	loc := actSentenceEnd.FindStringIndex(s[end:])
	return loc != nil && s[end+loc[0]] == '?'
}

// sentenceBounds are the bounds of the sentence of s that holds [start,
// end).
func sentenceBounds(s string, start, end int) (int, int) {
	from := 0
	for _, loc := range actSentenceEnd.FindAllStringIndex(s[:start], -1) {
		from = loc[1]
	}
	to := len(s)
	if loc := actSentenceEnd.FindStringIndex(s[end:]); loc != nil {
		to = end + loc[0]
	}
	return from, to
}

// actSentence is the sentence of s that holds [start, end).
func actSentence(s string, start, end int) string {
	from, to := sentenceBounds(s, start, end)
	return s[from:to]
}

// actRetractedAfter reports whether anything after i takes a request back:
// a later segment that opens with a retraction, or a phrase that only
// retracts, wherever it stands.
func actRetractedAfter(s string, i int) bool {
	return retractionAnywhere.MatchString(s[i:]) || opensLater(s, i, retractionOpens)
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
