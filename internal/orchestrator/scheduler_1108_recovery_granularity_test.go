package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// #1108: the coherence guard's judgement was right and its granularity was
// wrong.
//
// The observed tree was 89 deletions of generated
// `*.g.dart` files (a `build_runner` step that never regenerated) alongside 11
// hand-edited source and test files that WERE the deliverable. The ratio was
// computed over the tree as a whole, so all 100 entries were refused and the 11
// good files, later verified to analyze clean and pass their tests, were thrown
// away with the 89 incoherent ones.
//
// A deleted regeneratable artifact and an edited source file are different
// kinds of change. The deletion ratio is evidence about DELETIONS; it says
// nothing about a modification, which cannot leave a half-transformed tree.
// This test builds that exact shape at small scale and pins both halves of the
// contract: the modifications are recovered, the deletions are withheld.
func TestRecoverUncommittedWork_RecoversEditsWhileWithholdingGeneratedDeletions(t *testing.T) {
	const (
		generatedFiles = 12 // N deletions — the regeneration that never happened
		sourceFiles    = 4  // M modifications — the actual deliverable
	)

	dir := t.TempDir()
	gitInitRepo(t, dir)

	genName := func(i int) string { return fmt.Sprintf("lib/models/model_%d.g.dart", i) }
	srcName := func(i int) string { return fmt.Sprintf("lib/features/feature_%d.dart", i) }

	writeFile := func(rel, contents string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < generatedFiles; i++ {
		writeFile(genName(i), "// GENERATED CODE - DO NOT MODIFY BY HAND\n")
	}
	for i := 0; i < sourceFiles; i++ {
		writeFile(srcName(i), "class Feature {}\n")
	}
	gitCommitAll(t, dir, "baseline")

	// The stage deleted every generated artifact and died before regenerating.
	for i := 0; i < generatedFiles; i++ {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(genName(i)))); err != nil {
			t.Fatal(err)
		}
	}
	// The deliverable: hand-edited sources, no deletions among them.
	const edited = "class Feature { void redirect() {} }\n"
	for i := 0; i < sourceFiles; i++ {
		writeFile(srcName(i), edited)
	}

	rec, err := RecoverUncommittedWork(dir, 1108, "feature-dev")
	if err != nil {
		t.Fatalf("RecoverUncommittedWork = %v — a deletion-dominated tree must still rescue the edits it can", err)
	}

	// The M modifications are recovered.
	for i := 0; i < sourceFiles; i++ {
		if got := gitShowFile(t, dir, "HEAD:"+srcName(i)); got != edited {
			t.Errorf("%s in HEAD = %q, want %q — a hand-edited source was discarded with the deletions",
				srcName(i), got, edited)
		}
	}

	// The N deletions are withheld: still in HEAD, still gone on disk, still
	// reported so nobody is told the tree was rescued whole.
	head := gitHeadPaths(t, dir)
	for i := 0; i < generatedFiles; i++ {
		if !head[genName(i)] {
			t.Errorf("%s left HEAD — a mid-transformation deletion was published", genName(i))
		}
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(genName(i)))); !os.IsNotExist(statErr) {
			t.Errorf("%s was restored on disk; the withheld work must stay in the worktree", genName(i))
		}
	}
	if len(rec.WithheldDeletions) != generatedFiles {
		t.Errorf("withheld %d deletion(s), want %d", len(rec.WithheldDeletions), generatedFiles)
	}
	if rec.WithheldReason == "" {
		t.Error("partial rescue reported no reason — the caller cannot tell the operator what stayed behind")
	}
	if !hasUncommittedWork(dir) {
		t.Error("worktree is clean after a partial rescue; the withheld deletions must remain in it")
	}
}
