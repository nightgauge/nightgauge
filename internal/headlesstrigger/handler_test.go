package headlesstrigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

const (
	agentID = "agent-addressed"
	ackRun  = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
)

type ackCall struct{ op, agent, command, detail string }

type fakeAcks struct {
	mu         sync.Mutex
	calls      []ackCall
	ackErr     error
	rejectErr  error
	runID      string
	emptyRunID bool
}

func (f *fakeAcks) AcknowledgeAgentCommand(_ context.Context, agent, command string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ackCall{"ack", agent, command, ""})
	if f.ackErr != nil {
		return "", f.ackErr
	}
	if f.emptyRunID {
		return "", nil
	}
	if f.runID == "" {
		return ackRun, nil
	}
	return f.runID, nil
}

func (f *fakeAcks) RejectAgentCommand(_ context.Context, agent, command, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ackCall{"reject", agent, command, detail})
	return f.rejectErr
}

func (f *fakeAcks) all() []ackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ackCall(nil), f.calls...)
}

type report struct {
	runID, visibility string
	status            platform.RemoteRunStatus
	// ctxErr is the report context's error when the report was made: a
	// cancelled one would fail on the real client.
	ctxErr error
}

type fakeReports struct {
	mu      sync.Mutex
	reports []report
	// errs are returned by successive calls, then nil.
	errs  []error
	tries int
}

func (f *fakeReports) ReportRemoteRunTerminal(ctx context.Context, id string, status platform.RemoteRunStatus, vis string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	f.reports = append(f.reports, report{id, vis, status, ctx.Err()})
	return nil
}

func (f *fakeReports) all() []report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]report(nil), f.reports...)
}

type fakeRunner struct {
	mu   sync.Mutex
	reqs []Request
	res  Result
	err  error
	// block, when set, holds every run until it is closed.
	block chan struct{}
	panic bool
}

func (f *fakeRunner) Run(ctx context.Context, req Request) (Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	block := f.block
	f.mu.Unlock()
	if f.panic {
		panic("boom")
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	return f.res, f.err
}

func (f *fakeRunner) all() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

type fixture struct {
	acks    *fakeAcks
	reports *fakeReports
	runner  *fakeRunner
	busy    map[string]bool
	pin     string // the ValidatePin refusal; "" accepts
	pinArgs []string
	h       *Handler
}

func newFixture(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	f := &fixture{
		acks:    &fakeAcks{},
		reports: &fakeReports{},
		runner:  &fakeRunner{res: Result{Completed: true}},
		busy:    map[string]bool{},
	}
	f.h = New(ctx, Config{
		Acks:    f.acks,
		Reports: f.reports,
		Runner:  f.runner,
		Served: func() ([]platform.AgentRepo, error) {
			return []platform.AgentRepo{{Owner: "Acme", Repo: "Widgets"}}, nil
		},
		Busy: func(repo string, n int) bool { return f.busy[fmt.Sprintf("%s#%d", repo, n)] },
		ValidatePin: func(repo, adapter, model string) string {
			f.pinArgs = []string{repo, adapter, model}
			return f.pin
		},
	})
	return f
}

func trigger(id, payload string) platform.PendingCommand {
	return platform.PendingCommand{ID: id, Type: TriggerCommandType, Payload: []byte(payload)}
}

const okPayload = `{"owner":"acme","repo":"widgets","issueNumber":7}`

func TestHandler_AcceptedTriggerRunsAndReportsOnce(t *testing.T) {
	f := newFixture(t, context.Background())
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-1",
		`{"owner":"acme","repo":"widgets","issueNumber":7,"adapter":"codex","model":"gpt","visibility":"private"}`))
	f.h.Wait()

	calls := f.acks.all()
	if len(calls) != 1 || calls[0].op != "ack" || calls[0].agent != agentID || calls[0].command != "cmd-1" {
		t.Fatalf("acks = %+v, want one ack under the addressed agent id", calls)
	}
	reqs := f.runner.all()
	if len(reqs) != 1 {
		t.Fatalf("runs = %d, want 1", len(reqs))
	}
	want := Request{Repo: "Acme/Widgets", IssueNumber: 7, RemoteRunID: ackRun, Adapter: "codex", Model: "gpt", Visibility: state.VisibilityPrivate}
	if reqs[0] != want {
		t.Errorf("request = %+v, want %+v", reqs[0], want)
	}
	if got := f.pinArgs; len(got) != 3 || got[0] != "Acme/Widgets" || got[1] != "codex" || got[2] != "gpt" {
		t.Errorf("pin validated with %v", got)
	}
	reps := f.reports.all()
	if len(reps) != 1 || reps[0] != (report{ackRun, state.VisibilityPrivate, platform.RemoteRunSuccess, nil}) {
		t.Errorf("reports = %+v, want one success for the ack's run id", reps)
	}
}

func TestHandler_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		busy          bool
		pin           string
		wantDetail    string
	}{
		{"missing owner", `{"repo":"widgets","issueNumber":7}`, false, "", DetailInvalidCommand},
		{"missing repo", `{"owner":"acme","issueNumber":7}`, false, "", DetailInvalidCommand},
		{"missing issue", `{"owner":"acme","repo":"widgets"}`, false, "", DetailInvalidCommand},
		{"malformed payload", `not json`, false, "", DetailInvalidCommand},
		{"repo not in workspace", `{"owner":"acme","repo":"other","issueNumber":7}`, false, "", "not-covered:"},
		{"conversation turn, no operator flag", `{"owner":"acme","repo":"widgets","issueNumber":7,"conversationTurnId":"t1"}`, false, "", DetailOperatorRequired},
		{"poster not operator", `{"owner":"acme","repo":"widgets","issueNumber":7,"posterIsOperator":false}`, false, "", DetailOperatorRequired},
		{"poster operator as string", `{"owner":"acme","repo":"widgets","issueNumber":7,"posterIsOperator":"true"}`, false, "", DetailOperatorRequired},
		{"pin refused", `{"owner":"acme","repo":"widgets","issueNumber":7,"adapter":"nope"}`, false, "adapter-not-allowed", "adapter-not-allowed"},
		{"pin not a string", `{"owner":"acme","repo":"widgets","issueNumber":7,"adapter":3}`, false, "", DetailInvalidPin},
		{"pin null", `{"owner":"acme","repo":"widgets","issueNumber":7,"model":null}`, false, "", DetailInvalidPin},
		{"live or queued run", okPayload, true, "", "already-running:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, context.Background())
			f.pin = tc.pin
			if tc.busy {
				f.busy["Acme/Widgets#7"] = true
			}
			f.h.HandleCommand(context.Background(), agentID, trigger("cmd-r", tc.payload))
			f.h.Wait()
			calls := f.acks.all()
			if len(calls) != 1 || calls[0].op != "reject" || calls[0].agent != agentID {
				t.Fatalf("acks = %+v, want one reject under the addressed agent", calls)
			}
			if !strings.HasPrefix(calls[0].detail, tc.wantDetail) {
				t.Errorf("detail = %q, want prefix %q", calls[0].detail, tc.wantDetail)
			}
			if n := len(f.runner.all()); n != 0 {
				t.Errorf("runs = %d, want 0", n)
			}
			if n := len(f.reports.all()); n != 0 {
				t.Errorf("reports = %d, want 0", n)
			}
		})
	}
}

func TestHandler_OperatorPostedConversationRunRuns(t *testing.T) {
	f := newFixture(t, context.Background())
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-op",
		`{"owner":"acme","repo":"widgets","issueNumber":7,"conversationTurnId":"t1","posterIsOperator":true}`))
	f.h.Wait()
	if n := len(f.runner.all()); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	if f.pinArgs != nil {
		t.Error("no pin was requested, so none is validated")
	}
}

func TestHandler_RedeliveryStartsOneRun(t *testing.T) {
	f := newFixture(t, context.Background())
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-dup", okPayload))
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-dup", okPayload))
	f.h.Wait()
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-dup", okPayload))
	f.h.Wait()
	if n := len(f.runner.all()); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
	if n := len(f.acks.all()); n != 1 {
		t.Errorf("acks = %d, want 1", n)
	}
}

func TestHandler_SecondTriggerForARunningIssueIsRefused(t *testing.T) {
	f := newFixture(t, context.Background())
	f.runner.block = make(chan struct{})
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-a", okPayload))
	// Wait until the first run is under way.
	waitUntil(t, "the first run to start", func() bool { return len(f.runner.all()) > 0 })
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-b", okPayload))
	waitUntil(t, "the second trigger to be answered", func() bool { return len(f.acks.all()) >= 2 })
	close(f.runner.block)
	f.h.Wait()
	calls := f.acks.all()
	if calls[1].op != "reject" || calls[1].command != "cmd-b" || !strings.HasPrefix(calls[1].detail, "already-running:") {
		t.Errorf("second trigger answered %+v, want already-running reject", calls[1])
	}
	if n := len(f.runner.all()); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
	// The issue is free again once its run ended.
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-c", okPayload))
	f.h.Wait()
	if n := len(f.runner.all()); n != 2 {
		t.Errorf("runs after release = %d, want 2", n)
	}
}

func TestHandler_AlreadyAckedStartsNothing(t *testing.T) {
	f := newFixture(t, context.Background())
	f.acks.ackErr = fmt.Errorf("wrapped: %w", platform.ErrAgentCommandAlreadyAcked)
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-409", okPayload))
	f.h.Wait()
	if n := len(f.runner.all()); n != 0 {
		t.Errorf("runs = %d, want 0", n)
	}
	if n := len(f.reports.all()); n != 0 {
		t.Errorf("reports = %d, want 0", n)
	}
}

func TestHandler_OtherCommandTypesAreIgnored(t *testing.T) {
	f := newFixture(t, context.Background())
	f.h.HandleCommand(context.Background(), agentID, platform.PendingCommand{ID: "cmd-p", Type: "pause", Payload: []byte(okPayload)})
	f.h.Wait()
	if len(f.acks.all())+len(f.runner.all()) != 0 {
		t.Error("a non-trigger command was handled")
	}
}

func TestHandler_TerminalStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		res    Result
		err    error
		cancel bool
		panic  bool
		want   platform.RemoteRunStatus
	}{
		{"completed", Result{Completed: true}, nil, false, false, platform.RemoteRunSuccess},
		{"non-failure terminal kind", Result{TerminalKind: orchestrator.TerminalKindIssueClosed}, nil, false, false, platform.RemoteRunSuccess},
		{"approval hold", Result{TerminalKind: orchestrator.TerminalKindArchitectureApprovalRequired}, nil, false, false, platform.RemoteRunSuccess},
		{"failed", Result{TerminalKind: "stage_failed"}, nil, false, false, platform.RemoteRunFailure},
		{"never dispatched", Result{}, nil, false, false, platform.RemoteRunFailure},
		{"runner error", Result{}, errors.New("no adapter"), false, false, platform.RemoteRunFailure},
		{"runner panic", Result{}, nil, false, true, platform.RemoteRunFailure},
		{"cancelled", Result{}, context.Canceled, true, false, platform.RemoteRunCancelled},
		{"completed despite cancel", Result{Completed: true}, nil, true, false, platform.RemoteRunSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newFixture(t, ctx)
			f.runner.res, f.runner.err, f.runner.panic = tc.res, tc.err, tc.panic
			if tc.cancel {
				f.runner.block = make(chan struct{})
			}
			f.h.HandleCommand(context.Background(), agentID, trigger("cmd-t", okPayload))
			if tc.cancel {
				waitUntil(t, "the run to start", func() bool { return len(f.runner.all()) > 0 })
				cancel()
			}
			f.h.Wait()
			reps := f.reports.all()
			if len(reps) != 1 {
				t.Fatalf("reports = %+v, want exactly one", reps)
			}
			if reps[0].status != tc.want || reps[0].runID != ackRun {
				t.Errorf("report = %+v, want %s for %s", reps[0], tc.want, ackRun)
			}
			if reps[0].ctxErr != nil {
				t.Errorf("the report was made on a dead context (%v); the real client would fail it", reps[0].ctxErr)
			}
		})
	}
}

// waitUntil polls cond until it holds, failing the test after a bound.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHandler_AckFailuresStartNothingAndReleaseTheIssue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		runID string
	}{
		{"ack error", errors.New("503"), ""},
		{"empty run id", nil, "-"},
		{"run id not a UUID", nil, "run-1\ninjected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, context.Background())
			f.acks.ackErr = tc.err
			f.acks.runID = tc.runID
			if tc.runID == "-" {
				f.acks.emptyRunID = true
			}
			f.h.HandleCommand(context.Background(), agentID, trigger("cmd-x", okPayload))
			f.h.Wait()
			if n := len(f.runner.all()) + len(f.reports.all()); n != 0 {
				t.Fatalf("runs+reports = %d, want 0", n)
			}
			// The issue is not left reserved.
			f.acks.ackErr, f.acks.runID, f.acks.emptyRunID = nil, "", false
			f.h.HandleCommand(context.Background(), agentID, trigger("cmd-y", okPayload))
			f.h.Wait()
			if n := len(f.runner.all()); n != 1 {
				t.Errorf("runs after a failed ack = %d, want 1", n)
			}
		})
	}
}

// A transient ack failure leaves the command unanswered, so the platform's
// redelivery of it is decided again: one ack succeeds and one run starts.
func TestHandler_RedeliveryAfterAFailedAckRuns(t *testing.T) {
	f := newFixture(t, context.Background())
	f.acks.ackErr = errors.New("503")
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-503", okPayload))
	f.h.Wait()
	f.acks.mu.Lock()
	f.acks.ackErr = nil
	f.acks.mu.Unlock()
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-503", okPayload))
	f.h.Wait()
	if n := len(f.runner.all()); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
	if n := len(f.acks.all()); n != 2 {
		t.Errorf("ack attempts = %d, want 2", n)
	}
	// Answered now: a further delivery starts nothing.
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-503", okPayload))
	f.h.Wait()
	if n := len(f.acks.all()); n != 2 {
		t.Errorf("ack attempts after the answer = %d, want 2", n)
	}
}

// A failed reject leaves the command unanswered; its redelivery is answered.
func TestHandler_RedeliveryAfterAFailedRejectIsAnswered(t *testing.T) {
	f := newFixture(t, context.Background())
	f.acks.rejectErr = errors.New("503")
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-bad", `{}`))
	f.h.Wait()
	f.acks.mu.Lock()
	f.acks.rejectErr = nil
	f.acks.mu.Unlock()
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-bad", `{}`))
	f.h.Wait()
	if n := len(f.acks.all()); n != 2 {
		t.Errorf("reject attempts = %d, want 2", n)
	}
	if n := len(f.runner.all()); n != 0 {
		t.Errorf("runs = %d, want 0", n)
	}
}

func TestHandler_RunningReportsAnAcceptedIssueUntilItsRunEnds(t *testing.T) {
	f := newFixture(t, context.Background())
	f.runner.block = make(chan struct{})
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-run", okPayload))
	waitUntil(t, "the run to start", func() bool { return len(f.runner.all()) > 0 })
	req := f.runner.all()[0]
	if !f.h.Running(strings.ToUpper(req.Repo), req.IssueNumber) {
		t.Error("an issue running headlessly is not reported running")
	}
	if f.h.Running(req.Repo, req.IssueNumber+1) {
		t.Error("another issue is reported running")
	}
	close(f.runner.block)
	f.h.Wait()
	if f.h.Running(req.Repo, req.IssueNumber) {
		t.Error("an ended run is still reported running")
	}
}

func TestHandler_UnusableIDsAreLeftAlone(t *testing.T) {
	for _, ids := range [][2]string{
		{"", "cmd-1"}, {agentID, ""}, {"a/b", "cmd-1"}, {agentID, "cmd 1"},
		{agentID, strings.Repeat("c", 101)},
	} {
		f := newFixture(t, context.Background())
		f.h.HandleCommand(context.Background(), ids[0], trigger(ids[1], okPayload))
		f.h.Wait()
		if n := len(f.acks.all()); n != 0 {
			t.Errorf("ids %q: answered %d times, want 0", ids, n)
		}
	}
}

func TestHandler_WorkspaceAndPinEdgeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     func(*Config)
		payload string
		want    string
	}{
		{"served errors", func(c *Config) {
			c.Served = func() ([]platform.AgentRepo, error) { return nil, errors.New("boom") }
		}, okPayload, DetailWorkspaceUnreadable},
		{"no served", func(c *Config) { c.Served = nil }, okPayload, "not-covered:"},
		{"declared entry without owner", func(c *Config) {
			c.Served = func() ([]platform.AgentRepo, error) { return []platform.AgentRepo{{Repo: "widgets"}}, nil }
		}, `{"owner":"","repo":"widgets","issueNumber":7}`, DetailInvalidCommand},
		{"no pin validation", func(c *Config) { c.ValidatePin = nil },
			`{"owner":"acme","repo":"widgets","issueNumber":7,"adapter":"codex"}`, DetailPinUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, context.Background())
			tc.cfg(&f.h.cfg)
			f.h.HandleCommand(context.Background(), agentID, trigger("cmd-e", tc.payload))
			f.h.Wait()
			calls := f.acks.all()
			if len(calls) != 1 || calls[0].op != "reject" || !strings.HasPrefix(calls[0].detail, tc.want) {
				t.Fatalf("acks = %+v, want a reject with %q", calls, tc.want)
			}
			if n := len(f.runner.all()); n != 0 {
				t.Errorf("runs = %d, want 0", n)
			}
		})
	}
}

func TestPublicDetail(t *testing.T) {
	if got := publicDetail("bad\nline\u202eend"); strings.ContainsAny(got, "\n\u202e") {
		t.Errorf("control or format character kept: %q", got)
	}
	token := "ghp_" + strings.Repeat("a", 36)
	if got := publicDetail("leak " + token); strings.Contains(got, token) {
		t.Errorf("credential kept: %q", got)
	}
	long := strings.Repeat("é", platform.AgentCommandAckDetailMax) // 2 bytes each
	got := publicDetail(long)
	if len(got) > platform.AgentCommandAckDetailMax || !utf8.ValidString(got) {
		t.Errorf("cut to %d bytes, valid UTF-8 %v; want <= %d and valid", len(got), utf8.ValidString(got), platform.AgentCommandAckDetailMax)
	}
}

func TestHandler_WaitForTimesOutWhileARunIsBlocked(t *testing.T) {
	f := newFixture(t, context.Background())
	f.runner.block = make(chan struct{})
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-w", okPayload))
	waitUntil(t, "the run to start", func() bool { return len(f.runner.all()) > 0 })
	if f.h.WaitFor(20 * time.Millisecond) {
		t.Error("WaitFor reported done while the run is blocked")
	}
	close(f.runner.block)
	if !f.h.WaitFor(5 * time.Second) {
		t.Error("WaitFor did not see the run end")
	}
}

func TestHandler_TerminalReportRetriesATransientFailure(t *testing.T) {
	f := newFixture(t, context.Background())
	f.h.cfg.ReportRetryDelay = time.Millisecond
	f.reports.errs = []error{errors.New("503"), errors.New("connection reset")}
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-retry", okPayload))
	f.h.Wait()
	if n := len(f.reports.all()); n != 1 {
		t.Fatalf("successful reports = %d, want 1", n)
	}
	if f.reports.tries != 3 {
		t.Errorf("attempts = %d, want 3", f.reports.tries)
	}
}

func TestHandler_TerminalReportStopsOnAPermanentFailure(t *testing.T) {
	f := newFixture(t, context.Background())
	f.h.cfg.ReportRetryDelay = time.Millisecond
	f.reports.errs = []error{fmt.Errorf("wrapped: %w", platform.ErrInvalidRemoteRunID)}
	f.h.HandleCommand(context.Background(), agentID, trigger("cmd-perm", okPayload))
	f.h.Wait()
	if f.reports.tries != 1 {
		t.Errorf("attempts = %d, want 1", f.reports.tries)
	}
}
