package orchestrator

import (
	"context"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Tests for sidelineHalt (#281): the architecture-approval and
// pr-merge-unmerged halt paths must not park an issue at "In review" unless
// an OPEN PR actually backs it. No project config is wired on the fixture
// scheduler, so moveIssueToInReview/moveIssueToInProgress bail out at the
// "no project config" log line before touching ghClient — that line is
// enough to distinguish which status was chosen without a real GitHub call.

// lockedLog is the log sink withCapturedLog installs. log.Printf from a
// background goroutine and String() from the test body reach the same buffer
// from different goroutines, so both sides take the lock — a bare
// strings.Builder here is a data race even once the goroutine is joined,
// because the join is what the tests are being taught to do, not what an
// unsynchronized writer can assume.
type lockedLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func withCapturedLog(t *testing.T) *lockedLog {
	t.Helper()
	buf := &lockedLog{}
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})
	return buf
}

// TestSidelineHalt_NoPR_MovesToInProgress covers the architecture-approval
// case: no OPEN PR exists (the gate fires before feature-dev ever runs), so
// the log must never claim "PR exists" and the chosen status must be "In
// progress".
func TestSidelineHalt_NoPR_MovesToInProgress(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "list") {
			return []byte("[]"), nil // repo has no open PRs at all
		}
		return []byte("[]"), nil
	})
	buf := withCapturedLog(t)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.sidelineHalt(as.backgroundContext(), "O/app", 900, "architecture approval required")
	as.drainBackground()

	got := buf.String()
	if strings.Contains(got, "PR exists") {
		t.Errorf("no-PR halt must not claim a PR exists, got log: %q", got)
	}
	if !strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("expected move-to-in-progress path, got log: %q", got)
	}
	if strings.Contains(got, "move-to-in-review:") {
		t.Errorf("no-PR halt must not take the In-review path, got log: %q", got)
	}
}

// TestSidelineHalt_PRExists_MovesToInReview is the regression guard for the
// real pr-merge-unmerged case: a PR genuinely exists, so the behavior is
// unchanged — "In review" with the existing truthful log.
func TestSidelineHalt_PRExists_MovesToInReview(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "list") {
			return []byte(`[{"number":42,"headRefName":"feat/900-x","mergeStateStatus":"BLOCKED"}]`), nil
		}
		return []byte("[]"), nil
	})
	buf := withCapturedLog(t)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.sidelineHalt(as.backgroundContext(), "O/app", 900, "pr-merge: PR was not merged")
	as.drainBackground()

	got := buf.String()
	if !strings.Contains(got, "move-to-in-review:") {
		t.Errorf("expected move-to-in-review path when a PR exists, got log: %q", got)
	}
	if strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("PR-backed halt must not take the In-progress path, got log: %q", got)
	}
}

// TestSidelineHalt_QueryFails_FailsToInProgress: a transient gh error must
// never be read as "PR confirmed" — the safer state (In progress) wins.
func TestSidelineHalt_QueryFails_FailsToInProgress(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	buf := withCapturedLog(t)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.sidelineHalt(as.backgroundContext(), "O/app", 900, "architecture approval required")
	as.drainBackground()

	got := buf.String()
	if !strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("query failure must fail toward In progress, got log: %q", got)
	}
}

// parkedCards returns the open pr-merge-parked cards for repo#issue.
func parkedCards(t *testing.T, as *AutonomousScheduler, repo string, issue int) []attention.DecisionRequest {
	t.Helper()
	var out []attention.DecisionRequest
	for _, r := range openRequests(t, as) {
		if r.IdempotencyKey == keyPrMergeParked(repo, issue) {
			out = append(out, r)
		}
	}
	return out
}

// TestOnPipelineComplete_PrMergeUnmerged_RaisesParkedCard pins #3339: a failed
// pr-merge parked in In review behind an open PR raises a card naming the PR
// and the refusal, while a halt with no PR (In progress) raises none.
func TestOnPipelineComplete_PrMergeUnmerged_RaisesParkedCard(t *testing.T) {
	const reason = `merge-queue-failed: merge-group code failure: job "test" step "Run go test" failed`
	for _, tc := range []struct {
		name   string
		prList string
		want   int
	}{
		{"open PR parks in In review with a card", `[{"number":42,"headRefName":"feat/900-x","mergeStateStatus":"BLOCKED"}]`, 1},
		{"no PR moves to In progress without a card", `[]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := heldTestScheduler(t)
			as.attention = attention.New(layouttest.Repo(t))
			stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "list") {
					return []byte(tc.prList), nil
				}
				return []byte("[]"), nil
			})
			addRunning(as, "O/app", 900, "x")
			as.onPipelineComplete("O/app", 900, false, false, TerminalKindPrMergeUnmerged, reason, false)
			as.drainBackground()

			cards := parkedCards(t, as, "O/app", 900)
			if len(cards) != tc.want {
				t.Fatalf("got %d parked cards, want %d", len(cards), tc.want)
			}
			if tc.want == 0 {
				return
			}
			c := cards[0]
			// onPipelineComplete holds no run id; the card carries none.
			if c.Context.RunID != "" || c.Context.TraceRef != nil {
				t.Errorf("card run = %q / %+v, want none", c.Context.RunID, c.Context.TraceRef)
			}
			if c.Context.PR != 42 || c.Context.Issue != 900 || c.Context.Blocker != reason {
				t.Errorf("card context = %+v", c.Context)
			}
			if !strings.Contains(c.Title, "PR #42") || !strings.Contains(c.Body, reason) {
				t.Errorf("card title/body = %q / %q", c.Title, c.Body)
			}
		})
	}
}
