package layout

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// EnvStateHome overrides the machine-state root (ADR-024 § 1, § 16).
const EnvStateHome = "NIGHTGAUGE_STATE_HOME"

// stateDirName is the directory the XDG and platform defaults end in.
const stateDirName = "nightgauge"

// legacyStateDirName is the pre-ADR-024 home of every machine-state file,
// $HOME/.nightgauge. Only the one-time move reads it.
const legacyStateDirName = ".nightgauge"

// ErrNoStateHome reports that no usable machine-state root could be
// resolved: HOME (or %USERPROFILE%) is unset, the override is not absolute,
// or the root cannot be created or written. The error text always names
// NIGHTGAUGE_STATE_HOME, the fix.
var ErrNoStateHome = errors.New("no usable machine-state directory")

// ErrStateMoveConflict reports that a legacy machine-state file and its new
// location both exist with different contents. Nothing is overwritten.
var ErrStateMoveConflict = errors.New("machine-state file exists in both the legacy and the new location")

// StateHome returns the machine-state root, creating it with mode 0700 when
// it is absent (ADR-024 § 1, § 8, § 17). Resolution, highest first:
//
//  1. NIGHTGAUGE_STATE_HOME (must be absolute);
//  2. $XDG_STATE_HOME/nightgauge on every OS (a relative value is ignored, as
//     the XDG specification requires);
//  3. the platform default: ~/.local/state/nightgauge on Linux,
//     ~/.nightgauge/state on macOS, %LOCALAPPDATA%\nightgauge\state on
//     Windows.
//
// It never falls back into the working tree: a missing home or an unusable
// root is an error naming NIGHTGAUGE_STATE_HOME. A symlink as the root's
// final component is refused; parent directories resolve normally.
func StateHome() (string, error) {
	return StateHomeForGOOS(runtime.GOOS)
}

// StateHomeForGOOS is StateHome with the platform default chosen for goos.
// Tests use it to cover every OS from one host.
func StateHomeForGOOS(goos string) (string, error) {
	root, err := StateHomePathForGOOS(goos)
	if err != nil {
		return "", err
	}
	if err := ensureStateRoot(root); err != nil {
		return "", err
	}
	return root, nil
}

// StateHomePath resolves the machine-state root without creating it.
func StateHomePath() (string, error) {
	return StateHomePathForGOOS(runtime.GOOS)
}

// StateHomePathForGOOS is StateHomePath for an explicit goos, read from this
// process's environment and home directory.
func StateHomePathForGOOS(goos string) (string, error) {
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = h
	}
	return StateHomePathFrom(goos, home, os.LookupEnv)
}

// StateHomePathFrom resolves the machine-state root from explicit inputs: the
// OS whose default applies, the home directory, and an environment lookup.
// A caller that builds a child's environment uses it to resolve the root the
// operator's own process would, from the operator's inherited environment
// (the OpenCode per-run root pins NIGHTGAUGE_STATE_HOME to it, ADR-022 § 8).
func StateHomePathFrom(goos, home string, lookup func(string) (string, bool)) (string, error) {
	get := func(k string) string {
		if lookup == nil {
			return ""
		}
		v, _ := lookup(k)
		return v
	}
	if v := get(EnvStateHome); v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%w: %s=%q is not an absolute path; set it to an absolute directory",
				ErrNoStateHome, EnvStateHome, v)
		}
		return filepath.Clean(v), nil
	}
	if xdg := get("XDG_STATE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, stateDirName), nil
	}
	if goos == "windows" {
		if base := get("LOCALAPPDATA"); base != "" && filepath.IsAbs(base) {
			return filepath.Join(base, stateDirName, "state"), nil
		}
	}
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("%w: the home directory is not set; set %s to an absolute directory",
			ErrNoStateHome, EnvStateHome)
	}
	switch goos {
	case "linux":
		return filepath.Join(home, ".local", "state", stateDirName), nil
	case "windows":
		return filepath.Join(home, "AppData", "Local", stateDirName, "state"), nil
	default:
		return filepath.Join(home, legacyStateDirName, "state"), nil
	}
}

// verifiedStateRoots caches roots this process has already created and
// probed, so the write probe runs once per root per process.
var verifiedStateRoots sync.Map

// ensureStateRoot creates root with mode 0700 when absent; for an existing
// root it refuses a symlink, a non-directory, or (on Unix) a directory owned
// by another user, and narrows one of ours that is looser than 0700. It then
// proves the root writable.
func ensureStateRoot(root string) error {
	if _, ok := verifiedStateRoots.Load(root); ok {
		return nil
	}
	fail := func(what string, err error) error {
		return fmt.Errorf("%w: %s %s: %v; set %s to a writable absolute directory",
			ErrNoStateHome, what, root, err, EnvStateHome)
	}
	info, err := os.Lstat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(root, 0o700); err != nil {
			return fail("cannot create", err)
		}
		// MkdirAll applies the umask; the root itself is private.
		if err := os.Chmod(root, 0o700); err != nil {
			return fail("cannot set mode 0700 on", err)
		}
	case err != nil:
		return fail("cannot stat", err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fail("refusing", errors.New("the directory is a symlink"))
	case !info.IsDir():
		return fail("refusing", errors.New("not a directory"))
	case stateDirOwnedByOther(info):
		return fail("refusing", errors.New("the directory is owned by another user"))
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0:
		if err := os.Chmod(root, 0o700); err != nil {
			return fail("cannot narrow to mode 0700", err)
		}
	}
	probe, err := os.CreateTemp(root, ".write-probe-*")
	if err != nil {
		return fail("cannot write", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	verifiedStateRoots.Store(root, struct{}{})
	return nil
}

// StateFile returns <StateHome>/name after running the one-time move of a
// legacy $HOME/.nightgauge/name into it (see MoveLegacyStateFile). A move
// that cannot complete is returned as an error alongside the target path, so
// a caller for which the file is only a hint can ignore it.
func StateFile(name string) (string, error) {
	root, err := StateHome()
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, name)
	return target, MoveLegacyStateFile(name, root)
}

// StateHintFile is StateFile for a file that is only a hint (the rate-limit
// files, ADR-024 § 8): a missing file is a cold start, so a legacy copy that
// conflicts with the new one, or cannot be moved, is deleted rather than
// reported. Only a root that cannot be resolved is an error.
func StateHintFile(name string) (string, error) {
	root, err := StateHome()
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, name)
	if err := MoveLegacyStateFile(name, root); err != nil {
		if legacy := LegacyStatePath(name); legacy != "" {
			if info, lerr := os.Lstat(legacy); lerr == nil && info.Mode().IsRegular() {
				_ = os.Remove(legacy)
			}
		}
	}
	return target, nil
}

// LegacyStatePath is the pre-ADR-024 location of a machine-state entry,
// $HOME/.nightgauge/name, or "" when there is no home. Runtime code reads it
// only to move, copy or probe a legacy entry, never as a source of data.
func LegacyStatePath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, legacyStateDirName, name)
}

// legacyTempPrefix names the temporary copies the move creates in the target
// directory; a leftover from a crashed move is removed by a later one.
const legacyTempPrefix = ".migrate-"

// staleTempAge is how old a leftover temporary copy must be before a later
// move deletes it. A younger one may belong to a move still in progress.
const staleTempAge = 10 * time.Minute

// movedFileMode caps the mode of every file moved or copied here: none of
// them is meant for other users, and machine-id identifies the device.
const movedFileMode fs.FileMode = 0o600

// conflictError reports a legacy file and its new location holding different
// contents. It names both paths and the manual remedy.
//
// #2040 adds `nightgauge doctor --fix` and its migrator; that change replaces
// this message with one naming the command.
func conflictError(legacy, target, why string) error {
	return fmt.Errorf("%w: %s and %s (%s); nothing was overwritten. Nightgauge now reads only %s: "+
		"keep it and delete %s; or, if %s holds the value you need, move it over %s",
		ErrStateMoveConflict, legacy, target, why, target, legacy, legacy, target)
}

// MoveLegacyStateFile moves $HOME/.nightgauge/name to root/name once
// (ADR-024 § 15). It is not a fallback: after it the legacy file is gone and
// runtime code reads only root/name.
//
// Mechanics: the legacy bytes are installed at root/name by installExclusive
// (a temporary copy, fsynced, then hard-linked into place, or created with
// O_EXCL where hard links are unavailable), which never overwrites an
// existing target, with mode at most 0600; the directory is fsynced and the
// legacy file is deleted last. So:
//
//   - concurrent processes cannot both install a target: exactly one wins,
//     and the loser finds a target equal to the source and deletes the
//     source, or finds the source already gone;
//   - a crash between install and delete leaves a target equal to its source,
//     which the next call recognises as a finished move and completes;
//   - a target that exists and differs is never overwritten: the call returns
//     ErrStateMoveConflict naming both paths and the manual remedy;
//   - a legacy symlink or non-regular file is never followed or moved; it is
//     reported as a conflict.
//
// Nothing to move (no home, no legacy file, or the legacy path is the target)
// returns nil.
func MoveLegacyStateFile(name, root string) error {
	legacy, target, src, err := readLegacyForTarget(name, root)
	if err != nil || src == nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return finishOrConflict(legacy, target, src)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move %s: stat %s: %w", legacy, target, err)
	}
	removeStaleStateTemps(root, name)
	won, err := installExclusive(target, src, movedFileMode)
	if err != nil {
		return fmt.Errorf("move %s: install %s: %w", legacy, target, err)
	}
	if !won {
		// Another process installed the target first.
		return finishOrConflict(legacy, target, src)
	}
	if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move %s: remove legacy file after copying it: %w", legacy, err)
	}
	return nil
}

// CopyLegacyStateFile installs a copy of $HOME/.nightgauge/name at root/name
// when root/name does not exist, and KEEPS the legacy file, narrowed to mode
// 0600, as a compatibility copy for an older binary still running on the
// machine (machine-id: an older binary that found no legacy file would mint a
// new id and bind a new seat). #2040's migrator owns removing the legacy copy.
//
// root/name is authoritative. When both exist and differ, root/name is kept,
// nothing is changed, and diverged is true so the caller can warn; the
// legacy file can only differ if something other than this binary rewrote
// it. A legacy symlink or non-regular file is never followed; it is an error.
func CopyLegacyStateFile(name, root string) (diverged bool, err error) {
	legacy, target, src, err := readLegacyForTarget(name, root)
	if err != nil || src == nil {
		return false, err
	}
	if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		removeStaleStateTemps(root, name)
		won, ierr := installExclusive(target, src, movedFileMode)
		if ierr != nil {
			return false, fmt.Errorf("copy %s: install %s: %w", legacy, target, ierr)
		}
		if won {
			_ = os.Chmod(legacy, movedFileMode)
			return false, nil
		}
	} else if err != nil {
		return false, fmt.Errorf("copy %s: stat %s: %w", legacy, target, err)
	}
	dst, err := os.ReadFile(target)
	if err != nil {
		return false, fmt.Errorf("copy %s: read %s: %w", legacy, target, err)
	}
	_ = os.Chmod(legacy, movedFileMode)
	return !bytes.Equal(dst, src), nil
}

// readLegacyForTarget reads the legacy file for name. src is nil (with a nil
// error) when there is nothing to do: no home, no legacy file, or the legacy
// path is the target. A legacy path that is not a regular file is a conflict.
func readLegacyForTarget(name, root string) (legacy, target string, src []byte, err error) {
	legacy = LegacyStatePath(name)
	target = filepath.Join(root, name)
	if legacy == "" || filepath.Clean(legacy) == filepath.Clean(target) {
		return legacy, target, nil, nil
	}
	linfo, err := os.Lstat(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return legacy, target, nil, nil
	}
	if err != nil {
		return legacy, target, nil, fmt.Errorf("stat legacy file %s: %w", legacy, err)
	}
	if !linfo.Mode().IsRegular() {
		return legacy, target, nil, conflictError(legacy, target, "the legacy path is not a regular file")
	}
	src, err = os.ReadFile(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return legacy, target, nil, nil // another process finished the move
	}
	if err != nil {
		return legacy, target, nil, fmt.Errorf("read legacy file %s: %w", legacy, err)
	}
	if src == nil {
		src = []byte{}
	}
	return legacy, target, src, nil
}

// finishOrConflict handles a target that already exists: equal bytes mean the
// move had finished (the source is deleted); different bytes are a conflict.
func finishOrConflict(legacy, target string, src []byte) error {
	tinfo, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("move %s: stat %s: %w", legacy, target, err)
	}
	if !tinfo.Mode().IsRegular() {
		return conflictError(legacy, target, "the target is not a regular file")
	}
	dst, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("move %s: read %s: %w", legacy, target, err)
	}
	if !bytes.Equal(dst, src) {
		return conflictError(legacy, target, "their contents differ")
	}
	if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move %s: remove legacy file: %w", legacy, err)
	}
	return nil
}

// removeStaleStateTemps deletes temporary copies of name left in root by a
// move that crashed, once they are old enough not to belong to a live one.
func removeStaleStateTemps(root, name string) {
	matches, err := filepath.Glob(filepath.Join(root, legacyTempPrefix+name+"-*"))
	if err != nil {
		return
	}
	for _, m := range matches {
		info, err := os.Lstat(m)
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < staleTempAge {
			continue
		}
		_ = os.Remove(m)
	}
}

// syncStateDir fsyncs dir so a rename or link into it survives a crash.
// Windows cannot open a directory for sync; it is skipped there.
func syncStateDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// stateLink is os.Link, replaceable in tests to exercise the fallback.
var stateLink = os.Link

// installExclusive puts data at path only if nothing is there yet, and
// reports whether this call's data won. It writes a temporary file in the
// same directory with perm, fsyncs it, and hard-links it into place: the link
// fails rather than overwrites, and a reader sees either no file or the whole
// file. Where hard links are unavailable (a filesystem without them, EXDEV,
// EPERM, ENOTSUP, EMLINK) it falls back to creating path with O_EXCL, which
// also never overwrites, then writes and fsyncs it.
func installExclusive(path string, data []byte, perm fs.FileMode) (bool, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, legacyTempPrefix+filepath.Base(path)+"-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	werr := func() error {
		if err := tmp.Chmod(perm); err != nil {
			return err
		}
		if _, err := tmp.Write(data); err != nil {
			return err
		}
		return tmp.Sync()
	}()
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, werr
	}
	lerr := stateLink(tmpName, path)
	switch {
	case lerr == nil:
		syncStateDir(dir)
		return true, nil
	case errors.Is(lerr, fs.ErrExist):
		return false, nil
	}
	// No hard link here: an exclusive create never overwrites either.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("link failed (%v) and exclusive create failed: %w", lerr, err)
	}
	_, werr = f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return false, werr
	}
	syncStateDir(dir)
	return true, nil
}

// WriteStateFileExclusive installs data at path only if nothing is there yet
// (see installExclusive). It returns the content at path afterwards (the
// caller's on success, the winner's otherwise) and whether the caller's write
// won. perm is applied to the file.
func WriteStateFileExclusive(path string, data []byte, perm fs.FileMode) ([]byte, bool, error) {
	won, err := installExclusive(path, data, perm)
	if err != nil {
		return nil, false, err
	}
	if won {
		return data, true, nil
	}
	existing, err := os.ReadFile(path)
	return existing, false, err
}
