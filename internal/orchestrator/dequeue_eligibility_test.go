package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/trust"
)

// The queue is where every route lands — IPC queue.add, the extension's
// autonomous.dispatch handler, cloud queue_add, Action Center retries, epic
// sub-issues, `nightgauge queue add` — and DequeueIndependentFor is where a
// slot is claimed. An issue no writer vouched for is held there, whatever
// route queued it (#2130).
func TestDequeueIndependent_HoldsAnIssueNoWriterVouchedFor(t *testing.T) {
	t.Parallel()

	s := newDequeueExcludeFixture(t, nil)
	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead, "maintainer": trust.LevelWrite})
	s.trustGate = gate
	forge := &fakeTrustForge{levels: map[string]trust.Level{"stranger": trust.LevelRead, "maintainer": trust.LevelWrite},
		signals: map[string]trust.Signals{
			"org/repo#501": {Author: "stranger"},
			"org/repo#502": {Author: "maintainer"},
		}}
	s.trustGate = trust.NewGate(forge, nil)

	s.QueueAddItem(
		QueueItem{Repo: "Org/repo", IssueNumber: 501, Title: "outsider's issue"},
		QueueItem{Repo: "Org/repo", IssueNumber: 502, Title: "maintainer's issue"},
	)
	got := s.DequeueIndependent(t.Context(), 5, nil)
	if len(got) != 1 || got[0].IssueNumber != 502 {
		t.Fatalf("dequeued %+v, want exactly #502", got)
	}
	held := s.queueItemUnlocked("Org/repo", 501)
	if held == nil || held.Status != "paused" || held.PausedReason == nil || held.PausedReason.Kind != "not_vouched_for" {
		t.Fatalf("#501 = %+v, want it held with reason not_vouched_for", held)
	}
}

// A bare repository name in the queue is read in the scheduler's owner.
func TestDequeueIndependent_QualifiesABareRepoWithTheSchedulerOwner(t *testing.T) {
	t.Parallel()

	s := newDequeueExcludeFixture(t, nil)
	s.owner = "Org"
	forge := &fakeTrustForge{levels: map[string]trust.Level{"maintainer": trust.LevelWrite},
		signals: map[string]trust.Signals{"org/repo#503": {Author: "maintainer"}}}
	s.trustGate = trust.NewGate(forge, nil)
	s.QueueAddItem(QueueItem{Repo: "repo", IssueNumber: 503, Title: "bare repo"})
	if got := s.DequeueIndependent(t.Context(), 5, nil); len(got) != 1 {
		t.Fatalf("dequeued %+v, want #503", got)
	}
}

// The autonomous loop refuses to dispatch what the gate refuses, before any
// dispatcher or the extension's onDispatch callback sees it.
func TestEnqueueItem_RefusesAnIssueNoWriterVouchedFor(t *testing.T) {
	t.Parallel()

	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead})
	as := &AutonomousScheduler{trustGateOverride: gate, state: &AutonomousState{Status: "running"}}
	dispatched := false
	as.onDispatch = func(string, string, int, string, int) { dispatched = true }
	as.enqueueItem(t.Context(), CandidateItem{Repo: "o/r", Number: 9, Title: "x", AuthorLogin: "stranger"})
	if dispatched {
		t.Fatal("an issue no writer vouched for reached onDispatch")
	}
	if len(as.state.Running) != 0 {
		t.Fatalf("running = %+v, want empty", as.state.Running)
	}
}

// Applying pipeline:approved is the whole approval: a held item is released
// on the next dequeue once a writer vouches for it, and dispatched on the one
// after.
func TestDequeueIndependent_ReleasesAHeldItemOnceVouchedFor(t *testing.T) {
	t.Parallel()

	s := newDequeueExcludeFixture(t, nil)
	forge := &fakeTrustForge{levels: map[string]trust.Level{"stranger": trust.LevelRead, "maintainer": trust.LevelWrite},
		signals: map[string]trust.Signals{"org/repo#504": {Author: "stranger"}}}
	s.trustGate = trust.NewGate(forge, nil).WithAppOwners("Org").
		WithStageIdentities(func(context.Context) ([]string, error) { return nil, nil })
	s.QueueAddItem(QueueItem{Repo: "Org/repo", IssueNumber: 504, Title: "outsider's issue"})
	if got := s.DequeueIndependent(t.Context(), 5, nil); len(got) != 0 {
		t.Fatalf("dequeued %+v before approval", got)
	}

	forge.signals["org/repo#504"] = trust.Signals{Author: "stranger", Labels: []string{trust.ApprovedLabel},
		Events: []trust.Event{{Kind: trust.EventLabeled, Label: trust.ApprovedLabel, Actor: "maintainer", At: time.Now()}}}
	s.trustGate.ResetCycle()
	_ = s.DequeueIndependent(t.Context(), 5, nil) // releases
	got := s.DequeueIndependent(t.Context(), 5, nil)
	if len(got) != 1 || got[0].IssueNumber != 504 {
		t.Fatalf("dequeued %+v after approval, want #504", got)
	}
}

// Items queued while a dequeue's gates run used to be skipped as "the next
// pass decides it", and no next pass was scheduled (#3553). The same call now
// decides and takes them.
func TestDequeueIndependent_TakesItemsQueuedAfterTheGateRan(t *testing.T) {
	t.Parallel()

	s := newDequeueExcludeFixture(t, nil)
	added := false
	deps := twoRepoDeps(map[int]string{1: "Fix internal/a.go", 2: "Fix internal/a.go", 3: "Fix internal/a.go"})
	body := deps.body
	deps.body = func(ctx context.Context, o, r string, n int) (string, error) {
		if !added {
			added = true
			s.QueueAddItem(
				QueueItem{Repo: "o/r", IssueNumber: 2, Title: "late"},
				QueueItem{Repo: "o/r", IssueNumber: 3, Title: "later"},
			)
		}
		return body(ctx, o, r, n)
	}
	s.affinityDeps = deps
	s.QueueAddItem(QueueItem{Repo: "o/r", IssueNumber: 1, Title: "first"})

	got := s.DequeueIndependent(t.Context(), 5, nil)
	if len(got) != 3 {
		t.Fatalf("dequeued %+v, want #1, #2 and #3 in one call", got)
	}
}

// The second round counts what the first took: it stays within maxSlots and
// the per-repo cap (#3553).
func TestDequeueIndependent_SecondRoundKeepsTheCaps(t *testing.T) {
	t.Parallel()

	late := func(s *Scheduler) {
		added := false
		deps := twoRepoDeps(map[int]string{1: "Fix internal/a.go", 2: "Fix internal/a.go", 3: "Fix internal/a.go"})
		body := deps.body
		deps.body = func(ctx context.Context, o, r string, n int) (string, error) {
			if !added {
				added = true
				s.QueueAddItem(
					QueueItem{Repo: "o/r", IssueNumber: 2, Title: "late"},
					QueueItem{Repo: "o/r", IssueNumber: 3, Title: "later"},
				)
			}
			return body(ctx, o, r, n)
		}
		s.affinityDeps = deps
		s.QueueAddItem(QueueItem{Repo: "o/r", IssueNumber: 1, Title: "first"})
	}

	t.Run("maxSlots", func(t *testing.T) {
		t.Parallel()
		s := newDequeueExcludeFixture(t, nil)
		late(s)
		if got := s.DequeueIndependent(t.Context(), 2, nil); len(got) != 2 {
			t.Fatalf("dequeued %+v, want 2 (maxSlots)", got)
		}
	})
	t.Run("per-repo cap", func(t *testing.T) {
		t.Parallel()
		s := newDequeueExcludeFixture(t, nil)
		s.maxPerRepo = 1
		late(s)
		if got := s.DequeueIndependent(t.Context(), 5, nil); len(got) != 1 || got[0].IssueNumber != 1 {
			t.Fatalf("dequeued %+v, want only #1 (per-repo cap 1)", got)
		}
	})
}

// Regression pin (#3553): one refused item in a batch pauses only itself; the
// rest of the batch is taken in the same pass.
func TestDequeueIndependent_AffinityRefusalTakesTheRestOfTheBatch(t *testing.T) {
	t.Parallel()

	s := newDequeueExcludeFixture(t, nil)
	s.affinityDeps = twoRepoDeps(map[int]string{
		1: "Fix src/ext/one.ts and src/ext/two.ts.\n## Acceptance criteria\n- [ ] lib/three.ts works",
		2: "Fix internal/a.go",
		3: "Fix internal/a.go",
	})
	s.QueueAddItem(
		QueueItem{Repo: "o/r", IssueNumber: 1, Title: "misfiled"},
		QueueItem{Repo: "o/r", IssueNumber: 2, Title: "own"},
		QueueItem{Repo: "o/r", IssueNumber: 3, Title: "own too"},
	)
	got := s.DequeueIndependent(t.Context(), 5, nil)
	if len(got) != 2 || got[0].IssueNumber != 2 || got[1].IssueNumber != 3 {
		t.Fatalf("dequeued %+v, want #2 and #3", got)
	}
	held := s.queueItemUnlocked("o/r", 1)
	if held == nil || held.Status != "paused" || held.PausedReason == nil || held.PausedReason.Kind != pausedRepoAffinity {
		t.Fatalf("#1 = %+v, want it paused for repo affinity", held)
	}
}
