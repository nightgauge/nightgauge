package ipc

// #1539 — `attention resolve` and `attention ack` never returned. The client
// held an established socket, the daemon logged nothing, and there was no
// working way to resolve a card from the CLI or the sidebar.
//
// These tests assert the property that was missing: a mutating attention method
// ALWAYS returns, with an error a human can act on, rather than never.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

func raiseServerCard(t *testing.T, s *Server, key string) string {
	t.Helper()
	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := s.attentionStore().Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: key,
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          "deadline regression",
		Body:           "why",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme"},
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	return id
}

// withShortMutationTimeout shrinks the production ceiling so the test asserts
// the mechanism in milliseconds instead of waiting out the real one.
func withShortMutationTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := attentionMutationTimeout
	attentionMutationTimeout = d
	t.Cleanup(func() { attentionMutationTimeout = prev })
}

// TestMutatingAttentionMethodsCannotHangForever drives the real method table
// against a store whose lock is held by something that never gives it back —
// the shape of the incident, whatever wedged the holder.
func TestMutatingAttentionMethodsCannotHangForever(t *testing.T) {
	s := newAttentionTestServer(t)
	id := raiseServerCard(t, s, "deadline:1")
	withShortMutationTimeout(t, 200*time.Millisecond)

	// A card the store is mid-write on: the lock is taken by a "verb" that
	// never returns, standing in for the wedged write that caused #1539.
	wedge := make(chan struct{})
	held := make(chan struct{})
	finished := make(chan struct{})
	other := raiseServerCard(t, s, "deadline:2")
	go func() {
		defer close(finished)
		_, _ = s.attentionStore().Resolve(context.Background(), other, "go", "octocat", "", "",
			verbExecutorFunc(func(_ context.Context, _ *attention.DecisionRequest, _ attention.Option) error {
				close(held)
				<-wedge
				return nil
			}))
	}()
	<-held
	// Released and JOINED through Cleanup, not a bare defer: the wedged resolve
	// writes to the workspace on its way out, and t.TempDir's own removal is a
	// cleanup registered earlier — so it runs after this one and finds the
	// directory quiet.
	t.Cleanup(func() {
		close(wedge)
		<-finished
	})

	resolveRaw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: "go", Actor: "octocat"})
	ackRaw, _ := json.Marshal(AttentionAcknowledgeParams{ID: id, Actor: "octocat"})
	muteRaw, _ := json.Marshal(AttentionMuteParams{ID: id, Actor: "octocat"})
	unmuteRaw, _ := json.Marshal(AttentionUnmuteParams{ID: id, Actor: "octocat"})

	cases := []struct {
		method string
		raw    json.RawMessage
	}{
		{"attention.resolve", resolveRaw},
		{"attention.acknowledge", ackRaw},
		{"attention.mute", muteRaw},
		{"attention.unmute", unmuteRaw},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			handler, ok := s.methods[tc.method]
			if !ok {
				t.Fatalf("%s is not registered", tc.method)
			}
			type outcome struct {
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				_, err := handler(context.Background(), tc.raw)
				done <- outcome{err}
			}()

			select {
			case got := <-done:
				if got.err == nil {
					t.Fatalf("%s succeeded against a wedged store", tc.method)
				}
				// The caller must be told what happened, not handed silence.
				if !strings.Contains(got.err.Error(), "busy") && !strings.Contains(got.err.Error(), "deadline") {
					t.Fatalf("%s error = %v, want it to name the busy store", tc.method, got.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s never returned — this is the #1539 hang", tc.method)
			}
		})
	}
}

// TestAttentionResolveRefusesATooShortActorWithTheField: the daemon names the
// offending field rather than collapsing into "could not resolve request",
// because the actor came from the caller and is theirs to fix.
func TestAttentionResolveRefusesATooShortActorWithTheField(t *testing.T) {
	s := newAttentionTestServer(t)
	id := raiseServerCard(t, s, "actor:1")

	raw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: "go", Actor: "po"})
	_, err := s.handleAttentionResolve(context.Background(), raw)
	if err == nil {
		t.Fatal("attention.resolve accepted a 2-character actor")
	}
	if !strings.Contains(err.Error(), "lifecycle.*.actor") {
		t.Errorf("error = %v, want the field the mirror names", err)
	}

	// Still open: a refused resolve mutates nothing.
	got, _, _ := s.attentionStore().Get(id)
	if got.Lifecycle.State != attention.StateOpen {
		t.Errorf("state = %q, want open", got.Lifecycle.State)
	}
}

// TestAttentionMutationTimeoutExceedsTheStoresWorstHold keeps the two layers'
// numbers in a checked relationship. If the store's own bounds ever grow past
// this ceiling, a queued-but-healthy resolve would start failing and the
// failure would look like the bug this ceiling was added to fix.
func TestAttentionMutationTimeoutExceedsTheStoresWorstHold(t *testing.T) {
	if attentionMutationTimeout <= attention.WorstCaseHold() {
		t.Fatalf("attentionMutationTimeout (%s) must exceed attention.WorstCaseHold() (%s)",
			attentionMutationTimeout, attention.WorstCaseHold())
	}
}

// verbExecutorFunc adapts a func to attention.VerbExecutor.
type verbExecutorFunc func(ctx context.Context, req *attention.DecisionRequest, opt attention.Option) error

func (f verbExecutorFunc) ExecuteVerb(ctx context.Context, req *attention.DecisionRequest, opt attention.Option) error {
	return f(ctx, req, opt)
}
