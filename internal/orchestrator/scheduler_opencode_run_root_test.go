package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// openCodeRootRunner stands in for a run whose stages ran on opencode: every
// dispatch leaves the run's per-run root on disk the way the opencode adapter
// does, with a transcript database in it and config/git linked to a directory
// outside it, then succeeds or fails as told.
type openCodeRootRunner struct {
	runIDCapturingRunner
	home, linkTarget string
	fail             bool

	mu    sync.Mutex
	roots []string
}

func (r *openCodeRootRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	root := filepath.Join(r.home, ".nightgauge", "opencode", "runs", params.RunID)
	for _, dir := range []string{"config", "data/opencode", "cache", "state", "home"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "data", "opencode", "opencode.db"), []byte("transcript"), 0o600); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, "config", "git")); os.IsNotExist(err) {
		if err := os.Symlink(r.linkTarget, filepath.Join(root, "config", "git")); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.roots = append(r.roots, root)
	r.mu.Unlock()
	if r.fail {
		return &StageRunResult{ExitCode: 1}, nil
	}
	return r.runIDCapturingRunner.RunStage(ctx, params)
}

// TestRunPipeline_DeletesTheOpenCodeRunRootAtEveryTerminalOutcome (ADR-022
// § 22): the run's OpenCode per-run root, and the session database and
// transcripts in it, are deleted when the run ends, whether it succeeded or
// failed. The terminal defer deletes it beside the worktree, whatever adapter
// the run's stages used, and deleting it unlinks config/git without touching
// the operator's git config it points at.
func TestRunPipeline_DeletesTheOpenCodeRunRootAtEveryTerminalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{name: "success"},
		{name: "failure", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The failure path asks the forge for the issue's state; the
			// issue is still open.
			stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
				return []byte(`{"state":"OPEN"}`), nil
			})
			home := t.TempDir()
			t.Setenv("HOME", home)
			operatorGit := filepath.Join(home, ".config", "git")
			if err := os.MkdirAll(operatorGit, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(operatorGit, "config"), []byte("[user]\n\tname = operator\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			root := gitWorkspace(t)
			runner := &openCodeRootRunner{home: home, linkTarget: operatorGit, fail: tc.fail}
			s := newRunIdentityTestScheduler(t, root, runner)
			success, _ := s.runPipeline(context.Background(), types.BoardItem{Number: 1616, Repo: "nightgauge/nightgauge", ID: "item-1616"})
			if success == tc.fail {
				t.Fatalf("runPipeline success = %v; the fixture did not reach the %s outcome", success, tc.name)
			}

			runner.mu.Lock()
			roots := append([]string(nil), runner.roots...)
			runner.mu.Unlock()
			if len(roots) == 0 {
				t.Fatal("no stage was dispatched, so no root was created; the fixture is wrong, not the assertion")
			}
			for _, r := range roots {
				if _, err := os.Lstat(r); !os.IsNotExist(err) {
					t.Errorf("the run's OpenCode root %s survived the %s outcome (%v)", r, tc.name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(operatorGit, "config")); err != nil {
				t.Errorf("deleting the run root deleted the operator's git config through config/git: %v", err)
			}
		})
	}
}
