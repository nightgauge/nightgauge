package ipc

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// TestResumeVerbsDoNotDeadlockTheStore is the #2750 regression pin. Each case
// resolves a real card through Store.Resolve and the server's own executor, so
// the verb runs on the goroutine holding the store lock, as it does in the
// daemon. On the unfixed code ResumeRepo's reconcile, or Resume's safety-rail
// retraction, re-enters that lock and Resolve never returns.
func TestResumeVerbsDoNotDeadlockTheStore(t *testing.T) {
	const repo, issue = "octocat/acme", 2750
	key := fmt.Sprintf("%s#%d", repo, issue)
	cases := []struct {
		name string
		opt  attention.Option
	}{
		{"retry then resumeRepo", attention.Option{ID: "retry", Label: "Retry",
			Verb: attention.VerbAutonomousClearIssueFailures,
			Args: map[string]any{"key": key, "then": thenAutonomousResumeRepo}}},
		{"retry then resume", attention.Option{ID: "retry", Label: "Retry",
			Verb: attention.VerbAutonomousClearIssueFailures,
			Args: map[string]any{"key": key, "then": thenAutonomousResume}}},
		{"escalate then resumeRepo", attention.Option{ID: "escalate", Label: "Escalate",
			Verb: attention.VerbRunRetryWithEscalation,
			Args: map[string]any{"key": key, "issueNumber": issue, "then": thenAutonomousResumeRepo}}},
		{"escalate then resume", attention.Option{ID: "escalate", Label: "Escalate",
			Verb: attention.VerbRunRetryWithEscalation,
			Args: map[string]any{"key": key, "issueNumber": issue, "then": thenAutonomousResume}}},
		{"resume verb", attention.Option{ID: "resume", Label: "Resume", Verb: attention.VerbAutonomousResume}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := haltedState()
			state.PausedRepos = map[string]*orchestrator.RepoPauseRecord{repo: {}}
			server, as, ctx := newHaltedServer(t, state)
			store := as.Attention()
			if store == nil {
				t.Fatal("autonomous scheduler has no attention store")
			}

			raise := func(req attention.DecisionRequest) string {
				t.Helper()
				id, err := attention.NewID()
				if err != nil {
					t.Fatalf("NewID: %v", err)
				}
				req.ID = id
				req.Severity = attention.SeverityBlockingFleet
				req.Kind = attention.KindUnblock
				req.Body = "test"
				req.Standing = true
				req.Fingerprint = "halted"
				req.ExpiresAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
				if _, _, err := store.Raise(req); err != nil {
					t.Fatalf("Raise %s: %v", req.IdempotencyKey, err)
				}
				return id
			}
			cardID := raise(attention.DecisionRequest{
				IdempotencyKey: "terminal-failure:" + key,
				Title:          "halted",
				Producer:       "terminal-failure",
				Context:        attention.Context{Repo: repo, Issue: issue},
				Options:        []attention.Option{tc.opt, {ID: "park", Label: "Park", Verb: attention.VerbNoop}},
				DefaultAction:  attention.ExpireNoop,
			})
			railID := raise(attention.DecisionRequest{
				IdempotencyKey: "safety-rail-trip:fleet",
				Title:          "fleet halted",
				Producer:       "safety-rail-trip",
				Options:        []attention.Option{{ID: "ack", Label: "Ack", Verb: attention.VerbNoop}},
				DefaultAction:  "ack",
			})

			resolved := make(chan error, 1)
			go func() {
				_, err := store.Resolve(ctx, cardID, tc.opt.ID, "tester", "", "", server)
				resolved <- err
			}()
			select {
			case err := <-resolved:
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
			case <-time.After(10 * time.Second):
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				// Exit rather than Fatalf: the wedged goroutine still holds the
				// locks the harness cleanup needs (#2528).
				fmt.Fprintf(os.Stderr, "FAIL: %s: Resolve did not return within 10s — a resume path "+
					"re-entered the store lock (#2750)\n%s", tc.name, buf[:n])
				os.Exit(1)
			}

			if as.RepoPaused(repo) {
				t.Error("the repository is still halted after a successful resolve")
			}

			// The scheduler mutex and the store are both free again.
			free := make(chan struct{})
			go func() {
				as.TriggerRescan()
				_, _ = store.IncrementStreak("probe")
				close(free)
			}()
			select {
			case <-free:
			case <-time.After(5 * time.Second):
				t.Fatal("as.mu or the store lock is still held after Resolve returned")
			}

			waitBackground(t, as)
			req, ok, err := store.Get(cardID)
			if err != nil || !ok {
				t.Fatalf("Get card: ok=%v err=%v", ok, err)
			}
			if req.Lifecycle.State != attention.StateResolved {
				t.Errorf("card state = %q, want %q (AC4: Retry must not read as a retraction)",
					req.Lifecycle.State, attention.StateResolved)
			}
			if req.Lifecycle.Resolved == nil || req.Lifecycle.Resolved.OptionID != tc.opt.ID {
				t.Errorf("resolved record = %+v, want option %q", req.Lifecycle.Resolved, tc.opt.ID)
			}

			// The fleet resume retracts the safety-rail card off the lock. A
			// repo-scoped resume leaves it alone.
			if tc.name == "retry then resumeRepo" || tc.name == "escalate then resumeRepo" {
				return
			}
			rail, ok, err := store.Get(railID)
			if err != nil || !ok {
				t.Fatalf("Get rail card: ok=%v err=%v", ok, err)
			}
			if rail.Lifecycle.State != attention.StateAutoResolved {
				t.Errorf("rail card state = %q, want %q after a fleet resume",
					rail.Lifecycle.State, attention.StateAutoResolved)
			}
		})
	}
}

// waitBackground waits for the dispatched attention work to land.
func waitBackground(t *testing.T, as *orchestrator.AutonomousScheduler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for as.BackgroundInFlight() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d background goroutine(s) still running", as.BackgroundInFlight())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
