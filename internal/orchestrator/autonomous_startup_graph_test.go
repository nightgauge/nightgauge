package orchestrator

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// countingBoard is a forge.BoardService that counts full board reads and
// answers with an empty board, so no issue-body query follows the read.
type countingBoard struct {
	forge.BoardService
	reads atomic.Int32
}

func (b *countingBoard) ListOpenItems(context.Context) ([]forgetypes.BoardItem, int, error) {
	b.reads.Add(1)
	return nil, 0, nil
}

func newStartupGraphScheduler(t *testing.T) (*AutonomousScheduler, *countingBoard) {
	t.Helper()
	repos := []depgraph.RepoConfig{
		{Owner: "O", Name: "a", Project: 7},
		{Owner: "O", Name: "b", Project: 7},
		{Owner: "O", Name: "c", Project: 7},
	}
	as := NewAutonomousScheduler(nil, nil, repos, nil, DefaultAutonomousConfig(), t.TempDir())
	board := &countingBoard{}
	// The default provider is what production uses; only the board behind
	// it is substituted, so the build path under test is the real one.
	as.boardProvider = func(depgraph.RepoConfig) forge.BoardService { return board }
	return as, board
}

// TestRecoverOrphanedRunning_StoppedSchedulerReadsNoBoard pins the daemon
// start path: `nightgauge serve` calls RecoverOrphanedRunning at every
// extension-host reload, before anyone has started autonomous mode. That
// must not build the dependency graph — one full board read per board, and
// with a ~1,900-item shared board that was the single most expensive thing
// the daemon did at startup, while status was "stopped".
func TestRecoverOrphanedRunning_StoppedSchedulerReadsNoBoard(t *testing.T) {
	as, board := newStartupGraphScheduler(t)
	if as.IsRunning() {
		t.Fatal("precondition: a freshly constructed scheduler is stopped")
	}

	out := captureLog(t, func() { as.RecoverOrphanedRunning(context.Background()) })

	if got := board.reads.Load(); got != 0 {
		t.Errorf("stopped scheduler issued %d board reads at daemon start, want 0", got)
	}
	if !strings.Contains(out, "deferring startup Backlog->Ready promotion") {
		t.Errorf("the deferral is silent — nothing names why promotion did not run; log: %q", out)
	}
}

// TestRecoverOrphanedRunning_RunningSchedulerBuildsOnce is the control: once
// the loop is up the promotion scan still runs, and three repos on one board
// cost ONE read, not three.
func TestRecoverOrphanedRunning_RunningSchedulerBuildsOnce(t *testing.T) {
	as, board := newStartupGraphScheduler(t)
	as.mu.Lock()
	as.running = true
	as.state.Status = "running"
	as.mu.Unlock()

	as.RecoverOrphanedRunning(context.Background())

	if got := board.reads.Load(); got != 1 {
		t.Errorf("running scheduler issued %d board reads for 3 repos on one board, want 1", got)
	}
}
