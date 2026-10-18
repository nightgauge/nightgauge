package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// dispatch_run starts work from a conversation (#1894): it asks the hosted
// service for a pipeline run on the turn's lease and keeps only the handle it
// returns. The run executes in its own context, on this host's executor,
// through the path every trigger takes; nothing of it (transcript, stage
// output, diffs) ever comes back into the turn, and a later turn sees only the
// handle and the statuses the hosted service relays.
//
// Phase 1 of the dispatch credential: only a turn the host's own operator
// posted dispatches a run, and that run uses the operator's ordinary
// credential chain, unchanged. A teammate's turn never dispatches: there is
// no App-only credential mode for a whole run, so a teammate's run would
// write to GitHub as the operator. A teammate's request is proposed to the
// operator as a Needs Attention card on this host instead, and the run
// starts only when the operator acts on that card on their own machine.

// RunDispatcher is the hosted service's turn-bound run dispatch, bound to
// one turn's lease.
type RunDispatcher interface {
	DispatchRun(ctx context.Context, run platform.TurnRunRequest) (platform.TurnRunHandle, error)
}

// Executor reports whether a pipeline executor is attached to this host: the
// process a dispatched run's trigger is handed to. With none, a trigger would
// wait unacknowledged until it expired, so nothing is dispatched.
type Executor interface {
	ExecutorAttached() bool
}

// RunProposal is a teammate's request for a run, proposed to the host's
// operator. It carries ids only, never message text.
type RunProposal struct {
	Owner, Repo     string
	IssueNumber     int
	PosterAccountID string
	PosterLogin     string
	ConversationID  string
	TurnID          string
}

// RunProposals raises a run proposal as a Needs Attention card on this host,
// for its operator alone to act on. It returns the card's id.
type RunProposals interface {
	ProposeRun(ctx context.Context, p RunProposal) (string, error)
}

const (
	// maxDispatchesPerTurn bounds the runs and proposals one turn may make.
	maxDispatchesPerTurn = 3
	// dispatchTimeout bounds one hosted call.
	dispatchTimeout = 20 * time.Second
	// maxIssueNumber bounds an issue number a turn may name.
	maxIssueNumber = 1 << 30
)

// dispatchRun dispatches the run the poster asked for, or proposes it to the
// operator when a teammate asked. Every check runs before anything is sent:
// the arguments, the poster's identity, their read access to the repository
// and the workspace's checkout of it, their own request in this turn's
// message, and the per-turn bounds; then, for the operator, an attached
// executor.
func (s *Toolset) dispatchRun(ctx context.Context, raw json.RawMessage) (string, string, *Error) {
	var a struct {
		repoArgs
		IssueNumber  int    `json:"issue_number"`
		RequestQuote string `json:"request_quote"`
	}
	if err := decode(raw, &a); err != nil {
		return "", "", err
	}
	if a.IssueNumber < 1 || a.IssueNumber > maxIssueNumber {
		return "", "", fail(CodeInvalidArguments, "issue_number must be a positive issue number")
	}
	if !idPattern.MatchString(s.turn.PosterAccountID) ||
		(s.turn.PosterLogin != "" && !ownerPattern.MatchString(s.turn.PosterLogin)) {
		return "", "", fail(CodePosterUnknown, "this turn names no posting account to start work for")
	}
	if err := s.checkRepo(a.repoArgs); err != nil {
		return "", "", err
	}
	if _, ok := s.checkouts[strings.ToLower(a.Owner+"/"+a.Repo)]; !ok {
		return "", "", fail(CodeRepoNotInWorkspace, "this workspace has no checkout of that repository, so no run can start here")
	}
	target := RunTarget{Owner: a.Owner, Repo: a.Repo, Issue: a.IssueNumber}
	if !RunRequested(s.turn.Message, a.RequestQuote, s.turn.Proposal, target, s.turn.ReadableRepos) {
		return "", "", fail(CodeNotRequested,
			"the poster's own message in this turn does not ask to start work on that issue, so nothing is started; "+
				"propose it, naming the issue as repo#number, and ask the poster to confirm")
	}
	key := strings.ToLower(target.slug())
	s.mu.Lock()
	switch {
	case s.dispatched[key]:
		s.mu.Unlock()
		return "", "", fail(CodeDuplicateAct, "this turn already asked to start work on that issue")
	case s.dispatches >= maxDispatchesPerTurn:
		s.mu.Unlock()
		return "", "", fail(CodeLimitReached, fmt.Sprintf("one turn starts or proposes at most %d runs", maxDispatchesPerTurn))
	}
	// Counted before the request: a dispatch that timed out may still have
	// reached the hosted service, and it must never be sent twice.
	s.dispatches++
	if s.dispatched == nil {
		s.dispatched = map[string]bool{}
	}
	s.dispatched[key] = true
	s.mu.Unlock()

	if !s.turn.PosterIsOperator {
		return s.proposeRun(ctx, target)
	}
	if s.env.Executor == nil || !s.env.Executor.ExecutorAttached() {
		return "", "", fail(CodeNoExecutor, "no pipeline executor (the VS Code extension) is attached to this host, "+
			"so no run is dispatched; open the workspace in VS Code and ask again")
	}
	if s.env.Dispatch == nil {
		return "", "", fail(CodeHostedUnavailable, "this host cannot reach the hosted service's dispatch route; nothing was dispatched")
	}
	cctx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	handle, err := s.env.Dispatch.DispatchRun(cctx, platform.TurnRunRequest{Owner: a.Owner, Repo: a.Repo, IssueNumber: a.IssueNumber})
	if err != nil {
		return "", "", dispatchErr(err)
	}
	detail := fmt.Sprintf("dispatched %s as run %s at the request of account %s; it runs in its own context, "+
		"and this conversation keeps only its handle and status", target.slug(), handle.RunID, s.turn.PosterAccountID)
	out, _ := json.Marshal(map[string]any{
		"dispatched": target.slug(), "run_id": handle.RunID, "status": "dispatched",
		"requested_by_account": s.turn.PosterAccountID,
	})
	return clean(string(out)), clean(detail), nil
}

// proposeRun raises a teammate's request as a card for the host's operator.
// Nothing is dispatched, and the card's run starts only when the operator
// acts on it on their own machine.
func (s *Toolset) proposeRun(ctx context.Context, target RunTarget) (string, string, *Error) {
	if s.env.Proposals == nil {
		return "", "", fail(CodeOperatorRequired, "only the host's operator can start a run from this conversation, "+
			"and this host cannot propose one to them; ask the operator to start it")
	}
	cctx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	cardID, err := s.env.Proposals.ProposeRun(cctx, RunProposal{
		Owner: target.Owner, Repo: target.Repo, IssueNumber: target.Issue,
		PosterAccountID: s.turn.PosterAccountID, PosterLogin: s.turn.PosterLogin,
		ConversationID: s.turn.ConversationID, TurnID: s.turn.TurnID,
	})
	if err != nil || !cardIDPattern.MatchString(cardID) {
		return "", "", fail(CodeProposalFailed, "the request could not be proposed to the host's operator; nothing was started")
	}
	detail := fmt.Sprintf("proposed %s to the host's operator as Needs Attention card %s at the request of account %s; "+
		"no run was started: a teammate's turn cannot start one, and the run starts only when the operator approves "+
		"the card on their own machine", target.slug(), cardID, s.turn.PosterAccountID)
	out, _ := json.Marshal(map[string]any{
		"proposed": target.slug(), "card_id": cardID, "status": "awaiting_operator",
		"requested_by_account": s.turn.PosterAccountID,
	})
	return clean(string(out)), clean(detail), nil
}

// dispatchErr is a refused or failed dispatch, as the model is shown it. A
// failure the hosted service did not answer is reported as unknown, never as
// "nothing happened": the run may exist.
func dispatchErr(err error) *Error {
	var refused *platform.TurnRequestError
	if errors.As(err, &refused) && hostedCode.MatchString(refused.Code) {
		return fail(strings.ToLower(refused.Code), dispatchRefusal(refused.Code))
	}
	if errors.As(err, &refused) {
		return fail(CodeHostedRefused, fmt.Sprintf("the hosted service refused the dispatch (HTTP %d); no run was started", refused.Status))
	}
	return fail(CodeHostedUnavailable, "the hosted service did not confirm the dispatch; a run may or may not exist, "+
		"so do not ask again before checking the conversation's runs")
}

// dispatchRefusal explains a hosted refusal of a dispatch.
func dispatchRefusal(code string) string {
	switch code {
	case "OPERATOR_REQUIRED":
		return "only the host's operator may start a run from a turn; no run was started"
	case "THREAD_RUN_LIMIT":
		return "this conversation already has as many unfinished runs as it may; no run was started"
	case "WORKSPACE_THROTTLED":
		return "the workspace's throttle has no free slot; no run was started"
	case "REPO_NOT_IN_TARGET_WORKSPACE":
		return "that repository is not one this turn's workspace serves; no run was started"
	case "ACT_NOT_PERMITTED":
		return "the poster's role does not permit starting a run; no run was started"
	case "GITHUB_PERMISSION_DENIED", "GITHUB_AUTHORIZATION_MISSING", "GITHUB_AUTHORIZATION_EXPIRED":
		return "GitHub does not confirm the poster's write permission on that repository; no run was started"
	case "GITHUB_UNAVAILABLE":
		return "GitHub could not confirm the poster's permission; no run was started"
	case "TURN_ENDED":
		return "this turn is no longer running on the hosted service; no run was started"
	default:
		return "the hosted service refused the dispatch; no run was started"
	}
}

// The dispatch gate. RunRequested decides, on the poster's own message, that
// it asks to start work on exactly this issue. It is built from the card-act
// gate's pieces (#1587): a request is an imperative that opens its clause,
// unnegated, unconditional, not a question, not taken back and not someone
// else's words; and it binds the issue it names as its first words.

// RunTarget is the issue a dispatch would start work on.
type RunTarget struct {
	Owner, Repo string
	Issue       int
}

func (t RunTarget) slug() string { return t.Owner + "/" + t.Repo + "#" + strconv.Itoa(t.Issue) }

// runRequest is the words that ask for work to start, on text whose ASCII
// letters are lower-cased (gateLower). Only the imperative base form asks:
// "started", "running" and "builds" describe, and never request. A trailing
// "a run" belongs to the request ("start a run on api#12").
var runRequest = regexp.MustCompile(`\b(?:run|start|dispatch|kick off|launch|build|implement|work on|pick up|begin)` +
	`(?: (?:a |the |another )?(?:pipeline )?run)?\b`)

// runBindFillers may stand between a request and the issue it names.
var runBindFillers = map[string]bool{"the": true, "a": true, "an": true, "pipeline": true, "run": true,
	"on": true, "for": true, "of": true, "issue": true, "ticket": true, "work": true, "now": true}

// issueRef is one issue reference, as a word: "#12", "api#12", "acme/api#12",
// or a bare number after "issue" ("issue 12").
var issueRef = regexp.MustCompile(`^(?:(?:([A-Za-z0-9][A-Za-z0-9-]{0,38})/)?([A-Za-z0-9._-]{1,100}))?#([0-9]{1,10})$`)

// runBindToken is a word of a request's segment, an issue reference included
// with anything an id carries on with ("api#12-b" is one word, and names no
// issue).
var runBindToken = regexp.MustCompile(`[A-Za-z0-9._/-]*#[0-9]+[A-Za-z0-9_-]*|[\p{L}\p{M}\p{N}_'-]+`)

// RunRequested reports whether the poster's message in this turn asks to
// start work on target. message is the poster's own message, from the hosted
// service's record and bound to the poster's account; an empty message
// refuses everything. quote is the model's pointer to the words. proposal is
// the completed reply just before this turn when it answered the poster's
// own previous turn, or "". readable are the repositories the poster may
// read: a bare "#12" names an issue only when there is exactly one.
//
// Either a request lies inside the quoted words and stands as asked, and its
// own segment names target first (runBinds); or the whole message is a
// confirmation and the proposal offers exactly one run, this one, plainly.
// Nothing a tool returned takes part, so no earlier message, issue body, card
// or file can satisfy it. It understands English requests only and refuses
// what it does not recognize.
func RunRequested(message, quote, proposal string, target RunTarget, readable []Repo) bool {
	if target.Issue < 1 || target.Owner == "" || target.Repo == "" {
		return false
	}
	n := gateNormalize(message)
	low, q := gateLower(n), gateLower(gateNormalize(quote))
	if q == "" || low == "" || !strings.Contains(low, q) {
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
		for _, loc := range runRequest.FindAllStringIndex(low, -1) {
			if loc[0] < start || loc[1] > end || !standsAsAsked(low, n, loc[0], loc[1]) {
				continue
			}
			if runBinds(n, low, loc[1], target, readable) {
				return true
			}
		}
		if start == 0 && confirmationOnly.MatchString(low) && proposesOnlyRun(gateNormalize(proposal), target, readable) {
			return true
		}
	}
	return false
}

// runBinds reports whether the request that ends at i of low (n's lower-cased
// form) names target as the first word of its own segment other than
// fillers. A list after it lends nothing: "run api#12, api#13" starts api#12.
func runBinds(n, low string, i int, target RunTarget, readable []Repo) bool {
	seg := requestSegments(low, i)[0]
	words := runBindToken.FindAllString(n[seg[0]:seg[1]], -1)
	for k := 0; k < len(words); k++ {
		w := words[k]
		if runBindFillers[strings.ToLower(w)] {
			// "issue 12": a bare number after "issue" is a reference.
			if strings.EqualFold(w, "issue") && k+1 < len(words) && isDigits(words[k+1]) {
				return refNames("#"+words[k+1], target, readable)
			}
			continue
		}
		return refNames(w, target, readable)
	}
	return false
}

// refNames reports whether the word ref names exactly target: its number,
// and its repository (and owner) when it names one. A reference with no
// repository names target only when the poster may read exactly one
// repository, which must be target's.
func refNames(ref string, target RunTarget, readable []Repo) bool {
	m := issueRef.FindStringSubmatch(ref)
	if m == nil || m[3] != strconv.Itoa(target.Issue) {
		return false
	}
	owner, repo := m[1], m[2]
	switch {
	case repo == "":
		return len(readable) == 1 && strings.EqualFold(readable[0].Owner, target.Owner) &&
			strings.EqualFold(readable[0].Repo, target.Repo)
	case owner == "":
		return strings.EqualFold(repo, target.Repo)
	default:
		return strings.EqualFold(owner, target.Owner) && strings.EqualFold(repo, target.Repo)
	}
}

// proposesOnlyRun reports whether proposal, normalized, offers exactly one
// run, and that one: one request word, offered plainly ("Shall I start a
// run on api#12?", "I will dispatch api#12."), with no hedge, condition
// (other than "if you confirm"), negation or retraction, binding target, and
// no act on a card offered beside it.
func proposesOnlyRun(p string, target RunTarget, readable []Repo) bool {
	low := gateLower(p)
	runs := runRequest.FindAllStringIndex(low, -1)
	if len(runs) != 1 || len(statedActs(low)) != 0 {
		return false
	}
	start, end := runs[0][0], runs[0][1]
	cf, _ := clauseBounds(low, start, end)
	lead := strings.TrimSpace(low[cf:start])
	offered := offerQuestion.MatchString(lead) || (offerStatement.MatchString(lead) && !isQuestion(low, end))
	unconditional := !conditional(confirmCondition.ReplaceAllStringFunc(low, blank), start, end)
	sentence := actSentence(low, start, end)
	tentative := proposalHedge.MatchString(sentence) || opensLater(low, end, hedgeOpens)
	return offered && unconditional && !tentative && !actNegation.MatchString(sentence) &&
		!actRetractedAfter(low, end) && runBinds(p, low, end, target, readable)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
