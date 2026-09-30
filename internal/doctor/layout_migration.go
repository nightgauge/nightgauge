package doctor

// The `layout_migration` check and the `layout.migrate` remedy (#2040,
// ADR-024 § 15, ADR-025 § 3).
//
// ADR-024 moves the per-clone classes out of the working tree. Runtime code
// reads and writes only the new locations, so data an older build left at an
// old location is invisible until it is moved. This file is the one path that
// moves it: plain `nightgauge doctor` reports every class that still has files
// at an old location, with the exact target, and `nightgauge doctor --fix`
// moves them through the remedy engine.
//
// The framework is a table of (class, legacy path, new path) rows,
// LayoutEntry. The per-clone rows are perCloneLayoutEntries; the machine-state
// sibling adds its own rows with the same shape. A row's new path comes from
// the class resolver, never a hand-join, so the migration follows the
// resolver: while a resolver still returns the old location the row is "in
// place" and there is nothing to move.
//
// Mechanics (ADR-024 § 15):
//
//   - The move takes an exclusive lock (.migrate.lock in the new root) and
//     re-scans under it. It does not start while a daemon of the clone holds
//     its serve lease; it moves no file class while a run is in flight; and it
//     moves no worktree a run is in flight on (the in-flight set the worktree
//     sweep uses, state.ActiveIssuesFromSnapshots).
//   - Per file: on one filesystem a hard link then an unlink (a rename that can
//     never replace an existing target); across filesystems a copy to a
//     temporary file in the target directory, fsync, a link into place, an
//     fsync of the directory, then the source is deleted. Modes and mtimes are
//     kept. A symlink is recreated as a symlink with the same text, never
//     followed.
//   - A target equal byte for byte to its source means the move finished
//     earlier: the source is deleted. A target that differs is a conflict:
//     nothing in the root is moved and both paths are reported. Append-only
//     JSONL (pipeline history, github-api.jsonl) is the one exception: the two
//     files are merged as the union of their lines, ordered by timestamp.
//   - Caches are deleted, not moved: they are derived and rebuild on next use.
//   - Worktrees move with `git worktree move`.
//   - Sources must resolve inside the checkout they belong to and targets
//     inside the new root after symlink evaluation. Directories created under
//     the new root are 0700, and none of their components may be a symlink.
//   - The layout-version marker is written last, and only when nothing is left
//     at an old location, so an interrupted migration runs again.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// LayoutVersion is the per-clone layout this build reads and writes. The
// marker in the new root records the version a migration last completed.
const LayoutVersion = 1

// LayoutCheckID is the registry ID of the data-layout check, whose detail
// line names the layout version once nothing is left at an old location.
const LayoutCheckID = checkLayout

const (
	checkLayout        = "layout_migration"
	codeLayoutLegacy   = "NGD044" // files, a worktree or a cache at an old location
	codeLayoutConflict = "NGD045" // a file at both locations that differs
	codeLayoutRefused  = "NGD046" // an old location that cannot be migrated safely
	verbLayoutMigrate  = "layout.migrate"

	layoutMarkerName = "layout-version"
	layoutLockName   = ".migrate.lock"
	layoutTempPrefix = ".ng-migrate-"

	// legacyDataDirName is the working-tree directory the per-clone classes
	// lived in before ADR-024. A legacy path is fixed by definition, so it is
	// spelled here rather than asked of a resolver that has moved on.
	legacyDataDirName = ".nightgauge"

	// maxConflictFindings bounds the per-file conflict findings of one class;
	// the last one names how many more there are.
	maxConflictFindings = 20
)

// LayoutKind says how a row's data is migrated.
type LayoutKind string

const (
	// LayoutFiles is moved file by file into the new directory.
	LayoutFiles LayoutKind = "files"
	// LayoutWorktrees holds git worktrees, moved with `git worktree move`.
	LayoutWorktrees LayoutKind = "worktrees"
	// LayoutCache is derived data: the old copy is deleted, never moved.
	LayoutCache LayoutKind = "cache"
)

// LayoutEntry is one row of the migration table: a data class, where an older
// build kept it, and where this build keeps it. Legacy and Target take the
// repository's main checkout root.
type LayoutEntry struct {
	Class  string
	Kind   LayoutKind
	Legacy func(root string) (string, error)
	// Target is the new directory. For LayoutWorktrees it is the worktree
	// base; for LayoutCache it is nil.
	Target func(root string) (string, error)
	// AppendOnly reports whether a file (by its path relative to Legacy, with
	// forward slashes) is an append-only JSONL log whose two copies are merged
	// by line union instead of being a conflict.
	AppendOnly func(rel string) bool
}

// legacyDir is <root>/.nightgauge/<elem...>.
func legacyDir(elem ...string) func(string) (string, error) {
	return func(root string) (string, error) {
		if root == "" || !filepath.IsAbs(root) {
			return "", fmt.Errorf("%w: %q", layout.ErrRootNotAbsolute, root)
		}
		return filepath.Join(append([]string{root, legacyDataDirName}, elem...)...), nil
	}
}

// perCloneLayoutEntries is the per-clone migration table (ADR-024 § 2, § 15).
func perCloneLayoutEntries() []LayoutEntry {
	return []LayoutEntry{
		{Class: "pipeline", Kind: LayoutFiles, Legacy: legacyDir("pipeline"), Target: layout.PipelineStateDir,
			AppendOnly: func(rel string) bool { return strings.HasPrefix(rel, "history/") && strings.HasSuffix(rel, ".jsonl") }},
		{Class: "plans", Kind: LayoutFiles, Legacy: legacyDir("plans"), Target: layout.PlansDir},
		{Class: "retros", Kind: LayoutFiles, Legacy: legacyDir("retros"), Target: layout.RetrosDir},
		{Class: "logs", Kind: LayoutFiles, Legacy: legacyDir("logs"), Target: layout.CloneLogsDir,
			AppendOnly: func(rel string) bool { return strings.HasPrefix(rel, "github-api") && strings.HasSuffix(rel, ".jsonl") }},
		// Worktrees: the Go manager's base before #2038 and the extension's
		// repo-relative `.worktrees` default (ADR-024 § 9).
		{Class: "worktrees", Kind: LayoutWorktrees, Legacy: legacyDir("worktrees"), Target: config.ResolveWorktreeBase},
		{Class: "worktrees", Kind: LayoutWorktrees, Target: config.ResolveWorktreeBase,
			Legacy: func(root string) (string, error) {
				if root == "" || !filepath.IsAbs(root) {
					return "", fmt.Errorf("%w: %q", layout.ErrRootNotAbsolute, root)
				}
				return filepath.Join(root, ".worktrees"), nil
			}},
		// The recall index moved to CACHE/recall/<root-key> (#2028).
		{Class: "recall cache", Kind: LayoutCache, Legacy: legacyDir("knowledge", ".recall-cache")},
	}
}

// cloneRoot is CLONE = <git-common-dir>/nightgauge (ADR-024 § 1), where the
// marker and the lock live.
func cloneRoot(root string) (string, error) {
	common, err := layout.GitCommonDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "nightgauge"), nil
}

// layoutMigrator migrates one repository. Every effect on the world outside
// the file system is a field, so tests can point the new paths at a fixture.
type layoutMigrator struct {
	root    string // the main checkout
	entries []LayoutEntry
	newRoot func(root string) (string, error)
	// inFlight returns the issues with a run in flight, each with the arm
	// that vouched for it, scanning every pipeline-state directory given.
	inFlight func(dirs []string) (map[int]string, error)
	// daemonLive names a live daemon of any of the checkouts.
	daemonLive func(checkouts []string) (string, bool)
}

// newLayoutMigrator is the production migrator for the repository containing
// dir, or nil when dir is not inside a git checkout.
func newLayoutMigrator(dir string) *layoutMigrator {
	root := config.MainCheckoutRoot(dir)
	if root == "" {
		return nil
	}
	return &layoutMigrator{
		root: root, entries: perCloneLayoutEntries(), newRoot: cloneRoot,
		inFlight: snapshotInFlight, daemonLive: serveLeaseLive,
	}
}

// snapshotInFlight is the worktree sweep's in-flight detection over several
// pipeline-state directories: the old and the new one both hold snapshots
// until the migration has run.
func snapshotInFlight(dirs []string) (map[int]string, error) {
	out := map[int]string{}
	for _, dir := range dirs {
		active, err := state.ActiveIssuesFromSnapshots(dir)
		if err != nil {
			return nil, err
		}
		for n := range active.Issues {
			if _, seen := out[n]; !seen {
				out[n] = active.Protected[n]
			}
		}
	}
	return out, nil
}

// serveLeaseLive reports a daemon holding the serve lease of any checkout.
func serveLeaseLive(checkouts []string) (string, bool) {
	for _, c := range checkouts {
		if holder, held := runstate.InspectServeLease(c); held {
			if holder.Known {
				return fmt.Sprintf("a daemon (pid %d) serves %s", holder.PID, c), true
			}
			return "a daemon serves " + c, true
		}
	}
	return "", false
}

// layoutFile is one file of a LayoutFiles row.
type layoutFile struct {
	Rel      string // relative to the legacy directory, forward slashes
	Src, Dst string
	Link     string // symlink text when the source is a symlink
	Mode     fs.FileMode
	Done     bool // the target already holds these bytes: only the source goes
	Merge    bool // append-only JSONL present at both locations
}

// layoutConflict is a file at both locations with different content.
type layoutConflict struct {
	Class, Legacy, Target string
}

// layoutWorktree is one git worktree at an old base.
type layoutWorktree struct {
	Path, Target string
	Issue        int
	Busy         string // the in-flight arm that protects it; "" when idle
}

// layoutItem is one row of the table, scanned.
type layoutItem struct {
	Entry          LayoutEntry
	Legacy, Target string
	InPlace        bool   // the resolver still returns the old location
	Refused        string // why the row cannot be migrated safely
	Files          []layoutFile
	Dirs           []string // legacy directories, parents first
	Conflicts      []layoutConflict
	Worktrees      []layoutWorktree
	CacheExists    bool
}

// pending reports whether the row has anything at its old location.
func (it layoutItem) pending() bool {
	return len(it.Files) > 0 || len(it.Conflicts) > 0 || len(it.Worktrees) > 0 || it.CacheExists || it.Refused != ""
}

// layoutPlan is a scan of every row.
type layoutPlan struct {
	Root, NewRoot string
	Version       int // the marker's version; 0 when absent
	Items         []layoutItem
	InFlight      map[int]string
}

func (p layoutPlan) conflicts() []layoutConflict {
	var out []layoutConflict
	for _, it := range p.Items {
		out = append(out, it.Conflicts...)
	}
	return out
}

func (p layoutPlan) refused() []string {
	var out []string
	for _, it := range p.Items {
		if it.Refused != "" {
			out = append(out, it.Refused)
		}
	}
	return out
}

// pending reports whether anything is left at an old location.
func (p layoutPlan) pending() bool {
	for _, it := range p.Items {
		if it.pending() {
			return true
		}
	}
	return false
}

// relocated reports whether every file row's resolver returns a new location,
// which is when the layout this build uses is LayoutVersion.
func (p layoutPlan) relocated() bool {
	for _, it := range p.Items {
		if it.InPlace {
			return false
		}
	}
	return true
}

// inFlightSummary names the in-flight issues, "" when none.
func (p layoutPlan) inFlightSummary() string {
	if len(p.InFlight) == 0 {
		return ""
	}
	nums := make([]int, 0, len(p.InFlight))
	for n := range p.InFlight {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	parts := make([]string, 0, len(nums))
	for _, n := range nums {
		parts = append(parts, fmt.Sprintf("#%d (%s)", n, p.InFlight[n]))
	}
	return strings.Join(parts, ", ")
}

// scan reads every row without changing anything.
func (m *layoutMigrator) scan() (layoutPlan, error) {
	plan := layoutPlan{Root: m.root}
	newRoot, err := m.newRoot(m.root)
	if err != nil {
		return plan, fmt.Errorf("resolve the new per-clone root: %w", err)
	}
	plan.NewRoot = filepath.Clean(newRoot)
	plan.Version = readLayoutMarker(filepath.Join(plan.NewRoot, layoutMarkerName))

	resolvedRoot, err := filepath.EvalSymlinks(m.root)
	if err != nil {
		return plan, fmt.Errorf("resolve %s: %w", m.root, err)
	}

	var stateDirs []string
	for _, e := range m.entries {
		if e.Class != "pipeline" || e.Kind != LayoutFiles {
			continue
		}
		for _, f := range []func(string) (string, error){e.Legacy, e.Target} {
			if dir, err := f(m.root); err == nil && !slices.Contains(stateDirs, dir) {
				stateDirs = append(stateDirs, dir)
			}
		}
	}
	plan.InFlight = map[int]string{}
	if len(stateDirs) > 0 {
		active, err := m.inFlight(stateDirs)
		if err != nil {
			// "I could not look" is never "nothing is running" (#296).
			return plan, fmt.Errorf("the in-flight set is unreadable, so nothing may be moved: %w", err)
		}
		plan.InFlight = active
	}

	var worktrees []gitWorktree
	listed := false
	for _, e := range m.entries {
		it := layoutItem{Entry: e}
		legacy, err := e.Legacy(m.root)
		if err != nil {
			return plan, fmt.Errorf("resolve the old %s location: %w", e.Class, err)
		}
		it.Legacy = filepath.Clean(legacy)
		var targetErr error
		if e.Target != nil {
			target, err := e.Target(m.root)
			if err != nil {
				targetErr = err
			} else {
				it.Target = filepath.Clean(target)
			}
		}
		info, err := os.Lstat(it.Legacy)
		switch {
		case e.Kind == LayoutFiles && it.Legacy == it.Target:
			it.InPlace = true
		case errors.Is(err, fs.ErrNotExist):
		case targetErr != nil:
			it.Refused = fmt.Sprintf("the new %s location cannot be resolved: %v", e.Class, targetErr)
		case err != nil:
			it.Refused = fmt.Sprintf("cannot read %s: %v", it.Legacy, err)
		case e.Kind == LayoutCache:
			if reason := confineSource(resolvedRoot, it.Legacy); reason != "" {
				it.Refused = reason
			} else {
				it.CacheExists = true
			}
		case info.Mode()&fs.ModeSymlink != 0:
			it.Refused = fmt.Sprintf("%s is a symlink; the migration moves only a real directory, so a link cannot aim it elsewhere", it.Legacy)
		case !info.IsDir():
			it.Refused = fmt.Sprintf("%s is not a directory", it.Legacy)
		default:
			if reason := confineSource(resolvedRoot, it.Legacy); reason != "" {
				it.Refused = reason
				break
			}
			switch e.Kind {
			case LayoutFiles:
				if err := m.scanFiles(&it, plan.NewRoot); err != nil {
					return plan, err
				}
			case LayoutWorktrees:
				if !listed {
					if worktrees, err = listGitWorktrees(m.root); err != nil {
						return plan, err
					}
					listed = true
				}
				m.scanWorktrees(&it, worktrees, plan.InFlight)
			}
		}
		plan.Items = append(plan.Items, it)
	}
	return plan, nil
}

// confineSource refuses an old location that resolves outside the checkout.
func confineSource(resolvedRoot, legacy string) string {
	resolved, err := filepath.EvalSymlinks(legacy)
	if err != nil {
		return fmt.Sprintf("cannot resolve %s: %v", legacy, err)
	}
	if !withinDir(resolvedRoot, resolved) {
		return fmt.Sprintf("%s resolves to %s, outside the checkout %s", legacy, resolved, resolvedRoot)
	}
	return ""
}

// scanFiles lists a LayoutFiles row's untracked files and classifies each
// against its target.
func (m *layoutMigrator) scanFiles(it *layoutItem, newRoot string) error {
	resolvedTarget, err := layout.EvalExisting(it.Target)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", it.Target, err)
	}
	resolvedNewRoot, err := layout.EvalExisting(newRoot)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", newRoot, err)
	}
	if !withinDir(resolvedNewRoot, resolvedTarget) {
		it.Refused = fmt.Sprintf("the new %s location %s resolves to %s, outside the new root %s",
			it.Entry.Class, it.Target, resolvedTarget, resolvedNewRoot)
		return nil
	}
	tracked, err := trackedFiles(m.root, it.Legacy)
	if err != nil {
		return err
	}
	return filepath.WalkDir(it.Legacy, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if d.IsDir() {
			it.Dirs = append(it.Dirs, path)
			return nil
		}
		// A tracked file (the template's .gitkeep) is the repository's, not
		// run data: it stays, so `git status` is unchanged by the migration.
		if tracked[path] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		rel, err := filepath.Rel(it.Legacy, path)
		if err != nil {
			return err
		}
		f := layoutFile{Rel: filepath.ToSlash(rel), Src: path, Dst: filepath.Join(it.Target, rel), Mode: info.Mode()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if f.Link, err = os.Readlink(path); err != nil {
				return fmt.Errorf("read link %s: %w", path, err)
			}
		case !info.Mode().IsRegular():
			// A socket or a pipe is recreated by whoever owns it.
			return nil
		}
		same, exists, err := sameAtTarget(f)
		if err != nil {
			return err
		}
		switch {
		case !exists:
		case same:
			f.Done = true
		case f.Link == "" && it.Entry.AppendOnly != nil && it.Entry.AppendOnly(f.Rel) && isRegular(f.Dst):
			f.Merge = true
		default:
			it.Conflicts = append(it.Conflicts, layoutConflict{Class: it.Entry.Class, Legacy: f.Src, Target: f.Dst})
			return nil
		}
		it.Files = append(it.Files, f)
		return nil
	})
}

// sameAtTarget compares a source with its target: the same bytes for a
// regular file, the same link text for a symlink.
func sameAtTarget(f layoutFile) (same, exists bool, err error) {
	info, err := os.Lstat(f.Dst)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, true, fmt.Errorf("stat %s: %w", f.Dst, err)
	}
	if f.Link != "" {
		if info.Mode()&fs.ModeSymlink == 0 {
			return false, true, nil
		}
		text, err := os.Readlink(f.Dst)
		return err == nil && text == f.Link, true, nil
	}
	if !info.Mode().IsRegular() {
		return false, true, nil
	}
	eq, err := filesEqual(f.Src, f.Dst)
	return eq, true, err
}

func filesEqual(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	da, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(da, db), nil
}

func isRegular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// trackedFiles is the set of files git tracks under dir, as absolute paths.
func trackedFiles(root, dir string) (map[string]bool, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return map[string]bool{}, nil
	}
	out, err := gitCmd(root, "ls-files", "-z", "--", filepath.ToSlash(rel)).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", root, err)
	}
	set := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			set[filepath.Join(root, filepath.FromSlash(p))] = true
		}
	}
	return set, nil
}

// gitWorktree is one entry of `git worktree list --porcelain`.
type gitWorktree struct {
	Path      string
	Prunable  bool
	Locked    bool
	IsPrimary bool
}

func listGitWorktrees(root string) ([]gitWorktree, error) {
	out, err := gitCmd(root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list in %s: %w", root, err)
	}
	var list []gitWorktree
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			list = append(list, gitWorktree{Path: strings.TrimPrefix(line, "worktree "), IsPrimary: len(list) == 0})
		case len(list) == 0:
		case strings.HasPrefix(line, "prunable"):
			list[len(list)-1].Prunable = true
		case strings.HasPrefix(line, "locked"):
			list[len(list)-1].Locked = true
		}
	}
	return list, sc.Err()
}

// scanWorktrees finds the registered worktrees directly under a row's old
// base and names the new path of each.
func (m *layoutMigrator) scanWorktrees(it *layoutItem, list []gitWorktree, inFlight map[int]string) {
	resolvedLegacy, err := filepath.EvalSymlinks(it.Legacy)
	if err != nil {
		it.Refused = fmt.Sprintf("cannot resolve %s: %v", it.Legacy, err)
		return
	}
	for _, wt := range list {
		if wt.IsPrimary || wt.Prunable {
			continue
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(wt.Path))
		if err != nil || parent != resolvedLegacy {
			continue
		}
		leaf := filepath.Base(wt.Path)
		n, ok := execution.IssueNumberFromWorktreeDir(leaf)
		if !ok {
			// Not a pipeline worktree: the operator made it, and it stays.
			continue
		}
		// The Go manager's leaf is already <repo>-issue-<N>; the extension's
		// `issue-<N>` gains the repository's directory name.
		repo := strings.TrimSuffix(leaf, "-issue-"+strconv.Itoa(n))
		if repo == leaf || repo == "" {
			repo = filepath.Base(m.root)
		}
		lw := layoutWorktree{Path: filepath.Join(it.Legacy, leaf), Issue: n, Busy: inFlight[n]}
		target, err := layout.WorktreePath(it.Target, repo, n)
		if err != nil {
			it.Refused = fmt.Sprintf("cannot name a new path for %s: %v", wt.Path, err)
			continue
		}
		lw.Target = target
		if _, err := os.Lstat(target); err == nil {
			it.Conflicts = append(it.Conflicts, layoutConflict{Class: it.Entry.Class, Legacy: lw.Path, Target: target})
			continue
		}
		it.Worktrees = append(it.Worktrees, lw)
	}
}

// LayoutReport is the result of one migration.
type LayoutReport struct {
	Root, NewRoot string
	// Version is the marker's version after the run; 0 when none is written.
	Version        int
	Moved          int // files moved
	Merged         int // append-only files merged by line union
	Removed        int // sources whose target already held the same bytes
	WorktreesMoved int
	CachesDeleted  int
	// Skipped names each worktree a run is in flight on, left in place.
	Skipped   []string
	Conflicts []layoutConflict
	// Blocked is why the migration did not start; "" when it ran.
	Blocked string
	// FilesHeld is why the file classes were not moved (a run in flight);
	// worktrees no run is on and caches were still migrated.
	FilesHeld string
	// InPlace lists the classes whose resolver still returns the old location.
	InPlace []string
	// Refused names each old location left alone because it cannot be
	// migrated safely (a symlink, a path outside the checkout, an
	// unresolvable new location).
	Refused []string
	Errors  []string
}

// Summary is the one-line account of a migration.
func (r LayoutReport) Summary() string {
	var parts []string
	if len(r.Conflicts) > 0 {
		parts = append(parts, fmt.Sprintf("%d conflict(s), nothing moved", len(r.Conflicts)))
	}
	if r.Blocked != "" {
		parts = append(parts, "blocked: "+r.Blocked)
	}
	if r.FilesHeld != "" {
		parts = append(parts, "held: "+r.FilesHeld)
	}
	if len(r.Refused) > 0 {
		parts = append(parts, "refused: "+strings.Join(r.Refused, "; "))
	}
	if n := r.Moved + r.Merged + r.Removed; n > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) moved (%d merged, %d already at the new location)", n, r.Merged, r.Removed))
	}
	if r.WorktreesMoved > 0 {
		parts = append(parts, fmt.Sprintf("%d worktree(s) moved", r.WorktreesMoved))
	}
	if r.CachesDeleted > 0 {
		parts = append(parts, fmt.Sprintf("%d old cache(s) deleted", r.CachesDeleted))
	}
	if len(r.Skipped) > 0 {
		parts = append(parts, "skipped busy worktree(s): "+strings.Join(r.Skipped, "; "))
	}
	if len(r.Errors) > 0 {
		parts = append(parts, "errors: "+strings.Join(r.Errors, "; "))
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing to migrate")
	}
	switch {
	case r.Version > 0:
		parts = append(parts, fmt.Sprintf("layout v%d", r.Version))
	case len(r.InPlace) > 0:
		parts = append(parts, "this build keeps "+strings.Join(r.InPlace, ", ")+" at the old location")
	}
	return strings.Join(parts, "; ")
}

// Migrate moves everything the scan finds at an old location.
func (m *layoutMigrator) Migrate(ctx context.Context) LayoutReport {
	rep := LayoutReport{Root: m.root}
	newRoot, err := m.newRoot(m.root)
	if err != nil {
		rep.Blocked = fmt.Sprintf("the new per-clone root cannot be resolved: %v", err)
		return rep
	}
	rep.NewRoot = filepath.Clean(newRoot)

	release, err := acquireLayoutLock(rep.NewRoot)
	if err != nil {
		rep.Blocked = err.Error()
		return rep
	}
	defer release()

	// Re-scan under the lock: another process may have finished the move.
	plan, err := m.scan()
	if err != nil {
		rep.Blocked = err.Error()
		return rep
	}
	for _, it := range plan.Items {
		if it.InPlace {
			rep.InPlace = append(rep.InPlace, it.Entry.Class)
		}
	}
	if !plan.pending() {
		if plan.relocated() {
			if plan.Version < LayoutVersion {
				if err := writeLayoutMarker(plan.NewRoot); err != nil {
					rep.Errors = append(rep.Errors, err.Error())
					return rep
				}
			}
			rep.Version = LayoutVersion
		}
		return rep
	}
	if rep.Conflicts = plan.conflicts(); len(rep.Conflicts) > 0 {
		return rep
	}
	// A refused row is reported and left alone; the other rows still move.
	rep.Refused = plan.refused()
	if who, live := m.daemonLive(checkoutsOf(m.root)); live {
		rep.Blocked = who + "; stop it, then run `nightgauge doctor --fix`"
		return rep
	}

	inFlight := plan.inFlightSummary()
	for _, it := range plan.Items {
		if ctx.Err() != nil {
			rep.Errors = append(rep.Errors, ctx.Err().Error())
			return rep
		}
		switch it.Entry.Kind {
		case LayoutCache:
			if it.CacheExists {
				if err := removeCache(it.Legacy); err != nil {
					rep.Errors = append(rep.Errors, err.Error())
				} else {
					rep.CachesDeleted++
				}
			}
		case LayoutFiles:
			if len(it.Files) == 0 {
				continue
			}
			if inFlight != "" {
				rep.FilesHeld = fmt.Sprintf("a run is in flight (%s), so no file is moved until it ends", inFlight)
				continue
			}
			m.moveFiles(it, plan.NewRoot, &rep)
		case LayoutWorktrees:
			for _, wt := range it.Worktrees {
				if wt.Busy != "" {
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s (#%d: %s)", wt.Path, wt.Issue, wt.Busy))
					continue
				}
				if err := moveWorktree(m.root, it.Target, wt); err != nil {
					rep.Errors = append(rep.Errors, err.Error())
					continue
				}
				rep.WorktreesMoved++
			}
			removeEmptyDir(it.Legacy)
		}
	}

	// The marker is written last, and only when nothing is left behind, so an
	// interrupted or partial migration runs again.
	after, err := m.scan()
	if err != nil {
		rep.Errors = append(rep.Errors, err.Error())
		return rep
	}
	if !after.pending() && after.relocated() && len(rep.Errors) == 0 {
		if err := writeLayoutMarker(after.NewRoot); err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			return rep
		}
		rep.Version = LayoutVersion
	}
	return rep
}

// moveFiles moves one row's files and prunes the emptied old directories.
func (m *layoutMigrator) moveFiles(it layoutItem, newRoot string, rep *LayoutReport) {
	resolvedLegacy, err := filepath.EvalSymlinks(it.Legacy)
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("resolve %s: %v", it.Legacy, err))
		return
	}
	for _, f := range it.Files {
		// The directory may have been swapped for a link since the scan.
		if parent, err := filepath.EvalSymlinks(filepath.Dir(f.Src)); err != nil || !withinDir(resolvedLegacy, parent) {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s no longer resolves inside %s; left in place", f.Src, it.Legacy))
			continue
		}
		if err := ensureConfinedDir(newRoot, filepath.Dir(f.Dst)); err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			continue
		}
		var err error
		switch {
		case f.Done:
			err = os.Remove(f.Src)
			if err == nil {
				rep.Removed++
			}
		case f.Merge:
			err = mergeAppendOnly(f)
			if err == nil {
				rep.Merged++
			}
		default:
			err = moveOne(f)
			if errors.Is(err, fs.ErrExist) {
				rep.Conflicts = append(rep.Conflicts, layoutConflict{Class: it.Entry.Class, Legacy: f.Src, Target: f.Dst})
				continue
			}
			if err == nil {
				rep.Moved++
			}
		}
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
		}
	}
	for i := len(it.Dirs) - 1; i >= 0; i-- {
		removeEmptyDir(it.Dirs[i])
	}
}

// moveOne moves one file or symlink without ever replacing an existing
// target: an existing target makes it return an error wrapping fs.ErrExist.
func moveOne(f layoutFile) error {
	dir := filepath.Dir(f.Dst)
	if f.Link != "" {
		if err := os.Symlink(f.Link, f.Dst); err != nil {
			return fmt.Errorf("recreate symlink %s: %w", f.Dst, err)
		}
		syncDir(dir)
		return os.Remove(f.Src)
	}
	// One filesystem: a hard link is a rename that fails on an existing
	// target, and keeps the inode, so the mode and mtime come along.
	err := os.Link(f.Src, f.Dst)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s: %w", f.Dst, fs.ErrExist)
	}
	if err != nil {
		if err := copyInto(f); err != nil {
			return err
		}
	}
	syncDir(dir)
	if err := os.Remove(f.Src); err != nil {
		return fmt.Errorf("remove %s after moving it: %w", f.Src, err)
	}
	return nil
}

// copyInto is the cross-filesystem move: a temporary copy in the target
// directory, synced, then linked into place.
func copyInto(f layoutFile) error {
	info, err := os.Stat(f.Src)
	if err != nil {
		return err
	}
	src, err := os.Open(f.Src)
	if err != nil {
		return err
	}
	defer src.Close()
	return installTemp(f.Dst, info.Mode().Perm(), info.ModTime(), false, func(w io.Writer) error {
		_, err := io.Copy(w, src)
		return err
	})
}

// installTemp writes a temporary file beside dst with mode perm and mtime,
// syncs it, and puts it in place: by a link that fails on an existing target,
// or, when replace is set (a merge, whose content is a superset of the
// target's), by a rename.
func installTemp(dst string, perm fs.FileMode, mtime time.Time, replace bool, write func(io.Writer) error) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, layoutTempPrefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := write(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	if !mtime.IsZero() {
		_ = os.Chtimes(name, mtime, mtime)
	}
	if replace {
		return os.Rename(name, dst)
	}
	err = os.Link(name, dst)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	if err != nil {
		// No hard links on this filesystem: rename, having just seen that
		// nothing is there.
		if _, statErr := os.Lstat(dst); statErr == nil {
			return fmt.Errorf("%s: %w", dst, fs.ErrExist)
		}
		return os.Rename(name, dst)
	}
	return nil
}

// mergeAppendOnly replaces the target of an append-only JSONL file with the
// union of both copies' lines, ordered by timestamp, then deletes the source.
func mergeAppendOnly(f layoutFile) error {
	dstLines, err := readLines(f.Dst)
	if err != nil {
		return err
	}
	srcLines, err := readLines(f.Src)
	if err != nil {
		return err
	}
	merged := unionByTimestamp(dstLines, srcLines)
	info, err := os.Stat(f.Dst)
	if err != nil {
		return err
	}
	if err := installTemp(f.Dst, info.Mode().Perm(), time.Time{}, true, func(w io.Writer) error {
		for _, l := range merged {
			if _, err := io.WriteString(w, l+"\n"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	syncDir(filepath.Dir(f.Dst))
	return os.Remove(f.Src)
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// unionByTimestamp merges two JSONL line lists: every distinct line once,
// ordered by its timestamp field. A line without one sorts with the line
// before it in its own file.
func unionByTimestamp(a, b []string) []string {
	type row struct {
		line string
		at   time.Time
		seq  int
	}
	seen := map[string]bool{}
	var rows []row
	for _, list := range [][]string{a, b} {
		var last time.Time
		for _, l := range list {
			if seen[l] {
				continue
			}
			seen[l] = true
			if t, ok := lineTimestamp(l); ok {
				last = t
			}
			rows = append(rows, row{line: l, at: last, seq: len(rows)})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.Before(rows[j].at)
		}
		return rows[i].seq < rows[j].seq
	})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.line
	}
	return out
}

func lineTimestamp(line string) (time.Time, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &obj) != nil {
		return time.Time{}, false
	}
	for _, k := range []string{"timestamp", "ts", "time", "at", "created_at", "started_at"} {
		var s string
		if raw, ok := obj[k]; ok && json.Unmarshal(raw, &s) == nil {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// moveWorktree moves an idle worktree with git, so its registration follows.
func moveWorktree(root, base string, wt layoutWorktree) error {
	if err := layout.EnsureWorktreeBase(base); err != nil {
		return err
	}
	if err := layout.CheckWorktreeContainment(base, wt.Target); err != nil {
		return err
	}
	if out, err := gitCmd(root, "worktree", "move", wt.Path, wt.Target).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree move %s %s: %v: %s", wt.Path, wt.Target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeCache deletes an old cache directory. RemoveAll removes symlinks
// inside it and never follows them; a cache that is itself a symlink loses
// only the link.
func removeCache(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

// removeEmptyDir removes dir if it is an empty real directory.
func removeEmptyDir(dir string) {
	if info, err := os.Lstat(dir); err == nil && info.IsDir() {
		_ = os.Remove(dir)
	}
}

// ensureConfinedDir creates dir under root one component at a time with
// mode 0700. Both are compared after symlink evaluation, so dir must resolve
// inside root; each component created below root is a real directory.
func ensureConfinedDir(root, dir string) error {
	if err := ensureRealDir(root, true); err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", root, err)
	}
	resolvedDir, err := layout.EvalExisting(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedDir)
	if err != nil || !withinDir(resolvedRoot, resolvedDir) {
		return fmt.Errorf("%s resolves to %s, outside the new root %s", dir, resolvedDir, resolvedRoot)
	}
	if rel == "." {
		return nil
	}
	cur := resolvedRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := ensureRealDir(cur, false); err != nil {
			return err
		}
	}
	return nil
}

// ensureRealDir creates dir with mode 0700 when absent (with its parents when
// it is a root) and refuses a symlink or a non-directory.
func ensureRealDir(dir string, isRoot bool) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		mk := os.Mkdir
		if isRoot {
			mk = os.MkdirAll
		}
		if err := mk(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		info, err = os.Lstat(dir)
	}
	switch {
	case err != nil:
		return fmt.Errorf("stat %s: %w", dir, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("refusing %s: it is a symlink", dir)
	case !info.IsDir():
		return fmt.Errorf("refusing %s: not a directory", dir)
	}
	return nil
}

// acquireLayoutLock takes the migration lock in the new root. A lock another
// process holds is a refusal, not a wait.
func acquireLayoutLock(newRoot string) (func(), error) {
	if err := ensureRealDir(newRoot, true); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(newRoot, layoutLockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the migration lock: %w", err)
	}
	switch err := flock.Exclusive(f, 0); {
	case err == nil, errors.Is(err, flock.ErrUnsupported):
	case errors.Is(err, flock.ErrWouldBlock):
		f.Close()
		return nil, fmt.Errorf("another process holds %s (a migration is running)", f.Name())
	default:
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return func() {
		_ = flock.Unlock(f)
		_ = f.Close()
	}, nil
}

func readLayoutMarker(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// writeLayoutMarker records LayoutVersion in the new root, atomically.
func writeLayoutMarker(newRoot string) error {
	if err := ensureRealDir(newRoot, true); err != nil {
		return err
	}
	dst := filepath.Join(newRoot, layoutMarkerName)
	if err := installTemp(dst, 0o600, time.Time{}, true, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "%d\n", LayoutVersion)
		return err
	}); err != nil {
		return fmt.Errorf("write the layout-version marker %s: %w", dst, err)
	}
	syncDir(newRoot)
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // not supported on every platform; the rename already happened
		_ = d.Close()
	}
}

// checkoutsOf lists every checkout of the clone, the main one first.
func checkoutsOf(root string) []string {
	list, err := listGitWorktrees(root)
	if err != nil || len(list) == 0 {
		return []string{root}
	}
	out := make([]string, 0, len(list))
	for _, wt := range list {
		if !wt.Prunable {
			out = append(out, wt.Path)
		}
	}
	return out
}

// gitCmd runs git in dir with the inherited location variables cleared: a
// GIT_DIR exported by a hook would answer for another repository.
func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="), strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_COMMON_DIR="), strings.HasPrefix(kv, "GIT_INDEX_FILE="),
			strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY="):
		default:
			env = append(env, kv)
		}
	}
	cmd.Env = env
	return cmd
}

// withinDir reports whether child is parent or lies under it.
func withinDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// --- the check ----------------------------------------------------------------

func init() {
	builtinChecks = append(builtinChecks, layoutCheck(newLayoutMigrator))
}

// layoutCheck is the `layout_migration` check over the migrator mk builds for
// the working directory.
func layoutCheck(mk func(dir string) *layoutMigrator) Check {
	return Check{
		ID: checkLayout, Title: "Data layout", Group: "hygiene", Code: codeLayoutLegacy,
		Run: func(_ context.Context, env *Env) []Finding {
			fs, detail := layoutFindings(mk(env.Cwd))
			env.SetDetail(checkLayout, detail)
			return fs
		},
	}
}

// layoutFindings reports what is still at an old location. Every finding is
// housekeeping: the report never changes doctor's exit status (ADR-024 § 15).
func layoutFindings(m *layoutMigrator) ([]Finding, string) {
	if m == nil {
		return nil, "not inside a git checkout, so there is no per-clone layout to check"
	}
	plan, err := m.scan()
	if err != nil {
		return []Finding{unverifiableFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			"per-clone data layout", err.Error())}, "layout scan could not run"
	}
	var out []Finding
	for _, it := range plan.Items {
		out = append(out, itemFindings(plan, it)...)
	}
	if len(out) > 0 {
		return out, fmt.Sprintf("%d item(s) at an old location; `nightgauge doctor --fix` moves them", len(out))
	}
	switch {
	case !plan.relocated():
		var inPlace []string
		for _, it := range plan.Items {
			if it.InPlace {
				inPlace = append(inPlace, it.Entry.Class)
			}
		}
		return nil, "nothing at an old location; this build keeps " + strings.Join(inPlace, ", ") + " under " + filepath.Join(plan.Root, legacyDataDirName)
	case plan.Version > 0:
		return nil, fmt.Sprintf("layout v%d (%s)", plan.Version, filepath.Join(plan.NewRoot, layoutMarkerName))
	default:
		return nil, fmt.Sprintf("layout v%d: nothing at an old location", LayoutVersion)
	}
}

// itemFindings turns one scanned row into findings.
func itemFindings(plan layoutPlan, it layoutItem) []Finding {
	class := it.Entry.Class
	base := map[string]string{"repo_root": plan.Root, "class": class, "legacy": it.Legacy}
	if it.Target != "" {
		base["target"] = it.Target
	}
	ev := func(extra map[string]string) map[string]string {
		out := make(map[string]string, len(base)+len(extra))
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	migrate := func(summary, preview string) Remedy {
		return Remedy{ID: "migrate", Kind: RemedyAuto, Summary: summary, Preview: preview,
			Verb: verbLayoutMigrate, Reversible: true, Verify: checkLayout}
	}
	var out []Finding
	if it.Refused != "" {
		out = append(out, newFinding(checkLayout, codeLayoutRefused, SeverityHousekeeping,
			fmt.Sprintf("layout-refused: the old %s location cannot be migrated safely", class),
			it.Refused+". Nothing there is moved or deleted",
			ev(map[string]string{"reason": it.Refused}), []string{plan.Root, class, it.Legacy},
			manualRemedy("inspect", "Remove the reason the evidence names, then migrate", checkLayout,
				"Resolve: "+it.Refused,
				"Re-run `nightgauge doctor --fix`; the other classes are migrated meanwhile")))
	}
	for i, c := range it.Conflicts {
		if i == maxConflictFindings {
			out = append(out, newFinding(checkLayout, codeLayoutConflict, SeverityHousekeeping,
				fmt.Sprintf("layout-conflict: %d more %s file(s) exist at both locations", len(it.Conflicts)-i, class),
				"the migration never overwrites a file, so it moves nothing in this clone until every conflict is resolved",
				ev(map[string]string{"count": strconv.Itoa(len(it.Conflicts) - i)}), []string{plan.Root, class, it.Legacy, "more"},
				manualRemedy("resolve", "Resolve each conflict", checkLayout,
					"Run `nightgauge doctor --only "+codeLayoutConflict+"` again after resolving the ones listed")))
			break
		}
		out = append(out, newFinding(checkLayout, codeLayoutConflict, SeverityHousekeeping,
			fmt.Sprintf("layout-conflict: a %s file exists at both %s and %s", class, c.Legacy, c.Target),
			"the two copies differ and the migration never overwrites a file, so nothing in this clone is moved until you choose one",
			ev(map[string]string{"legacy": c.Legacy, "target": c.Target}), []string{plan.Root, class, c.Legacy, c.Target},
			manualRemedy("resolve", "Keep one copy at the new location", checkLayout,
				"Compare "+c.Legacy+" with "+c.Target,
				"Keep the copy you want at "+c.Target+" and delete "+c.Legacy,
				"Re-run `nightgauge doctor --fix`")))
	}
	if len(it.Files) > 0 {
		sample := it.Files[0].Rel
		extra := map[string]string{"files": strconv.Itoa(len(it.Files)), "sample": sample}
		cause := fmt.Sprintf("this build reads and writes %s data only at %s, so these files are invisible to it. "+
			"`nightgauge doctor --fix` moves each one to %s (a rename on one filesystem, copy then delete across "+
			"filesystems) and never overwrites a file", class, it.Target, it.Target)
		if s := plan.inFlightSummary(); s != "" {
			extra["in_flight"] = s
			cause += ". A run is in flight (" + s + "), so --fix moves nothing until it ends"
		}
		out = append(out, newFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			fmt.Sprintf("legacy-layout: %d %s file(s) at the old location %s", len(it.Files), class, it.Legacy),
			cause, ev(extra), []string{plan.Root, class, it.Legacy},
			migrate("Move the "+class+" files to "+it.Target,
				fmt.Sprintf("move %d file(s) from %s to %s", len(it.Files), it.Legacy, it.Target))))
	}
	for _, wt := range it.Worktrees {
		extra := map[string]string{"path": wt.Path, "target": wt.Target, "issue": strconv.Itoa(wt.Issue)}
		cause := fmt.Sprintf("worktrees now live outside the working tree; `nightgauge doctor --fix` runs "+
			"`git worktree move %s %s`", wt.Path, wt.Target)
		if wt.Busy != "" {
			extra["busy"] = wt.Busy
			cause += fmt.Sprintf(". A run is in flight on it (%s), so --fix skips it until the run ends", wt.Busy)
		}
		out = append(out, newFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			fmt.Sprintf("legacy-worktree: the worktree for #%d is at the old location %s", wt.Issue, wt.Path),
			cause, ev(extra), []string{plan.Root, class, wt.Path},
			migrate("Move the worktree to "+wt.Target, "git worktree move "+wt.Path+" "+wt.Target)))
	}
	if it.CacheExists {
		out = append(out, newFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			fmt.Sprintf("legacy-cache: the old %s is still at %s", class, it.Legacy),
			"the cache now lives in the machine cache directory and rebuilds on next use; the old copy is "+
				"disposable, so `nightgauge doctor --fix` deletes it rather than moving it",
			ev(nil), []string{plan.Root, class, it.Legacy},
			migrate("Delete the old cache", "delete "+it.Legacy)))
	}
	return out
}

// --- the verb -----------------------------------------------------------------

// layoutMigrateVerb is `layout.migrate`: it runs the whole migration for the
// finding's repository, because the marker is written only once everything
// has moved. The engine re-runs the check to decide each finding's outcome.
func layoutMigrateVerb(mk func(dir string) *layoutMigrator) RemedyVerb {
	migrator := func(f Finding) (*layoutMigrator, error) {
		root, err := evidence(f, "repo_root")
		if err != nil {
			return nil, err
		}
		m := mk(root)
		if m == nil {
			return nil, fmt.Errorf("%s is no longer a git checkout", root)
		}
		return m, nil
	}
	return VerbFuncs{
		PreviewFunc: declaredPreview(verbLayoutMigrate),
		PreconditionFunc: func(_ context.Context, f Finding) error {
			m, err := migrator(f)
			if err != nil {
				return err
			}
			plan, err := m.scan()
			if err != nil {
				return err
			}
			return layoutRefusal(plan, f, m.daemonLive)
		},
		ApplyFunc: func(ctx context.Context, f Finding) error {
			m, err := migrator(f)
			if err != nil {
				return err
			}
			rep := m.Migrate(ctx)
			switch {
			case len(rep.Conflicts) > 0:
				c := rep.Conflicts[0]
				return fmt.Errorf("%w: %s exists at both %s and %s (%d conflict(s)); %s",
					ErrRemedyConflict, c.Class, c.Legacy, c.Target, len(rep.Conflicts), rep.Summary())
			case rep.Blocked != "":
				return fmt.Errorf("%w: %s", ErrRemedyBlocked, rep.Summary())
			case rep.FilesHeld != "" && f.Evidence["files"] != "":
				return fmt.Errorf("%w: %s", ErrRemedyBlocked, rep.Summary())
			}
			if wt := f.Evidence["path"]; wt != "" {
				for _, s := range rep.Skipped {
					if strings.HasPrefix(s, wt+" ") {
						return fmt.Errorf("%w: %s", ErrRemedyBlocked, rep.Summary())
					}
				}
			}
			if len(rep.Errors) > 0 {
				return errors.New(rep.Summary())
			}
			return nil
		},
	}
}

// layoutRefusal re-derives, at apply time, whether f's object may move now.
func layoutRefusal(plan layoutPlan, f Finding, daemonLive func([]string) (string, bool)) error {
	var cur *Finding
	for _, it := range plan.Items {
		for _, nf := range itemFindings(plan, it) {
			if nf.Fingerprint == f.Fingerprint {
				nf := nf
				cur = &nf
			}
		}
	}
	if cur == nil {
		return fmt.Errorf("%s is no longer at the old location", f.Evidence["legacy"])
	}
	if cs := plan.conflicts(); len(cs) > 0 {
		return fmt.Errorf("%w: %s exists at both %s and %s (%d conflict(s) in this clone)",
			ErrRemedyConflict, cs[0].Class, cs[0].Legacy, cs[0].Target, len(cs))
	}
	if who, live := daemonLive(checkoutsOf(plan.Root)); live {
		return fmt.Errorf("%w: %s; stop it, then run `nightgauge doctor --fix`", ErrRemedyBlocked, who)
	}
	if busy := cur.Evidence["busy"]; busy != "" {
		return fmt.Errorf("%w: a run is in flight on %s (%s); it is skipped until the run ends",
			ErrRemedyBlocked, cur.Evidence["path"], busy)
	}
	if s := cur.Evidence["in_flight"]; s != "" {
		return fmt.Errorf("%w: a run is in flight (%s), so no %s file is moved until it ends",
			ErrRemedyBlocked, s, cur.Evidence["class"])
	}
	return nil
}
