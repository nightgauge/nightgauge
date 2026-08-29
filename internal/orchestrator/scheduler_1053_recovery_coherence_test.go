package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #1053: the auto-recovery rescue published a tree that could not build.
//
// A real run deleted 89 generated files against 10 edited ones and committed
// the result to the issue branch AND pushed it to origin, under a message that
// reads as success. The surviving sources still declared the deleted artifacts
// (`part 'router.g.dart';` for a file the same commit removed), so the branch
// was a compile error wearing a plausible commit.
//
// The guard withholds the DELETIONS rather than refusing the whole tree
// (narrowed by #1108). The rename test below is why the withheld set is read
// from the INDEX and not from the worktree column: withholding every
// worktree-column deletion commits BOTH halves of every rename and breaks a
// case that works today. After `git add -A` a rename is a single `R`, so its
// source is never in the withheld set.

// gitCommitAll is a small local helper: stage everything and commit, failing the
// test on error. Kept separate from the recovery path under test.
func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-m", msg).CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}

func gitHeadPaths(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", "HEAD").Output()
	if err != nil {
		t.Fatalf("git ls-tree: %v", err)
	}
	paths := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p != "" {
			paths[p] = true
		}
	}
	return paths
}

// gitShowFile returns a path's contents at a revision, failing the test on
// error. Used to assert what the recovery commit actually published.
func gitShowFile(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "show", rev).Output()
	if err != nil {
		t.Fatalf("git show %s: %v", rev, err)
	}
	return string(out)
}

// TestRecoverUncommittedWork_WithholdsDeletionsFromADominatedTree reproduces
// the #1053 shape: many tracked files removed from disk, a couple edited,
// nothing regenerated. The deletions must stay out of the commit — that is the
// coherence property — while the edits, which carry no mid-transformation risk,
// are rescued rather than discarded with them (#1108).
func TestRecoverUncommittedWork_WithholdsDeletionsFromADominatedTree(t *testing.T) {
	dir := t.TempDir()
	gitInitRepo(t, dir)

	// A committed baseline: 6 generated artifacts and 2 sources.
	for i := 0; i < 6; i++ {
		name := filepath.Join(dir, "gen"+string(rune('a'+i))+".g.txt")
		if err := os.WriteFile(name, []byte("generated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"src_one.txt", "src_two.txt"} {
		if err := os.WriteFile(filepath.Join(dir, s), []byte("source\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, dir, "baseline")

	// The mid-transformation tree: generated output removed, sources edited,
	// nothing regenerated.
	for i := 0; i < 6; i++ {
		if err := os.Remove(filepath.Join(dir, "gen"+string(rune('a'+i))+".g.txt")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src_one.txt"), []byte("source edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, err := RecoverUncommittedWork(dir, 1053, "feature-dev")
	if err != nil {
		t.Fatalf("RecoverUncommittedWork = %v, want the edits rescued with the deletions withheld", err)
	}
	if rec.WithheldReason == "" {
		t.Error("nothing reported as withheld; a deletion-dominated tree must say what it held back")
	}
	if len(rec.WithheldDeletions) != 6 {
		t.Errorf("withheld %d deletion(s), want 6", len(rec.WithheldDeletions))
	}

	// The deletions must NOT be published: every artifact is still in HEAD.
	head := gitHeadPaths(t, dir)
	for i := 0; i < 6; i++ {
		name := "gen" + string(rune('a'+i)) + ".g.txt"
		if !head[name] {
			t.Errorf("%s left HEAD — the incoherent half was published", name)
		}
	}
	// They are still deleted on disk, so the next attempt sees the same tree.
	if _, statErr := os.Stat(filepath.Join(dir, "gena.g.txt")); !os.IsNotExist(statErr) {
		t.Errorf("withheld deletion was undone on disk: %v", statErr)
	}
	if !hasUncommittedWork(dir) {
		t.Error("worktree is clean; the withheld deletions must remain in it")
	}
	// The edit IS published — it is the deliverable.
	if got := gitShowFile(t, dir, "HEAD:src_one.txt"); got != "source edited\n" {
		t.Errorf("src_one.txt in HEAD = %q, want the edited contents — the deliverable was discarded", got)
	}
}

// TestRecoverUncommittedWork_CommitsARenameCoherently is the case that refutes
// the obvious fix. A filesystem rename is `" D old"` + `"?? new"` before
// staging — the same column shape as a mid-transformation deletion. Withholding
// worktree-column deletions would commit BOTH paths, a duplicate-declaration
// build break on a case that is correct today.
//
// `git add -A` resolves the pair to a single `R`, so the guard must not fire.
func TestRecoverUncommittedWork_CommitsARenameCoherently(t *testing.T) {
	dir := t.TempDir()
	gitInitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("stable contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, dir, "baseline")

	// Rename on disk: delete + untracked add, which is the ambiguous shape.
	if err := os.Rename(filepath.Join(dir, "old.txt"), filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}

	if _, err := RecoverUncommittedWork(dir, 1053, "feature-dev"); err != nil {
		t.Fatalf("RecoverUncommittedWork refused a plain rename: %v", err)
	}

	head := gitHeadPaths(t, dir)
	if head["old.txt"] && head["new.txt"] {
		t.Error("HEAD contains BOTH sides of the rename — the recovery commit is incoherent")
	}
	if !head["new.txt"] {
		t.Error("HEAD is missing the rename destination")
	}
	if head["old.txt"] {
		t.Error("HEAD still carries the rename source")
	}
}

// TestRecoverUncommittedWork_CommitsAMinorityDeletion keeps the guard honest in
// the other direction: deleting one file while adding real work is ordinary and
// must still be rescued.
func TestRecoverUncommittedWork_CommitsAMinorityDeletion(t *testing.T) {
	dir := t.TempDir()
	gitInitRepo(t, dir)
	for _, s := range []string{"a.txt", "b.txt", "c.txt", "doomed.txt"} {
		if err := os.WriteFile(filepath.Join(dir, s), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, dir, "baseline")

	if err := os.Remove(filepath.Join(dir, "doomed.txt")); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, s), []byte("v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := RecoverUncommittedWork(dir, 1053, "feature-dev"); err != nil {
		t.Fatalf("RecoverUncommittedWork refused a normal edit-with-one-deletion: %v", err)
	}
	if head := gitHeadPaths(t, dir); head["doomed.txt"] {
		t.Error("the deletion was not committed")
	}
}

// TestRecoverUncommittedWork_RescuesAPurelyDeletionalDeliverable pins the
// #332/#701 contract inside this guard's own test file, so a future tightening
// of the ratio cannot quietly reintroduce the defect that destroyed a
// bookkeeping deliverable. A stage whose deliverable IS removal arrives as
// removals and nothing else, and must still be rescued.
func TestRecoverUncommittedWork_RescuesAPurelyDeletionalDeliverable(t *testing.T) {
	dir := t.TempDir()
	gitInitRepo(t, dir)
	for _, s := range []string{"one.json", "two.json", "three.json"} {
		if err := os.WriteFile(filepath.Join(dir, s), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitAll(t, dir, "baseline")

	// Removal is the whole deliverable: nothing else changes.
	for _, s := range []string{"one.json", "two.json", "three.json"} {
		if err := os.Remove(filepath.Join(dir, s)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := RecoverUncommittedWork(dir, 1053, "feature-dev"); err != nil {
		t.Fatalf("RecoverUncommittedWork refused a purely deletional deliverable: %v", err)
	}
	head := gitHeadPaths(t, dir)
	for _, s := range []string{"one.json", "two.json", "three.json"} {
		if head[s] {
			t.Errorf("%s survived; the deletion deliverable was not committed", s)
		}
	}
}

// TestStagedDeletions_CountsRenamesAsOneNonDeletion pins the counting
// contract directly, so a future change to the parser cannot silently
// reintroduce the rename miscount.
func TestStagedDeletions_CountsRenamesAsOneNonDeletion(t *testing.T) {
	dir := t.TempDir()
	gitInitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("stable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, dir, "baseline")
	if err := os.Rename(filepath.Join(dir, "old.txt"), filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	deletions, total := stagedDeletions(dir)
	if len(deletions) != 0 {
		t.Errorf("deletions = %v, want none — a rename is not a deletion", deletions)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 — a rename is one entry, not two", total)
	}
}
