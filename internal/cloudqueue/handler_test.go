package cloudqueue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// fakeQueue is the local queue: it answers result (or err) and counts calls.
type fakeQueue struct {
	mu        sync.Mutex
	result    Outcome
	err       error
	added     []Item
	withdrawn []Item
	// block, when set, holds AddCloudItem until it is closed.
	block chan struct{}
}

func (q *fakeQueue) AddCloudItem(_ context.Context, item Item) (Outcome, error) {
	if q.block != nil {
		<-q.block
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.added = append(q.added, item)
	return q.result, q.err
}

func (q *fakeQueue) WithdrawCloudItem(item Item) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.withdrawn = append(q.withdrawn, item)
	return true
}

func (q *fakeQueue) adds() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Item(nil), q.added...)
}

func (q *fakeQueue) withdrawals() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Item(nil), q.withdrawn...)
}

type ack struct {
	agentID, commandID, outcome, detail string
}

// fakeAcks records every acknowledgement and answers errs in order, then nil.
type fakeAcks struct {
	mu   sync.Mutex
	acks []ack
	errs []error
}

func (a *fakeAcks) record(agentID, commandID, outcome, detail string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks = append(a.acks, ack{agentID, commandID, outcome, detail})
	if len(a.errs) > 0 {
		err := a.errs[0]
		a.errs = a.errs[1:]
		return err
	}
	return nil
}

func (a *fakeAcks) ApplyAgentCommand(_ context.Context, agentID, commandID, detail string) error {
	return a.record(agentID, commandID, platform.AgentCommandAppliedOutcome, detail)
}

func (a *fakeAcks) AlreadyResolvedAgentCommand(_ context.Context, agentID, commandID, detail string) error {
	return a.record(agentID, commandID, platform.AgentCommandAlreadyResolvedOutcome, detail)
}

func (a *fakeAcks) RejectAgentCommand(_ context.Context, agentID, commandID, detail string) error {
	return a.record(agentID, commandID, platform.AgentCommandRejectedOutcome, detail)
}

func (a *fakeAcks) all() []ack {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ack(nil), a.acks...)
}

func served(repos ...platform.AgentRepo) func() ([]platform.AgentRepo, error) {
	return func() ([]platform.AgentRepo, error) { return repos, nil }
}

var acmeWeb = platform.AgentRepo{Owner: "acme", Repo: "web"}

func command(id, payload string) platform.PendingCommand {
	return platform.PendingCommand{ID: id, Type: platform.QueueAddCommandType, Payload: json.RawMessage(payload)}
}

const validPayload = `{"type":"queue_add","owner":"acme","repo":"web","issueNumber":637,"priority":"high",` +
	`"queueItemId":"00000000-0000-4000-8000-000000000042","workspaceId":"00000000-0000-4000-8000-000000000009"}`

func newHandler(q Queue, a Acker, s func() ([]platform.AgentRepo, error)) *Handler {
	return New(context.Background(), Config{Queue: q, Acks: a, Served: s, Timeout: 5 * time.Second})
}

func handle(h *Handler, agentID string, cmd platform.PendingCommand) {
	h.HandleCommand(context.Background(), agentID, cmd)
	h.Wait()
}

func TestParseItem(t *testing.T) {
	item, err := ParseItem(json.RawMessage(validPayload))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Item{Owner: "acme", Repo: "web", IssueNumber: 637, Priority: "high"}); item != want {
		t.Errorf("item = %+v, want %+v", item, want)
	}
	item, err = ParseItem(json.RawMessage(`{"owner":"acme","repo":"web","issueNumber":1}`))
	if err != nil || item.Priority != "medium" {
		t.Errorf("no priority: item %+v err %v, want the service's default, medium", item, err)
	}

	for name, raw := range map[string]string{
		"not JSON":          `nope`,
		"no owner":          `{"repo":"web","issueNumber":1}`,
		"slash in owner":    `{"owner":"acme/evil","repo":"web","issueNumber":1}`,
		"dot-dot repo":      `{"owner":"acme","repo":"..","issueNumber":1}`,
		"newline in repo":   `{"owner":"acme","repo":"web\n[ok]","issueNumber":1}`,
		"long owner":        `{"owner":"` + strings.Repeat("a", 101) + `","repo":"web","issueNumber":1}`,
		"zero issue":        `{"owner":"acme","repo":"web","issueNumber":0}`,
		"negative issue":    `{"owner":"acme","repo":"web","issueNumber":-3}`,
		"unknown priority":  `{"owner":"acme","repo":"web","issueNumber":1,"priority":"urgent"}`,
		"issue as a string": `{"owner":"acme","repo":"web","issueNumber":"1"}`,
	} {
		if _, err := ParseItem(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

// The ack outcome for each thing the local queue can answer.
func TestHandler_AcknowledgesWhatTheQueueFound(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      Outcome
		err         error
		wantOutcome string
		wantDetail  string
	}{
		{"queued", Queued, nil, "applied", ""},
		{"epic", QueuedEpic, nil, "applied", "queued as its open sub-issues"},
		{"already queued", AlreadyQueued, nil, "already_resolved", "already-queued: acme/web#637 is already in this agent's queue"},
		{"already running", AlreadyRunning, nil, "already_resolved", "already-running: acme/web#637 is already running on this agent"},
		{"refused", 0, Refuse("human-only-label: acme/web#637 carries %q", "owner-action"), "rejected", `human-only-label: acme/web#637 carries "owner-action"`},
		{"failed", 0, errors.New("open /home/someone/.nightgauge/queue-state.json: permission denied"), "rejected", "cannot-queue: this agent could not queue acme/web#637"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQueue{result: tc.result, err: tc.err}
			a := &fakeAcks{}
			handle(newHandler(q, a, served(acmeWeb)), "agent-1", command("cmd-1", validPayload))

			got := a.all()
			if len(got) != 1 {
				t.Fatalf("acks = %+v, want one", got)
			}
			if want := (ack{"agent-1", "cmd-1", tc.wantOutcome, tc.wantDetail}); got[0] != want {
				t.Errorf("ack = %+v, want %+v", got[0], want)
			}
			if adds := q.adds(); len(adds) != 1 || adds[0] != (Item{"acme", "web", 637, "high"}) {
				t.Errorf("queue got %+v, want acme/web#637 at high", adds)
			}
		})
	}
}

// A daemon that does not declare the repository refuses it without touching
// its queue, so the daemon that does can apply it: an `applied` replaces an
// earlier `rejected` from another connection of the same agent.
func TestHandler_RefusesARepositoryItDoesNotServe(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{}
	handle(newHandler(q, a, served(platform.AgentRepo{Owner: "acme", Repo: "api"})), "agent-1", command("cmd-1", validPayload))

	if got := a.all(); len(got) != 1 || got[0].outcome != "rejected" ||
		got[0].detail != "not-covered: this agent's workspace does not include acme/web" {
		t.Errorf("acks = %+v, want one not-covered refusal", got)
	}
	if len(q.adds()) != 0 {
		t.Errorf("queue got %+v for a repository this daemon does not serve", q.adds())
	}
}

func TestHandler_RefusesWhenTheWorkspaceCannotBeRead(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{}
	broken := func() ([]platform.AgentRepo, error) {
		return nil, errors.New("manifest: line 3: bad indent at /home/x")
	}
	handle(newHandler(q, a, broken), "agent-1", command("cmd-1", validPayload))

	got := a.all()
	if len(got) != 1 || got[0].outcome != "rejected" || got[0].detail != "cannot-queue: this agent could not read the workspace it serves" {
		t.Errorf("acks = %+v, want one cannot-queue refusal that names no path", got)
	}
	if len(q.adds()) != 0 {
		t.Error("queued with an unreadable workspace")
	}
}

// GitHub names are case-insensitive; the item is queued under the spelling
// the workspace declares, which is the spelling the local queue keys on.
func TestHandler_QueuesUnderTheDeclaredSpelling(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{}
	payload := `{"type":"queue_add","owner":"ACME","repo":"Web","issueNumber":5,"priority":"low"}`
	handle(newHandler(q, a, served(acmeWeb)), "agent-1", command("cmd-1", payload))

	if adds := q.adds(); len(adds) != 1 || adds[0] != (Item{"acme", "web", 5, "low"}) {
		t.Errorf("queue got %+v, want acme/web#5", adds)
	}
}

func TestHandler_RefusesAMalformedPayloadWithoutEchoingIt(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{}
	payload := `{"owner":"acme/../../etc","repo":"web","issueNumber":1}`
	handle(newHandler(q, a, served(acmeWeb)), "agent-1", command("cmd-1", payload))

	got := a.all()
	if len(got) != 1 || got[0].outcome != "rejected" || !strings.HasPrefix(got[0].detail, "invalid-command: owner") {
		t.Fatalf("acks = %+v, want one invalid-command refusal", got)
	}
	if strings.Contains(got[0].detail, "etc") {
		t.Errorf("detail %q echoes the payload", got[0].detail)
	}
	if len(q.adds()) != 0 {
		t.Error("queued a malformed payload")
	}
}

// An id that could not be used in a URL path or a log line is never
// acknowledged, and nothing is queued for it.
func TestHandler_LeavesAnUnusableIDAlone(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{}
	h := newHandler(q, a, served(acmeWeb))
	handle(h, "agent-1", command("../ack", validPayload))
	handle(h, "agent/1", command("cmd-1", validPayload))
	handle(h, "agent-1", platform.PendingCommand{ID: "cmd-2", Type: "trigger", Payload: json.RawMessage(validPayload)})

	if len(a.all()) != 0 || len(q.adds()) != 0 {
		t.Errorf("acks %+v, adds %+v: want neither", a.all(), q.adds())
	}
}

// A copy delivered again after the first was answered re-sends that answer
// and never queues again: the platform replays a command whose ack it did not
// record.
func TestHandler_ARedeliveryNeverQueuesTwice(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{errs: []error{errors.New("connection reset")}}
	h := newHandler(q, a, served(acmeWeb))

	handle(h, "agent-1", command("cmd-1", validPayload))
	handle(h, "agent-1", command("cmd-1", validPayload))

	if adds := q.adds(); len(adds) != 1 {
		t.Fatalf("queue got %d adds, want 1", len(adds))
	}
	got := a.all()
	if len(got) != 2 || got[0].outcome != "applied" || got[1].outcome != "applied" {
		t.Errorf("acks = %+v, want applied twice (the first failed)", got)
	}
	if len(q.withdrawals()) != 0 {
		t.Errorf("withdrew %+v after a transient ack failure", q.withdrawals())
	}
}

// A copy that arrives while the first is still being handled is dropped; the
// first answers it.
func TestHandler_AConcurrentCopyIsDropped(t *testing.T) {
	q := &fakeQueue{result: Queued, block: make(chan struct{})}
	a := &fakeAcks{}
	h := newHandler(q, a, served(acmeWeb))

	h.HandleCommand(context.Background(), "agent-1", command("cmd-1", validPayload))
	h.HandleCommand(context.Background(), "agent-1", command("cmd-1", validPayload))
	close(q.block)
	h.Wait()

	if len(q.adds()) != 1 || len(a.all()) != 1 {
		t.Errorf("adds %d, acks %d: want one each", len(q.adds()), len(a.all()))
	}
}

// When the ack finds the command already taken (another daemon or window of
// the agent applied it first, or it was cancelled), the item this command
// queued is taken back, so the issue is not queued twice on one machine.
func TestHandler_TakesBackItsItemWhenAnotherConnectionWon(t *testing.T) {
	q := &fakeQueue{result: Queued}
	a := &fakeAcks{errs: []error{alreadyAcked()}}
	handle(newHandler(q, a, served(acmeWeb)), "agent-1", command("cmd-1", validPayload))

	if w := q.withdrawals(); len(w) != 1 || w[0] != (Item{"acme", "web", 637, "high"}) {
		t.Errorf("withdrawn %+v, want acme/web#637", w)
	}
}

// An item that was queued before the command is the operator's own and is
// never taken back.
func TestHandler_NeverTakesBackAnItemItDidNotQueue(t *testing.T) {
	for _, result := range []Outcome{AlreadyQueued, AlreadyRunning, QueuedEpic} {
		q := &fakeQueue{result: result}
		a := &fakeAcks{errs: []error{alreadyAcked()}}
		handle(newHandler(q, a, served(acmeWeb)), "agent-1", command("cmd-1", validPayload))
		if w := q.withdrawals(); len(w) != 0 {
			t.Errorf("outcome %d: withdrew %+v", result, w)
		}
	}
}

func alreadyAcked() error {
	return &platform.AgentCommandAckError{Status: 409, Code: "ALREADY_ACKED"}
}

// The detail goes through the shared credential registry, loses control and
// format characters, and fits the service's bound.
func TestPublicDetail(t *testing.T) {
	token := "ghp_" + strings.Repeat("A", 36)
	got := publicDetail("human-only-label: x carries \"" + token + "\"\n‮evil")
	if strings.Contains(got, token) {
		t.Errorf("detail %q still carries the token", got)
	}
	if strings.ContainsAny(got, "\n‮") {
		t.Errorf("detail %q keeps a control or format character", got)
	}
	long := publicDetail(strings.Repeat("é", 1500))
	if len(long) > platform.AgentCommandAckDetailMax || !strings.HasSuffix(long, "é") {
		t.Errorf("long detail: %d bytes, cut mid-rune = %v", len(long), !strings.HasSuffix(long, "é"))
	}
}

func TestHandler_RedactsARefusalReason(t *testing.T) {
	token := "github_pat_" + strings.Repeat("b", 30)
	q := &fakeQueue{err: Refuse("human-only-label: acme/web#637 carries %q", token)}
	a := &fakeAcks{}
	handle(newHandler(q, a, served(acmeWeb)), "agent-1", command("cmd-1", validPayload))

	if got := a.all(); len(got) != 1 || strings.Contains(got[0].detail, token) {
		t.Errorf("acks = %+v, want one with the token redacted", got)
	}
}

// Only answered claims are forgotten, oldest first.
func TestHandler_ForgetsOnlyAnsweredClaims(t *testing.T) {
	h := New(context.Background(), Config{})
	h.claims["in-flight"] = &claim{}
	h.order = append(h.order, "in-flight")
	for i := 0; i < maxClaims+5; i++ {
		id := "done-" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + time.Duration(i).String()
		h.claims[id] = &claim{done: true}
		h.order = append(h.order, id)
	}
	h.mu.Lock()
	h.forgetLocked()
	h.mu.Unlock()
	if len(h.order) > maxClaims {
		t.Errorf("kept %d claims, want at most %d", len(h.order), maxClaims)
	}
	if _, ok := h.claims["in-flight"]; !ok {
		t.Error("forgot a claim still being handled")
	}
}
