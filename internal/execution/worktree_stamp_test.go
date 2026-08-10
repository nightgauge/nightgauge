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

// realGitRepo is the default repo root for the stamp cases: a real git repo, so
// ensureWorktree gets all the way through `git worktree add`.
func realGitRepo(t *testing.T) string { return initTestGitRepo(t, "main") }

// codexRepoWithFailingNpm is a real git repo configured for a CLI adapter
// (ui.core.adapter: codex) with a failing `npm` first on PATH. That combination
// drives ensureWorktree's LAST provisioning step — the SDK-CLI build, which runs
// AFTER `git worktree add` has already created and registered the worktree —
// into failure.
//
// HOME is redirected at the same time so applyNodeResolution's nvm cascade finds
// no $HOME/.nvm/nvm.sh, resolves nothing, and therefore does not prepend a real
// node bin dir ahead of the shim. Without that the host's own npm would run and
// the case would depend on the developer's machine.
func codexRepoWithFailingNpm(t *testing.T) string {
	t.Helper()
	repoRoot := initTestGitRepo(t, "main")

	cfgDir := filepath.Join(repoRoot, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("ui:\n  core:\n    adapter: codex\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "npm"),
		[]byte("#!/bin/sh\necho 'npm: build refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing npm shim: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return repoRoot
}

// notAGitRepo is a directory that exists but has no git repository, so
// ensureWorktree fails at `git rev-parse HEAD` — BEFORE `git worktree add`, and
// therefore before anything is created.
func notAGitRepo(t *testing.T) string { return t.TempDir() }

// TestRunStage_StampsWorktreeOnRuntime_WhenStageFailsBeforeSpawn is the #399
// regression guard.
//
// RunStage learns the worktree path from ensureWorktree, but before the fix the
// only writer of RuntimeState.WorktreeDir was SetProcess — called after
// cmd.Start() succeeds. Every error return in between (worktree provisioning
// itself, model validation, the three pipes, the spawn) left WorktreeDir empty
// on a run whose worktree was already on disk, so the scheduler's stageWorkspace
// fell back to the workspace root and every failure-path consumer (feature-branch
// resolution, post-condition gates, cleanup) inspected the wrong tree.
//
// The cases drive real RunStage to a real failure exit and assert the runtime
// names the worktree exactly when one exists — including the last case, which
// fails BEFORE creation and must therefore leave the runtime empty. That pair
// is what pins the stamp to the worktree's existence rather than to a position
// in RunStage: stamping unconditionally (before ensureWorktree, or from the
// derived path regardless of outcome) makes a run claim a directory that is not
// there, and the sweep and every path consumer believe it.
func TestRunStage_StampsWorktreeOnRuntime_WhenStageFailsBeforeSpawn(t *testing.T) {
	const repo = "nightgauge/nightgauge"

	cases := []struct {
		name      string
		issue     int
		setupRepo func(t *testing.T) string
		adapter   adapters.SkillRunner
		model     string
		wantErr   string
		// wantWorktree is whether provisioning got far enough to create the
		// worktree: true means it must exist on disk, be registered with git,
		// and be stamped on the runtime; false means nothing was created and
		// nothing may be claimed.
		wantWorktree bool
	}{
		{
			name:         "cmd.Start fails on a binary that does not exist",
			issue:        399,
			setupRepo:    realGitRepo,
			adapter:      &unstartableAdapter{bin: filepath.Join(t.TempDir(), "no-such-agent-cli")},
			wantErr:      "start ",
			wantWorktree: true,
		},
		{
			name:         "ValidateModel rejects the model before the command is built",
			issue:        3990,
			setupRepo:    realGitRepo,
			adapter:      adapters.NewCodexAdapter(),
			model:        "definitely-not-a-real-model",
			wantErr:      "model validation failed",
			wantWorktree: true,
		},
		{
			// The exit inside ensureWorktree itself: `git worktree add` has
			// already created and registered the worktree when the SDK-CLI
			// build fails, so RunStage's own "worktree setup" error arrives
			// with a worktree on disk.
			name:         "SDK CLI build fails after the worktree is created",
			issue:        3991,
			setupRepo:    codexRepoWithFailingNpm,
			adapter:      &unstartableAdapter{bin: filepath.Join(t.TempDir(), "no-such-agent-cli")},
			wantErr:      "worktree setup: SDK CLI build failed",
			wantWorktree: true,
		},
		{
			// The other side of the same ordering: provisioning fails BEFORE
			// creation, so there is no tree to name. A stamp that fires here
			// hands every consumer a path that does not exist.
			name:         "worktree provisioning fails before anything is created",
			issue:        3992,
			setupRepo:    notAGitRepo,
			adapter:      &unstartableAdapter{bin: filepath.Join(t.TempDir(), "no-such-agent-cli")},
			wantErr:      "worktree setup: get current HEAD commit",
			wantWorktree: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Keep codex provisioning ($CODEX_HOME/config.toml) inside the test.
			t.Setenv("CODEX_HOME", t.TempDir())

			repoRoot := tc.setupRepo(t)
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

			if !tc.wantWorktree {
				if _, statErr := os.Stat(want); !os.IsNotExist(statErr) {
					t.Fatalf("precondition: %s must NOT exist — this case fails before creation; stat err = %v",
						want, statErr)
				}
				if got := rt.Snapshot().WorktreeDir; got != "" {
					t.Errorf("runtime.WorktreeDir = %q, want \"\" — provisioning failed before the worktree "+
						"existed, so the run must not name a directory that is not there (#399)", got)
				}
				if pid := rt.StageChildPID(); pid != 0 {
					t.Errorf("runtime.PID = %d, want 0 — no child process started, so nothing may claim one", pid)
				}
				return
			}

			if _, statErr := os.Stat(want); statErr != nil {
				t.Fatalf("precondition: worktree %s must exist on disk after the failed stage: %v", want, statErr)
			}
			if !gitWorktreeListed(t, repoRoot, filepath.Base(want)) {
				t.Fatalf("precondition: git must still know worktree %q after the failed stage", filepath.Base(want))
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
