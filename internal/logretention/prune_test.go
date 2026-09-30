package logretention

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

var testNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// fixture writes name with size bytes and an mtime age before testNow.
func fixture(t *testing.T, dir, name string, size int, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := testNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err == nil && info.Mode().IsRegular() && e.Name()[0] != '.' {
			n += info.Size()
		}
	}
	return n
}

const day = 24 * time.Hour

// TestPruneSizeAndAge is the Verification fixture: files of known sizes and
// mtimes; afterwards the total is within the cap, nothing older than the age
// cap remains, and the open file survives although it is the oldest file and
// past the age cap.
func TestPruneSizeAndAge(t *testing.T) {
	dir := t.TempDir()
	open := fixture(t, dir, "go-backend.log", 400, 60*day) // oldest, over age, open
	old := fixture(t, dir, "2026-08-01_session.log", 100, 45*day)
	a := fixture(t, dir, "a.log", 300, 10*day)
	b := fixture(t, dir, "b.log", 300, 5*day)
	c := fixture(t, dir, "c.log", 300, 2*day)
	keep := fixture(t, dir, ".gitkeep", 0, 90*day)

	pol := Policy{MaxBytes: 1100, MaxAge: 30 * day}
	res, err := Prune(dir, Options{Policy: pol, Now: testNow, Open: []string{"go-backend.log"}, InFlight: func(int) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(open) {
		t.Fatal("the open file was deleted")
	}
	if !exists(keep) {
		t.Fatal(".gitkeep was deleted")
	}
	if exists(old) {
		t.Error("a file past the age cap survived")
	}
	// 400 + 300*3 = 1300 > 1100: the oldest prunable (a.log) goes, leaving 1000.
	if exists(a) || !exists(b) || !exists(c) {
		t.Errorf("size pass removed the wrong files: a=%v b=%v c=%v", exists(a), exists(b), exists(c))
	}
	if got := dirSize(t, dir); got > pol.MaxBytes || got != res.SizeAfter {
		t.Errorf("size after = %d (result %d), want <= %d", got, res.SizeAfter, pol.MaxBytes)
	}
	if res.OverCap {
		t.Error("OverCap set although the directory is under its cap")
	}
	if res.SizeBefore != 1400 || res.Files != 5 {
		t.Errorf("before = %d bytes in %d files, want 1400 in 5", res.SizeBefore, res.Files)
	}
	for _, f := range res.Deleted {
		if f.Name == "go-backend.log" {
			t.Fatal("result lists the open file as deleted")
		}
	}
}

// TestPruneDefaultOpenSetIsLiveFiles: with no explicit Open list the live
// writers' files are never deleted: only the current day's ledger segment. Earlier segments, size backups and the pre-segment ledger
// are prunable, which is what bounds the ledger.
func TestPruneDefaultOpenSetIsLiveFiles(t *testing.T) {
	dir := t.TempDir()
	var live []string
	for _, n := range LiveFiles(testNow) {
		live = append(live, fixture(t, dir, n, 1000, 100*day))
	}
	if len(live) != 1 || filepath.Base(live[0]) != "github-api-2026-09-29.jsonl" {
		t.Fatalf("live files = %v, want today's ledger segment only", live)
	}
	var prunable []string
	for _, n := range []string{
		"github-api-2026-09-28.jsonl", "github-api-2026-09-29.jsonl.1",
		"github-api.jsonl", "github-api.jsonl.1",
	} {
		prunable = append(prunable, fixture(t, dir, n, 1000, 100*day))
	}
	if _, err := Prune(dir, Options{Policy: Policy{MaxBytes: 1, MaxAge: day}, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	for _, p := range live {
		if !exists(p) {
			t.Errorf("%s was deleted", filepath.Base(p))
		}
	}
	for _, p := range prunable {
		if exists(p) {
			t.Errorf("%s is prunable and should be gone", filepath.Base(p))
		}
	}
}

// TestPruneKeepsInFlightRunLogs: a run that is not terminal keeps its logs
// past the age cap, and when only such files remain over the cap the
// directory is reported as over it.
func TestPruneKeepsInFlightRunLogs(t *testing.T) {
	dir := t.TempDir()
	paused := fixture(t, dir, "2026-08-20_42_session.log", 500, 40*day)
	extra := fixture(t, dir, "2026-08-20_42_retry_session.log", 500, 40*day)
	done := fixture(t, dir, "2026-08-20_7_session.log", 500, 40*day)
	res, err := Prune(dir, Options{
		Policy:   Policy{MaxBytes: 100, MaxAge: 30 * day},
		Now:      testNow,
		InFlight: func(n int) bool { return n == 42 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(paused) || !exists(extra) {
		t.Fatal("a paused run's logs were deleted")
	}
	if exists(done) {
		t.Error("a terminal run's old log survived")
	}
	if !res.OverCap {
		t.Error("OverCap = false with only in-flight files over the cap")
	}
}

// TestPruneUndeterminedRunStateKeepsIssueLogs: without an in-flight answer
// every issue-keyed file is kept; unkeyed files are still pruned.
func TestPruneUndeterminedRunStateKeepsIssueLogs(t *testing.T) {
	dir := t.TempDir()
	keyed := fixture(t, dir, "2026-08-20_42_session.log", 10, 40*day)
	unkeyed := fixture(t, dir, "2026-08-20_session.log", 10, 40*day)
	if _, err := Prune(dir, Options{Policy: Policy{MaxAge: 30 * day}, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	if !exists(keyed) {
		t.Error("an issue-keyed log was deleted with the run state undetermined")
	}
	if exists(unkeyed) {
		t.Error("an unkeyed old log survived")
	}
}

// TestPruneRecentFilesSurvive: a file written within RecentWindow may be open
// by a writer that opens per event, so it is kept even over the cap.
func TestPruneRecentFilesSurvive(t *testing.T) {
	dir := t.TempDir()
	fresh := fixture(t, dir, "sanitization.log", 1000, time.Minute)
	res, err := Prune(dir, Options{Policy: Policy{MaxBytes: 10}, Now: testNow, InFlight: func(int) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(fresh) || !res.OverCap {
		t.Errorf("recent file kept = %v, over cap = %v; want true, true", exists(fresh), res.OverCap)
	}
}

// TestPruneRefusesSymlinks: a symlink entry is neither followed nor deleted,
// and a log directory that is itself a symlink is refused outright.
func TestPruneRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	victim := fixture(t, outside, "precious.txt", 1000, 100*day)

	dir := t.TempDir()
	link := filepath.Join(dir, "old.log")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	mt := testNow.Add(-100 * day)
	_ = os.Chtimes(link, mt, mt)
	res, err := Prune(dir, Options{Policy: Policy{MaxBytes: 1, MaxAge: day}, Now: testNow, InFlight: func(int) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(link) || !exists(victim) {
		t.Fatal("a symlink or its target was deleted")
	}
	if len(res.Refused) != 1 || res.Refused[0] != "old.log" {
		t.Errorf("Refused = %v, want [old.log]", res.Refused)
	}

	linkedDir := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(linkedDir, Options{Policy: Policy{MaxBytes: 1, MaxAge: day}, Now: testNow}); err == nil {
		t.Fatal("a symlinked log directory was accepted")
	}
	if !exists(victim) {
		t.Fatal("pruning through a symlinked directory deleted a file outside it")
	}
}

// TestPruneIgnoresSubdirectories: deletion is not recursive.
func TestPruneIgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := fixture(t, sub, "old.log", 1000, 100*day)
	if _, err := Prune(dir, Options{Policy: Policy{MaxBytes: 1, MaxAge: day}, Now: testNow}); err != nil {
		t.Fatal(err)
	}
	if !exists(inner) {
		t.Fatal("a file in a subdirectory was deleted")
	}
}

func TestPruneRejectsRelativeAndTolerateAbsent(t *testing.T) {
	if _, err := Prune("relative/logs", Options{}); err == nil {
		t.Error("a relative directory was accepted")
	}
	res, err := Prune(filepath.Join(t.TempDir(), "absent"), Options{Policy: DefaultPolicy()})
	if err != nil || res.Files != 0 {
		t.Errorf("absent dir: res=%+v err=%v, want empty and nil", res, err)
	}
}

func TestPruneDryRunDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "old.log", 10, 100*day)
	res, err := Prune(dir, Options{Policy: DefaultPolicy(), Now: testNow, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(p) || len(res.Deleted) != 1 {
		t.Errorf("dry run: exists=%v deleted=%d, want true and 1", exists(p), len(res.Deleted))
	}
}

func TestRemoveConfinedRefusesSwap(t *testing.T) {
	dir := t.TempDir()
	p := fixture(t, dir, "x.log", 10, day)
	listed, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the listed file alive under another name so its inode cannot be
	// reused by the replacement (Linux reuses a freed inode at once).
	if err := os.Rename(p, filepath.Join(t.TempDir(), "x.log")); err != nil {
		t.Fatal(err)
	}
	fixture(t, dir, "x.log", 10, day) // a different file at the same name
	if err := removeConfined(dir, "x.log", listed); err == nil {
		t.Fatal("a file replaced after listing was deleted")
	}
	// A replacement that reuses the inode is still caught when its size or
	// modification time differs from what was listed.
	reused, err := os.Lstat(filepath.Join(dir, "x.log"))
	if err != nil {
		t.Fatal(err)
	}
	fixture(t, dir, "x.log", 11, day)
	if err := removeConfined(dir, "x.log", reused); err == nil {
		t.Fatal("a same-inode file whose size changed after listing was deleted")
	}
	if err := removeConfined(dir, "../x.log", listed); err == nil {
		t.Fatal("a name with a parent reference was accepted")
	}
}

func TestIssueOf(t *testing.T) {
	cases := map[string]int{
		"2026-02-04_42_session.log":       42,
		"2026-02-04_42_retry_session.log": 42,
		"2026-02-04_session.log":          0,
		"go-backend.log":                  0,
	}
	for name, want := range cases {
		got, ok := IssueOf(name)
		if got != want || ok != (want != 0) {
			t.Errorf("IssueOf(%q) = %d, %v; want %d", name, got, ok, want)
		}
	}
}

func TestDueAndMarker(t *testing.T) {
	dir := t.TempDir()
	if !Due(dir, testNow) {
		t.Fatal("a never-pruned directory is not due")
	}
	if Due(filepath.Join(dir, "absent"), testNow) {
		t.Fatal("an absent directory is due")
	}
	if err := markPruned(dir, testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if Due(dir, testNow) {
		t.Error("due one hour after a prune")
	}
	if !Due(dir, testNow.Add(Interval)) {
		t.Error("not due a day after a prune")
	}
}

func TestLoadPolicy(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	defer config.SwapMachineConfigPathForTest(func() (string, error) { return cfg, nil })()

	cp := LoadPolicy()
	if cp.MaxBytes != 200<<20 || cp.MaxAge != 30*day || cp.SizeSource != "default" {
		t.Errorf("defaults = %d bytes, %s (%s); want 200 MiB, 30 days", cp.MaxBytes, cp.MaxAge, cp.SizeSource)
	}

	yaml := "pipeline:\n  logs:\n    max_size_mb: 50\n    max_age_days: nope\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cp = LoadPolicy()
	if cp.MaxBytes != 50<<20 || cp.SizeSource != cfg {
		t.Errorf("max_size_mb: got %d from %q, want 50 MiB from the machine file", cp.MaxBytes, cp.SizeSource)
	}
	if cp.MaxAge != 30*day || len(cp.Warnings) != 1 {
		t.Errorf("invalid max_age_days: age %s, warnings %v; want the default and one warning", cp.MaxAge, cp.Warnings)
	}
}
