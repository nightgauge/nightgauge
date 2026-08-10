package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/state"
)

// unstartableAdapter is a SkillRunner whose BuildCommand names a binary that
// does not exist, so RunStage fails at cmd.Start() — an exit inside the window
// between "worktree provisioned" and "process registered".
//
// It is the only substitution in the test: the Manager, the git worktree, the
// RuntimeState and RunStage itself are the production objects, and SetAdapter /
// NewManager's adapter argument is how a real dispatch selects its CLI. The
// seam under test (RunStage → RuntimeState) is not faked.
type unstartableAdapter struct{ bin string }

func (a *unstartableAdapter) Name() string { return "test-unstartable" }

func (a *unstartableAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	return a.bin, nil, nil
}

func (a *unstartableAdapter) UsesStdin() bool { return false }

func (a *unstartableAdapter) Agentic() bool { return true }

// TestRunStage_StampsWorktreeOnRuntime_WhenStageFailsBeforeSpawn is the #399
// regression guard.
//
// RunStage learns the worktree path from ensureWorktree, but before the fix the
// only writer of RuntimeState.WorktreeDir was SetProcess — called after
// cmd.Start() succeeds. Every error return in between (model validation, the
// three pipes, the spawn) left WorktreeDir empty on a run whose worktree was
// already on disk, so the scheduler's stageWorkspace fell back to the workspace
// root and every failure-path consumer (feature-branch resolution, post-condition
// gates, cleanup) inspected the wrong tree.
//
// Both cases drive real RunStage to a real failure exit inside that window and
// assert the runtime still names the worktree that exists.
func TestRunStage_StampsWorktreeOnRuntime_WhenStageFailsBeforeSpawn(t *testing.T) {
	const repo = "nightgauge/nightgauge"

	cases := []struct {
		name    string
		issue   int
		adapter adapters.SkillRunner
		model   string
		wantErr string
	}{
		{
			name:    "cmd.Start fails on a binary that does not exist",
			issue:   399,
			adapter: &unstartableAdapter{bin: filepath.Join(t.TempDir(), "no-such-agent-cli")},
			wantErr: "start ",
		},
		{
			name:    "ValidateModel rejects the model before the command is built",
			issue:   3990,
			adapter: adapters.NewCodexAdapter(),
			model:   "definitely-not-a-real-model",
			wantErr: "model validation failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Keep codex provisioning ($CODEX_HOME/config.toml) inside the test.
			t.Setenv("CODEX_HOME", t.TempDir())

			repoRoot := initTestGitRepo(t, "main")
			m := NewManager(repoRoot, tc.adapter)
			rt := state.NewRuntimeState(repo, tc.issue, "", "01890a5d-ac96-774b-bcce-b302099a8057")

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			result, err := m.RunStage(ctx, StageOptions{
				Repo:        repo,
				IssueNumber: tc.issue,
				Stage:       "feature-dev",
				Model:       tc.model,
				Timeout:     30 * time.Second,
				Runtime:     rt,
			})
			if err == nil {
				t.Fatalf("expected RunStage to fail inside the provision→start window, got result %+v", result)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RunStage failed at the wrong exit: error %q does not contain %q", err, tc.wantErr)
			}

			want := m.worktreePath(repo, tc.issue)
			if _, statErr := os.Stat(want); statErr != nil {
				t.Fatalf("precondition: worktree %s must exist on disk after the failed stage: %v", want, statErr)
			}
			if got := rt.Snapshot().WorktreeDir; got != want {
				t.Errorf("runtime.WorktreeDir = %q, want %q — a stage that failed AFTER its worktree was "+
					"provisioned must still leave the run's worktree resolvable (#399); empty makes "+
					"stageWorkspace fall back to the workspace root", got, want)
			}
			// The worktree stamp must not smuggle in a process identity: no child
			// ever started here, and PID is what liveness readers consult.
			if pid := rt.StageChildPID(); pid != 0 {
				t.Errorf("runtime.PID = %d, want 0 — no child process started, so nothing may claim one", pid)
			}
		})
	}
}
