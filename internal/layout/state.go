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

// StateHomePathForGOOS is StateHomePath for an explicit goos.
func StateHomePathForGOOS(goos string) (string, error) {
	if v := os.Getenv(EnvStateHome); v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%w: %s=%q is not an absolute path; set it to an absolute directory",
				ErrNoStateHome, EnvStateHome, v)
		}
		return filepath.Clean(v), nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, stateDirName), nil
	}
	if goos == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" && filepath.IsAbs(base) {
			return filepath.Join(base, stateDirName, "state"), nil
		}
	}
	home, err := stateUserHome()
	if err != nil {
		return "", err
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

// stateUserHome is os.UserHomeDir, required to be absolute, with an error
// that names the override.
func stateUserHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("%w: the home directory is not set; set %s to an absolute directory",
			ErrNoStateHome, EnvStateHome)
	}
	return home, nil
}

// verifiedStateRoots caches roots this process has already created and
// probed, so the write probe runs once per root per process.
var verifiedStateRoots sync.Map

// ensureStateRoot creates root with mode 0700 when absent, refuses a symlink
// or non-directory in its place, and proves it writable.
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
		if legacy := legacyStatePath(name); legacy != "" {
			if info, lerr := os.Lstat(legacy); lerr == nil && info.Mode().IsRegular() {
				_ = os.Remove(legacy)
			}
		}
	}
	return target, nil
}

// legacyStatePath is $HOME/.nightgauge/name, or "" when there is no home.
func legacyStatePath(name string) string {
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

// MoveLegacyStateFile moves $HOME/.nightgauge/name to root/name once
// (ADR-024 § 15). It is not a fallback: after it the legacy file is gone and
// runtime code reads only root/name.
//
// Mechanics: the legacy bytes are copied to a temporary file in root with the
// legacy file's permission bits, fsynced, and hard-linked into place, which
// fails rather than overwrites when the target already exists; the directory
// is fsynced and the legacy file is deleted last. So:
//
//   - concurrent processes cannot both install a target: exactly one link
//     wins, and the loser finds a target equal to the source and deletes the
//     source, or finds the source already gone;
//   - a crash between install and delete leaves a target equal to its source,
//     which the next call recognises as a finished move and completes;
//   - a target that exists and differs is never overwritten: the call returns
//     ErrStateMoveConflict naming both paths and `nightgauge doctor --fix`;
//   - a legacy symlink or non-regular file is never followed or moved; it is
//     reported as a conflict.
//
// Nothing to move (no home, no legacy file, or the legacy path is the target)
// returns nil.
func MoveLegacyStateFile(name, root string) error {
	legacy := legacyStatePath(name)
	target := filepath.Join(root, name)
	if legacy == "" || filepath.Clean(legacy) == filepath.Clean(target) {
		return nil
	}
	linfo, err := os.Lstat(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("move %s: stat legacy file: %w", legacy, err)
	}
	conflict := func(why string) error {
		return fmt.Errorf("%w: %s and %s (%s); nothing was overwritten. Run `nightgauge doctor --fix`, or remove the one you do not want",
			ErrStateMoveConflict, legacy, target, why)
	}
	if !linfo.Mode().IsRegular() {
		return conflict("the legacy path is not a regular file")
	}
	src, err := os.ReadFile(legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // another process finished the move
	}
	if err != nil {
		return fmt.Errorf("move %s: read legacy file: %w", legacy, err)
	}

	if _, err := os.Lstat(target); err == nil {
		return finishOrConflict(legacy, target, src, conflict)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move %s: stat %s: %w", legacy, target, err)
	}

	removeStaleStateTemps(root, name)
	tmp, err := os.CreateTemp(root, legacyTempPrefix+name+"-*")
	if err != nil {
		return fmt.Errorf("move %s: create temporary copy: %w", legacy, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(linfo.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("move %s: set mode: %w", legacy, err)
	}
	if _, err := tmp.Write(src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("move %s: write temporary copy: %w", legacy, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("move %s: fsync temporary copy: %w", legacy, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("move %s: close temporary copy: %w", legacy, err)
	}
	if err := os.Link(tmpName, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another process installed the target first.
			return finishOrConflict(legacy, target, src, conflict)
		}
		return fmt.Errorf("move %s: install %s: %w", legacy, target, err)
	}
	syncStateDir(root)
	if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move %s: remove legacy file after copying it: %w", legacy, err)
	}
	return nil
}

// finishOrConflict handles a target that already exists: equal bytes mean the
// move had finished (the source is deleted); different bytes are a conflict.
func finishOrConflict(legacy, target string, src []byte, conflict func(string) error) error {
	tinfo, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("move %s: stat %s: %w", legacy, target, err)
	}
	if !tinfo.Mode().IsRegular() {
		return conflict("the target is not a regular file")
	}
	dst, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("move %s: read %s: %w", legacy, target, err)
	}
	if !bytes.Equal(dst, src) {
		return conflict("their contents differ")
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

// WriteStateFileExclusive installs data at path only if nothing is there yet,
// atomically: a concurrent reader sees either no file or the whole file, and
// of two concurrent writers exactly one wins. It returns the content that is
// at path afterwards (the caller's on success, the winner's otherwise) and
// whether the caller's write won. perm is applied to the file.
func WriteStateFileExclusive(path string, data []byte, perm fs.FileMode) ([]byte, bool, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, legacyTempPrefix+filepath.Base(path)+"-*")
	if err != nil {
		return nil, false, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return nil, false, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			existing, rerr := os.ReadFile(path)
			return existing, false, rerr
		}
		return nil, false, err
	}
	syncStateDir(dir)
	return data, true, nil
}
