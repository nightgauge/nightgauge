package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// backgroundingAdapter spawns a stage that starts a grandchild in its own
// process group and records the grandchild's pid, the shape of an adapter CLI
// with a server of its own.
type backgroundingAdapter struct{ pidFile string }

func (backgroundingAdapter) Name() string { return "backgrounding-fake" }
func (a backgroundingAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "sh", []string{"-c", "sleep 60 & echo $! > " + a.pidFile + "; wait"}, nil
}
func (backgroundingAdapter) UsesStdin() bool { return false }
func (backgroundingAdapter) Agentic() bool   { return true }

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestSIGINTStopsTheStageProcessGroup is #2171 item 2: SIGINT to
// `nightgauge run` must leave no adapter child behind. The signal is sent to
// this test process, which withStageShutdown has taken over, exactly as a
// terminal's Ctrl-C reaches `nightgauge run`.
func TestSIGINTStopsTheStageProcessGroup(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-2171"), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "grandchild.pid")
	m := execution.NewManager(root, backgroundingAdapter{pidFile: pidFile})

	ctx, shutdown := withStageShutdown(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.RunStage(ctx, execution.StageOptions{
			Repo: "nightgauge/nightgauge", IssueNumber: 2171, Stage: "issue-pickup", Timeout: time.Minute,
		})
	}()

	var stagePID, grandchild int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if running := m.ListRunning(); len(running) == 1 {
			stagePID = running[0].PID
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			grandchild, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if stagePID > 0 && grandchild > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stagePID == 0 || grandchild == 0 {
		t.Fatal("stage never started its grandchild")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGINT did not cancel the run context")
	}
	shutdown()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunStage did not return after the signal")
	}
	if n := execution.LiveStageCount(); n != 0 {
		t.Fatalf("%d stages still live after shutdown", n)
	}
	gone := time.Now().Add(5 * time.Second)
	for processAlive(grandchild) && time.Now().Before(gone) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(stagePID) || processAlive(grandchild) {
		t.Fatalf("stage %d or its grandchild %d survived the signal", stagePID, grandchild)
	}
}
