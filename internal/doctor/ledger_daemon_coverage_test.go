package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// heldLease takes this workspace's lease and names pid as its holder, so the
// arm sees a live daemon the way it would in production.
func heldLease(t *testing.T, root string, pid int, now time.Time) {
	t.Helper()
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID: pid, StartedAt: now.Add(-time.Hour), LastHeartbeatAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
}

// writeLedgerRecord appends one record to the workspace's ledger.
func writeLedgerRecord(t *testing.T, root string, rec gh.APILedgerRecord) {
	t.Helper()
	path := gh.DefaultLedgerPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(&rec); err != nil {
		t.Fatalf("encode record: %v", err)
	}
}

// markPipelineActivity is the independent evidence the arm requires before it
// will call a missing record a finding.
func markPipelineActivity(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime-1-abc.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write runtime file: %v", err)
	}
}

func TestLedgerCoverageNoWorkspaceRoot(t *testing.T) {
	item, warning := checkLedgerDaemonCoverage("", time.Now())
	if !item.OK || warning != "" {
		t.Errorf("checkLedgerDaemonCoverage(\"\") = %+v / %q, want a clean skip", item, warning)
	}
}

// No daemon, nothing to cover.
func TestLedgerCoverageNoDaemonIsClean(t *testing.T) {
	isolateMachineState(t)
	item, warning := checkLedgerDaemonCoverage(t.TempDir(), time.Now())
	if !item.OK || warning != "" {
		t.Fatalf("a free lease produced %+v / %q, want a clean result", item, warning)
	}
}

// The happy path: a live daemon whose records are in this workspace's ledger.
func TestLedgerCoverageDaemonRecordsPresent(t *testing.T) {
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()
	heldLease(t, root, 4242, now)
	markPipelineActivity(t, root)
	writeLedgerRecord(t, root, gh.APILedgerRecord{
		TS:  now.Add(-time.Second).UTC().Format(time.RFC3339Nano),
		PID: 4242, Kind: "graphql", Status: 200,
	})

	item, warning := checkLedgerDaemonCoverage(root, now)
	if !item.OK || warning != "" {
		t.Fatalf("a covered daemon produced %+v / %q, want a clean result", item, warning)
	}
}

// The finding this arm exists for: a working daemon whose spend went nowhere.
func TestLedgerCoverageActiveDaemonWithNoRecordsIsAFinding(t *testing.T) {
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()
	heldLease(t, root, 4242, now)
	markPipelineActivity(t, root)

	item, warning := checkLedgerDaemonCoverage(root, now)
	if item.OK || warning == "" {
		t.Fatalf("an uncovered active daemon produced %+v / %q, want a finding", item, warning)
	}
	if !strings.Contains(warning, "4242") {
		t.Errorf("warning %q does not name the holding pid", warning)
	}
	if !strings.Contains(warning, gh.DefaultLedgerPath(root)) {
		t.Errorf("warning %q does not name the ledger path it looked at", warning)
	}
	if !strings.Contains(warning, "--workspace") {
		t.Errorf("warning %q does not tell the operator what to do", warning)
	}
}

// An idle daemon spends nothing, so no records is the CORRECT answer. Firing
// here would train operators to ignore the arm.
func TestLedgerCoverageIdleDaemonIsNotAFinding(t *testing.T) {
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()
	heldLease(t, root, 4242, now)

	item, warning := checkLedgerDaemonCoverage(root, now)
	if !item.OK || warning != "" {
		t.Fatalf("an idle daemon produced %+v / %q, want a clean result", item, warning)
	}
	if !strings.Contains(item.Detail, "idle") {
		t.Errorf("Detail = %q, want it to say why no finding was raised", item.Detail)
	}
}

// Records from a DIFFERENT process do not cover this daemon: the whole defect
// was one process writing somewhere else while another wrote here.
func TestLedgerCoverageOtherPidRecordsDoNotCount(t *testing.T) {
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()
	heldLease(t, root, 4242, now)
	markPipelineActivity(t, root)
	writeLedgerRecord(t, root, gh.APILedgerRecord{
		TS:  now.Add(-time.Second).UTC().Format(time.RFC3339Nano),
		PID: 9999, Kind: "graphql", Status: 200,
	})

	item, warning := checkLedgerDaemonCoverage(root, now)
	if item.OK || warning == "" {
		t.Fatalf("records from another pid were accepted as coverage: %+v / %q", item, warning)
	}
}

// A wedged holder belongs to serve_lease; reporting it twice buries the repair.
func TestLedgerCoverageStaleHolderDefersToServeLease(t *testing.T) {
	isolateMachineState(t)
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := t.TempDir()
	now := time.Now()
	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID:             4242,
		StartedAt:       now.Add(-2 * time.Hour),
		LastHeartbeatAt: now.Add(-10 * runstate.ServeLeaseStaleAfter),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	markPipelineActivity(t, root)

	item, warning := checkLedgerDaemonCoverage(root, now)
	if !item.OK || warning != "" {
		t.Fatalf("a wedged holder produced %+v / %q, want serve_lease to own it", item, warning)
	}
}
