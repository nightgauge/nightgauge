package gittest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// recordingTB captures what ReportWritersOnCleanup registers and reports, so
// the failure path can be asserted without failing the real test.
type recordingTB struct {
	testing.TB
	cleanups []func()
	errors   []string
	fatals   []string
}

func (r *recordingTB) Helper()                   {}
func (r *recordingTB) Cleanup(f func())          { r.cleanups = append(r.cleanups, f) }
func (r *recordingTB) Errorf(f string, a ...any) { r.errors = append(r.errors, fmt.Sprintf(f, a...)) }
func (r *recordingTB) Fatalf(f string, a ...any) { r.fatals = append(r.fatals, fmt.Sprintf(f, a...)) }

func (r *recordingTB) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func plant(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReportWritersOnCleanup_RemovesCleanDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	plant(t, dir, ".git/HEAD", "ref: refs/heads/main\n")

	rec := &recordingTB{TB: t}
	ReportWritersOnCleanup(rec, dir)
	if len(rec.cleanups) != 1 {
		t.Fatalf("registered %d cleanups, want 1", len(rec.cleanups))
	}
	rec.runCleanups()

	if len(rec.errors) != 0 || len(rec.fatals) != 0 {
		t.Fatalf("clean removal reported failures: errors=%q fatals=%q", rec.errors, rec.fatals)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still present after cleanup: stat err = %v", err)
	}
}

func TestReportWritersOnCleanup_RefusesDirOutsideTempDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if underTempDir(wd) {
		t.Skip("the package directory is itself inside os.TempDir() on this machine")
	}
	rec := &recordingTB{TB: t}
	ReportWritersOnCleanup(rec, wd)
	if len(rec.fatals) != 1 || len(rec.cleanups) != 0 {
		t.Fatalf("outside-TempDir dir: fatals=%q cleanups=%d, want one fatal and no cleanup", rec.fatals, len(rec.cleanups))
	}
	for _, d := range []string{"relative/path", os.TempDir()} {
		if underTempDir(d) {
			t.Errorf("underTempDir(%q) = true, want false", d)
		}
	}
}

// TestReportWritersOnCleanup_FailedRemovalNamesLeftovers forces the removal to
// fail with a read-only .git, standing in for a writer that keeps creating
// entries, and checks the failure names what is left and who likely left it.
func TestReportWritersOnCleanup_FailedRemovalNamesLeftovers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not block removal the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "repo")
	gitDir := filepath.Join(dir, ".git")
	plant(t, dir, ".git/gc.pid", "4242 build-host\n")
	plant(t, dir, ".git/index.lock", "")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	rec := &recordingTB{TB: t}
	ReportWritersOnCleanup(rec, dir)
	rec.runCleanups()

	if len(rec.errors) != 1 {
		t.Fatalf("failed removal reported %d errors, want 1: %q", len(rec.errors), rec.errors)
	}
	msg := rec.errors[0]
	for _, want := range []string{
		"cleanup of " + dir + " failed",
		".git/gc.pid",
		".git/index.lock",
		"git gc (detached): gc.pid = 4242 build-host",
		"a running git command holds .git/index.lock",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message lacks %q:\n%s", want, msg)
		}
	}
}

func TestDescribeLeftovers_HintsPerWriter(t *testing.T) {
	root := t.TempDir()
	plant(t, root, ".git/gc.pid", "123 host\n")
	plant(t, root, ".git/gc.log", "warning: There are too many unreachable loose objects\n")
	plant(t, root, ".git/objects/info/commit-graphs/graph-abc.graph", "xx")
	plant(t, root, ".git/objects/pack/tmp_pack_q1", "")
	plant(t, root, ".git/refs/heads/main.lock", "")

	got := describeLeftovers(root, time.Now().Add(-time.Second))
	for _, want := range []string{
		".git/objects/info/commit-graphs/graph-abc.graph  2 bytes  mtime +",
		"after cleanup began",
		"git gc (detached): gc.pid = 123 host",
		"an earlier detached git gc logged: warning: There are too many unreachable loose objects",
		"git maintenance or gc: commit-graph/multi-pack-index written (.git/objects/info/commit-graphs/",
		"git gc or repack: temporary pack .git/objects/pack/tmp_pack_q1",
		"a running git command holds .git/refs/heads/main.lock",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("describeLeftovers lacks %q:\n%s", want, got)
		}
	}
}

func TestDescribeLeftovers_CapsListing(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxLeftoverEntries+5; i++ {
		plant(t, root, fmt.Sprintf("f%03d", i), "")
	}
	got := describeLeftovers(root, time.Now())
	if !strings.Contains(got, "... 5 more entries not listed") {
		t.Errorf("listing not capped at %d:\n%s", maxLeftoverEntries, got)
	}
	if strings.Contains(got, "likely writer") {
		t.Errorf("plain files produced a writer hint:\n%s", got)
	}
}

func TestDescribeLeftovers_EmptyDir(t *testing.T) {
	got := describeLeftovers(t.TempDir(), time.Now())
	if !strings.Contains(got, "no entries") {
		t.Errorf("empty dir not reported as such:\n%s", got)
	}
}
