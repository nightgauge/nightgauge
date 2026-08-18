package runstate

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestArchiveRun_AnchoredSuffix_DoesNotMoveUnrelatedIssue pins the #654 fix:
// archiving issue 33 must match files ending in "-33.json" only. Before the
// fix, the match was an unanchored numeric suffix ("33.json"), which also
// matched issue-633.json (and every other -N33.json file) because "633.json"
// ends with "33.json". That silently relocated a concurrently running
// issue's live context files into issue 33's archive directory.
func TestArchiveRun_AnchoredSuffix_DoesNotMoveUnrelatedIssue(t *testing.T) {
	dir := t.TempDir()

	// Issue 33's own context files — all six kinds, all must move.
	own := []string{
		"issue-33.json",
		"planning-33.json",
		"dev-33.json",
		"validate-33.json",
		"pr-33.json",
		"feedback-33.json",
	}
	// Issue 633's context files — none of these must move. "issue-633.json"
	// ends with "33.json" and is exactly the collision the unanchored suffix
	// produced.
	other := []string{
		"issue-633.json",
		"planning-633.json",
		"dev-633.json",
		"validate-633.json",
		"pr-633.json",
		"feedback-633.json",
	}

	for _, name := range append(append([]string{}, own...), other...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	// run-state.json itself must never be matched/moved by the suffix scan.
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{}`), 0644); err != nil {
		t.Fatalf("seed %s: %v", FileName, err)
	}

	rs := &RunState{
		SchemaVersion:   SchemaVersion,
		IssueNumber:     33,
		State:           StateCompleted,
		RunID:           "run-archive-anchor-test",
		AttemptNumber:   1,
		CompletedStages: []Stage{},
		Branch:          "fix/33-something",
		CreatedAt:       "2026-01-01T00:00:00Z",
		UpdatedAt:       "2026-01-01T00:00:00Z",
		Attempts:        []Attempt{{RunID: "run-archive-anchor-test", AttemptNumber: 1, StartedAt: "2026-01-01T00:00:00Z"}},
	}

	archiveDir, err := ArchiveRun(dir, rs)
	if err != nil {
		t.Fatalf("ArchiveRun: %v", err)
	}

	// All six of issue 33's own context files must have moved.
	for _, name := range own {
		if _, err := os.Stat(filepath.Join(archiveDir, name)); err != nil {
			t.Errorf("expected %s to be moved into archive dir, but it wasn't: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be gone from base dir, still present (err=%v)", name, err)
		}
	}

	// None of issue 633's context files may have moved.
	for _, name := range other {
		if _, err := os.Stat(filepath.Join(archiveDir, name)); err == nil {
			t.Errorf("issue 633 file %s was incorrectly moved into issue 33's archive dir", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("issue 633 file %s should still be in base dir, got err: %v", name, err)
		}
	}

	// run-state.json must remain in the base dir (a fresh snapshot is
	// written into the archive dir separately, but the live file is never
	// matched/moved by the suffix scan).
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Errorf("run-state.json should remain in base dir: %v", err)
	}

	// Sanity: base dir now contains exactly the 6 issue-633 files plus
	// run-state.json (and the history/ dir ArchiveRun creates) — nothing
	// else leaked or was left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var remaining []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		remaining = append(remaining, e.Name())
	}
	sort.Strings(remaining)
	want := append(append([]string{}, other...), FileName)
	sort.Strings(want)
	if len(remaining) != len(want) {
		t.Errorf("base dir contents = %v, want %v", remaining, want)
	}
}
