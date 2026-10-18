package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// Only the extension on the stdio transport receives relayed triggers, so
// only it can say it executes them (#1894). A socket client cannot.
func TestAgentAttachExecutor_OnlyTheStdioPeer(t *testing.T) {
	s := newAttentionTestServer(t)
	if s.ExecutorAttached() {
		t.Fatal("attached before anyone said so")
	}
	if _, err := s.handleAgentAttachExecutor(withSocketTransport(context.Background()), nil); err == nil {
		t.Fatal("a socket client attached as the executor")
	}
	if s.ExecutorAttached() {
		t.Fatal("a refused socket call attached the executor")
	}
	res, err := s.handleAgentAttachExecutor(context.Background(), nil)
	if err != nil || !res.(AgentAttachExecutorResult).Attached || !s.ExecutorAttached() {
		t.Fatalf("stdio attach: %v %v", res, err)
	}
}

// The socket transport marks its requests, which is what the refusal reads.
func TestHandleRequestOnWriter_MarksTheSocketTransport(t *testing.T) {
	s := newAttentionTestServer(t)
	var sawSocket bool
	s.methods["test.probe"] = func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
		sawSocket = onSocketTransport(ctx)
		return nil, nil
	}
	var buf strings.Builder
	s.handleRequestOnWriter(context.Background(), Request{ID: 1, Method: "test.probe"}, nopFlusher{&buf})
	if !sawSocket {
		t.Error("a socket request was not marked")
	}
}

type nopFlusher struct{ *strings.Builder }

func (nopFlusher) Flush() error { return nil }

func proposal() ConversationRunProposal {
	return ConversationRunProposal{Owner: "o", Repo: "r", IssueNumber: 12, PosterAccountID: "acct-bob",
		PosterLogin: "bob", ConversationID: "conv-1", TurnID: "turn-1"}
}

// A teammate's request becomes one card for the operator: queue it here or
// dismiss it, dismissed on expiry, naming the poster and nothing they wrote.
func TestRaiseConversationRunProposal_RaisesOneCard(t *testing.T) {
	s := newAttentionTestServer(t)
	id, err := s.RaiseConversationRunProposal(proposal())
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	card := onlyOpenRequest(t, s)
	if card.ID != id || card.Producer != ProducerConversationRunProposal || card.Context.Repo != "o/r" ||
		card.Context.Issue != 12 || card.DefaultAction != runProposalDismiss {
		t.Fatalf("card = %+v", card)
	}
	if start := card.FindOption(runProposalStart); start == nil || start.Verb != attention.VerbQueueAdd {
		t.Errorf("start option = %+v", start)
	}
	if dismiss := card.FindOption(runProposalDismiss); dismiss == nil || dismiss.Verb != attention.VerbNoop {
		t.Errorf("dismiss option = %+v", dismiss)
	}
	if !strings.Contains(card.Body, "@bob (account acct-bob)") || !strings.Contains(card.Body, "o/r#12") {
		t.Errorf("body = %q", card.Body)
	}
	// A repeated request refreshes the one open card.
	if again, err := s.RaiseConversationRunProposal(proposal()); err != nil || again != id {
		t.Errorf("repeat = %q, %v; want %q", again, err, id)
	}
	onlyOpenRequest(t, s)

	for _, bad := range []ConversationRunProposal{
		{Owner: "evil", Repo: "r", IssueNumber: 1, PosterAccountID: "a"},
		{Owner: "o", Repo: "r", IssueNumber: 0, PosterAccountID: "a"},
		{Owner: "o", Repo: "..", IssueNumber: 1, PosterAccountID: "a"},
		{Owner: "o", Repo: "r", IssueNumber: 1, PosterAccountID: "a b"},
		{Owner: "o", Repo: "r", IssueNumber: 1, PosterAccountID: "a", PosterLogin: "x\ny"},
	} {
		if _, err := s.RaiseConversationRunProposal(bad); err == nil {
			t.Errorf("%+v was raised", bad)
		}
	}
}

// A relayed resolution (the dashboard, a conversation, any teammate who may
// act on cards) never starts a proposed run: refused, the card left open.
// Dismissing it from anywhere is allowed, and the operator's own resolve on
// this machine reaches the verb.
func TestRelayedResolve_NeverStartsAProposedRun(t *testing.T) {
	s := newAttentionTestServer(t)
	id, err := s.RaiseConversationRunProposal(proposal())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRelayedResolve(context.Background(), id, runProposalStart, "dashboard:bob", ""); !errors.Is(err, errRelayedRunProposal) {
		t.Fatalf("relayed start: %v, want refused", err)
	}
	if card := onlyOpenRequest(t, s); card.Lifecycle.State != attention.StateOpen {
		t.Fatalf("the card left open: %+v", card.Lifecycle)
	}

	// The operator's own resolve, on this machine, is not held by the guard:
	// it reaches the verb, which fails here only because this test has no
	// scheduler to queue on.
	raw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: runProposalStart, Actor: "vscode:op"})
	if _, err := s.handleAttentionResolve(context.Background(), raw); err == nil || errors.Is(err, errRelayedRunProposal) {
		t.Errorf("local start: %v, want the verb reached", err)
	}

	out, err := s.ApplyRelayedResolve(context.Background(), id, runProposalDismiss, "dashboard:bob", "")
	if err != nil || !out.Applied {
		t.Errorf("relayed dismiss: %+v %v", out, err)
	}
}
