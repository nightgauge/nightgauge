package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// sleepingAdapter spawns a stage that outlives any short stage timeout.
type sleepingAdapter struct{}

func (sleepingAdapter) Name() string { return "sleeping-fake" }
func (sleepingAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return "sh", []string{"-c", "sleep 30"}, nil
}
func (sleepingAdapter) UsesStdin() bool { return false }
func (sleepingAdapter) Agentic() bool   { return true }

// TestRunStage_StageTimeoutIsClassifiedAsTimeout is #2171 item 1: a stage
// killed by its own stage timeout ended with a silent stderr and was recorded
// as `exit -1: <nil>`, a subagent_crash. It must carry a notice naming the
// timeout and the elapsed time, and that notice must classify as stall_kill.
func TestRunStage_StageTimeoutIsClassifiedAsTimeout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", "nightgauge-issue-2171"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root, sleepingAdapter{})
	result, err := m.RunStage(context.Background(), StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 2171,
		Stage:       "issue-pickup",
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunStage err = %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a failure for a stage stopped at its timeout")
	}
	if !strings.Contains(result.Stderr, "[stage-timeout]") || !strings.Contains(result.Stderr, "stage timeout of 2s") ||
		!strings.Contains(result.Stderr, "elapsed") {
		t.Fatalf("stderr = %q, want the stage-timeout notice naming the timeout and elapsed time", result.Stderr)
	}
	if kind := terminalkind.Classify("exit -1: " + strings.TrimSpace(result.Stderr)); kind != "stall_kill" {
		t.Fatalf("classified %q, want stall_kill", kind)
	}
}

func TestStageTimeoutNotice_OnlyTheStageDeadline(t *testing.T) {
	base := stageTimeoutEvidence{execErr: context.DeadlineExceeded, timeout: 30 * time.Minute, elapsed: 30*time.Minute + 400*time.Millisecond}
	if got := stageTimeoutNotice(base); got != "[stage-timeout] stage stopped at its stage timeout of 30m0s after 30m0s elapsed" {
		t.Fatalf("notice = %q", got)
	}
	for name, e := range map[string]stageTimeoutEvidence{
		"no deadline":      {execErr: nil},
		"cancelled":        {execErr: context.Canceled},
		"parent cancelled": {execErr: context.DeadlineExceeded, parentErr: context.Canceled},
		"operator stop":    {execErr: context.DeadlineExceeded, stopped: true},
		"budget marked":    {execErr: context.DeadlineExceeded, otherMarker: true},
		"wrapped other":    {execErr: errors.New("boom")},
	} {
		if got := stageTimeoutNotice(e); got != "" {
			t.Errorf("%s: notice = %q, want none", name, got)
		}
	}
}
