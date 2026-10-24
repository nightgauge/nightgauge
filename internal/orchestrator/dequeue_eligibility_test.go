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
	gate, _ := levelsGate(map[string]trust.Level{"stranger": trust.LevelRead})
	as := &AutonomousScheduler{trustGateOverride: gate, state: &AutonomousState{Status: "running"}}
	dispatched := false
	as.onDispatch = func(string, string, int, string) { dispatched = true }
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
