package doctor

// The machine-state rows of the `layout_migration` check and the
// `layout.migrate` remedy (#2041, ADR-024 § 2, § 8, § 15).
//
// Machine state moved from the hard-coded $HOME/.nightgauge to the
// machine-state root, STATE (layout.StateHome): the serve claims,
// rate-limit.json, the GitLab rate-limit files, machine-id and the telemetry
// notice (#2031), then usage/, OpenCode's run roots, evidence, last-dispatch
// record and endpoint slots, and the machine logs (#2032). Runtime code reads
// only STATE. These rows move what an older build left behind, once, with the
// per-clone migration's mechanics: the same lock, marker, confinement and
// per-file move (moveOne, mergeAppendOnly, ensureConfinedDir).
//
// Machine config (config.yaml) is not a row: CONFIG is its own root with its
// own resolver (internal/configpath), and on macOS it IS $HOME/.nightgauge.
// Nor is tools/, the operator-installed OpenCode pin (ADR-024 § 2, the
// "Operator-installed tools" exception).
//
// Rules the per-clone rows do not need:
//
//   - A live daemon is detected through the serve lease: the lock files in the
//     legacy serve/ directory, which an older daemon holds for its life and a
//     daemon of this build holds as its compatibility lock. While one is held
//     the serve claims and the OpenCode run roots (which a daemon's stage may be
//     using) are skipped and reported; every other row still moves.
//   - A hint (a rate-limit file, the telemetry notice, the usage readings, the
//     last-dispatch record, the endpoint slots, a serve claim) found at both
//     locations keeps the new copy and deletes the old one: the new one is what
//     this build wrote since, and a hint is never a reason to stop.
//   - machine-id is moved byte for byte, never regenerated, and ends mode 0600.
//     A machine-id at both locations that differs is a conflict: nothing is
//     moved and doctor --fix exits 3.
//   - opencode/self-test has had no reader since #2148, so it is deleted.
//   - The machine logs merge like append-only JSONL: a log is never a conflict.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// MachineStateLayoutVersion is the machine-state layout this build reads and
// writes. The marker STATE/layout-version records the version a migration
// last completed: 1 is every machine-state class under STATE (#2031, #2032).
const MachineStateLayoutVersion = 1

// machineScope marks a finding's evidence as a machine-state row, whose
// remedy runs the machine-state migration rather than a clone's.
const machineScope = "machine"

// machineStateKind says how a machine-state row's files are migrated.
type machineStateKind int

const (
	// machineMove moves each file; a target that differs is a conflict.
	machineMove machineStateKind = iota
	// machineHint moves each file; a target that differs is kept and the old
	// copy is deleted.
	machineHint
	// machineAppend moves each file; one at both locations is merged by line
	// union (mergeAppendOnly).
	machineAppend
	// machineObsolete has no reader in this build: the old copy is deleted.
	machineObsolete
)

// machineStateEntry is one machine-state row: a name under both the legacy
// root and STATE.
type machineStateEntry struct {
	Class string
	// Name is the path under both roots, with forward slashes. When Glob is
	// set it is a pattern of files directly in the legacy root, and the row
	// becomes one row per file that matches now.
	Name string
	Dir  bool
	Glob bool
	Kind machineStateKind
	// Daemon marks a class a live daemon may be using: it is held, not moved,
	// while a serve lease is held.
	Daemon bool
	// Mode, when non-zero, is the mode every migrated file of the row ends
	// with (machine-id: 0600).
	Mode fs.FileMode
}

// machineStateEntries is the machine-state migration table (ADR-024 § 2).
func machineStateEntries() []machineStateEntry {
	return []machineStateEntry{
		{Class: "serve claims", Name: "serve", Dir: true, Kind: machineHint, Daemon: true},
		{Class: "GitHub rate-limit hint", Name: "rate-limit.json", Kind: machineHint},
		{Class: "GitLab rate-limit hint", Name: "ratelimit-gitlab-*.json", Glob: true, Kind: machineHint},
		{Class: "machine-id", Name: "machine-id", Kind: machineMove, Mode: 0o600},
		{Class: "telemetry notice", Name: "telemetry-notice-v1", Kind: machineHint},
		{Class: "usage", Name: layout.StateUsage, Dir: true, Kind: machineHint},
		{Class: "OpenCode run roots", Name: layout.StateOpenCode + "/runs", Dir: true, Kind: machineMove, Daemon: true},
		{Class: "OpenCode evidence", Name: layout.StateOpenCode + "/evidence", Dir: true, Kind: machineMove},
		{Class: "OpenCode last dispatch", Name: layout.StateOpenCode + "/last-dispatch.json", Kind: machineHint},
		{Class: "OpenCode endpoint slots", Name: layout.StateOpenCode + "/endpoint-slots.json", Kind: machineHint},
		{Class: "OpenCode self-test records", Name: layout.StateOpenCode + "/self-test", Dir: true, Kind: machineObsolete},
		{Class: "machine logs", Name: layout.StateLogs, Dir: true, Kind: machineAppend},
	}
}

// machineStateMigrator migrates this user's machine state. Every effect on
// the world outside the file system is a field, so a test points both roots
// at a fixture.
type machineStateMigrator struct {
	// legacyRoot is $HOME/.nightgauge; "" when there is no home.
	legacyRoot string
	// statePath resolves STATE without creating it (a scan creates nothing);
	// stateHome resolves it and creates it 0700 (a migration).
	statePath, stateHome func() (string, error)
	// daemonLive names a live daemon from the legacy serve directory.
	daemonLive func(legacyServe string) (string, bool)
	entries    []machineStateEntry
}

// newMachineStateMigrator is the production machine-state migrator. A
// variable so a test drives the check without the machine's state.
var newMachineStateMigrator = func() *machineStateMigrator {
	return &machineStateMigrator{
		legacyRoot: layout.LegacyStatePath(""),
		statePath:  layout.StateHomePath,
		stateHome:  layout.StateHome,
		daemonLive: legacyServeLeaseLive,
		entries:    machineStateEntries(),
	}
}

// legacyServeLeaseLive reports a daemon holding a serve lease lock in the
// legacy serve directory: an older daemon's own lock, or the compatibility
// lock a daemon of this build takes there (runstate.acquireCompatServeLocks).
// It opens only existing lock files and creates nothing. A platform without
// flock proves nothing about a lock file; there a record younger than the
// claim window stands in for a lease.
func legacyServeLeaseLive(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".lock") {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR, 0)
		if err != nil {
			continue
		}
		lockErr := flock.Exclusive(f, 0)
		if lockErr == nil {
			_ = flock.Unlock(f)
		}
		_ = f.Close()
		if errors.Is(lockErr, flock.ErrWouldBlock) {
			if root, ok := runstate.ServeRegistryWorkspaceRoot(name); ok {
				return "a daemon serves " + root + " (its serve lease " + filepath.Join(dir, name) + " is held)", true
			}
			return "a daemon holds the serve lease " + filepath.Join(dir, name), true
		}
	}
	return "", false
}

// machineItem is one machine-state row, scanned.
type machineItem struct {
	Entry          machineStateEntry
	Legacy, Target string
	InPlace        bool   // the legacy path is the target
	Refused        string // why the row cannot be migrated safely
	Held           string // the live daemon that holds a Daemon row
	Files          []layoutFile
	Dirs           []string // legacy directories, parents first
	Conflicts      []layoutConflict
	Obsolete       bool // an obsolete row with something to delete
}

// pending reports whether the row has anything at its old location.
func (it machineItem) pending() bool {
	return len(it.Files) > 0 || len(it.Conflicts) > 0 || it.Obsolete || it.Refused != "" || it.Held != ""
}

// machinePlan is a scan of every machine-state row.
type machinePlan struct {
	Legacy, State string
	Version       int
	Items         []machineItem
}

func (p machinePlan) pending() bool {
	for _, it := range p.Items {
		if it.pending() {
			return true
		}
	}
	return false
}

func (p machinePlan) conflicts() []layoutConflict {
	var out []layoutConflict
	for _, it := range p.Items {
		out = append(out, it.Conflicts...)
	}
	return out
}

// scan reads every row without changing anything. state is STATE; resolved
// is the resolved legacy root ("" when it does not exist).
func (m *machineStateMigrator) scan(state string) (machinePlan, error) {
	plan := machinePlan{Legacy: m.legacyRoot, State: filepath.Clean(state)}
	plan.Version = readLayoutMarker(filepath.Join(plan.State, layoutMarkerName))
	if m.legacyRoot == "" {
		return plan, nil
	}
	info, err := os.Lstat(m.legacyRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, fmt.Errorf("stat %s: %w", m.legacyRoot, err)
	}
	if !info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return plan, nil
	}
	resolvedLegacy, err := filepath.EvalSymlinks(m.legacyRoot)
	if err != nil {
		return plan, fmt.Errorf("resolve %s: %w", m.legacyRoot, err)
	}
	resolvedState, err := layout.EvalExisting(plan.State)
	if err != nil {
		return plan, fmt.Errorf("resolve %s: %w", plan.State, err)
	}

	held, live := "", false
	if m.daemonLive != nil {
		held, live = m.daemonLive(filepath.Join(m.legacyRoot, "serve"))
	}
	for _, e := range m.expand() {
		it := machineItem{
			Entry:  e,
			Legacy: filepath.Join(m.legacyRoot, filepath.FromSlash(e.Name)),
			Target: filepath.Join(plan.State, filepath.FromSlash(e.Name)),
		}
		if filepath.Clean(it.Legacy) == filepath.Clean(it.Target) {
			it.InPlace = true
			plan.Items = append(plan.Items, it)
			continue
		}
		linfo, err := os.Lstat(it.Legacy)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			plan.Items = append(plan.Items, it)
			continue
		case err != nil:
			it.Refused = fmt.Sprintf("cannot read %s: %v", it.Legacy, err)
		case linfo.Mode()&fs.ModeSymlink != 0:
			it.Refused = fmt.Sprintf("%s is a symlink; the migration moves only a real file or directory, so a link cannot aim it elsewhere", it.Legacy)
		case e.Dir && !linfo.IsDir():
			it.Refused = fmt.Sprintf("%s is not a directory", it.Legacy)
		case !e.Dir && !linfo.Mode().IsRegular():
			it.Refused = fmt.Sprintf("%s is not a regular file", it.Legacy)
		}
		if it.Refused == "" {
			it.Refused = confineMachineRow(resolvedLegacy, resolvedState, it)
		}
		if it.Refused == "" {
			switch {
			case e.Kind == machineObsolete:
				it.Obsolete = true
			case e.Daemon && live:
				it.Held = held
			default:
				if err := scanMachineFiles(&it); err != nil {
					return plan, err
				}
			}
		}
		plan.Items = append(plan.Items, it)
	}
	return plan, nil
}

// hasLegacyData reports whether any row has something at the legacy root, by
// one Lstat per row and no reads.
func (m *machineStateMigrator) hasLegacyData() bool {
	if m.legacyRoot == "" {
		return false
	}
	for _, e := range m.expand() {
		if _, err := os.Lstat(filepath.Join(m.legacyRoot, filepath.FromSlash(e.Name))); err == nil {
			return true
		}
	}
	return false
}

// expand turns each Glob row into one row per legacy file that matches now.
func (m *machineStateMigrator) expand() []machineStateEntry {
	var out []machineStateEntry
	for _, e := range m.entries {
		if !e.Glob {
			out = append(out, e)
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(m.legacyRoot, filepath.FromSlash(e.Name)))
		sort.Strings(matches)
		for _, match := range matches {
			row := e
			row.Glob = false
			row.Name = filepath.Base(match)
			out = append(out, row)
		}
	}
	return out
}

// confineMachineRow refuses a row whose old location resolves outside the
// legacy root or whose new location resolves outside STATE, both after
// symlink evaluation (ADR-024 § 17).
func confineMachineRow(resolvedLegacy, resolvedState string, it machineItem) string {
	src, err := filepath.EvalSymlinks(it.Legacy)
	if err != nil {
		return fmt.Sprintf("cannot resolve %s: %v", it.Legacy, err)
	}
	if !withinDir(resolvedLegacy, src) {
		return fmt.Sprintf("%s resolves to %s, outside %s", it.Legacy, src, resolvedLegacy)
	}
	dst, err := layout.EvalExisting(it.Target)
	if err != nil {
		return fmt.Sprintf("cannot resolve %s: %v", it.Target, err)
	}
	if !withinDir(resolvedState, dst) {
		return fmt.Sprintf("the new %s location %s resolves to %s, outside the machine-state directory %s",
			it.Entry.Class, it.Target, dst, resolvedState)
	}
	return ""
}

// scanMachineFiles lists a row's files and classifies each against its
// target.
func scanMachineFiles(it *machineItem) error {
	if !it.Entry.Dir {
		return classifyMachineFile(it, filepath.Base(it.Legacy), it.Legacy, it.Target)
	}
	return filepath.WalkDir(it.Legacy, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if d.IsDir() {
			it.Dirs = append(it.Dirs, path)
			return nil
		}
		rel, err := filepath.Rel(it.Legacy, path)
		if err != nil {
			return err
		}
		return classifyMachineFile(it, rel, path, filepath.Join(it.Target, rel))
	})
}

// classifyMachineFile adds one source file to a row: to move, already done,
// to merge, superseded by the target, or a conflict.
func classifyMachineFile(it *machineItem, rel, path, dst string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	f := layoutFile{Rel: filepath.ToSlash(rel), Src: path, Dst: dst, Mode: info.Mode()}
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
	case it.Entry.Kind == machineHint:
		// The new copy is what this build wrote since; the old one goes.
		f.Done = true
	case f.Link == "" && it.Entry.Kind == machineAppend && isRegular(f.Dst):
		f.Merge = true
	default:
		it.Conflicts = append(it.Conflicts, layoutConflict{Class: it.Entry.Class, Legacy: f.Src, Target: f.Dst})
		return nil
	}
	it.Files = append(it.Files, f)
	return nil
}

// Migrate moves everything the scan finds at the legacy root.
func (m *machineStateMigrator) Migrate(ctx context.Context) LayoutReport {
	rep := LayoutReport{Root: m.legacyRoot}
	state, err := m.stateHome()
	if err != nil {
		rep.Blocked = fmt.Sprintf("the machine-state directory cannot be resolved: %v", err)
		return rep
	}
	rep.NewRoot = filepath.Clean(state)
	release, err := acquireLayoutLock(rep.NewRoot)
	if err != nil {
		rep.Blocked = err.Error()
		return rep
	}
	defer release()

	// Re-scan under the lock: another process may have finished the move.
	plan, err := m.scan(rep.NewRoot)
	if err != nil {
		rep.Blocked = err.Error()
		return rep
	}
	if !plan.pending() {
		if plan.Version < MachineStateLayoutVersion {
			if err := writeLayoutMarkerVersion(plan.State, MachineStateLayoutVersion); err != nil {
				rep.Errors = append(rep.Errors, err.Error())
				return rep
			}
		}
		rep.Version = MachineStateLayoutVersion
		return rep
	}
	if rep.Conflicts = plan.conflicts(); len(rep.Conflicts) > 0 {
		return rep
	}
	var held []string
	for _, it := range plan.Items {
		if ctx.Err() != nil {
			rep.Errors = append(rep.Errors, ctx.Err().Error())
			return rep
		}
		switch {
		case it.Refused != "":
			rep.Refused = append(rep.Refused, it.Refused)
		case it.Held != "":
			held = append(held, it.Entry.Class)
			if rep.FilesHeld == "" {
				rep.FilesHeld = it.Held
			}
		case it.Obsolete:
			if err := removeCache(it.Legacy); err != nil {
				rep.Errors = append(rep.Errors, err.Error())
			} else {
				rep.CachesDeleted++
			}
		case len(it.Files) > 0 || len(it.Dirs) > 0:
			m.moveFiles(plan.State, it, &rep)
		}
	}
	if len(held) > 0 {
		rep.FilesHeld += "; " + strings.Join(held, " and ") + " stay at the old location until it stops"
	}
	// Remove the emptied OpenCode directory and the legacy root's other empty
	// parents a row left; the legacy root itself stays (it may be CONFIG).
	removeEmptyDir(filepath.Join(m.legacyRoot, layout.StateOpenCode))

	after, err := m.scan(rep.NewRoot)
	if err != nil {
		rep.Errors = append(rep.Errors, err.Error())
		return rep
	}
	if !after.pending() && len(rep.Errors) == 0 {
		if err := writeLayoutMarkerVersion(after.State, MachineStateLayoutVersion); err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			return rep
		}
		rep.Version = MachineStateLayoutVersion
	}
	return rep
}

// moveFiles moves one row's files into STATE, confined to it, and prunes the
// emptied legacy directories.
func (m *machineStateMigrator) moveFiles(state string, it machineItem, rep *LayoutReport) {
	legacyDir := it.Legacy
	if !it.Entry.Dir {
		legacyDir = filepath.Dir(it.Legacy)
	}
	resolvedLegacy, err := filepath.EvalSymlinks(legacyDir)
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("resolve %s: %v", legacyDir, err))
		return
	}
	for _, f := range it.Files {
		// The directory may have been swapped for a link since the scan.
		if parent, err := filepath.EvalSymlinks(filepath.Dir(f.Src)); err != nil || !withinDir(resolvedLegacy, parent) {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s no longer resolves inside %s; left in place", f.Src, it.Legacy))
			continue
		}
		if err := ensureConfinedDir(state, filepath.Dir(f.Dst)); err != nil {
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
		if err == nil && it.Entry.Mode != 0 && f.Link == "" {
			// machine-id identifies this device: private whatever mode the
			// old copy had (ADR-024 § 17).
			err = os.Chmod(f.Dst, it.Entry.Mode)
		}
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
		}
	}
	for i := len(it.Dirs) - 1; i >= 0; i-- {
		removeEmptyDir(it.Dirs[i])
	}
}

// --- the automatic run ----------------------------------------------------------

// AutoMigrateMachineState is the automatic machine-state migration ADR-024
// § 15 runs ahead of a command, with the same code as `nightgauge doctor
// --fix`. ran is false when nothing was attempted.
//
// With the marker in place it costs one path resolution and one file read,
// and creates nothing. It never fails the caller: a conflict, a held lock or
// a live daemon leaves the data where it is, reported by `nightgauge doctor`,
// and the machine is not rescanned for autoMigrateRetry.
func AutoMigrateMachineState(ctx context.Context) (rep LayoutReport, ran bool) {
	return autoMigrateMachineState(ctx, newMachineStateMigrator(), time.Now())
}

func autoMigrateMachineState(ctx context.Context, m *machineStateMigrator, now time.Time) (rep LayoutReport, ran bool) {
	defer func() {
		// A bug in the migration must not take the user's command down.
		if r := recover(); r != nil {
			rep, ran = LayoutReport{Errors: []string{fmt.Sprintf("machine-state migration panicked: %v", r)}}, true
		}
	}()
	if m == nil || m.statePath == nil {
		return LayoutReport{}, false
	}
	state, err := m.statePath()
	if err != nil {
		return LayoutReport{}, false
	}
	if readLayoutMarker(filepath.Join(state, layoutMarkerName)) >= MachineStateLayoutVersion {
		return LayoutReport{}, false
	}
	if !m.hasLegacyData() {
		// Nothing an older build left: record that where the root already
		// exists, and create nothing on a machine that never ran one.
		if info, err := os.Lstat(state); err == nil && info.IsDir() {
			_ = writeLayoutMarkerVersion(state, MachineStateLayoutVersion)
		}
		return LayoutReport{}, false
	}
	attempt := filepath.Join(state, layoutAttemptName)
	if info, err := os.Lstat(attempt); err == nil && now.Sub(info.ModTime()) < autoMigrateRetry {
		return LayoutReport{}, false
	}
	rep = m.Migrate(ctx)
	if rep.Version >= MachineStateLayoutVersion {
		_ = os.Remove(attempt)
	} else if info, err := os.Lstat(state); err == nil && info.IsDir() {
		_ = os.WriteFile(attempt, []byte(rep.Summary()+"\n"), 0o600)
		_ = os.Chtimes(attempt, now, now)
	}
	return rep, true
}

// --- findings and the verb ---------------------------------------------------------

// machineFindings reports the machine state still at the legacy root, and a
// detail fragment; nil and "" when m is nil.
func machineFindings(m *machineStateMigrator) ([]Finding, string) {
	if m == nil {
		return nil, ""
	}
	state, err := m.statePath()
	if err != nil {
		return []Finding{unverifiableFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			"machine-state layout", err.Error())}, "machine state: the state directory cannot be resolved"
	}
	plan, err := m.scan(state)
	if err != nil {
		return []Finding{unverifiableFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			"machine-state layout", err.Error())}, "machine state: the scan could not run"
	}
	var out []Finding
	for _, it := range plan.Items {
		out = append(out, machineItemFindings(plan, it)...)
	}
	switch {
	case len(out) > 0:
		return out, fmt.Sprintf("machine state: %d item(s) under %s", len(out), plan.Legacy)
	case plan.Version > 0:
		return nil, fmt.Sprintf("machine state layout v%d (%s)", plan.Version, filepath.Join(plan.State, layoutMarkerName))
	default:
		return nil, "machine state: nothing at an old location"
	}
}

// machineItemFindings turns one scanned machine-state row into findings.
func machineItemFindings(plan machinePlan, it machineItem) []Finding {
	class := it.Entry.Class
	base := map[string]string{"scope": machineScope, "class": class, "legacy": it.Legacy, "target": it.Target}
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
	id := func(extra ...string) []string { return append([]string{machineScope, class, it.Legacy}, extra...) }
	migrate := func(summary, preview string) Remedy {
		return Remedy{ID: "migrate", Kind: RemedyAuto, Summary: summary, Preview: preview,
			Verb: verbLayoutMigrate, Reversible: true, Verify: checkLayout}
	}
	var out []Finding
	if it.Refused != "" {
		out = append(out, newFinding(checkLayout, codeLayoutRefused, SeverityHousekeeping,
			fmt.Sprintf("layout-refused: the old %s location cannot be migrated safely", class),
			it.Refused+". Nothing there is moved or deleted",
			ev(map[string]string{"reason": it.Refused}), id(),
			manualRemedy("inspect", "Remove the reason the evidence names, then migrate", checkLayout,
				"Resolve: "+it.Refused,
				"Re-run `nightgauge doctor --fix`; the other classes are migrated meanwhile")))
	}
	for i, c := range it.Conflicts {
		if i == maxConflictFindings {
			out = append(out, newFinding(checkLayout, codeLayoutConflict, SeverityHousekeeping,
				fmt.Sprintf("layout-conflict: %d more %s file(s) exist at both locations", len(it.Conflicts)-i, class),
				"the migration never overwrites a file, so it moves no machine state until every conflict is resolved",
				ev(map[string]string{"count": strconv.Itoa(len(it.Conflicts) - i)}), id("more"),
				migrate("Migrate once the conflicts are resolved (refused while they stand)",
					"refused: files differ at both locations; nothing is overwritten"),
				manualRemedy("resolve", "Resolve each conflict", checkLayout,
					"Run `nightgauge doctor --only "+codeLayoutConflict+"` again after resolving the ones listed")))
			break
		}
		why := "the two copies differ and the migration never overwrites a file, so no machine state is moved until you choose one"
		if it.Entry.Mode != 0 {
			why += ". It is never regenerated: a new id is a new device to the platform"
		}
		out = append(out, newFinding(checkLayout, codeLayoutConflict, SeverityHousekeeping,
			fmt.Sprintf("layout-conflict: a %s file exists at both %s and %s", class, c.Legacy, c.Target),
			why, ev(map[string]string{"legacy": c.Legacy, "target": c.Target}), id(c.Target),
			migrate("Migrate once the conflict is resolved (refused while it stands)",
				"refused: "+c.Legacy+" and "+c.Target+" differ; nothing is overwritten"),
			manualRemedy("resolve", "Keep one copy at the new location", checkLayout,
				"Compare "+c.Legacy+" with "+c.Target,
				"Keep the copy you want at "+c.Target+" and delete "+c.Legacy,
				"Re-run `nightgauge doctor --fix`")))
	}
	if it.Held != "" {
		out = append(out, newFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			fmt.Sprintf("legacy-layout: the %s at the old location %s wait for a live daemon", class, it.Legacy),
			fmt.Sprintf("this build reads the %s only at %s. %s, and it may be using them, so --fix skips them until it stops",
				class, it.Target, it.Held),
			ev(map[string]string{"busy": it.Held}), id(),
			migrate("Move the "+class+" to "+it.Target+" once no daemon runs",
				"skipped while "+it.Held)))
	}
	if len(it.Files) > 0 {
		cause := fmt.Sprintf("this build reads and writes the %s only at %s, so these files are invisible to it. "+
			"`nightgauge doctor --fix` moves each one there (a rename on one filesystem, copy then delete across "+
			"filesystems) and never overwrites a file", class, it.Target)
		switch it.Entry.Kind {
		case machineHint:
			cause += "; where the new location already has a copy, that copy is kept and the old one deleted"
		case machineAppend:
			cause += "; a log at both locations is merged as the union of its lines"
		}
		if it.Entry.Mode != 0 {
			cause += fmt.Sprintf("; it is moved byte for byte with mode %04o and never regenerated", it.Entry.Mode)
		}
		out = append(out, newFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			fmt.Sprintf("legacy-layout: %d %s file(s) at the old location %s", len(it.Files), class, it.Legacy),
			cause, ev(map[string]string{"files": strconv.Itoa(len(it.Files)), "sample": it.Files[0].Rel}), id(),
			migrate("Move the "+class+" to "+it.Target,
				fmt.Sprintf("move %d file(s) from %s to %s", len(it.Files), it.Legacy, it.Target))))
	}
	if it.Obsolete {
		out = append(out, newFinding(checkLayout, codeLayoutLegacy, SeverityHousekeeping,
			fmt.Sprintf("legacy-layout: the %s at %s have no reader", class, it.Legacy),
			"nothing in this build reads them, so `nightgauge doctor --fix` deletes them rather than moving them",
			ev(nil), id(), migrate("Delete the old "+class, "delete "+it.Legacy)))
	}
	return out
}

// machineMigrateApply is the layout.migrate verb for a machine-state finding.
func machineMigrateApply(ctx context.Context, mm func() *machineStateMigrator, f Finding) error {
	if mm == nil {
		return errors.New("no machine-state migrator")
	}
	m := mm()
	rep := m.Migrate(ctx)
	switch {
	case len(rep.Conflicts) > 0:
		c := rep.Conflicts[0]
		return fmt.Errorf("%w: %s exists at both %s and %s (%d conflict(s)); %s",
			ErrRemedyConflict, c.Class, c.Legacy, c.Target, len(rep.Conflicts), rep.Summary())
	case rep.Blocked != "":
		return fmt.Errorf("%w: %s", ErrRemedyBlocked, rep.Summary())
	case f.Evidence["busy"] != "" && rep.FilesHeld != "":
		return fmt.Errorf("%w: %s", ErrRemedyBlocked, rep.Summary())
	case len(rep.Errors) > 0:
		return errors.New(rep.Summary())
	}
	return nil
}

// machineRefusal re-derives, at apply time, whether f's machine state may
// move now.
func machineRefusal(mm func() *machineStateMigrator, f Finding) error {
	if mm == nil {
		return errors.New("no machine-state migrator")
	}
	m := mm()
	state, err := m.statePath()
	if err != nil {
		return err
	}
	plan, err := m.scan(state)
	if err != nil {
		return err
	}
	var cur *Finding
	for _, it := range plan.Items {
		for _, nf := range machineItemFindings(plan, it) {
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
		return fmt.Errorf("%w: %s exists at both %s and %s (%d machine-state conflict(s))",
			ErrRemedyConflict, cs[0].Class, cs[0].Legacy, cs[0].Target, len(cs))
	}
	if busy := cur.Evidence["busy"]; busy != "" {
		return fmt.Errorf("%w: %s; the %s stay at the old location until it stops",
			ErrRemedyBlocked, busy, cur.Evidence["class"])
	}
	return nil
}
