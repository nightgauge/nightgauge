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
	"errors"
	"os"
	"path/filepath"
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

// --- #3353: the post-merge re-run runs outside the attention deadline --------

type slowRerunner struct {
	pr      int
	target  error
	delay   time.Duration
	runErr  error
	ran     chan struct{}
	release chan struct{}
}

func (r *slowRerunner) ResolveRerunTarget(context.Context, string, int) (int, error) {
	return r.pr, r.target
}

func (r *slowRerunner) RunPostMergeRerun(ctx context.Context, _ string, _, _ int) error {
	if r.ran != nil {
		close(r.ran)
	}
	if r.release != nil {
		<-r.release
	}
	select {
	case <-time.After(r.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.runErr
}

func newRerunServer(t *testing.T, runner attention.PostMergeRerunner) (*Server, string) {
	t.Helper()
	s := newAttentionTestServer(t)
	cfgDir := filepath.Join(s.workspaceRoot, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("project:\n  owner: o\n  number: 3\n  repo: r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.postMergeRerunOverride = runner
	id, err := attention.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.attentionStore().Raise(attention.DecisionRequest{
		ID: id, IdempotencyKey: "rerun:" + id, Kind: attention.KindChoose,
		Severity: attention.SeverityBlockingRun, Title: "held dependency", Body: "why",
		Producer: "test", Context: attention.Context{Repo: "o/r", Issue: 21},
		Options: []attention.Option{
			{ID: "rerun-post-merge", Label: "Run post-merge hook", Verb: attention.VerbIssueRerunPostMerge},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	return s, id
}

func resolveRerun(t *testing.T, s *Server, id string) (AttentionResolveResult, error) {
	t.Helper()
	raw, err := json.Marshal(AttentionResolveParams{ID: id, OptionID: "rerun-post-merge", Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.handleAttentionResolve(context.Background(), raw)
	if err != nil {
		return AttentionResolveResult{}, err
	}
	return out.(AttentionResolveResult), nil
}

// AC1/AC2: a re-run slower than the attention deadline is queued at once and
// resolves the card when the marker is written.
func TestRerunPostMergeOutlivesTheAttentionDeadline(t *testing.T) {
	withShortMutationTimeout(t, 200*time.Millisecond)
	runner := &slowRerunner{pr: 7, delay: 600 * time.Millisecond}
	s, id := newRerunServer(t, runner)
	start := time.Now()
	res, err := resolveRerun(t, s, id)
	if err != nil || !res.Ok || !res.Queued {
		t.Fatalf("resolve = %+v, %v; want Ok+Queued", res, err)
	}
	if time.Since(start) > 150*time.Millisecond {
		t.Fatalf("resolve waited %s for the hook", time.Since(start))
	}
	if got, _, _ := s.attentionStore().Get(id); got.Lifecycle.State != attention.StateOpen {
		t.Fatalf("card resolved before the marker existed: %q", got.Lifecycle.State)
	}
	s.postMergeRerunWG.Wait()
	got, _, _ := s.attentionStore().Get(id)
	if got.Lifecycle.State != attention.StateResolved || got.Lifecycle.Resolved.OptionID != "rerun-post-merge" {
		t.Fatalf("card after a green re-run = %q", got.Lifecycle.State)
	}
}

// A second click while a re-run is in flight joins it rather than starting another.
func TestRerunPostMergeDedupesInFlight(t *testing.T) {
	runner := &slowRerunner{pr: 7, ran: make(chan struct{}), release: make(chan struct{})}
	s, id := newRerunServer(t, runner)
	if res, err := resolveRerun(t, s, id); err != nil || !res.Queued {
		t.Fatalf("first resolve = %+v, %v", res, err)
	}
	<-runner.ran
	if res, err := resolveRerun(t, s, id); err != nil || !res.Queued {
		t.Fatalf("second resolve = %+v, %v", res, err)
	}
	close(runner.release) // a second RunPostMergeRerun would panic on close(ran)
	s.postMergeRerunWG.Wait()
}

// AC3: an unreadable main keeps the card open and records the cause on it.
func TestRerunPostMergeFailureNamesTheCause(t *testing.T) {
	runner := &slowRerunner{pr: 7, runErr: errors.New("post-merge re-run for o/r#21 (PR #7): main checks could not be read at abc: 502")}
	s, id := newRerunServer(t, runner)
	if _, err := resolveRerun(t, s, id); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s.postMergeRerunWG.Wait()
	got, _, _ := s.attentionStore().Get(id)
	if got.Lifecycle.State != attention.StateOpen || !strings.Contains(got.Context.Blocker, "main checks could not be read") {
		t.Fatalf("card = %q, blocker %q", got.Lifecycle.State, got.Context.Blocker)
	}
}

// AC3: a synchronous target failure names its cause, not "could not resolve request".
func TestRerunPostMergeTargetFailureIsNotCollapsed(t *testing.T) {
	runner := &slowRerunner{target: errors.New("post-merge re-run for o/r#21: no merged pull request closes it")}
	s, id := newRerunServer(t, runner)
	_, err := resolveRerun(t, s, id)
	if err == nil || !strings.Contains(err.Error(), "no merged pull request closes it") {
		t.Fatalf("err = %v, want the named cause", err)
	}
	if got, _, _ := s.attentionStore().Get(id); got.Lifecycle.State != attention.StateOpen {
		t.Fatalf("card = %q, want open", got.Lifecycle.State)
	}
}
