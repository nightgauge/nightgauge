package ci

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// #2941: only files the branch adds; a modified file already on the base is
// not one this change introduced.
func TestAddedFilesAgainstDefaultBase(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := AddedFilesAgainstDefaultBase(dir); got != nil {
		t.Errorf("no repository is no diff, got %v", got)
	}
	gittest.Run(t, dir, "init", "-q", "-b", "main")
	write("old_test.go", "package x\n")
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "base")
	gittest.Run(t, dir, "checkout", "-q", "-b", "feat")
	write("old_test.go", "package x\n// changed\n")
	write("new_test.go", "package x\n")
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "work")

	if got := AddedFilesAgainstDefaultBase(dir); len(got) != 1 || got[0] != "new_test.go" {
		t.Errorf("AddedFilesAgainstDefaultBase = %v, want [new_test.go]", got)
	}
	if got := ChangedFilesAgainstDefaultBase(dir); len(got) != 2 {
		t.Errorf("ChangedFilesAgainstDefaultBase = %v, want both files", got)
	}
}
