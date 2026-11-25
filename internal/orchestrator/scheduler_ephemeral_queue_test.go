package orchestrator

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// #3605: a scheduler built with EphemeralQueue neither loads, writes nor
// crash-recovers the checkout's queue file, so a second scheduler on the same
// checkout cannot read or overwrite the queue the checkout's own one owns.

// seedQueueAndCrashedRun writes a queue file holding one pending item and a
// current-run sidecar naming a dead pid, which construction would recover as a
// crash. It returns the queue file's path and bytes.
func seedQueueAndCrashedRun(t *testing.T, root string) (string, []byte) {
	t.Helper()
	data, err := json.MarshalIndent(QueueState{
		SchemaVersion: queueSchemaVersion,
		Status:        "idle",
		Items:         []QueueItem{{Repo: "acme/platform", IssueNumber: 900, Status: "pending", Position: 1}},
		UpdatedAt:     time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layout.WriteCheckoutFile(root, layout.CheckoutQueueState, bytes.NewReader(data)); err != nil {
		t.Fatalf("write queue file: %v", err)
	}
	startedAt := time.Now().UTC().Add(-30 * time.Second)
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{
		RunID: testRunID(), IssueNumber: 899, Repo: "acme/platform",
		StartedAt: startedAt, Stage: "feature-dev", StageStart: startedAt, PID: 0,
	}); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	p := layouttest.CheckoutPath(t, root, layout.CheckoutQueueState)
	onDisk, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, onDisk
}

func newQueueTestScheduler(t *testing.T, root string, ephemeral bool) *Scheduler {
	t.Helper()
	srv := emptyBoardServer(t)
	t.Cleanup(srv.Close)
	return NewScheduler(gh.NewClientWithURL("test-token", srv.URL), SchedulerConfig{
		Owner: "acme", ProjectNumber: 1, WorkspaceRoot: root, EphemeralQueue: ephemeral,
	})
}

func TestEphemeralQueue_NeitherLoadsWritesNorRecovers(t *testing.T) {
	t.Parallel()
	root := layouttest.Repo(t)
	queuePath, before := seedQueueAndCrashedRun(t, root)

	s := newQueueTestScheduler(t, root, true)
	if got := s.QueueList(); len(got) != 0 {
		t.Fatalf("ephemeral scheduler loaded %d item(s) from the queue file", len(got))
	}
	if _, err := os.Stat(layouttest.CheckoutPath(t, root, layout.CheckoutCurrentRun)); err != nil {
		t.Errorf("crash recovery ran: the dead run's sidecar is gone (%v)", err)
	}
	if hasDailyJSONL(t, root) {
		t.Error("crash recovery ran: a crash RunRecord was written")
	}

	s.QueueAddItem(QueueItem{Repo: "acme/platform", IssueNumber: 901})
	s.trustGate = allWritersGate()
	if _, err := s.RunQueue(t.Context()); err != nil {
		t.Fatalf("RunQueue: %v", err)
	}
	after, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("ephemeral scheduler rewrote the queue file:\nbefore %s\nafter  %s", before, after)
	}
}

func TestEphemeralQueue_FalseLoadsWritesAndRecovers(t *testing.T) {
	t.Parallel()
	root := layouttest.Repo(t)
	queuePath, before := seedQueueAndCrashedRun(t, root)

	s := newQueueTestScheduler(t, root, false)
	if got := s.QueueList(); len(got) != 1 || got[0].IssueNumber != 900 {
		t.Fatalf("persisted scheduler loaded %+v, want #900", got)
	}
	if !hasDailyJSONL(t, root) {
		t.Error("crash recovery did not run for the dead run's sidecar")
	}
	s.QueueAddItem(QueueItem{Repo: "acme/platform", IssueNumber: 901})
	after, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Error("persisted scheduler did not write the queue file")
	}
}
