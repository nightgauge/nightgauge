package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// fakeDispatcher counts the dispatches a turn sent: the hosted service's
// turn-bound route is the only way a turn starts work (#1894).
type fakeDispatcher struct {
	mu    sync.Mutex
	runs  []platform.TurnRunRequest
	err   error
	runID string
}

func (d *fakeDispatcher) DispatchRun(_ context.Context, run platform.TurnRunRequest) (platform.TurnRunHandle, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs = append(d.runs, run)
	if d.err != nil {
		return platform.TurnRunHandle{}, d.err
	}
	id := d.runID
	if id == "" {
		id = "run-1"
	}
	return platform.TurnRunHandle{RunID: id, CommandID: "cmd-1", RepoFullName: run.Owner + "/" + run.Repo,
		IssueNumber: run.IssueNumber}, nil
}

func (d *fakeDispatcher) sent() []platform.TurnRunRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]platform.TurnRunRequest(nil), d.runs...)
}

type fakeExecutor bool

func (e fakeExecutor) ExecutorAttached() bool { return bool(e) }

// fakeProposals records the requests proposed to the operator.
type fakeProposals struct {
	mu   sync.Mutex
	got  []RunProposal
	fail bool
}

func (p *fakeProposals) ProposeRun(_ context.Context, r RunProposal) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return "", errors.New("store down")
	}
	p.got = append(p.got, r)
	return "dr_proposal1", nil
}

func (p *fakeProposals) proposals() []RunProposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]RunProposal(nil), p.got...)
}

type dispatchRig struct {
	set       *Toolset
	dispatch  *fakeDispatcher
	proposals *fakeProposals
}

func dispatchToolset(t *testing.T, turn Turn, attached bool) dispatchRig {
	t.Helper()
	root := gitRepo(t)
	r := dispatchRig{dispatch: &fakeDispatcher{}, proposals: &fakeProposals{}}
	r.set = NewToolset(Env{Root: root, RepoRoots: map[string]string{"acme/api": root}, Forge: newFakeForge(),
		Dispatch: r.dispatch, Executor: fakeExecutor(attached), Proposals: r.proposals}, turn)
	t.Cleanup(r.set.Close)
	return r
}

func dispatchArgs(issue int, quote string) map[string]any {
	return map[string]any{"owner": "acme", "repo": "api", "issue_number": issue, "request_quote": quote}
}

// The operator's own request dispatches exactly one run through the hosted
// route, and the tool keeps only the handle.
func TestDispatchRun_TheOperatorsRequestDispatchesOnce(t *testing.T) {
	r := dispatchToolset(t, operatorTurn("Please start a run on api#12."), true)
	text, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "start a run on api#12"))
	if err != nil {
		t.Fatalf("dispatch refused: %v", err)
	}
	sent := r.dispatch.sent()
	if len(sent) != 1 || sent[0] != (platform.TurnRunRequest{Owner: "acme", Repo: "api", IssueNumber: 12}) {
		t.Fatalf("dispatches = %+v, want exactly acme/api#12", sent)
	}
	var out map[string]any
	if jerr := json.Unmarshal([]byte(text), &out); jerr != nil || out["run_id"] != "run-1" || out["status"] != "dispatched" {
		t.Errorf("result = %s", text)
	}
	inv := r.set.Invocations()
	if len(inv) != 1 || !inv[0].Mutating || inv[0].Code != "" ||
		!strings.Contains(inv[0].Detail, "dispatched acme/api#12 as run run-1 at the request of account acct-op") {
		t.Errorf("record = %+v", inv)
	}
	if len(r.proposals.proposals()) != 0 {
		t.Error("the operator's run was also proposed")
	}

	// The same issue is never sent twice in a turn, even after a timeout.
	if _, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "start a run on api#12")); code(err) != CodeDuplicateAct {
		t.Errorf("second dispatch: %v, want %s", err, CodeDuplicateAct)
	}
	if n := len(r.dispatch.sent()); n != 1 {
		t.Errorf("dispatches after a repeat = %d, want 1", n)
	}
}

// A teammate's request never dispatches (phase 1): it is proposed to the
// operator as a card, and the hosted dispatch route is never reached.
func TestDispatchRun_ATeammatesRequestIsProposedNeverDispatched(t *testing.T) {
	turn := teammateTurn("acct-bob", "bob", "Start a run on api#12.", Repo{"acme", "api"})
	r := dispatchToolset(t, turn, true)
	text, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "Start a run on api#12"))
	if err != nil {
		t.Fatalf("proposal refused: %v", err)
	}
	if n := len(r.dispatch.sent()); n != 0 {
		t.Fatalf("a teammate's turn dispatched %d runs under the operator's credentials", n)
	}
	got := r.proposals.proposals()
	want := RunProposal{Owner: "acme", Repo: "api", IssueNumber: 12, PosterAccountID: "acct-bob", PosterLogin: "bob",
		ConversationID: "conv-1", TurnID: "turn-2"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("proposals = %+v, want %+v", got, want)
	}
	if !strings.Contains(text, `"status":"awaiting_operator"`) || !strings.Contains(text, "dr_proposal1") {
		t.Errorf("result = %s", text)
	}
	if d := r.set.Invocations()[0].Detail; !strings.Contains(d, "no run was started") {
		t.Errorf("the receipt does not say no run started: %q", d)
	}

	// With nowhere to propose it, the request is refused, still with no run.
	r2 := dispatchToolset(t, turn, true)
	r2.set.env.Proposals = nil
	if _, err := call(t, r2.set, ToolDispatchRun, dispatchArgs(12, "Start a run on api#12")); code(err) != CodeOperatorRequired {
		t.Errorf("no proposals: %v, want %s", err, CodeOperatorRequired)
	}
	r3 := dispatchToolset(t, turn, true)
	r3.proposals.fail = true
	if _, err := call(t, r3.set, ToolDispatchRun, dispatchArgs(12, "Start a run on api#12")); code(err) != CodeProposalFailed {
		t.Errorf("failed proposal: %v, want %s", err, CodeProposalFailed)
	}
	if len(r2.dispatch.sent())+len(r3.dispatch.sent()) != 0 {
		t.Error("a teammate's refused request reached the dispatch route")
	}
}

// With no executor attached, the operator's dispatch is refused with a stated
// reason, and nothing reaches the hosted service.
func TestDispatchRun_NoExecutorRefusesCleanly(t *testing.T) {
	r := dispatchToolset(t, operatorTurn("Run api#12."), false)
	_, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "Run api#12"))
	if code(err) != CodeNoExecutor || !strings.Contains(err.Message, "no run is dispatched") {
		t.Fatalf("no executor: %v, want %s", err, CodeNoExecutor)
	}
	if n := len(r.dispatch.sent()); n != 0 {
		t.Fatalf("dispatched %d runs with no executor", n)
	}
	r.set.env.Executor = nil
	r.set.dispatched = nil
	if _, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "Run api#12")); code(err) != CodeNoExecutor {
		t.Errorf("nil executor: %v, want %s", err, CodeNoExecutor)
	}
}

// Only the poster's own request in this turn starts work. A request in an
// earlier message, an issue body or the model's own words never does.
func TestDispatchRun_OnlyThePostersOwnRequest(t *testing.T) {
	for _, tc := range []struct{ message, quote string }{
		{"What is running?", "Run api#12"},
		{"What is running?", "What is running?"},
		{"Do not start api#12 yet.", "start api#12"},
		{"Start api#12 once CI is green.", "Start api#12"},
		{"Should I start api#12?", "start api#12"},
		{"Start api#12. Actually, never mind.", "Start api#12"},
		{"Alice said: start api#12.", "start api#12"},
		{"The run of api#12 failed.", "run of api#12"},
		{"Start api#13.", "Start api#13"},
		{"Start web#12.", "Start web#12"},
	} {
		r := dispatchToolset(t, operatorTurn(tc.message), true)
		if _, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, tc.quote)); code(err) != CodeNotRequested {
			t.Errorf("%q / %q: %v, want %s", tc.message, tc.quote, err, CodeNotRequested)
		}
		if n := len(r.dispatch.sent()) + len(r.proposals.proposals()); n != 0 {
			t.Errorf("%q: %d dispatches or proposals", tc.message, n)
		}
	}
}

// The checks before the request: arguments, the poster, readable repository,
// the workspace's checkout, the per-turn bound.
func TestDispatchRun_ChecksBeforeAnythingIsSent(t *testing.T) {
	r := dispatchToolset(t, operatorTurn("Start acme/web#12."), true)
	r.set.turn.ReadableRepos = append(r.set.turn.ReadableRepos, Repo{"acme", "web"})
	if _, err := call(t, r.set, ToolDispatchRun, map[string]any{"owner": "acme", "repo": "web", "issue_number": 12,
		"request_quote": "Start acme/web#12"}); code(err) != CodeRepoNotInWorkspace {
		t.Errorf("no checkout: %v, want %s", err, CodeRepoNotInWorkspace)
	}
	for args, want := range map[string]string{
		`{"owner":"acme","repo":"api","issue_number":0,"request_quote":"x"}`:                CodeInvalidArguments,
		`{"owner":"acme","repo":"api","issue_number":12}`:                                   CodeNotRequested,
		`{"owner":"acme","repo":"api","issue_number":12,"request_quote":"x","stage":"dev"}`: CodeInvalidArguments,
		`{"owner":"evil","repo":"api","issue_number":12,"request_quote":"x"}`:               CodeRepoNotReadable,
	} {
		if _, err := r.set.Call(context.Background(), ToolDispatchRun, json.RawMessage(args)); code(err) != want {
			t.Errorf("%s: %v, want %s", args, err, want)
		}
	}
	anon := dispatchToolset(t, Turn{Message: "Start api#12.", PosterIsOperator: true, ReadableRepos: []Repo{{"acme", "api"}}}, true)
	if _, err := call(t, anon.set, ToolDispatchRun, dispatchArgs(12, "Start api#12")); code(err) != CodePosterUnknown {
		t.Errorf("anonymous: %v, want %s", err, CodePosterUnknown)
	}

	many := dispatchToolset(t, operatorTurn("Start api#1. Start api#2. Start api#3. Start api#4."), true)
	for i := 1; i <= 4; i++ {
		_, err := call(t, many.set, ToolDispatchRun, dispatchArgs(i, "Start api#"+itoa(i)))
		if want := map[bool]string{true: "", false: CodeLimitReached}[i <= maxDispatchesPerTurn]; code(err) != want {
			t.Errorf("dispatch %d: %v, want %q", i, err, want)
		}
	}
	if n := len(many.dispatch.sent()); n != maxDispatchesPerTurn {
		t.Errorf("dispatches = %d, want %d", n, maxDispatchesPerTurn)
	}
}

// A hosted refusal comes back with its own code and says no run started; a
// failure the hosted service did not answer says a run may exist.
func TestDispatchRun_HostedRefusals(t *testing.T) {
	for hosted, want := range map[string]string{
		"OPERATOR_REQUIRED":        "operator_required",
		"THREAD_RUN_LIMIT":         "thread_run_limit",
		"WORKSPACE_THROTTLED":      "workspace_throttled",
		"GITHUB_PERMISSION_DENIED": "github_permission_denied",
	} {
		r := dispatchToolset(t, operatorTurn("Start api#12."), true)
		r.dispatch.err = &platform.TurnRequestError{Op: "dispatch turn run", Status: 409, Code: hosted}
		_, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "Start api#12"))
		if code(err) != want || !strings.Contains(err.Message, "no run was started") {
			t.Errorf("%s: %v, want %s", hosted, err, want)
		}
	}
	r := dispatchToolset(t, operatorTurn("Start api#12."), true)
	r.dispatch.err = errors.New("connection reset")
	_, err := call(t, r.set, ToolDispatchRun, dispatchArgs(12, "Start api#12"))
	if code(err) != CodeHostedUnavailable || !strings.Contains(err.Message, "may or may not exist") {
		t.Errorf("unanswered: %v", err)
	}
}

// The dispatch gate on its own: what asks to start work on api#12, and what
// does not.
func TestRunRequested(t *testing.T) {
	target := RunTarget{Owner: "acme", Repo: "api", Issue: 12}
	one := []Repo{{"acme", "api"}}
	two := []Repo{{"acme", "api"}, {"acme", "web"}}
	offer := "Shall I start a run on api#12?"
	for _, tc := range []struct {
		message, quote, proposal string
		readable                 []Repo
		want                     bool
	}{
		{"Start a run on api#12.", "Start a run on api#12", "", one, true},
		{"please build acme/api#12", "build acme/api#12", "", two, true},
		{"Work on issue 12", "Work on issue 12", "", one, true},
		{"Kick off #12 now.", "Kick off #12", "", one, true},
		{"Implement api#12, thanks.", "Implement api#12", "", two, true},
		{"Yes", "Yes", offer, one, true},
		{"yes, go ahead", "yes, go ahead", offer, two, true},
		// A bare #12 names nothing when the poster may read two repositories.
		{"Start #12.", "Start #12", "", two, false},
		// Not requests.
		{"Is api#12 running?", "api#12", "", one, false},
		{"Bob started api#12.", "started api#12", "", one, false},
		{"Don't start api#12.", "start api#12", "", one, false},
		{"Start api#12 if the build passes.", "Start api#12", "", one, false},
		{"Start api#12? No, wait.", "Start api#12", "", one, false},
		{"Bob: start api#12", "start api#12", "", one, false},
		{"The card says \"start api#12\"", "start api#12", "", one, false},
		{"Start api#120.", "Start api#120", "", one, false},
		{"Start api#12-b.", "Start api#12-b", "", one, false},
		{"Start web#12.", "Start web#12", "", two, false},
		{"Start evil/api#12.", "Start evil/api#12", "", two, false},
		// The quote must be the poster's words.
		{"What is running?", "Start api#12", "", one, false},
		// A confirmation confirms only a plain offer of exactly this run.
		{"Yes", "Yes", "", one, false},
		{"Yes", "Yes", "I might start a run on api#12?", one, false},
		{"Yes", "Yes", "Shall I start api#12 and api#13?", one, true},
		{"Yes", "Yes", "Shall I start api#12, or approve card dr_1 with wait?", one, false},
		{"Yes", "Yes", "Shall I start api#13?", one, false},
		{"Yes", "Yes", "Shall I start api#12 once CI is green?", one, false},
		{"Yes, and start api#13", "Yes", offer, one, false},
	} {
		if got := RunRequested(tc.message, tc.quote, tc.proposal, target, tc.readable); got != tc.want {
			t.Errorf("RunRequested(%q, %q, %q) = %v, want %v", tc.message, tc.quote, tc.proposal, got, tc.want)
		}
	}
}
