package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// --- #882: launch repo ≠ target repo -----------------------------------------
//
// Every pre-existing runPipeline test models ONE repo, which is exactly why a
// cross-repo run could root its worktree, its pipeline state and its epic
// branch at the launch repo for as long as it did: with one repo, the launch
// root and the target root are the same directory and every assertion passes
// either way. These tests model TWO repos with TWO remotes, so the wrong root
// is observable.

// repoWithOrigin builds a git repo with a real (bare) origin remote and a
// pushed `main`, and returns (repoRoot, originPath). The origin is what makes
// "pushed to the WRONG repository's remote" a checkable fact rather than a
// log line.
func repoWithOrigin(t *testing.T, parent, owner, name string) (string, string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), name+".git")
	root := filepath.Join(parent, name)

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	run(origin, "init", "--bare", "--initial-branch=main")

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	run(root, "init", "--initial-branch=main")
	run(root, "config", "user.email", "cross-repo-test@example.com")
	run(root, "config", "user.name", "Cross Repo Test")
	run(root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".nightgauge/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "-A")
	run(root, "commit", "-m", "seed")
	run(root, "remote", "add", "origin", origin)
	run(root, "push", "-u", "origin", "main")

	// The repo's own .nightgauge/config.yaml is what makes it DISCOVERABLE by
	// the workspace repo registry — the same file the daemon's resolver reads.
	cfgDir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgYAML := "owner: " + owner + "\ndefaultRepo: " + name + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, origin
}

// remoteBranches lists the branch refs present in a bare origin.
func remoteBranches(t *testing.T, origin string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", origin, "for-each-ref", "--format=%(refname:short)", "refs/heads/").Output()
	if err != nil {
		t.Fatalf("for-each-ref in %s: %v", origin, err)
	}
	var refs []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			refs = append(refs, l)
		}
	}
	return refs
}

// exitZeroNoContextRunner exits 0 without writing the stage's output context.
// That is the shape that reaches the epic-branch creation (which runs after
// issue-pickup) and then terminates the run at the missing-output gate, so the
// test observes the epic branch without needing a full six-stage fixture.
type exitZeroNoContextRunner struct {
	mu         sync.Mutex
	dispatched []state.PipelineStage
}

func (r *exitZeroNoContextRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.dispatched = append(r.dispatched, params.Stage)
	r.mu.Unlock()
	return &StageRunResult{ExitCode: 0}, nil
}

func (r *exitZeroNoContextRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dispatched)
}

func crossRepoScheduler(t *testing.T, launchRoot string, runner StageRunner) *Scheduler {
	t.Helper()
	writeSkillFile(t, launchRoot, "nightgauge-issue-pickup")
	return &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      newMockIssueSvc(),
		execMgr:       execution.NewManager(launchRoot, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		workspaceRoot: launchRoot,
		launchRepo:    "owner/launch",
	}
}

// TestCrossRepoRun_RootsStateAndEpicBranchAtTargetRepo is the #882 acceptance
// test for criteria 1 and 2.
//
// Observed pre-fix, on a real run: the worktree came out at
// LAUNCH/.nightgauge/worktrees/<target>-issue-227 (created, empty), history and
// trace landed in LAUNCH/.nightgauge/pipeline/, and the epic branch was cut from
// the LAUNCH repo's default branch and PUSHED to the LAUNCH repo's remote. The
// target repo received nothing.
func TestCrossRepoRun_RootsStateAndEpicBranchAtTargetRepo(t *testing.T) {
	workspace := t.TempDir()
	launchRoot, launchOrigin := repoWithOrigin(t, workspace, "owner", "launch")
	targetRoot, targetOrigin := repoWithOrigin(t, workspace, "owner", "target")

	runner := &exitZeroNoContextRunner{}
	s := crossRepoScheduler(t, launchRoot, runner)
	// The stage's skill is composed from the RUN's root, which is the target
	// repo — the same relocation this issue is about.
	writeSkillFile(t, targetRoot, "nightgauge-issue-pickup")
	// THE PRODUCTION CLI WIRING, not a hand-built stub: discovery from the
	// launch root, exactly as `nightgauge queue run` now does. Before #882 this
	// call did not exist and the resolver stayed nil.
	s.WithWorkspaceRepoRegistry(launchRoot)

	item := types.BoardItem{
		Number:       227,
		Repo:         "owner/target",
		ID:           "item-227",
		Title:        "cross-repo issue",
		ParentNumber: 206,
		ParentTitle:  "Parent epic",
	}
	s.runPipeline(context.Background(), item)

	// 1. Pipeline state (history, trace, run snapshot) roots at the TARGET repo.
	if _, err := os.Stat(filepath.Join(targetRoot, ".nightgauge", "pipeline")); err != nil {
		t.Errorf("target repo has no .nightgauge/pipeline — the run's state did not root at the target repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(launchRoot, ".nightgauge", "pipeline")); err == nil {
		t.Errorf("the LAUNCH repo received .nightgauge/pipeline for a run targeting another repo (#882)")
	}

	// 2. The worktree path roots at the TARGET repo. (The leaf already carried
	// the target repo's name pre-fix; the base is what was wrong.)
	if got := s.execMgr.RepoRoot("owner/target"); got != targetRoot {
		t.Errorf("execMgr.RepoRoot(owner/target) = %q, want the target checkout %q", got, targetRoot)
	}
	if _, err := os.Stat(filepath.Join(launchRoot, ".nightgauge", "worktrees")); err == nil {
		t.Errorf("a worktree directory was created inside the LAUNCH repo for a run targeting another repo (#882)")
	}

	// 3. The epic branch was created from the TARGET repo and pushed to the
	// TARGET repo's remote — never the launch repo's.
	targetRefs := remoteBranches(t, targetOrigin)
	launchRefs := remoteBranches(t, launchOrigin)
	hasEpic := func(refs []string) bool {
		for _, r := range refs {
			if strings.HasPrefix(r, "epic/206-") {
				return true
			}
		}
		return false
	}
	if !hasEpic(targetRefs) {
		t.Errorf("no epic/206-* branch on the TARGET repo's remote; got %v", targetRefs)
	}
	if hasEpic(launchRefs) {
		t.Errorf("epic/206-* was pushed to the LAUNCH repo's remote — a write-containment escape (#882); got %v", launchRefs)
	}
}

// TestCrossRepoRun_UnresolvableTargetRepoRefusesTheRun is the #882 acceptance
// test for criterion 3, the fail-closed arm.
//
// A workspace can always hold a repo the registry does not know. The old
// behavior rooted such a run at the launch repo, which is a REAL repository
// with a REAL remote: writing another project's work there is worse than not
// running. Nothing may be dispatched, created or pushed.
func TestCrossRepoRun_UnresolvableTargetRepoRefusesTheRun(t *testing.T) {
	launchRoot, launchOrigin := repoWithOrigin(t, t.TempDir(), "owner", "launch")

	runner := &exitZeroNoContextRunner{}
	s := crossRepoScheduler(t, launchRoot, runner)
	// A resolver IS wired, and it does not know this repo — the case the issue
	// singles out as the one that must fail closed on its own.
	s.WithRepoPathResolver(func(string) string { return "" })

	item := types.BoardItem{
		Number:       227,
		Repo:         "owner/unregistered",
		ID:           "item-227",
		ParentNumber: 206,
		ParentTitle:  "Parent epic",
	}
	s.runPipeline(context.Background(), item)

	if n := runner.count(); n != 0 {
		t.Errorf("%d stage(s) were dispatched for a repo with no resolvable root; want 0 — the run must refuse", n)
	}
	if _, err := os.Stat(filepath.Join(launchRoot, ".nightgauge", "worktrees")); err == nil {
		t.Errorf("a worktree was created inside the launch repo for an unresolvable target repo")
	}
	for _, r := range remoteBranches(t, launchOrigin) {
		if strings.HasPrefix(r, "epic/") {
			t.Errorf("an epic branch was pushed to the launch repo's remote for an unresolvable target repo: %s", r)
		}
	}

	// The refusal names the repo it could not resolve — an operator must be
	// able to tell WHICH repo the workspace does not know.
	_, err := s.resolveRunRoot("owner/unregistered")
	if err == nil {
		t.Fatalf("resolveRunRoot returned no error for an unregistered repo")
	}
	if !strings.Contains(err.Error(), "owner/unregistered") {
		t.Errorf("refusal does not name the unresolved repo: %v", err)
	}
}

// TestCrossRepoRun_NoResolverRefusesANonLaunchRepo covers the CLI-mode shape as
// it was actually observed: no resolver wired at all. Rooting at the launch
// repo is still refused, because the launch root's own config names a different
// repo than the item does.
func TestCrossRepoRun_NoResolverRefusesANonLaunchRepo(t *testing.T) {
	launchRoot, _ := repoWithOrigin(t, t.TempDir(), "owner", "launch")
	s := crossRepoScheduler(t, launchRoot, &exitZeroNoContextRunner{})

	if _, err := s.resolveRunRoot("owner/target"); err == nil {
		t.Errorf("a nil resolver rooted a run for owner/target at the launch repo owner/launch — this is #882")
	}
	if got, err := s.resolveRunRoot("owner/launch"); err != nil || got != launchRoot {
		t.Errorf("the launch repo's own run must still root at the launch root; got %q, %v", got, err)
	}
}

// TestOriginRepoSlug_RejectsLocalPathRemotes guards the fail-closed gate
// against its own worst failure mode: a WRONG launch identity.
//
// The trailing two segments of a local-path remote look exactly like an
// owner/repo pair — a fixture pushing to /tmp/.../001/origin reads as the repo
// "001/origin". Believing that turns the gate into a refusal machine for runs
// that were never in danger, which is a worse outcome than the mis-rooting it
// prevents.
func TestOriginRepoSlug_RejectsLocalPathRemotes(t *testing.T) {
	root, _ := repoWithOrigin(t, t.TempDir(), "owner", "launch") // origin is a local bare repo path
	if got := originRepoSlug(root); got != "" {
		t.Errorf("originRepoSlug on a local-path remote = %q, want \"\"", got)
	}

	notARepo := t.TempDir()
	if got := originRepoSlug(notARepo); got != "" {
		t.Errorf("originRepoSlug outside a git repo = %q, want \"\"", got)
	}
}

func TestRepoSlugsMatch(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"owner/repo", "owner/repo", true},
		{"Owner/Repo", "owner/repo", true},
		// One side omitted its owner — the IPC runItem verb accepts a bare name.
		{"repo", "owner/repo", true},
		{"owner/repo", "repo", true},
		// Two fully-qualified slugs with different owners are different repos,
		// even when the short names collide. This is the fork case, and reading
		// it as a match would root a fork's run in the upstream checkout.
		{"owner/repo", "other/repo", false},
		{"owner/a", "owner/b", false},
		{"", "owner/repo", false},
		{"owner/repo", "", false},
	}
	for _, c := range cases {
		if got := repoSlugsMatch(c.a, c.b); got != c.want {
			t.Errorf("repoSlugsMatch(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
