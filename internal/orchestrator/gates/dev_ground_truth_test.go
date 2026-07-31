package gates

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive REAL git repositories rather than stubbing the git calls.
// #202 was a gate believing a skill's self-report over the filesystem, and the
// recovered-but-incomplete fixes on #173/#193 in the same week were both
// "looked right, still wrong". A stub here would assert that the code calls the
// helper it calls; only real git proves the invocation answers the question.

// gitRepo creates an initialized repository on `main` with one commit, so
// DefaultDiffBases's `main` fallback resolves.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "checkout", "-b", "main")
	git(t, dir, "config", "user.email", "gate-test@example.com")
	git(t, dir, "config", "user.name", "Gate Test")
	// Pin the CI default explicitly (#223). A developer machine with
	// `status.showUntrackedFiles=all` set globally makes porcelain enumerate
	// untracked files, while CI's default collapses them to a directory entry —
	// so a gate asserting on file names passed locally and failed in CI. Setting
	// the restrictive value here means the tests exercise the shape the fleet
	// actually runs against, on every machine.
	git(t, dir, "config", "status.showUntrackedFiles", "normal")
	writeFile(t, filepath.Join(dir, "README.md"), "base\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// devContext writes the dev-{N}.json a healthy feature-dev run produces: a
// truthful report of changed files with verification recorded. Every test here
// uses the SAME passing self-report, so the only variable is what git sees.
func devContext(t *testing.T, ws string, issue int) {
	t.Helper()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(issue)), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{"src/added.go"},
			"modified": []string{"src/changed.go"},
			"deleted":  []string{},
		},
		"build_verification": map[string]any{"ran": true, "status": "passed"},
	})
}

func devContextName(issue int) string {
	return "dev-" + itoa(issue) + ".json"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestFeatureDevGate_GroundTruth_EmptyWorkspaceFails is the #202 reproduction.
// The dev context truthfully reports changed files — the subagent really did
// change five of them — but it did the work in an agent-isolation worktree, so
// the stage workspace the pipeline reads is clean and its branch is level with
// base. Pre-fix this passed and cost another $0.87 at feature-validate.
func TestFeatureDevGate_GroundTruth_EmptyWorkspaceFails(t *testing.T) {
	ws := gitRepo(t)
	devContext(t, ws, 42)

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if gr.Passed {
		t.Fatalf("expected fail: workspace is clean and level with base; reason=%q", gr.Reason)
	}
	if gr.Kind != KindNoOp {
		t.Errorf("Kind = %q, want %q — a stage that produced nothing is a no-op, not a hard error", gr.Kind, KindNoOp)
	}
	if gr.TerminalKind != TerminalKindDevProducedNoChanges {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevProducedNoChanges)
	}
	// The AC requires the failure to NAME the cause rather than leave the next
	// stage to infer it.
	if !strings.Contains(gr.Reason, "produced none in the stage workspace") {
		t.Errorf("reason does not name the cause: %q", gr.Reason)
	}
	if !strings.Contains(gr.Reason, "[dev-produced-no-changes]") {
		t.Errorf("reason lacks the stable classifier marker: %q", gr.Reason)
	}
}

// TestFeatureDevGate_GroundTruth_DirtyWorkspacePasses guards the direction
// that matters most: feature-dev's contract is to leave changes UNCOMMITTED
// (SKILL.md Phase 7, #1608), so the healthy shape is a dirty tree. A gate that
// demanded a commit here — as issue #202's acceptance criteria literally
// proposed — would fail every correct dev run and halt the fleet.
func TestFeatureDevGate_GroundTruth_DirtyWorkspacePasses(t *testing.T) {
	ws := gitRepo(t)
	devContext(t, ws, 42)
	writeFile(t, filepath.Join(ws, "src", "added.go"), "package src\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if !gr.Passed {
		t.Fatalf("expected pass for uncommitted work — the contracted shape; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// TestFeatureDevGate_GroundTruth_CommittedWorkPasses covers a stage that
// committed anyway. That violates the skill contract, but the work exists and
// reaches every later stage, so it is not this gate's failure to report.
func TestFeatureDevGate_GroundTruth_CommittedWorkPasses(t *testing.T) {
	ws := gitRepo(t)
	git(t, ws, "checkout", "-b", "feat/42-thing")
	writeFile(t, filepath.Join(ws, "src", "added.go"), "package src\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "feat: add thing")
	devContext(t, ws, 42)

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if !gr.Passed {
		t.Fatalf("expected pass when the branch carries commits; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// TestFeatureDevGate_GroundTruth_BookkeepingOnlyFails is the silent-disable
// case. The stage's own dev-{N}.json lands under .nightgauge/pipeline/, which
// THIS repo gitignores and a consumer repo may not. Counting it as work would
// make every empty workspace read as productive, turning the gate off in
// exactly the repos nobody would think to check.
func TestFeatureDevGate_GroundTruth_BookkeepingOnlyFails(t *testing.T) {
	ws := gitRepo(t)
	devContext(t, ws, 42) // untracked: no .nightgauge/.gitignore in this fixture
	writeFile(t, filepath.Join(ws, ".claude", "settings.local.json"), "{}\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if gr.Passed {
		t.Fatalf("expected fail: only bookkeeping dirs changed, no deliverable; evidence=%v", gr.Evidence)
	}
	if gr.TerminalKind != TerminalKindDevProducedNoChanges {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevProducedNoChanges)
	}
}

// devContextDeleted writes a dev-{N}.json whose files_changed.deleted is
// exactly `deleted` and whose created/modified are empty — the shape a stage
// produces when its entire deliverable is removing bookkeeping paths (#237).
func devContextDeleted(t *testing.T, ws string, issue int, deleted []string) {
	t.Helper()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(issue)), map[string]any{
		"files_changed": map[string]any{
			"created":  []string{},
			"modified": []string{},
			"deleted":  deleted,
		},
		"build_verification": map[string]any{"ran": true, "status": "passed"},
	})
}

// TestFeatureDevGate_GroundTruth_BookkeepingDeliverablePasses is #237's fix
// case: an issue whose entire deliverable is a change under .nightgauge/,
// which the default exclusion-scoped probe can never see.
func TestFeatureDevGate_GroundTruth_BookkeepingDeliverablePasses(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, ".nightgauge", "pipeline", "dev-1.json"), "{}\n")
	git(t, ws, "add", ".nightgauge/pipeline/dev-1.json")
	git(t, ws, "commit", "-m", "add tracked bookkeeping file")
	git(t, ws, "rm", "--cached", ".nightgauge/pipeline/dev-1.json")

	devContextDeleted(t, ws, 42, []string{".nightgauge/pipeline/dev-1.json"})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if !gr.Passed {
		t.Fatalf("expected pass: declared bookkeeping deletion matches git; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, "deliverable=bookkeeping") {
		t.Errorf("evidence does not record the bookkeeping mode:\n%s", joined)
	}
}

// TestFeatureDevGate_GroundTruth_UndeclaredBookkeepingStillFails is the #202
// regression guard: a dev context that declares a bookkeeping path it never
// actually touched must still fail — widening must not become a way to
// self-certify an empty run.
func TestFeatureDevGate_GroundTruth_UndeclaredBookkeepingStillFails(t *testing.T) {
	ws := gitRepo(t)
	devContextDeleted(t, ws, 42, []string{".nightgauge/pipeline/never-touched.json"})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if gr.Passed {
		t.Fatalf("expected fail: declared bookkeeping path was never actually touched; evidence=%v", gr.Evidence)
	}
	if gr.TerminalKind != TerminalKindDevProducedNoChanges {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevProducedNoChanges)
	}
}

// TestFeatureDevGate_GroundTruth_MismatchedBookkeepingDeclarationFails covers
// the case where the workspace DOES have real bookkeeping changes, but the dev
// context declares different, unrelated bookkeeping paths that were never
// touched — the widening must be scoped to what was declared, not to the whole
// bookkeeping tree.
func TestFeatureDevGate_GroundTruth_MismatchedBookkeepingDeclarationFails(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, ".nightgauge", "pipeline", "actual.json"), "{}\n")
	git(t, ws, "add", ".nightgauge/pipeline/actual.json")
	git(t, ws, "commit", "-m", "add tracked bookkeeping file")
	git(t, ws, "rm", "--cached", ".nightgauge/pipeline/actual.json")

	devContextDeleted(t, ws, 42, []string{".nightgauge/pipeline/unrelated.json"})

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if gr.Passed {
		t.Fatalf("expected fail: declared paths do not match the actual bookkeeping change; evidence=%v", gr.Evidence)
	}
	if gr.TerminalKind != TerminalKindDevProducedNoChanges {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindDevProducedNoChanges)
	}
}

// TestFeatureDevGate_GroundTruth_NonRepoPassesOpen pins the fail-open
// direction. This gate runs after a stage has already spent real money, and a
// false accusation costs a full re-run — and, through the safety rails, can
// halt the queue. "Cannot verify" must never mean "verified empty".
func TestFeatureDevGate_GroundTruth_NonRepoPassesOpen(t *testing.T) {
	ws := t.TempDir() // deliberately not a git repository
	devContext(t, ws, 42)

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if !gr.Passed {
		t.Fatalf("expected pass when git cannot answer; reason=%q", gr.Reason)
	}
}

// TestFeatureDevGate_GroundTruth_UnresolvableBasePassesOpen is the same
// fail-open rule for the other unanswerable case: a clean tree in a repo whose
// default branch is neither origin/main nor main. Collapsing "no base
// resolved" into "empty diff" — which the plain
// ChangedFilesAgainstDefaultBase does — would fail every dev stage in such a
// repo.
func TestFeatureDevGate_GroundTruth_UnresolvableBasePassesOpen(t *testing.T) {
	ws := t.TempDir()
	git(t, ws, "init")
	git(t, ws, "checkout", "-b", "trunk")
	git(t, ws, "config", "user.email", "gate-test@example.com")
	git(t, ws, "config", "user.name", "Gate Test")
	writeFile(t, filepath.Join(ws, "README.md"), "base\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "base")
	devContext(t, ws, 42)

	gr := FeatureDevGate{}.Verify(context.Background(), 42, ws)

	if !gr.Passed {
		t.Fatalf("expected pass when no base ref resolves; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// TestFeatureDevGate_GroundTruth_NamesStrandedWorktree covers the recovery
// half of #202. The verdict "dev produced nothing" is correct but useless on
// its own — the implementation existed the whole time, in
// .claude/worktrees/agent-<id>, and was salvaged by hand only because someone
// went looking. Naming the path makes the failure recoverable from the record.
func TestFeatureDevGate_GroundTruth_NamesStrandedWorktree(t *testing.T) {
	repo := gitRepo(t)

	// The stage's own worktree: clean, level with base.
	stage := filepath.Join(repo, ".worktrees", "issue-42")
	git(t, repo, "worktree", "add", "--detach", stage, "main")
	devContext(t, stage, 42)

	// The agent-isolation worktree the subagent actually worked in.
	agent := filepath.Join(repo, ".claude", "worktrees", "agent-a54fdb5e")
	git(t, repo, "worktree", "add", "--detach", agent, "main")
	writeFile(t, filepath.Join(agent, "src", "the-real-fix.go"), "package src\n")

	gr := FeatureDevGate{}.Verify(context.Background(), 42, stage)

	if gr.Passed {
		t.Fatalf("expected fail: the stage worktree is empty; evidence=%v", gr.Evidence)
	}
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, "agent-a54fdb5e") {
		t.Errorf("evidence does not name the worktree holding the work:\n%s", joined)
	}
	if !strings.Contains(joined, "stranded") {
		t.Errorf("evidence does not flag the work as stranded:\n%s", joined)
	}
}

// TestFeatureDevGate_GroundTruth_SelfNotReportedAsStranded guards the
// path-comparison. On macOS t.TempDir() hands back /var/... while git reports
// the symlink-resolved /private/var/..., so a naive string compare makes the
// stage's own worktree report itself as the thief of its own work.
func TestFeatureDevGate_GroundTruth_SelfNotReportedAsStranded(t *testing.T) {
	ws := gitRepo(t)
	devContext(t, ws, 42)
	writeFile(t, filepath.Join(ws, "src", "added.go"), "package src\n")

	if got := strandedWorktrees(ws); len(got) != 0 {
		t.Fatalf("the workspace reported itself as stranded: %v", got)
	}
}
