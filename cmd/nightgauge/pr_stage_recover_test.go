package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator/recovery"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

func recoverWorkspace(t *testing.T) (string, string) {
	t.Helper()
	ws := layouttest.Repo(t)
	return ws, layouttest.MkPipelineDir(t, ws)
}

func TestRunPrStageRecover_Outcomes(t *testing.T) {
	signal := `{"signals":[{"signal_type":"OTHER"},{"signal_type":"CONFLICT_RESOLUTION_NEEDED","backtrack_target_stage":"feature-dev"}]}`
	cases := []struct {
		name     string
		res      recovery.RecoveryResult
		matched  bool
		feedback string
		want     string
	}{
		{"recovered", recovery.RecoveryResult{Recovered: true, FollowUp: recovery.FollowUpStageCanResume}, true, "", prRecoverRecovered},
		{"rewind", recovery.RecoveryResult{FollowUp: recovery.FollowUpStageCanResume, Evidence: []string{"conflicting_file=a.go", "conflicting_file=a.go"}}, true, signal, prRecoverRewind},
		{"rewind without a signal escalates", recovery.RecoveryResult{FollowUp: recovery.FollowUpStageCanResume}, true, "", prRecoverEscalated},
		{"escalated", recovery.RecoveryResult{FollowUp: recovery.FollowUpHumanTriageRequired, Evidence: []string{"conflicting_file=b.go"}}, true, "", prRecoverEscalated},
		{"unmatched", recovery.RecoveryResult{}, false, "", prRecoverUnmatched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, dir := recoverWorkspace(t)
			if tc.feedback != "" {
				if err := os.WriteFile(filepath.Join(dir, "feedback-5.json"), []byte(tc.feedback), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runPrStageRecover(context.Background(), ws, 5, 9, "reason",
				func(context.Context, recovery.StageFailure) (recovery.RecoveryResult, bool) {
					return tc.res, tc.matched
				})
			if err != nil {
				t.Fatal(err)
			}
			if out.Outcome != tc.want {
				t.Fatalf("outcome = %s (%+v)", out.Outcome, out)
			}
			if tc.want == prRecoverRewind {
				if len(out.ConflictingFiles) != 1 || out.ConflictingFiles[0] != "a.go" {
					t.Errorf("files = %v", out.ConflictingFiles)
				}
				var s map[string]string
				if err := json.Unmarshal(out.BacktrackSignal, &s); err != nil || s["backtrack_target_stage"] != "feature-dev" {
					t.Errorf("signal = %s (%v)", out.BacktrackSignal, err)
				}
			}
		})
	}
}

// unusedMergeRunner fails the test if recovery tries to land the merge.
type unusedMergeRunner struct{ t *testing.T }

func (r unusedMergeRunner) Run(context.Context, int, string, string) (pmstages.PRMergeResult, error) {
	r.t.Helper()
	r.t.Error("a real conflict must rewind without re-running the merge")
	return pmstages.PRMergeResult{}, nil
}

// TestRunPrStageRecover_RealRegistryRewindsARealConflict runs the verb over
// recovery.Default and real git, so the signal the capture writes is the one the
// verb reads back. The seam test above hands the verb a file it wrote itself.
func TestRunPrStageRecover_RealRegistryRewindsARealConflict(t *testing.T) {
	ws, _ := gittest.ClonedRepo(t)
	dir := layouttest.MkPipelineDir(t, ws)
	commit := func(body, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, ws, "add", "a.go")
		gittest.Run(t, ws, "commit", "-q", "-m", msg)
	}
	commit("package a\n", "seed")
	gittest.Run(t, ws, "push", "-q", "origin", "main")
	gittest.Run(t, ws, "checkout", "-q", "-b", "feat/x")
	commit("package a\n\nconst Side = \"ours\"\n", "ours")
	gittest.Run(t, ws, "push", "-q", "origin", "feat/x")
	head := gittest.Run(t, ws, "rev-parse", "HEAD")
	gittest.Run(t, ws, "checkout", "-q", "main")
	commit("package a\n\nconst Side = \"theirs\"\n", "theirs")
	gittest.Run(t, ws, "push", "-q", "origin", "main")
	gittest.Run(t, ws, "checkout", "-q", "feat/x")

	const issue, pr = 5, 7
	reason := fmt.Sprintf("%s: %s", pmstages.ReasonMergeQueueFailed,
		pmstages.MergeQueueConflictReason("main", pr, "DIRTY"))
	registry := recovery.Default(ws, unusedMergeRunner{t}, nil)
	out, err := runPrStageRecover(context.Background(), ws, issue, pr, reason,
		func(ctx context.Context, f recovery.StageFailure) (recovery.RecoveryResult, bool) {
			return registry.TryRecover(ctx, f, 0)
		})
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != prRecoverRewind {
		t.Fatalf("outcome = %s (%+v)", out.Outcome, out)
	}
	if len(out.ConflictingFiles) != 1 || out.ConflictingFiles[0] != "a.go" {
		t.Errorf("conflicting_files = %v", out.ConflictingFiles)
	}
	if out.MaxDevRedispatch <= 0 {
		t.Errorf("max_dev_redispatch = %d", out.MaxDevRedispatch)
	}

	// These are the fields executeBacktrack and feature-dev intake read.
	var signal map[string]any
	if err := json.Unmarshal(out.BacktrackSignal, &signal); err != nil {
		t.Fatalf("backtrack_signal %s: %v", out.BacktrackSignal, err)
	}
	if signal["signal_type"] != conflictResolutionSignal || signal["backtrack_target_stage"] != "feature-dev" {
		t.Errorf("backtrack_signal = %s", out.BacktrackSignal)
	}

	var conflictCtx struct {
		Branch           string `json:"branch"`
		BaseRef          string `json:"base_ref"`
		ConflictingFiles []struct {
			Path string `json:"path"`
		} `json:"conflicting_files"`
	}
	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("conflict-context-%d.json", issue)))
	if err != nil {
		t.Fatalf("feature-dev needs the conflict context: %v", err)
	}
	if err := json.Unmarshal(data, &conflictCtx); err != nil {
		t.Fatal(err)
	}
	if conflictCtx.Branch != "feat/x" || conflictCtx.BaseRef != "main" ||
		len(conflictCtx.ConflictingFiles) != 1 || conflictCtx.ConflictingFiles[0].Path != "a.go" {
		t.Errorf("conflict context = %+v", conflictCtx)
	}

	// The rewind reuses the PR branch, so recovery must leave it where it was.
	if got := gittest.Run(t, ws, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/x" {
		t.Errorf("HEAD = %s, want feat/x", got)
	}
	if got := gittest.Run(t, ws, "rev-parse", "origin/feat/x"); got != head {
		t.Errorf("origin/feat/x moved to %s, want %s", got, head)
	}
	if got := gittest.Run(t, ws, "status", "--porcelain"); got != "" {
		t.Errorf("worktree is not clean:\n%s", got)
	}
}

func TestRunPrStageRecover_ClearsStaleConflictContext(t *testing.T) {
	ws, dir := recoverWorkspace(t)
	path := filepath.Join(dir, fmt.Sprintf("conflict-context-%d.json", 5))
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runPrStageRecover(context.Background(), ws, 5, 9, "r",
		func(context.Context, recovery.StageFailure) (recovery.RecoveryResult, bool) {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Error("stale context must be gone before recovery runs")
			}
			return recovery.RecoveryResult{}, false
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrMergeBudget_RecoveryCoversCIWaitPlusMerge(t *testing.T) {
	ws, _ := recoverWorkspace(t)
	b := prMergeBudget(ws)
	if b.RecoveryClientTimeoutMs <= b.ClientTimeoutMs {
		t.Fatalf("recovery %d must exceed merge client %d", b.RecoveryClientTimeoutMs, b.ClientTimeoutMs)
	}
}
