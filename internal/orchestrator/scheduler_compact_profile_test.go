package orchestrator

import (
	"context"
	"fmt"
	"github.com/nightgauge/nightgauge/internal/layout"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/skillrender"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ADR 023 Q3/Q5 through the real scheduler and the real skills/ tree: at a
// small local window the full render does not fit, so a stage with a compact
// profile dispatches the compact render; a window the compact render does not
// fit either is refused as context_window_exceeded; and a hosted model with a
// large window still dispatches the full render.

// realSkillsWorkspace is a git workspace whose skills/ is this repository's
// real skills tree.
func realSkillsWorkspace(t *testing.T) string {
	t.Helper()
	root := gitWorkspace(t)
	skills, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(skills, "nightgauge-feature-dev", "_profiles", "compact.md")); err != nil {
		t.Fatalf("the real skills tree is not at %s: %v", skills, err)
	}
	if err := os.Symlink(skills, filepath.Join(root, "skills")); err != nil {
		t.Skipf("cannot symlink the skills tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("skills\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// profileCapturingRunner plays a run like steppedPipelineRunner and keeps
// every prompt feature-dev was dispatched with.
type profileCapturingRunner struct {
	steppedPipelineRunner
	mu        sync.Mutex
	devPrompt []string
	stages    []state.PipelineStage
}

func (r *profileCapturingRunner) RunStage(ctx context.Context, p StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.stages = append(r.stages, p.Stage)
	if p.Stage == state.StageFeatureDev {
		r.devPrompt = append(r.devPrompt, p.Prompt)
	}
	r.mu.Unlock()
	return r.steppedPipelineRunner.RunStage(ctx, p)
}

func (r *profileCapturingRunner) dispatched(stage state.PipelineStage) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.stages {
		if s == stage {
			return true
		}
	}
	return false
}

func newProfileTestScheduler(root string, runner StageRunner, adapter adapters.SkillRunner) *Scheduler {
	return &Scheduler{
		trustGate:      allWritersGate(),
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(root, adapter),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
}

// runLocalFeatureDev runs a pipeline whose feature-dev dispatches the local
// model, served at window tokens.
func runLocalFeatureDev(t *testing.T, window int) (root string, runner *profileCapturingRunner) {
	t.Helper()
	stubReconcileGhUnreachable(t)
	server := newLoadedLMStudioStub(t, "qwen/qwen3.8-27b", window)
	withMachineOpenCodeConfig(t, fmt.Sprintf("opencode:\n  provider: lm-studio\n  base_url: %s\n  limit:\n    context: %d\n", server.URL, window))

	root = realSkillsWorkspace(t)
	runner = &profileCapturingRunner{steppedPipelineRunner: steppedPipelineRunner{root: root}}
	s := newProfileTestScheduler(root, runner, adapters.NewOpenCodeAdapter())
	runKeepingSnapshot(t, s, root, func() {
		s.retryEngine.RecordEscalation(string(state.StageFeatureDev), openCodeReadinessModel)
	})
	return root, runner
}

func renderStage(t *testing.T, root, stage, model, adapter, profile string) *skillrender.Result {
	t.Helper()
	r, err := skillrender.Render(skillrender.Options{
		Stage: stage, Model: model, Adapter: adapter,
		SkillsRoots: dispatchedSnapshotRoots(t, root, 1662), Profile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// (i) At a 32768-token local window feature-dev's full render does not fit
// and its compact one does, so every sub-session is dispatched on the
// compact render: the sub-session policy and the compact profile compose.
func TestScheduler_SmallLocalWindowDispatchesTheCompactProfile(t *testing.T) {
	root, runner := runLocalFeatureDev(t, 32768)

	full := renderStage(t, root, "feature-dev", openCodeReadinessModel, "opencode", "")
	compact := renderStage(t, root, "feature-dev", openCodeReadinessModel, "opencode", skillrender.ProfileCompact)
	if skillrender.Fit("feature-dev", full.Content, 32768).Fits {
		t.Fatal("the full feature-dev render fits 32768 tokens; this test no longer exercises the compact hop")
	}
	if len(runner.devPrompt) != 3 {
		t.Fatalf("feature-dev sessions = %d, want 3 (one per plan task)", len(runner.devPrompt))
	}
	for k, p := range runner.devPrompt {
		if !strings.HasPrefix(p, compact.Content) {
			t.Errorf("sub-session %d was not dispatched on the compact render", k+1)
		}
		if !strings.Contains(p, fmt.Sprintf("step %d of 3", k+1)) {
			t.Errorf("sub-session %d prompt lacks its step preamble", k+1)
		}
	}
}

// (ii) A window neither render fits is refused before dispatch as
// context_window_exceeded, and names the compact attempt.
func TestScheduler_WindowTheCompactProfileMissesIsRefused(t *testing.T) {
	const window = 12288
	root, runner := runLocalFeatureDev(t, window)

	compact := renderStage(t, root, "feature-dev", openCodeReadinessModel, "opencode", skillrender.ProfileCompact)
	if skillrender.Fit("feature-dev", compact.Content, window).Fits {
		t.Fatalf("the compact feature-dev render fits %d tokens; pick a smaller window", window)
	}
	if runner.dispatched(state.StageFeatureDev) {
		t.Fatal("feature-dev was dispatched; a render that fits no profile must be refused first")
	}
	rec := recordForIssue(t, root, 1662)
	if rec.TerminalFailureKind != TerminalKindContextWindowExceeded {
		t.Errorf("terminal kind = %q, want %q", rec.TerminalFailureKind, TerminalKindContextWindowExceeded)
	}
}

// (iii) A hosted model with a large window keeps the full render.
func TestScheduler_LargeHostedWindowKeepsTheFullProfile(t *testing.T) {
	stubReconcileGhUnreachable(t)
	t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "sonnet")
	root := realSkillsWorkspace(t)
	runner := &profileCapturingRunner{steppedPipelineRunner: steppedPipelineRunner{root: root}}
	s := newProfileTestScheduler(root, runner, nil)
	runKeepingSnapshot(t, s, root, nil)

	if len(runner.devPrompt) != 1 {
		t.Fatalf("feature-dev sessions = %d, want 1 (a large window runs one session)", len(runner.devPrompt))
	}
	full := renderStage(t, root, "feature-dev", "sonnet", "claude", "")
	if full.ContextWindow < 200_000 {
		t.Fatalf("sonnet resolved a %d-token window; this test needs a large hosted window", full.ContextWindow)
	}
	if !strings.HasPrefix(runner.devPrompt[0], full.Content) {
		t.Error("feature-dev on a large hosted window was not dispatched on the full render")
	}
}

// dispatchedSnapshotRoots is the root list the dispatch used: the issue's one
// run snapshot (earlier runs are pruned), so a test render resolves the same
// include paths as the dispatch did (#2813).
func dispatchedSnapshotRoots(t *testing.T, root string, issue int) []string {
	t.Helper()
	base, err := layout.CheckoutPath(root, filepath.Join(skillSnapshotsDir, strconv.Itoa(issue)))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 1 {
		t.Fatalf("run snapshots for #%d = %v, %v; want exactly one", issue, entries, err)
	}
	return skillrender.ResolveRoots(skillrender.RootInputs{
		Workspace: filepath.Join(base, entries[0].Name()),
		Bundle:    skillrender.ExecutableBundleRoot(),
		Override:  os.Getenv(skillrender.SkillsRootEnv),
	})
}

// runKeepingSnapshot runs issue #1662 and leaves its skill snapshot in place.
// The run drops the snapshot when it ends (#2813), but rendered prompts embed
// the snapshot's include paths, so a re-render must use the same path: copy it
// while the run is live, assert the run removed it, then restore it. onPickup
// runs when issue-pickup completes.
func runKeepingSnapshot(t *testing.T, s *Scheduler, root string, onPickup func()) {
	t.Helper()
	var snapDir string
	keep := filepath.Join(t.TempDir(), "skills")
	s.telemetrySvc = &hookTelemetry{onEvent: func(e platform.PipelineEvent) {
		if e.EventType != "stage_completed" || e.Stage != string(state.StageIssuePickup) {
			return
		}
		if onPickup != nil {
			onPickup()
		}
		roots := dispatchedSnapshotRoots(t, root, 1662)
		for _, r := range roots {
			if strings.Contains(r, skillSnapshotsDir) {
				snapDir = filepath.Dir(r)
			}
		}
		if snapDir == "" {
			t.Fatalf("no snapshot root among %v", roots)
		}
		if err := copySkillTree(filepath.Join(snapDir, "skills"), keep); err != nil {
			t.Fatal(err)
		}
	}}
	s.telemetryEnabled = true
	s.runPipeline(context.Background(), types.BoardItem{Number: 1662, Repo: "nightgauge/test", ID: "item-1662"})
	if snapDir == "" {
		t.Fatal("issue-pickup never completed; no snapshot was captured")
	}
	if _, err := os.Stat(snapDir); !os.IsNotExist(err) {
		t.Fatalf("the finished run kept its skill snapshot %s: %v", snapDir, err)
	}
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copySkillTree(keep, filepath.Join(snapDir, "skills")); err != nil {
		t.Fatal(err)
	}
}
