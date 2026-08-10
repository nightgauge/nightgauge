package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/dockercompose"
)

// worktreeRepo builds a git repo holding one pipeline-shaped worktree
// (issue-<n>) and returns the repo root. Unlike mergedWorktreeRepo it does not
// merge the branch — the worktree here stands for a run that is still LIVE,
// which is the state #296 destroyed.
func worktreeRepo(t *testing.T, issue int) string {
	t.Helper()
	base := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
		}
	}
	git(base, "init", "-b", "main", "repo")
	root, err := filepath.EvalSymlinks(filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("resolve repo path: %v", err)
	}
	git(root, "config", "user.email", "test@test")
	git(root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "add", ".")
	git(root, "commit", "-m", "initial")
	git(root, "worktree", "add",
		filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue)),
		"-b", "fix/"+strconv.Itoa(issue)+"-work")
	return root
}

// recordingTeardown returns a compose teardown seam that records every project
// name it is asked to destroy instead of shelling out to docker.
func recordingTeardown(torn *[]string) func(context.Context, string, dockercompose.TeardownOptions) (dockercompose.TeardownResult, error) {
	return func(_ context.Context, name string, _ dockercompose.TeardownOptions) (dockercompose.TeardownResult, error) {
		*torn = append(*torn, name)
		return dockercompose.TeardownResult{}, nil
	}
}

func listing(projects ...dockercompose.Project) func(context.Context) ([]dockercompose.Project, error) {
	return func(context.Context) ([]dockercompose.Project, error) { return projects, nil }
}

// TestActiveWorktreeIssues_SeesCrossRepoWorktree is case 3 of #296. A cross-repo
// run registers its worktree in the TARGET repo (execution.ensureWorktree sets
// cmd.Dir = repoRoot), so a scan rooted only at the launch root never sees it
// and the run looks orphaned.
func TestActiveWorktreeIssues_SeesCrossRepoWorktree(t *testing.T) {
	launchRoot := worktreeRepo(t, 401)
	siblingRoot := worktreeRepo(t, 402)

	s := &Scheduler{
		workspaceRoot:     launchRoot,
		repoRootsResolver: func() []string { return []string{siblingRoot} },
	}

	active, determined := s.activeWorktreeIssuesFor(s.repoScanRoots())
	if !determined {
		t.Fatal("a readable workspace must produce a determined answer")
	}
	if !active[401] {
		t.Error("launch-root worktree #401 must be active")
	}
	if !active[402] {
		t.Error("sibling-repo worktree #402 must be active — this is the #296 cross-repo blindness")
	}
}

// TestActiveWorktreeIssues_UndeterminedPaths covers the three ways the old
// implementation returned an empty map that was indistinguishable from "no
// worktrees exist".
func TestActiveWorktreeIssues_UndeterminedPaths(t *testing.T) {
	t.Run("no roots configured", func(t *testing.T) {
		s := &Scheduler{}
		if _, determined := s.activeWorktreeIssuesFor(s.repoScanRoots()); determined {
			t.Error("no workspace root is not evidence that no worktrees exist")
		}
	})

	t.Run("git worktree list fails", func(t *testing.T) {
		// A directory that exists but is not a git repo, with GIT_CEILING
		// preventing an upward walk into any enclosing repo.
		notARepo := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(notARepo))
		s := &Scheduler{workspaceRoot: notARepo}
		if _, determined := s.activeWorktreeIssuesFor(s.repoScanRoots()); determined {
			t.Error("a failed `git worktree list` must undetermine the answer, not report zero worktrees")
		}
	})

	t.Run("readable repo with no worktrees is DETERMINED empty", func(t *testing.T) {
		// The guard must not degrade into "never act": a real, readable repo
		// that genuinely holds no pipeline worktrees is a determined answer.
		root := worktreeRepo(t, 403)
		cmd := exec.Command("git", "worktree", "remove", "--force",
			filepath.Join(root, ".worktrees", "issue-403"))
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("remove worktree: %v: %s", err, out)
		}
		active, determined := s0(root).activeWorktreeIssuesFor(s0(root).repoScanRoots())
		if !determined {
			t.Fatal("a readable repo with no worktrees is a determined answer")
		}
		if len(active) != 0 {
			t.Errorf("expected no active issues, got %v", active)
		}
	})
}

// TestActiveWorktreeIssues_MissingRootIsSkipped: a registered repo path that no
// longer exists genuinely holds no worktrees. Undetermining on it would let one
// deleted sibling permanently disable compose reconciliation.
func TestActiveWorktreeIssues_MissingRootIsSkipped(t *testing.T) {
	root := worktreeRepo(t, 404)
	s := &Scheduler{
		workspaceRoot:     root,
		repoRootsResolver: func() []string { return []string{filepath.Join(t.TempDir(), "deleted-sibling")} },
	}
	active, determined := s.activeWorktreeIssuesFor(s.repoScanRoots())
	if !determined {
		t.Fatal("a missing sibling root must be skipped, not undetermine the whole answer")
	}
	if !active[404] {
		t.Error("the readable root's worktree must still be reported")
	}
}

// TestReconcileOrphanedCompose_UndeterminedTearsDownNothing is the assertion the
// bug would fail. #296's teardown ran `docker compose down -v
// --remove-orphans` — it returned no error while destroying a live run's named
// volumes, so an error-only assertion would pass against it. Assert the side
// effect: zero teardowns.
func TestReconcileOrphanedCompose_UndeterminedTearsDownNothing(t *testing.T) {
	notARepo := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(notARepo))

	var torn []string
	s := &Scheduler{
		workspaceRoot:   notARepo, // `git worktree list` here fails → undetermined
		composeLister:   listing(dockercompose.Project{Name: "issue-501", IssueNumber: 501}),
		composeTeardown: recordingTeardown(&torn),
	}

	// The in-flight set is now the caller's to supply (#410). Undetermined is
	// what the CALLER could not read, and it must veto teardown regardless of
	// how empty the set it managed to build looks.
	inFlight, determined := s.activeWorktreeIssuesFor(s.repoScanRoots())
	if determined {
		t.Fatal("fixture is not the undetermined state — `git worktree list` succeeded")
	}
	s.reconcileOrphanedComposeProjects(inFlight, determined)

	if len(torn) != 0 {
		t.Errorf("tore down %v on an undetermined worktree set — that is `down -v` against a possibly-live run", torn)
	}
}

// TestReconcileOrphanedCompose_CrossRepoRunSurvives is the end-to-end shape of
// the reported defect: a live run in a SIBLING repo whose compose stack was
// destroyed because the worktree scan only looked at the launch root.
//
// Driven through the autonomous receiver (#410) rather than the Scheduler
// method, because the receiver is what builds the in-flight union now — testing
// the method with a hand-built set would assert nothing about the roots the
// union actually covers.
func TestReconcileOrphanedCompose_CrossRepoRunSurvives(t *testing.T) {
	launchRoot := worktreeRepo(t, 601)
	siblingRoot := worktreeRepo(t, 602)

	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:     launchRoot,
			repoRootsResolver: func() []string { return []string{siblingRoot} },
			composeLister: listing(
				dockercompose.Project{Name: "issue-602", IssueNumber: 602}, // live, in the sibling
				dockercompose.Project{Name: "issue-999", IssueNumber: 999}, // genuinely orphaned
			),
			composeTeardown: recordingTeardown(&torn),
		},
		state: &AutonomousState{},
	}

	as.sweepOrphanedComposeProjects()

	for _, name := range torn {
		if name == "issue-602" {
			t.Error("tore down a live cross-repo run's compose stack — this is #296")
		}
	}
	// The guard must not disable reconciliation: the real orphan still goes.
	if len(torn) != 1 || torn[0] != "issue-999" {
		t.Errorf("expected exactly the orphaned project to be torn down, got %v", torn)
	}
}

// installRecordingDocker puts a `docker` shim on PATH that appends its argv to
// a log file, so a test can assert what the code under test asked docker to do
// — including that it asked NOTHING. Without the shim a host with no docker
// makes every "no teardown happened" assertion pass for the wrong reason:
// dockercompose.IsAvailable fails and ListIssueProjects returns early, so the
// production default seams are indistinguishable from a fixed constructor.
//
// lsOutput is what `docker compose ls` reports; teardown of `issue-N` is
// accepted and recorded like any other call.
func installRecordingDocker(t *testing.T, lsOutput string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "$RECORDING_DOCKER_LOG"
case "$1" in
  version) exit 0 ;;
  compose)
    case "$2" in
      ls) printf '%s' "$RECORDING_DOCKER_LS" ; exit 0 ;;
    esac
    exit 0 ;;
  images) exit 0 ;;
  rmi) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write recording docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(dir, "docker-calls.log")
	t.Setenv("RECORDING_DOCKER_LOG", logPath)
	t.Setenv("RECORDING_DOCKER_LS", lsOutput)
	return logPath
}

func dockerCalls(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read docker call log: %v", err)
	}
	return string(data)
}

// TestNewScheduler_ConstructionTearsDownNoContainers is #410 gap 2 — the other
// half of #403's inversion, which fixed worktrees and left containers behind.
//
// loadQueue ran reconcileOrphanedComposeProjects, and loadQueue is on the
// construction path of every `nightgauge queue …` invocation and of the
// deps-gate / baseline-gate promote commands. So `queue list`, a printf loop,
// ran `docker compose down -v --remove-orphans` (plus image removal) as a side
// effect of being constructed — on behalf of a process that can see no other
// process's in-flight runs. Constructors delete NOTHING: not worktrees, not
// containers, not volumes.
//
// The assertion is on the shim's call log rather than on a recording seam,
// because the seams are unexported fields that cannot be injected before
// NewScheduler runs loadQueue — this is the PRODUCTION attach path, defaults and
// all (#399's lesson).
func TestNewScheduler_ConstructionTearsDownNoContainers(t *testing.T) {
	// issue-909 has no worktree in this repo, so pre-fix the constructor
	// classified it as orphaned and tore it down.
	logPath := installRecordingDocker(t, `[{"Name":"issue-909","Status":"running(1)"}]`)
	root := worktreeRepo(t, 908)

	_ = NewScheduler(nil, SchedulerConfig{WorkspaceRoot: root})

	if calls := dockerCalls(t, logPath); strings.TrimSpace(calls) != "" {
		t.Errorf("constructing a Scheduler shelled out to docker — construction must never delete containers.\ndocker calls:\n%s", calls)
	}
}

// The merged-worktree sweep's undetermined guard used to be pinned twice here,
// once per receiver. (*Scheduler).sweepMergedWorktrees no longer exists (#403 —
// constructors never delete), so the surviving pin is
// TestSweepMergedWorktrees_UndeterminedSkipsAutonomousSweep, on the one caller
// that still reclaims.

func s0(root string) *Scheduler { return &Scheduler{workspaceRoot: root} }
