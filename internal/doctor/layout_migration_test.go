package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// layoutRepo is a fixture repository whose migration table points the new
// per-clone locations at <repo>/.git/nightgauge/<class>, where ADR-024 puts
// them, whatever the class resolvers of this build return.
type layoutRepo struct {
	root, newRoot string
}

func newLayoutRepo(t *testing.T) layoutRepo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture uses symlinks and POSIX modes")
	}
	root, err := filepath.EvalSymlinks(gittest.InitRepo(t, t.TempDir(), "-b", "main"))
	if err != nil {
		t.Fatal(err)
	}
	writeLayoutFile(t, filepath.Join(root, ".gitignore"), strings.Join([]string{
		"/.worktrees/", "/.nightgauge/plans/", "/.nightgauge/retros/", "/.nightgauge/logs/",
		"/.nightgauge/worktrees/", "/.nightgauge/knowledge/", "/.nightgauge/pipeline/*",
		"!/.nightgauge/pipeline/.gitkeep", "",
	}, "\n"), 0o644)
	writeLayoutFile(t, filepath.Join(root, ".nightgauge", "pipeline", ".gitkeep"), "", 0o644)
	writeLayoutFile(t, filepath.Join(root, "README.md"), "fixture\n", 0o644)
	gittest.Run(t, root, "add", "-A")
	gittest.Run(t, root, "commit", "-q", "-m", "init")
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(layout.EnvStateHome, state)
	return layoutRepo{root: root, newRoot: filepath.Join(root, ".git", "nightgauge")}
}

// migrator builds the migrator under test: the production table with each
// file class's new path pointed at the fixture's new root.
func (r layoutRepo) migrator(daemon string) *layoutMigrator {
	entries := perCloneLayoutEntries()
	for i := range entries {
		if entries[i].Kind == LayoutFiles {
			class := entries[i].Class
			entries[i].Target = func(string) (string, error) { return filepath.Join(r.newRoot, class), nil }
		}
	}
	return &layoutMigrator{
		root: r.root, entries: entries,
		newRoot:  func(string) (string, error) { return r.newRoot, nil },
		inFlight: snapshotInFlight,
		daemonLive: func([]string) (string, bool) {
			return daemon, daemon != ""
		},
	}
}

// fixer runs the real remedy engine over the layout check and verb alone.
func (r layoutRepo) fixer(m *layoutMigrator) *Fixer {
	mk := func(string) *layoutMigrator { return m }
	reg := NewRegistry()
	reg.MustRegister(layoutCheck(mk, nil))
	verbs := NewVerbRegistry()
	if err := verbs.Register(verbLayoutMigrate, layoutMigrateVerb(mk, nil)); err != nil {
		panic(err)
	}
	return &Fixer{Registry: reg, Verbs: verbs, Env: &Env{Cwd: r.root, Now: time.Now()}}
}

func writeLayoutFile(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readLayoutFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s still exists (err %v)", path, err)
	}
}

// treeDigest hashes every entry under dir (path, mode, content or link text),
// so a second run can be shown to change nothing.
func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		line := p + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			line += " -> " + l
		case info.Mode().IsRegular():
			data, _ := os.ReadFile(p)
			sum := sha256.Sum256(data)
			line += " " + hex.EncodeToString(sum[:]) + " " + info.ModTime().String()
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func worktreeList(t *testing.T, root string) string {
	t.Helper()
	return gittest.Run(t, root, "worktree", "list", "--porcelain")
}

func findingsWith(list []Finding, code string) []Finding {
	var out []Finding
	for _, f := range list {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

// TestLayoutMigration (#2040): every legacy per-clone class is reported with
// its exact target, `doctor --fix` moves each file to its new path with the
// same bytes and mode, the old paths are gone, the marker exists, a second run
// changes nothing, and a file at both locations is never overwritten.
func TestLayoutMigration(t *testing.T) {
	t.Run("every class moves once", func(t *testing.T) {
		r := newLayoutRepo(t)
		nd := filepath.Join(r.root, ".nightgauge")
		files := map[string]string{ // path under .nightgauge -> content
			"pipeline/context-5.json":      `{"issue":5}`,
			"pipeline/trace/run-a.jsonl":   `{"t":1}` + "\n",
			"plans/issue-5.md":             "# plan 5\n",
			"retros/issue-5.md":            "# retro 5\n",
			"logs/issue-5_session.log":     "session\n",
			"logs/sanitization.log":        "sanitized\n",
			"pipeline/already-there.json":  "same bytes\n",
			"pipeline/history/runs.jsonl":  `{"timestamp":"2026-09-01T00:00:00Z","n":1}` + "\n" + `{"timestamp":"2026-09-03T00:00:00Z","n":3}` + "\n",
			"knowledge/.recall-cache/x.db": "index\n",
		}
		for rel, content := range files {
			writeLayoutFile(t, filepath.Join(nd, rel), content, 0o644)
		}
		if err := os.Chmod(filepath.Join(nd, "plans", "issue-5.md"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("context-5.json", filepath.Join(nd, "pipeline", "latest")); err != nil {
			t.Fatal(err)
		}
		// A move a crash interrupted after the copy, and an append-only log both
		// the old and the new build wrote.
		writeLayoutFile(t, filepath.Join(r.newRoot, "pipeline", "already-there.json"), "same bytes\n", 0o644)
		writeLayoutFile(t, filepath.Join(r.newRoot, "pipeline", "history", "runs.jsonl"),
			`{"timestamp":"2026-09-02T00:00:00Z","n":2}`+"\n"+`{"timestamp":"2026-09-03T00:00:00Z","n":3}`+"\n", 0o600)
		// Worktrees at both old bases: the Go manager's and the extension's.
		gittest.Run(t, r.root, "worktree", "add", "-q", "-b", "issue-8", filepath.Join(nd, "worktrees", "repo-issue-8"))
		gittest.Run(t, r.root, "worktree", "add", "-q", "-b", "issue-9", filepath.Join(r.root, ".worktrees", "issue-9"))
		statusBefore := gittest.Run(t, r.root, "status", "--porcelain")

		m := r.migrator("")
		wtBase, err := m.entries[4].Target(r.root)
		if err != nil {
			t.Fatalf("worktree base: %v", err)
		}

		// Plain doctor: one housekeeping finding per class with the exact target,
		// and the exit status is unchanged.
		found, _ := layoutFindings(m)
		wantTargets := map[string]string{
			"pipeline": filepath.Join(r.newRoot, "pipeline"), "plans": filepath.Join(r.newRoot, "plans"),
			"retros": filepath.Join(r.newRoot, "retros"), "logs": filepath.Join(r.newRoot, "logs"),
		}
		seen := map[string]bool{}
		for _, f := range found {
			if f.Code != codeLayoutLegacy || f.Severity != SeverityHousekeeping {
				t.Errorf("unexpected finding %s %s: %s", f.Code, f.Severity, f.Title)
				continue
			}
			class := f.Evidence["class"]
			seen[class] = true
			if want, ok := wantTargets[class]; ok && f.Evidence["target"] != want {
				t.Errorf("%s target = %q, want %q", class, f.Evidence["target"], want)
			}
			if class == "worktrees" && !strings.HasPrefix(f.Evidence["target"], wtBase+string(filepath.Separator)) {
				t.Errorf("worktree target %q is not under the base %s", f.Evidence["target"], wtBase)
			}
			if len(f.Remedies) != 1 || f.Remedies[0].Verb != verbLayoutMigrate || f.Remedies[0].Kind != RemedyAuto {
				t.Errorf("%s: remedy %+v, want one auto %s", f.Title, f.Remedies, verbLayoutMigrate)
			}
		}
		for _, class := range []string{"pipeline", "plans", "retros", "logs", "worktrees", "recall cache"} {
			if !seen[class] {
				t.Errorf("no finding for the %s class; findings:\n%s", class, findingsText(found))
			}
		}
		if got := len(findingsWith(found, codeLayoutLegacy)); got != 7 {
			t.Errorf("%d legacy findings, want 7 (4 file classes, 2 worktrees, 1 cache):\n%s", got, findingsText(found))
		}
		if code := BuildResult([]CheckResult{{ID: checkLayout, Status: StatusFailed, Findings: found}}).ExitCode; code != 0 {
			t.Errorf("doctor exit code with only legacy-layout findings = %d, want 0 (unchanged)", code)
		}

		// doctor --fix through the remedy engine.
		rep := r.fixer(m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 0 || rep.Counts.Fixed != len(found) {
			t.Fatalf("fix: exit %d, counts %+v, want exit 0 and %d fixed; results %+v", rep.ExitCode, rep.Counts, len(found), rep.Results)
		}

		for rel, content := range files {
			if strings.HasPrefix(rel, "knowledge/") || rel == "pipeline/history/runs.jsonl" {
				continue
			}
			dst := filepath.Join(r.newRoot, filepath.FromSlash(rel))
			if got := readLayoutFile(t, dst); got != content {
				t.Errorf("%s = %q, want %q", dst, got, content)
			}
			assertGone(t, filepath.Join(nd, filepath.FromSlash(rel)))
		}
		if info, err := os.Stat(filepath.Join(r.newRoot, "plans", "issue-5.md")); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("moved plan mode = %v (err %v), want 0640", info.Mode().Perm(), err)
		}
		for _, class := range []string{"plans", "retros", "logs", "pipeline/trace"} {
			if info, err := os.Stat(filepath.Join(r.newRoot, class)); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("created %s mode = %v (err %v), want 0700", class, info.Mode().Perm(), err)
			}
		}
		link := filepath.Join(r.newRoot, "pipeline", "latest")
		if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink (err %v)", link, err)
		} else if text, _ := os.Readlink(link); text != "context-5.json" {
			t.Errorf("%s -> %q, want context-5.json", link, text)
		}
		merged := readLayoutFile(t, filepath.Join(r.newRoot, "pipeline", "history", "runs.jsonl"))
		wantMerged := `{"timestamp":"2026-09-01T00:00:00Z","n":1}` + "\n" + `{"timestamp":"2026-09-02T00:00:00Z","n":2}` + "\n" +
			`{"timestamp":"2026-09-03T00:00:00Z","n":3}` + "\n"
		if merged != wantMerged {
			t.Errorf("merged history =\n%s\nwant the union ordered by timestamp:\n%s", merged, wantMerged)
		}
		assertGone(t, filepath.Join(nd, "pipeline", "history"))
		assertGone(t, filepath.Join(nd, "plans"))
		assertGone(t, filepath.Join(nd, "knowledge", ".recall-cache"))
		if _, err := os.Stat(filepath.Join(nd, "pipeline", ".gitkeep")); err != nil {
			t.Errorf("the tracked .gitkeep was moved: %v", err)
		}
		wts := worktreeList(t, r.root)
		for _, want := range []string{filepath.Join(wtBase, "repo-issue-8"), filepath.Join(wtBase, filepath.Base(r.root)+"-issue-9")} {
			if !strings.Contains(wts, "worktree "+want+"\n") {
				t.Errorf("worktree %s not registered after the move:\n%s", want, wts)
			}
		}
		assertGone(t, filepath.Join(nd, "worktrees", "repo-issue-8"))
		assertGone(t, filepath.Join(r.root, ".worktrees", "issue-9"))
		if got := strings.TrimSpace(readLayoutFile(t, filepath.Join(r.newRoot, layoutMarkerName))); got != strconv.Itoa(LayoutVersion) {
			t.Errorf("layout-version marker = %q, want %d", got, LayoutVersion)
		}
		if status := gittest.Run(t, r.root, "status", "--porcelain"); status != statusBefore {
			t.Errorf("git status changed:\nbefore:\n%s\nafter:\n%s", statusBefore, status)
		}

		// A second run is a no-op that reports the layout version.
		before := treeDigest(t, r.newRoot)
		second := m.Migrate(context.Background())
		if s, want := second.Summary(), fmt.Sprintf("nothing to migrate; layout v%d", LayoutVersion); s != want {
			t.Errorf("second run summary = %q, want %q", s, want)
		}
		if after := treeDigest(t, r.newRoot); after != before {
			t.Errorf("a second run changed the new root:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if found, detail := layoutFindings(m); len(found) != 0 || !strings.HasPrefix(detail, fmt.Sprintf("layout v%d", LayoutVersion)) {
			t.Errorf("after the migration: %d finding(s), detail %q; want none and \"layout v%d ...\"", len(found), detail, LayoutVersion)
		}

	})

	t.Run("conflict is never overwritten", func(t *testing.T) {
		r := newLayoutRepo(t)
		nd := filepath.Join(r.root, ".nightgauge")
		legacyPlan := filepath.Join(nd, "plans", "issue-5.md")
		newPlan := filepath.Join(r.newRoot, "plans", "issue-5.md")
		legacyRetro := filepath.Join(nd, "retros", "issue-6.md")
		writeLayoutFile(t, legacyPlan, "old plan\n", 0o644)
		writeLayoutFile(t, newPlan, "new plan\n", 0o600)
		writeLayoutFile(t, legacyRetro, "retro\n", 0o644)
		m := r.migrator("")

		found, _ := layoutFindings(m)
		conflicts := findingsWith(found, codeLayoutConflict)
		// Errorf, not Fatalf: the bytes below are the assertion that matters,
		// and they must be checked even when the report is wrong.
		if len(conflicts) != 1 || conflicts[0].Evidence["legacy"] != legacyPlan || conflicts[0].Evidence["target"] != newPlan {
			t.Errorf("conflict findings = %s, want one naming %s and %s", findingsText(conflicts), legacyPlan, newPlan)
		} else if !strings.Contains(conflicts[0].Title, legacyPlan) || !strings.Contains(conflicts[0].Title, newPlan) {
			t.Errorf("conflict title %q does not name both paths", conflicts[0].Title)
		}

		rep := r.fixer(m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 3 {
			t.Errorf("fix exit = %d, want 3 (conflict); results %+v", rep.ExitCode, rep.Results)
		}
		if got := readLayoutFile(t, newPlan); got != "new plan\n" {
			t.Errorf("the target was overwritten: %q", got)
		}
		if got := readLayoutFile(t, legacyPlan); got != "old plan\n" {
			t.Errorf("the source changed: %q", got)
		}
		// The migration stops for the whole root on a conflict.
		if got := readLayoutFile(t, legacyRetro); got != "retro\n" {
			t.Errorf("a non-conflicting file moved while a conflict stood: %q", got)
		}
		assertGone(t, filepath.Join(r.newRoot, layoutMarkerName))
	})

	t.Run("symlinked old location is refused", func(t *testing.T) {
		r := newLayoutRepo(t)
		outside := t.TempDir()
		writeLayoutFile(t, filepath.Join(outside, "secret.md"), "outside\n", 0o600)
		if err := os.MkdirAll(filepath.Join(r.root, ".nightgauge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(r.root, ".nightgauge", "plans")); err != nil {
			t.Fatal(err)
		}
		retro := filepath.Join(r.root, ".nightgauge", "retros", "issue-6.md")
		writeLayoutFile(t, retro, "retro\n", 0o644)
		m := r.migrator("")
		found, _ := layoutFindings(m)
		if len(findingsWith(found, codeLayoutRefused)) != 1 {
			t.Fatalf("findings = %s, want one %s", findingsText(found), codeLayoutRefused)
		}
		rep := m.Migrate(context.Background())
		if len(rep.Refused) != 1 || rep.Version != 0 {
			t.Errorf("migration through a symlinked old location was not refused: %s", rep.Summary())
		}
		if got := readLayoutFile(t, filepath.Join(outside, "secret.md")); got != "outside\n" {
			t.Errorf("the link target changed: %q", got)
		}
		assertGone(t, filepath.Join(r.newRoot, "plans", "secret.md"))
		// A refused row is left alone; the other rows still move.
		assertGone(t, retro)
		if got := readLayoutFile(t, filepath.Join(r.newRoot, "retros", "issue-6.md")); got != "retro\n" {
			t.Errorf("the retro beside a refused row did not move: %q", got)
		}
	})

	t.Run("a live daemon blocks", func(t *testing.T) {
		r := newLayoutRepo(t)
		legacyPlan := filepath.Join(r.root, ".nightgauge", "plans", "issue-5.md")
		writeLayoutFile(t, legacyPlan, "plan\n", 0o644)
		rep := r.fixer(r.migrator("a daemon (pid 1) serves the repo")).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 4 {
			t.Errorf("fix exit = %d, want 4 (blocked)", rep.ExitCode)
		}
		if got := readLayoutFile(t, legacyPlan); got != "plan\n" {
			t.Errorf("a file moved while a daemon was live: %q", got)
		}
	})

	t.Run("classes the resolver has not moved are in place", func(t *testing.T) {
		r := newLayoutRepo(t)
		writeLayoutFile(t, filepath.Join(r.root, ".nightgauge", "plans", "issue-5.md"), "plan\n", 0o644)
		m := r.migrator("")
		for i := range m.entries {
			if m.entries[i].Kind == LayoutFiles {
				m.entries[i].Target = m.entries[i].Legacy
			}
		}
		found, detail := layoutFindings(m)
		if len(found) != 0 || !strings.Contains(detail, "this build keeps") {
			t.Errorf("in place: %d finding(s), detail %q; want none", len(found), detail)
		}
		rep := m.Migrate(context.Background())
		if rep.Version != 0 || rep.Moved != 0 {
			t.Errorf("in place: %s; want no move and no marker", rep.Summary())
		}
		assertGone(t, filepath.Join(r.newRoot, layoutMarkerName))
	})
}

// TestLayoutMigrationSkipsBusyWorktree (#2040): a worktree a run is in flight
// on is not moved, and is reported; an idle one beside it is.
func TestLayoutMigrationSkipsBusyWorktree(t *testing.T) {
	r := newLayoutRepo(t)
	nd := filepath.Join(r.root, ".nightgauge")
	busy := filepath.Join(nd, "worktrees", "repo-issue-7")
	idle := filepath.Join(nd, "worktrees", "repo-issue-8")
	gittest.Run(t, r.root, "worktree", "add", "-q", "-b", "issue-7", busy)
	gittest.Run(t, r.root, "worktree", "add", "-q", "-b", "issue-8", idle)
	// The live run marker the worktree sweep reads: the in-flight sidecar,
	// stamped with a live pid (this test's).
	sidecar, err := json.Marshal(map[string]any{"issue_number": 7, "run_id": "run-7", "pid": os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	writeLayoutFile(t, filepath.Join(nd, "pipeline", "current-run.json"), string(sidecar), 0o600)

	m := r.migrator("")
	found, _ := layoutFindings(m)
	var busyFinding *Finding
	for i, f := range found {
		if f.Evidence["path"] == busy {
			busyFinding = &found[i]
		}
	}
	if busyFinding == nil || !strings.Contains(busyFinding.Evidence["busy"], "live-sidecar") {
		t.Fatalf("the busy worktree is not reported as busy: %s", findingsText(found))
	}

	rep := r.fixer(m).Run(context.Background(), FixOptions{})
	if rep.ExitCode != 4 {
		t.Errorf("fix exit = %d, want 4 (blocked by the in-flight run)", rep.ExitCode)
	}
	var busyResult *FixResult
	for i, res := range rep.Results {
		if res.Finding.Fingerprint == busyFinding.Fingerprint {
			busyResult = &rep.Results[i]
		}
	}
	if busyResult == nil || busyResult.Outcome != OutcomeBlocked || !strings.Contains(busyResult.Detail, busy) {
		t.Errorf("busy worktree result = %+v, want blocked naming %s", busyResult, busy)
	}
	if _, err := os.Stat(filepath.Join(busy, "README.md")); err != nil {
		t.Errorf("the busy worktree moved: %v", err)
	}
	if wts := worktreeList(t, r.root); !strings.Contains(wts, "worktree "+busy+"\n") {
		t.Errorf("the busy worktree is no longer registered at %s:\n%s", busy, wts)
	}
	assertGone(t, idle)
	// The in-flight run also holds its own pipeline state in place, so the
	// marker is not written.
	if _, err := os.Stat(filepath.Join(nd, "pipeline", "current-run.json")); err != nil {
		t.Errorf("pipeline state moved under an in-flight run: %v", err)
	}
	assertGone(t, filepath.Join(r.newRoot, layoutMarkerName))
	if s := m.Migrate(context.Background()).Summary(); !strings.Contains(s, "skipped busy worktree(s): "+busy+" (#7") {
		t.Errorf("summary %q does not report the skipped worktree", s)
	}
}

// TestLayoutCopyAcrossFilesystems covers the copy path a hard link cannot
// take: bytes, mode and mtime arrive, and an existing target is not replaced.
func TestLayoutCopyAcrossFilesystems(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.log")
	writeLayoutFile(t, src, "payload\n", 0o640)
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "dst.log")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyInto(layoutFile{Src: src, Dst: dst}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil || readLayoutFile(t, dst) != "payload\n" || info.Mode().Perm() != 0o640 || !info.ModTime().Equal(mtime) {
		t.Errorf("copy: err %v, mode %v, mtime %v", err, info.Mode().Perm(), info.ModTime())
	}
	if err := copyInto(layoutFile{Src: src, Dst: dst}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("a copy onto an existing target returned %v, want an exists error", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

// TestAutoMigrateLayoutUsesTheResolvers: the automatic run ADR-024 § 15 puts
// ahead of every command moves legacy data into the directories the flipped
// class resolvers return, <repo>/.git/nightgauge/<class>, with nothing
// injected, and writes the marker.
func TestAutoMigrateLayoutUsesTheResolvers(t *testing.T) {
	r := newLayoutRepo(t)
	legacyPlan := filepath.Join(r.root, ".nightgauge", "plans", "issue-5.md")
	legacyCtx := filepath.Join(r.root, ".nightgauge", "pipeline", "context-5.json")
	writeLayoutFile(t, legacyPlan, "plan\n", 0o644)
	writeLayoutFile(t, legacyCtx, "{}\n", 0o644)
	want, err := layout.PlansDir(r.root)
	if err != nil {
		t.Fatal(err)
	}
	if want != filepath.Join(r.newRoot, "plans") {
		t.Fatalf("layout.PlansDir = %s, want %s: the resolvers are not flipped", want, filepath.Join(r.newRoot, "plans"))
	}

	rep, ran := AutoMigrateLayout(context.Background(), r.root)
	if !ran || rep.Version != LayoutVersion || rep.Moved != 2 {
		t.Fatalf("auto-migrate: ran %v, %s; want a run that moves 2 files and reaches the current layout", ran, rep.Summary())
	}
	if got := readLayoutFile(t, filepath.Join(r.newRoot, "plans", "issue-5.md")); got != "plan\n" {
		t.Errorf("moved plan = %q", got)
	}
	if got := readLayoutFile(t, filepath.Join(r.newRoot, "pipeline", "context-5.json")); got != "{}\n" {
		t.Errorf("moved context = %q", got)
	}
	assertGone(t, legacyPlan)
	assertGone(t, legacyCtx)
	if got := strings.TrimSpace(readLayoutFile(t, filepath.Join(r.newRoot, layoutMarkerName))); got != strconv.Itoa(LayoutVersion) {
		t.Errorf("marker = %q, want %d", got, LayoutVersion)
	}
}

// TestAutoMigrateLayoutNoOpOnceMarked: with the marker in place the automatic
// run neither scans nor moves anything, even with data at an old location
// (which `doctor` still reports).
func TestAutoMigrateLayoutNoOpOnceMarked(t *testing.T) {
	r := newLayoutRepo(t)
	legacyPlan := filepath.Join(r.root, ".nightgauge", "plans", "issue-5.md")
	writeLayoutFile(t, legacyPlan, "plan\n", 0o644)
	writeLayoutFile(t, filepath.Join(r.newRoot, layoutMarkerName), strconv.Itoa(LayoutVersion)+"\n", 0o600)

	mk := func(string) *layoutMigrator {
		t.Fatal("the migrator was built although the marker is current")
		return nil
	}
	if rep, ran := autoMigrateLayout(context.Background(), r.root, mk, time.Now()); ran {
		t.Errorf("auto-migrate ran with the marker current: %s", rep.Summary())
	}
	if got := readLayoutFile(t, legacyPlan); got != "plan\n" {
		t.Errorf("legacy plan changed: %q", got)
	}
}

// TestAutoMigrateLayoutRetryWindow: a run that cannot finish (here, a
// conflict) moves nothing, and the clone is not rescanned on every command
// until the retry window has passed.
func TestAutoMigrateLayoutRetryWindow(t *testing.T) {
	r := newLayoutRepo(t)
	legacyPlan := filepath.Join(r.root, ".nightgauge", "plans", "issue-5.md")
	newPlan := filepath.Join(r.newRoot, "plans", "issue-5.md")
	writeLayoutFile(t, legacyPlan, "old\n", 0o644)
	writeLayoutFile(t, newPlan, "new\n", 0o600)

	now := time.Now()
	calls := 0
	mk := func(string) *layoutMigrator { calls++; return r.migrator("") }
	rep, ran := autoMigrateLayout(context.Background(), r.root, mk, now)
	if !ran || len(rep.Conflicts) != 1 || rep.Version != 0 {
		t.Fatalf("first run: ran %v, %s; want one conflict and no marker", ran, rep.Summary())
	}
	if readLayoutFile(t, legacyPlan) != "old\n" || readLayoutFile(t, newPlan) != "new\n" {
		t.Error("the conflict was not left untouched")
	}
	if _, ran := autoMigrateLayout(context.Background(), r.root, mk, now.Add(time.Minute)); ran || calls != 1 {
		t.Errorf("rescanned inside the retry window (ran %v, %d builds)", ran, calls)
	}
	if _, ran := autoMigrateLayout(context.Background(), r.root, mk, now.Add(autoMigrateRetry+time.Minute)); !ran || calls != 2 {
		t.Errorf("not retried after the window (ran %v, %d builds)", ran, calls)
	}
}

// TestLayoutMigrationPerCheckout (#2037, #2040): a layout-v1 clone migrates
// again for the v2 rows. Each checkout's per-checkout files (its legacy
// .nightgauge/ run control and runtime state) move to that checkout's own
// CHECKOUT, the run-control singletons v1 kept in CLONE move to the main
// checkout's CHECKOUT, a linked worktree's keyed data merges into the one
// CLONE, and `git status` stays clean in every checkout.
func TestLayoutMigrationPerCheckout(t *testing.T) {
	r := newLayoutRepo(t)
	writeLayoutFile(t, filepath.Join(r.root, ".gitignore"), "/.nightgauge/\n", 0o644)
	gittest.Run(t, r.root, "add", "-A")
	gittest.Run(t, r.root, "commit", "-q", "-m", "ignore .nightgauge")
	wt := filepath.Join(evalDir(t, t.TempDir()), "wt")
	gittest.Run(t, r.root, "worktree", "add", "-q", "-b", "feat", wt)

	mainCheckout, err := layout.CheckoutDir(r.root)
	if err != nil {
		t.Fatal(err)
	}
	wtCheckout, err := layout.CheckoutDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	if mainCheckout == wtCheckout {
		t.Fatalf("the main checkout and the linked worktree share %s", mainCheckout)
	}
	clone, err := layout.CloneDir(wt)
	if err != nil || clone != r.newRoot {
		t.Fatalf("CloneDir(worktree) = %q, %v; want the main clone's %s", clone, err, r.newRoot)
	}

	nd, wnd := filepath.Join(r.root, ".nightgauge"), filepath.Join(wt, ".nightgauge")
	// A layout-v1 clone: the marker, and the singletons v1 kept in CLONE.
	writeLayoutFile(t, filepath.Join(r.newRoot, layoutMarkerName), "1\n", 0o600)
	writeLayoutFile(t, filepath.Join(r.newRoot, "pipeline", "batch-state.json"), `{"batch":1}`, 0o600)
	writeLayoutFile(t, filepath.Join(r.newRoot, "logs", "go-backend.log"), "v1 daemon\n", 0o600)
	moves := map[string]string{ // legacy -> new
		filepath.Join(nd, "pipeline", "queue-state.json"):        filepath.Join(mainCheckout, "queue-state.json"),
		filepath.Join(nd, "attention", "cards", "c1.json"):       filepath.Join(mainCheckout, "attention", "cards", "c1.json"),
		filepath.Join(nd, "health", "trends.jsonl"):              filepath.Join(mainCheckout, "health", "trends.jsonl"),
		filepath.Join(nd, "focus.yaml"):                          filepath.Join(mainCheckout, "focus.yaml"),
		filepath.Join(r.newRoot, "pipeline", "batch-state.json"): filepath.Join(mainCheckout, "batch-state.json"),
		filepath.Join(r.newRoot, "logs", "go-backend.log"):       filepath.Join(mainCheckout, "go-backend.log"),
		filepath.Join(wnd, "pipeline", "run-state.json"):         filepath.Join(wtCheckout, "run-state.json"),
		filepath.Join(wnd, "performance-mode.yaml"):              filepath.Join(wtCheckout, "performance-mode.yaml"),
		filepath.Join(wnd, "attention", "cards", "c2.json"):      filepath.Join(wtCheckout, "attention", "cards", "c2.json"),
		filepath.Join(wnd, "plans", "issue-9.md"):                filepath.Join(r.newRoot, "plans", "issue-9.md"),
		filepath.Join(wnd, "pipeline", "context-9.json"):         filepath.Join(r.newRoot, "pipeline", "context-9.json"),
	}
	content := map[string]string{}
	for legacy := range moves {
		content[legacy] = "from " + legacy + "\n"
		if _, seeded := map[string]bool{
			filepath.Join(r.newRoot, "pipeline", "batch-state.json"): true,
			filepath.Join(r.newRoot, "logs", "go-backend.log"):       true,
		}[legacy]; seeded {
			content[legacy] = readLayoutFile(t, legacy)
			continue
		}
		writeLayoutFile(t, legacy, content[legacy], 0o644)
	}
	statusMain, statusWT := gittest.Run(t, r.root, "status", "--porcelain"), gittest.Run(t, wt, "status", "--porcelain")

	rep, ran := AutoMigrateLayout(context.Background(), r.root)
	if !ran || rep.Version != LayoutVersion {
		t.Fatalf("auto-migrate of a v1 clone: ran %v, %s; want layout v%d", ran, rep.Summary(), LayoutVersion)
	}
	for legacy, dst := range moves {
		if got := readLayoutFile(t, dst); got != content[legacy] {
			t.Errorf("%s = %q, want the bytes of %s", dst, got, legacy)
		}
		assertGone(t, legacy)
	}
	for _, dir := range []string{mainCheckout, wtCheckout} {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v (err %v), want 0700", dir, info.Mode().Perm(), err)
		}
	}
	if got := strings.TrimSpace(readLayoutFile(t, filepath.Join(r.newRoot, layoutMarkerName))); got != strconv.Itoa(LayoutVersion) {
		t.Errorf("marker = %q, want %d", got, LayoutVersion)
	}
	if s := gittest.Run(t, r.root, "status", "--porcelain"); s != statusMain {
		t.Errorf("main checkout git status changed:\n%s\nwant\n%s", s, statusMain)
	}
	if s := gittest.Run(t, wt, "status", "--porcelain"); s != statusWT {
		t.Errorf("worktree git status changed:\n%s\nwant\n%s", s, statusWT)
	}
	// Once marked, the automatic run is a no-op.
	if rep, ran := AutoMigrateLayout(context.Background(), r.root); ran {
		t.Errorf("auto-migrate ran again after v%d: %s", LayoutVersion, rep.Summary())
	}
}

// TestLayoutMigrationPerCheckoutConflict: a per-checkout file at both its old
// location and in CHECKOUT is a conflict like any other: never overwritten,
// and doctor --fix exits 3.
func TestLayoutMigrationPerCheckoutConflict(t *testing.T) {
	r := newLayoutRepo(t)
	checkout, err := layout.CheckoutDir(r.root)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(r.root, ".nightgauge", "focus.yaml")
	target := filepath.Join(checkout, "focus.yaml")
	writeLayoutFile(t, legacy, "lens: old\n", 0o644)
	writeLayoutFile(t, target, "lens: new\n", 0o600)
	// A movable file beside it: the migration stops for the whole root.
	card := filepath.Join(r.root, ".nightgauge", "attention", "c.json")
	writeLayoutFile(t, card, "{}\n", 0o644)
	m := r.migrator("")
	m.checkoutEntries = allCheckoutEntries
	found, _ := layoutFindings(m)
	if c := findingsWith(found, codeLayoutConflict); len(c) != 1 || c[0].Evidence["target"] != target {
		t.Errorf("conflict findings = %s, want one naming %s", findingsText(c), target)
	}
	if rep := r.fixer(m).Run(context.Background(), FixOptions{}); rep.ExitCode != 3 {
		t.Errorf("fix exit = %d, want 3 (conflict)", rep.ExitCode)
	}
	if readLayoutFile(t, legacy) != "lens: old\n" || readLayoutFile(t, target) != "lens: new\n" {
		t.Error("a conflicting per-checkout file was changed")
	}
	if readLayoutFile(t, card) != "{}\n" {
		t.Error("a file moved while a conflict stood")
	}
}

func evalDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestLayoutMigrationConflictOnlyExits3: a conflict with nothing else to move
// still makes `doctor --fix` exit 3 (ADR-024 § 15), not 0, and nothing is
// overwritten.
func TestLayoutMigrationConflictOnlyExits3(t *testing.T) {
	r := newLayoutRepo(t)
	legacyPlan := filepath.Join(r.root, ".nightgauge", "plans", "issue-5.md")
	newPlan := filepath.Join(r.newRoot, "plans", "issue-5.md")
	writeLayoutFile(t, legacyPlan, "old plan\n", 0o644)
	writeLayoutFile(t, newPlan, "new plan\n", 0o600)
	m := r.migrator("")
	found, _ := layoutFindings(m)
	if len(found) != 1 || found[0].Code != codeLayoutConflict {
		t.Fatalf("findings = %s, want exactly one %s", findingsText(found), codeLayoutConflict)
	}
	rep := r.fixer(m).Run(context.Background(), FixOptions{})
	if rep.ExitCode != 3 {
		t.Errorf("fix exit with only a conflict = %d, want 3; results %+v", rep.ExitCode, rep.Results)
	}
	if readLayoutFile(t, legacyPlan) != "old plan\n" || readLayoutFile(t, newPlan) != "new plan\n" {
		t.Error("a conflicting file was changed")
	}
}

// TestLayoutMigrationRestOfRuntimeFiles (ADR-024 § 7, "and the rest"): the
// remaining runtime files the old template ignored one by one move to
// CHECKOUT (reports into CHECKOUT/reports, backlog-*.md by pattern), while
// the committed audit/ keeps its tracked files and only loses the per-machine
// counter.
func TestLayoutMigrationRestOfRuntimeFiles(t *testing.T) {
	r := newLayoutRepo(t)
	nd := filepath.Join(r.root, ".nightgauge")
	writeLayoutFile(t, filepath.Join(nd, "audit", "features.yaml"), "features: []\n", 0o644)
	writeLayoutFile(t, filepath.Join(r.root, ".gitignore"), "/.nightgauge/*\n!/.nightgauge/audit/\n/.nightgauge/audit/scope-drift-stats.json\n", 0o644)
	gittest.Run(t, r.root, "add", "-A")
	gittest.Run(t, r.root, "commit", "-q", "-m", "audit")
	checkout, err := layout.CheckoutDir(r.root)
	if err != nil {
		t.Fatal(err)
	}
	moves := map[string]string{
		"complexity-model.yaml":             "complexity-model.yaml",
		"audit/scope-drift-stats.json":      "scope-drift-stats.json",
		"health-report.json":                "reports/health-report.json",
		"backlog-2026-09-29.md":             "reports/backlog-2026-09-29.md",
		"backlog-triage.md":                 "reports/backlog-triage.md",
		"session-handoff.md":                "session-handoff.md",
		"history/brownfield-snapshots.json": "brownfield-history/brownfield-snapshots.json",
		"release-watch/state.json":          "release-watch/state.json",
		"doctor/automation-pauses.json":     "doctor/automation-pauses.json",
		".refresh-trigger":                  ".refresh-trigger",
	}
	for legacy := range moves {
		writeLayoutFile(t, filepath.Join(nd, filepath.FromSlash(legacy)), legacy+"\n", 0o644)
	}
	status := gittest.Run(t, r.root, "status", "--porcelain")
	m := r.migrator("")
	m.checkoutEntries = allCheckoutEntries
	rep := m.Migrate(context.Background())
	if rep.Version != LayoutVersion || len(rep.Errors) > 0 {
		t.Fatalf("migration: %s", rep.Summary())
	}
	for legacy, dst := range moves {
		if got := readLayoutFile(t, filepath.Join(checkout, filepath.FromSlash(dst))); got != legacy+"\n" {
			t.Errorf("%s = %q, want the bytes of .nightgauge/%s", dst, got, legacy)
		}
		assertGone(t, filepath.Join(nd, filepath.FromSlash(legacy)))
	}
	if got := readLayoutFile(t, filepath.Join(nd, "audit", "features.yaml")); got != "features: []\n" {
		t.Errorf("the tracked audit file changed: %q", got)
	}
	if s := gittest.Run(t, r.root, "status", "--porcelain"); s != status {
		t.Errorf("git status changed:\n%s\nwant\n%s", s, status)
	}
}

// machineStateFixture is a fake HOME holding every legacy machine-state class
// (#2031, #2032) under ~/.nightgauge, and a machine-state root of its own.
type machineStateFixture struct {
	home, legacy, state string
	files               map[string]string // path under ~/.nightgauge -> content
	serveLock           string
}

func newMachineStateFixture(t *testing.T) machineStateFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture uses symlinks, flock and POSIX modes")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := machineStateFixture{
		home:  filepath.Join(base, "home"),
		state: filepath.Join(base, "state"),
	}
	f.legacy = filepath.Join(f.home, ".nightgauge")
	t.Setenv("HOME", f.home)
	t.Setenv(layout.EnvStateHome, f.state)
	serveKey := strings.TrimSuffix(runstate.ServeSidecarName(filepath.Join(base, "workspace")), ".json")
	f.serveLock = filepath.Join(f.legacy, "serve", serveKey+".lock")
	f.files = map[string]string{
		"serve/" + serveKey + ".json":              `{"pid":1,"workspace_root":"/w"}`,
		"serve/" + serveKey + ".lock":              "",
		"rate-limit.json":                          `{"remaining":4000}`,
		"ratelimit-gitlab-gitlab.example.com.json": `{"remaining":12}`,
		"telemetry-notice-v1":                      "shown\n",
		"usage/claude-rate-limits.json":            `{"version":1,"buckets":{}}` + "\n",
		"opencode/runs/01890a5d-ac96-774b-bcce-b302099a8057/data/opencode/opencode.db": "transcript",
		"opencode/evidence/01890a5d-ac96-774b-bcce-b302099a8057/opencode.db":           "evidence",
		"opencode/last-dispatch.json":  `{"binary":"/bin/opencode","version":"1.18.30"}`,
		"opencode/endpoint-slots.json": `{"pid":1,"in_use":{}}`,
		"logs/machine.log":             "one\n",
	}
	for rel, content := range f.files {
		writeLayoutFile(t, filepath.Join(f.legacy, filepath.FromSlash(rel)), content, 0o644)
	}
	// machine-id is 0644, as an older binary wrote it.
	writeLayoutFile(t, filepath.Join(f.legacy, "machine-id"), "11111111-2222-4333-8444-555555555555\n", 0o644)
	// A run root's link to the operator's config: recreated, never followed.
	if err := os.Symlink(filepath.Join(f.home, ".config", "git"),
		filepath.Join(f.legacy, "opencode", "runs", "01890a5d-ac96-774b-bcce-b302099a8057", "config-git")); err != nil {
		t.Fatal(err)
	}
	// Obsolete since #2148: deleted, not moved.
	writeLayoutFile(t, filepath.Join(f.legacy, "opencode", "self-test", "abc"), "passed\n", 0o644)
	// Not machine state: CONFIG and the operator-installed tools stay.
	writeLayoutFile(t, filepath.Join(f.legacy, "config.yaml"), "owner: someone\n", 0o600)
	writeLayoutFile(t, filepath.Join(f.legacy, "tools", "opencode", "package.json"), "{}\n", 0o644)
	return f
}

// migrator is the production machine-state table over the fixture's roots,
// with the daemon check reading the fixture's legacy serve directory.
func (f machineStateFixture) migrator() *machineStateMigrator {
	return &machineStateMigrator{
		legacyRoot: f.legacy,
		statePath:  func() (string, error) { return f.state, nil },
		stateHome: func() (string, error) {
			if err := os.MkdirAll(f.state, 0o700); err != nil {
				return "", err
			}
			return f.state, nil
		},
		daemonLive: legacyServeLeaseLive,
		entries:    machineStateEntries(),
	}
}

// fixer runs the real remedy engine over the layout check with the
// machine-state rows alone (no clone: the working directory is not a git
// checkout).
func (f machineStateFixture) fixer(m *machineStateMigrator) *Fixer {
	mk := func(string) *layoutMigrator { return nil }
	mm := func() *machineStateMigrator { return m }
	reg := NewRegistry()
	reg.MustRegister(layoutCheck(mk, mm))
	verbs := NewVerbRegistry()
	if err := verbs.Register(verbLayoutMigrate, layoutMigrateVerb(mk, mm)); err != nil {
		panic(err)
	}
	return &Fixer{Registry: reg, Verbs: verbs, Env: &Env{Cwd: f.home, Now: time.Now()}}
}

// TestLayoutMigrationMachineState (#2041): every legacy machine-state class
// moves to the machine-state directory once, byte for byte; machine-id keeps
// its value, ends 0600 and is never regenerated; the marker is written; a
// second run changes nothing; a live serve lease leaves the serve claims (and
// the run roots a daemon may be using) in place and --fix exits 4; config.yaml
// and tools/ are not moved.
func TestLayoutMigrationMachineState(t *testing.T) {
	t.Run("every class moves once", func(t *testing.T) {
		f := newMachineStateFixture(t)
		// A hint this build already rewrote at the new location: the new copy
		// wins and the old one goes.
		writeLayoutFile(t, filepath.Join(f.state, "rate-limit.json"), `{"remaining":3999}`, 0o600)
		if err := os.Chmod(f.state, 0o755); err != nil {
			t.Fatal(err)
		}
		m := f.migrator()

		// Plain doctor: one housekeeping finding per class, each naming its
		// exact target, and the exit status is unchanged.
		found, _ := machineFindings(m)
		targets := map[string]string{}
		for _, fd := range found {
			if fd.Code != codeLayoutLegacy || fd.Severity != SeverityHousekeeping || fd.Evidence["scope"] != machineScope {
				t.Errorf("unexpected finding %s %s %v: %s", fd.Code, fd.Severity, fd.Evidence, fd.Title)
			}
			targets[fd.Evidence["class"]] = fd.Evidence["target"]
		}
		for class, rel := range map[string]string{
			"serve claims": "serve", "GitHub rate-limit hint": "rate-limit.json",
			"GitLab rate-limit hint": "ratelimit-gitlab-gitlab.example.com.json", "machine-id": "machine-id",
			"telemetry notice": "telemetry-notice-v1", "usage": "usage",
			"OpenCode run roots": "opencode/runs", "OpenCode evidence": "opencode/evidence",
			"OpenCode last dispatch": "opencode/last-dispatch.json", "OpenCode endpoint slots": "opencode/endpoint-slots.json",
			"OpenCode self-test records": "opencode/self-test", "machine logs": "logs",
		} {
			if want := filepath.Join(f.state, filepath.FromSlash(rel)); targets[class] != want {
				t.Errorf("%s target = %q, want %q", class, targets[class], want)
			}
		}
		if len(found) != 12 {
			t.Errorf("%d findings, want 12 (one per class):\n%s", len(found), findingsText(found))
		}
		if code := BuildResult([]CheckResult{{ID: checkLayout, Status: StatusFailed, Findings: found}}).ExitCode; code != 0 {
			t.Errorf("doctor exit code with only legacy machine-state findings = %d, want 0 (unchanged)", code)
		}

		rep := f.fixer(m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 0 || rep.Counts.Fixed != len(found) {
			t.Fatalf("fix: exit %d, counts %+v, want exit 0 and %d fixed; results %+v", rep.ExitCode, rep.Counts, len(found), rep.Results)
		}

		for rel, content := range f.files {
			if rel == "rate-limit.json" {
				continue
			}
			if got := readLayoutFile(t, filepath.Join(f.state, filepath.FromSlash(rel))); got != content {
				t.Errorf("%s = %q, want the legacy bytes %q", rel, got, content)
			}
			assertGone(t, filepath.Join(f.legacy, filepath.FromSlash(rel)))
		}
		if got := readLayoutFile(t, filepath.Join(f.state, "rate-limit.json")); got != `{"remaining":3999}` {
			t.Errorf("the newer rate-limit hint was replaced: %q", got)
		}
		assertGone(t, filepath.Join(f.legacy, "rate-limit.json"))
		link, err := os.Readlink(filepath.Join(f.state, "opencode", "runs", "01890a5d-ac96-774b-bcce-b302099a8057", "config-git"))
		if err != nil || link != filepath.Join(f.home, ".config", "git") {
			t.Errorf("the run root's link = %q (%v), want it recreated with the same text", link, err)
		}
		assertGone(t, filepath.Join(f.legacy, "opencode"))
		assertGone(t, filepath.Join(f.legacy, "serve"))

		// machine-id: the same bytes, mode 0600, and the resolver reads it
		// without the legacy-copy warning or error (#2031) and mints nothing.
		idPath := filepath.Join(f.state, "machine-id")
		if got := readLayoutFile(t, idPath); got != "11111111-2222-4333-8444-555555555555\n" {
			t.Errorf("machine-id = %q, want the legacy bytes", got)
		}
		if info, err := os.Stat(idPath); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("machine-id mode = %v (%v), want 0600", info.Mode().Perm(), err)
		}
		assertGone(t, filepath.Join(f.legacy, "machine-id"))
		if diverged, err := layout.CopyLegacyStateFile("machine-id", f.state); diverged || err != nil {
			t.Errorf("after the move the legacy copy check reports diverged=%v err=%v", diverged, err)
		}
		t.Setenv("NIGHTGAUGE_AGENT_ID", "")
		if id, err := platform.MachineID(); err != nil || id != "11111111-2222-4333-8444-555555555555" {
			t.Errorf("MachineID after the move = %q, %v; want the moved id", id, err)
		}

		// The state directory is private, the marker is present.
		if info, err := os.Stat(f.state); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("state dir mode = %v (%v), want 0700", info.Mode().Perm(), err)
		}
		if got := readLayoutFile(t, filepath.Join(f.state, layoutMarkerName)); got != strconv.Itoa(MachineStateLayoutVersion)+"\n" {
			t.Errorf("marker = %q, want %d", got, MachineStateLayoutVersion)
		}
		// Not machine state: left exactly where they were.
		if got := readLayoutFile(t, filepath.Join(f.legacy, "config.yaml")); got != "owner: someone\n" {
			t.Errorf("config.yaml changed: %q", got)
		}
		if got := readLayoutFile(t, filepath.Join(f.legacy, "tools", "opencode", "package.json")); got != "{}\n" {
			t.Errorf("tools/ changed: %q", got)
		}
		assertGone(t, filepath.Join(f.state, "config.yaml"))
		assertGone(t, filepath.Join(f.state, "opencode", "self-test"))

		// A second run is a no-op.
		legacyBefore, stateBefore := treeDigest(t, f.legacy), treeDigest(t, f.state)
		if again, _ := machineFindings(m); len(again) != 0 {
			t.Errorf("findings after the move:\n%s", findingsText(again))
		}
		rep = f.fixer(m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 0 || rep.Counts.Fixed != 0 {
			t.Errorf("second fix: exit %d, counts %+v, want a no-op", rep.ExitCode, rep.Counts)
		}
		if again := m.Migrate(context.Background()); again.Changed() || again.Version != MachineStateLayoutVersion {
			t.Errorf("second migration: %s", again.Summary())
		}
		if treeDigest(t, f.legacy) != legacyBefore || treeDigest(t, f.state) != stateBefore {
			t.Error("the second run changed the tree")
		}
	})

	t.Run("a live serve lease leaves the serve claims in place", func(t *testing.T) {
		f := newMachineStateFixture(t)
		lock, err := os.OpenFile(f.serveLock, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := flock.Exclusive(lock, 0); err != nil {
			t.Skipf("flock unavailable: %v", err)
		}
		defer func() { _ = flock.Unlock(lock) }()
		m := f.migrator()
		found, _ := machineFindings(m)
		var busy []string
		for _, fd := range found {
			if fd.Evidence["busy"] != "" {
				busy = append(busy, fd.Evidence["class"])
			}
		}
		sort.Strings(busy)
		if strings.Join(busy, ",") != "OpenCode run roots,serve claims" {
			t.Errorf("held classes = %v, want the serve claims and the OpenCode run roots", busy)
		}
		rep := f.fixer(m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 4 {
			t.Errorf("fix exit = %d, want 4 (blocked by the live daemon); results %+v", rep.ExitCode, rep.Results)
		}
		for rel := range f.files {
			held := strings.HasPrefix(rel, "serve/") || strings.HasPrefix(rel, "opencode/runs/")
			_, legacyErr := os.Lstat(filepath.Join(f.legacy, filepath.FromSlash(rel)))
			if held && legacyErr != nil {
				t.Errorf("%s moved while a daemon held the serve lease", rel)
			}
			if !held && legacyErr == nil {
				t.Errorf("%s was not moved; only the daemon's classes wait", rel)
			}
		}
		if got := readLayoutFile(t, filepath.Join(f.state, "machine-id")); got != "11111111-2222-4333-8444-555555555555\n" {
			t.Errorf("machine-id = %q, want it moved meanwhile", got)
		}
		assertGone(t, filepath.Join(f.state, layoutMarkerName))

		// The automatic run at CLI start leaves the same data and fails nothing.
		auto, ran := autoMigrateMachineState(context.Background(), m, time.Now())
		if !ran || auto.FilesHeld == "" || auto.Version != 0 {
			t.Errorf("automatic run: ran=%v %s; want it held by the daemon", ran, auto.Summary())
		}
	})

	t.Run("a differing machine-id is a conflict: nothing moves, exit 3", func(t *testing.T) {
		f := newMachineStateFixture(t)
		writeLayoutFile(t, filepath.Join(f.state, "machine-id"), "99999999-2222-4333-8444-555555555555\n", 0o600)
		before := treeDigest(t, f.legacy)
		m := f.migrator()
		if len(findingsWith(mustMachineFindings(t, m), codeLayoutConflict)) != 1 {
			t.Fatal("no conflict finding for the differing machine-id")
		}
		rep := f.fixer(m).Run(context.Background(), FixOptions{})
		if rep.ExitCode != 3 {
			t.Errorf("fix exit = %d, want 3 (conflict); results %+v", rep.ExitCode, rep.Results)
		}
		if treeDigest(t, f.legacy) != before {
			t.Error("the legacy tree changed despite the conflict")
		}
		if got := readLayoutFile(t, filepath.Join(f.state, "machine-id")); got != "99999999-2222-4333-8444-555555555555\n" {
			t.Errorf("the new machine-id was overwritten: %q", got)
		}
		// The automatic run never fails the command.
		if auto, ran := autoMigrateMachineState(context.Background(), m, time.Now()); !ran || len(auto.Conflicts) == 0 {
			t.Errorf("automatic run: ran=%v %s; want the conflict reported", ran, auto.Summary())
		}
	})

	t.Run("a legacy class that is a symlink is refused, never followed", func(t *testing.T) {
		f := newMachineStateFixture(t)
		outside := filepath.Join(t.TempDir(), "outside")
		writeLayoutFile(t, filepath.Join(outside, "claude-rate-limits.json"), "outside\n", 0o644)
		if err := os.RemoveAll(filepath.Join(f.legacy, "usage")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(f.legacy, "usage")); err != nil {
			t.Fatal(err)
		}
		m := f.migrator()
		if refused := findingsWith(mustMachineFindings(t, m), codeLayoutRefused); len(refused) != 1 || refused[0].Evidence["class"] != "usage" {
			t.Fatalf("refused findings = %v, want the usage symlink", refused)
		}
		rep := m.Migrate(context.Background())
		if len(rep.Refused) != 1 || rep.Version != 0 {
			t.Errorf("migration: %s; want the symlink refused and no marker", rep.Summary())
		}
		if got := readLayoutFile(t, filepath.Join(outside, "claude-rate-limits.json")); got != "outside\n" {
			t.Errorf("the link target changed: %q", got)
		}
		assertGone(t, filepath.Join(f.state, "usage"))
	})

	t.Run("the automatic run is a no-op once the marker is current", func(t *testing.T) {
		f := newMachineStateFixture(t)
		writeLayoutFile(t, filepath.Join(f.state, layoutMarkerName), strconv.Itoa(MachineStateLayoutVersion)+"\n", 0o600)
		before := treeDigest(t, f.legacy)
		if _, ran := autoMigrateMachineState(context.Background(), f.migrator(), time.Now()); ran {
			t.Error("the automatic run ran with a current marker")
		}
		if treeDigest(t, f.legacy) != before {
			t.Error("the legacy tree changed")
		}
	})
}

// TestAutoMigrateMachineStateCreatesNothingWithoutLegacyData: on a machine an
// older build never ran on, the automatic run at CLI start does nothing and
// creates no directory.
func TestAutoMigrateMachineStateCreatesNothingWithoutLegacyData(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	m := &machineStateMigrator{
		legacyRoot: filepath.Join(base, "home", ".nightgauge"),
		statePath:  func() (string, error) { return state, nil },
		stateHome:  func() (string, error) { t.Error("the state root was created"); return state, nil },
		entries:    machineStateEntries(),
	}
	if _, ran := autoMigrateMachineState(context.Background(), m, time.Now()); ran {
		t.Error("the automatic run ran with nothing to migrate")
	}
	assertGone(t, state)
}

func mustMachineFindings(t *testing.T, m *machineStateMigrator) []Finding {
	t.Helper()
	found, _ := machineFindings(m)
	return found
}
