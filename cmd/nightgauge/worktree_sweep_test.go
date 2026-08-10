package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// #410 gap 1. `worktree sweep` called execution.SweepMergedWorktrees with no
// ActiveIssues at all, so SkipActiveRun was unreachable from the CLI: one
// `nightgauge worktree sweep` while any run was mid-flight past its merge ran
// `git worktree remove --force` on the directory that run was still executing
// in. These tests drive the real cobra command against real git repos and real
// snapshots written by the state package's own writer — the defect WAS an
// unwired call site, so nothing short of the production attach path proves it.

// sweepRepo builds an "origin + clone" pair holding one pipeline worktree whose
// branch is already squash-merged onto main, i.e. a worktree the sweep will
// reclaim unless something protects it.
func sweepRepo(t *testing.T, issue int) (root, worktree string) {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	clone := filepath.Join(base, "clone")

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git(base, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	git(seed, "init", "-b", "main")
	git(seed, "config", "user.email", "test@test")
	git(seed, "config", "user.name", "test")
	write(filepath.Join(seed, "README"), "hello\n")
	git(seed, "add", ".")
	git(seed, "commit", "-m", "initial")
	git(seed, "remote", "add", "origin", origin)
	git(seed, "push", "-u", "origin", "main")

	git(base, "clone", origin, clone)
	git(clone, "config", "user.email", "test@test")
	git(clone, "config", "user.name", "test")

	resolved, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatalf("resolve clone path: %v", err)
	}
	root = resolved

	branch := "fix/" + strconv.Itoa(issue) + "-work"
	worktree = filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue))
	git(root, "worktree", "add", worktree, "-b", branch, "origin/main")
	write(filepath.Join(worktree, "fix.txt"), "fixed\n")
	git(worktree, "add", ".")
	git(worktree, "commit", "-m", "work")
	git(root, "merge", "--squash", branch)
	git(root, "commit", "-m", "squash: "+branch)
	git(root, "push", "origin", "main")
	git(root, "fetch", "origin")
	return root, worktree
}

// writeLiveSnapshot persists a NON-terminal runtime snapshot for issue through
// the state package's own writer — never hand-authored JSON of the shape under
// test. pid is what the run's stage child would be; 0 means "no live child".
func writeLiveSnapshot(t *testing.T, root string, issue, pid int) string {
	t.Helper()
	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatalf("mint run id: %v", err)
	}
	rs := state.NewRuntimeState("owner/clone", issue, "item-"+strconv.Itoa(issue), runID)
	if pid > 0 {
		rs.SetProcess(pid, filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue)))
	}
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	return filepath.Join(dir, state.SnapshotFilename(issue, runID))
}

func runWorktreeSweep(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := worktreeSweepCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// runWorktreeSweepSplit keeps the two streams apart, which the --json tests need:
// the human runner above merges them so a `[WARN]` line lands in front of the
// document and no decoder can read it.
func runWorktreeSweepSplit(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := worktreeSweepCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestWorktreeSweep_SkipsIssueWithNonTerminalSnapshot is the gap-1 defect. The
// branch is merged, so every content-based guard clears — the ONLY thing that
// can protect this directory is an in-flight set, and the CLI had none.
func TestWorktreeSweep_SkipsIssueWithNonTerminalSnapshot(t *testing.T) {
	root, wt := sweepRepo(t, 811)
	writeLiveSnapshot(t, root, 811, os.Getpid())

	out, err := runWorktreeSweep(t, "--workdir", root)
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(wt); statErr != nil {
		t.Fatalf("a live run's worktree was reclaimed by the CLI sweep: %v\noutput:\n%s", statErr, out)
	}
	if !strings.Contains(out, "active-run") {
		t.Errorf("the skip does not report active-run; output:\n%s", out)
	}
}

// TestWorktreeSweep_ReclaimsWhenSnapshotIsStale is the other half of the same
// predicate, and the reason the guard is a LIVENESS bound and not "any
// non-terminal snapshot exists". A run killed mid-flight (window reload, crash,
// SIGKILL) leaves its non-terminal snapshot behind forever — the Go-scheduler
// path never latches terminal and never removes it — and that leaked worktree is
// the entire population this sweep exists to reclaim (#110). Protecting it on
// the strength of a snapshot nothing has touched in hours turns the operator's
// command into a permanent no-op, which is #403's structural-no-op defect
// re-created in the opposite direction.
func TestWorktreeSweep_ReclaimsWhenSnapshotIsStale(t *testing.T) {
	root, wt := sweepRepo(t, 812)
	// pid 0: the run's stage child is gone, as it is after a crash.
	snap := writeLiveSnapshot(t, root, 812, 0)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(snap, old, old); err != nil {
		t.Fatalf("age the snapshot: %v", err)
	}

	out, err := runWorktreeSweep(t, "--workdir", root)
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("a crashed run's leaked worktree was not reclaimed (err=%v) — the sweep's primary use is now unreachable\noutput:\n%s", statErr, out)
	}
}

// TestWorktreeSweep_PrintsTheReclaimDoor is gap 3 at the CLI surface. The sweep
// has two reclaim rules and only one of them compares content; an operator
// auditing a removal cannot tell them apart from a line that names neither.
func TestWorktreeSweep_PrintsTheReclaimDoor(t *testing.T) {
	root, _ := sweepRepo(t, 814)

	out, err := runWorktreeSweep(t, "--workdir", root, "--dry-run")
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	if !strings.Contains(out, "door content-merged") {
		t.Errorf("the reclaim line does not name the door that authorized it; output:\n%s", out)
	}
}

// TestWorktreeSweep_DefaultSweepsEveryWorkspaceRoot is gap 1's second half. A
// run's worktree is created in its TARGET repo (#229), so a sweep rooted only at
// the invocation directory is blind to exactly the cross-repo leftovers that
// accumulate fastest — the operator has to remember to re-run the command once
// per repo, and nothing tells them when they forget.
func TestWorktreeSweep_DefaultSweepsEveryWorkspaceRoot(t *testing.T) {
	primary, wtPrimary := sweepRepo(t, 821)
	sibling, wtSibling := sweepRepo(t, 822)

	// The manifest shape config.WorkspaceRepoRoots reads: a primary repo
	// carrying .vscode/nightgauge-workspace.yaml that names the sibling by
	// absolute path (the temp roots are not siblings on disk).
	if err := os.MkdirAll(filepath.Join(primary, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "repositories:\n" +
		"  - name: primary\n    path: .\n    project_number: 3\n" +
		"  - name: sibling\n    path: " + sibling + "\n    project_number: 4\n"
	if err := os.WriteFile(filepath.Join(primary, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(primary)
	out, err := runWorktreeSweep(t)
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}

	for _, wt := range []string{wtPrimary, wtSibling} {
		if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
			t.Errorf("a bare sweep did not reclaim %s (err=%v)\noutput:\n%s", wt, statErr, out)
		}
	}
	if !strings.Contains(out, sibling) {
		t.Errorf("multi-root output does not label the sibling root; output:\n%s", out)
	}
}

// TestWorktreeSweep_WorkdirIsASingleRootOverride: --workdir is now the explicit
// narrowing, and it must NOT reach the sibling repo the manifest names.
func TestWorktreeSweep_WorkdirIsASingleRootOverride(t *testing.T) {
	primary, wtPrimary := sweepRepo(t, 831)
	sibling, wtSibling := sweepRepo(t, 832)

	if err := os.MkdirAll(filepath.Join(primary, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "repositories:\n" +
		"  - name: primary\n    path: .\n    project_number: 3\n" +
		"  - name: sibling\n    path: " + sibling + "\n    project_number: 4\n"
	if err := os.WriteFile(filepath.Join(primary, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(primary)
	out, err := runWorktreeSweep(t, "--workdir", primary)
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(wtPrimary); !os.IsNotExist(statErr) {
		t.Errorf("the named root was not swept (err=%v)\noutput:\n%s", statErr, out)
	}
	if _, statErr := os.Stat(wtSibling); statErr != nil {
		t.Errorf("--workdir reached beyond the root it named: %v\noutput:\n%s", statErr, out)
	}
}

// TestWorktreeSweep_ReclaimsWhenNoSnapshotExists pins the residual this design
// accepts: an issue with NO snapshot is not protected. That is deliberate —
// the orchestrator's session-start reconcile has to keep reclaiming hand-made
// `.nightgauge/worktrees/issue-N` directories, which never had a snapshot, and a
// live run with no snapshot at all is a bug elsewhere (ADR-017 Decision 8 says
// every dispatched run writes one).
func TestWorktreeSweep_ReclaimsWhenNoSnapshotExists(t *testing.T) {
	root, wt := sweepRepo(t, 813)

	out, err := runWorktreeSweep(t, "--workdir", root)
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("a merged worktree with no snapshot must still be reclaimable (err=%v)\noutput:\n%s", statErr, out)
	}
}

// addWorktree registers a second pipeline worktree carrying a commit of its own,
// i.e. a worktree the sweep will NOT reclaim — the directory an agent is standing
// in while it runs the documented bare command.
func addWorktree(t *testing.T, root string, issue int) string {
	t.Helper()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
		}
	}
	wt := filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue))
	git(root, "worktree", "add", wt, "-b", "fix/"+strconv.Itoa(issue)+"-work", "origin/main")
	if err := os.WriteFile(filepath.Join(wt, "own.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(wt, "add", ".")
	git(wt, "commit", "-m", "own work")
	return wt
}

// TestWorktreeSweep_FromInsideALinkedWorktreeStillProtectsTheLiveRun is the
// dogfood shape, and the one that made every protection in this command
// unreachable.
//
// Every pipeline stage runs INSIDE a worktree, so "cwd is a linked worktree" is
// the normal case, not an edge. `git worktree list` from there enumerates the
// whole repository — so the sweep's reach is undiminished — while the worktree's
// own .nightgauge/pipeline directory exists (the .gitkeep is tracked) and is
// always empty, so the in-flight scan returned a DETERMINED empty set with no
// error and no warning. Agent A, standing in its own worktree and running the
// documented bare command, deleted the directory live run B was executing in.
func TestWorktreeSweep_FromInsideALinkedWorktreeStillProtectsTheLiveRun(t *testing.T) {
	root, wtLive := sweepRepo(t, 902)
	writeLiveSnapshot(t, root, 902, os.Getpid())
	standingIn := addWorktree(t, root, 903)

	t.Chdir(standingIn)
	out, err := runWorktreeSweep(t) // NOT --dry-run: the harm is the removal
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}

	if _, statErr := os.Stat(wtLive); statErr != nil {
		t.Fatalf("a live run's worktree was REMOVED by a sweep run from a sibling worktree (%v)\noutput:\n%s", statErr, out)
	}
	if !strings.Contains(out, "active-run") {
		t.Errorf("the live run was not reported as protected; output:\n%s", out)
	}
}

// TestWorktreeSweep_WorkdirNamingAWorktreeIsCanonicalized: an operator can point
// --workdir at a worktree (that is where their shell already is), and the
// explicit override must not be the one path that skips canonicalization.
func TestWorktreeSweep_WorkdirNamingAWorktreeIsCanonicalized(t *testing.T) {
	root, wtLive := sweepRepo(t, 904)
	writeLiveSnapshot(t, root, 904, os.Getpid())
	other := addWorktree(t, root, 905)

	out, err := runWorktreeSweep(t, "--workdir", other)
	if err != nil {
		t.Fatalf("sweep: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(wtLive); statErr != nil {
		t.Fatalf("--workdir pointing at a worktree lost the in-flight protection (%v)\noutput:\n%s", statErr, out)
	}
	if !strings.Contains(out, "active-run") {
		t.Errorf("the live run was not reported as protected; output:\n%s", out)
	}
}

// TestWorktreeSweep_CanonicalizationDedupesTheSameRepository is the second half of
// the same defect. In the dogfood shape (cwd = a worktree, manifest naming the
// main root) the SAME repository resolved twice, worktree first — so the
// unprotected pass ran before the protected one and the protection could not
// matter even when the correct root was in the set. Canonicalizing before the
// dedupe collapses both entries into the one repository they always were.
func TestWorktreeSweep_CanonicalizationDedupesTheSameRepository(t *testing.T) {
	root, wtLive := sweepRepo(t, 906)
	writeLiveSnapshot(t, root, 906, os.Getpid())
	standingIn := addWorktree(t, root, 907)

	if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "repositories:\n  - name: primary\n    path: " + root + "\n    project_number: 3\n"
	if err := os.WriteFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(standingIn)
	out, errOut, err := runWorktreeSweepSplit(t, "--json")
	if err != nil {
		t.Fatalf("sweep: %v (%s%s)", err, out, errOut)
	}

	var decoded struct {
		Results []struct {
			RepoRoot string `json:"repoRoot"`
		} `json:"results"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &decoded); jsonErr != nil {
		t.Fatalf("decode --json: %v\noutput:\n%s", jsonErr, out)
	}
	if len(decoded.Results) != 1 {
		t.Errorf("the same repository was swept %d times, want 1: %+v", len(decoded.Results), decoded.Results)
	}
	for _, r := range decoded.Results {
		if r.RepoRoot != root {
			t.Errorf("repoRoot = %q, want the repository's main checkout %q", r.RepoRoot, root)
		}
	}
	if _, statErr := os.Stat(wtLive); statErr != nil {
		t.Fatalf("a live run's worktree was removed (%v)\noutput:\n%s", statErr, out)
	}
}

// TestWorktreeSweep_JSONContractMatchesTheExtensionParser pins the document
// HeadlessOrchestrator.runStartupWorktreeSweep parses, on the Go side.
//
// Two things were unpinned. The field names had no test at all, so renaming
// `results`, `door` or `skippedRoots` would silently mute the extension's
// startup-sweep log rather than fail anything. And the JSON went to os.Stdout via
// fmt.Println instead of the cobra writer, so no cmd-level test could capture it
// even if one existed — every test here exercised only the human output.
//
// The struct below is deliberately a COPY of the extension's shape rather than a
// reference to worktreeSweepOutput: asserting a type against itself would pass
// through any rename.
func TestWorktreeSweep_JSONContractMatchesTheExtensionParser(t *testing.T) {
	root, _ := sweepRepo(t, 908)
	// A corrupt snapshot is the cheapest way to populate `warnings` — the scan
	// treats an unreadable body as ACTIVE and says so.
	corruptRunID, idErr := runstate.NewRunID()
	if idErr != nil {
		t.Fatalf("mint run id: %v", idErr)
	}
	corrupt := filepath.Join(state.PipelineStateDir(root), state.SnapshotFilename(909, corruptRunID))
	if err := os.MkdirAll(filepath.Dir(corrupt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runWorktreeSweepSplit(t, "--workdir", root, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("sweep: %v (%s%s)", err, out, errOut)
	}

	var decoded struct {
		Results []struct {
			RepoRoot  string `json:"repoRoot"`
			Reclaimed []struct {
				Path        string `json:"path"`
				Branch      string `json:"branch"`
				IssueNumber int    `json:"issueNumber"`
				Door        string `json:"door"`
			} `json:"reclaimed"`
			Errors []string `json:"errors"`
		} `json:"results"`
		Warnings     []string `json:"warnings"`
		SkippedRoots []struct {
			Root   string `json:"root"`
			Reason string `json:"reason"`
		} `json:"skippedRoots"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &decoded); jsonErr != nil {
		t.Fatalf("the extension's parser cannot read this document: %v\noutput:\n%s", jsonErr, out)
	}

	if len(decoded.Results) != 1 {
		t.Fatalf("results = %d, want 1; output:\n%s", len(decoded.Results), out)
	}
	res := decoded.Results[0]
	if res.RepoRoot != root {
		t.Errorf("repoRoot = %q, want %q", res.RepoRoot, root)
	}
	if len(res.Reclaimed) != 1 {
		t.Fatalf("reclaimed = %d, want 1; output:\n%s", len(res.Reclaimed), out)
	}
	got := res.Reclaimed[0]
	if got.IssueNumber != 908 || got.Branch != "fix/908-work" || got.Path == "" {
		t.Errorf("reclaimed[0] = %+v, want issue 908 on fix/908-work with a path", got)
	}
	if got.Door != "content-merged" {
		t.Errorf("reclaimed[0].door = %q, want content-merged — the extension logs this verbatim", got.Door)
	}
	if len(decoded.Warnings) == 0 {
		t.Errorf("warnings is empty although the scan guessed on a corrupt snapshot; output:\n%s", out)
	}
}

// TestWorktreeSweep_SkippedRootsCarryTheirReason: three different failures land in
// skippedRoots and they are not interchangeable, so the entry names its own cause
// instead of leaving every consumer to assert one for all three.
func TestWorktreeSweep_SkippedRootsCarryTheirReason(t *testing.T) {
	root, _ := sweepRepo(t, 910)
	notARepo := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "repositories:\n" +
		"  - name: primary\n    path: " + root + "\n    project_number: 3\n" +
		"  - name: junk\n    path: " + notARepo + "\n    project_number: 4\n"
	if err := os.WriteFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(notARepo))
	out, errOut, err := runWorktreeSweepSplit(t, "--dry-run", "--json")
	if err != nil {
		t.Fatalf("sweep: %v (%s%s)", err, out, errOut)
	}
	if !strings.Contains(errOut, "not-a-git-repo") {
		t.Errorf("the skip was not reported on stderr with its reason; got:\n%s", errOut)
	}

	var decoded struct {
		SkippedRoots []struct {
			Root   string `json:"root"`
			Reason string `json:"reason"`
		} `json:"skippedRoots"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &decoded); jsonErr != nil {
		t.Fatalf("decode --json: %v\noutput:\n%s", jsonErr, out)
	}
	found := false
	for _, s := range decoded.SkippedRoots {
		if s.Root == notARepo {
			found = true
			if s.Reason != "not-a-git-repo" {
				t.Errorf("skipped %s with reason %q, want not-a-git-repo", s.Root, s.Reason)
			}
		}
	}
	if !found {
		t.Errorf("a root that resolves to no main checkout was not reported as skipped: %+v\noutput:\n%s", decoded.SkippedRoots, out)
	}
}
