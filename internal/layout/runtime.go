package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// EnvRuntimeDir overrides the per-user runtime root (ADR-024 § 1, § 10).
const EnvRuntimeDir = "NIGHTGAUGE_RUNTIME_DIR"

// ErrUnsafeRuntimeDir reports a runtime directory that another local user
// could read, replace or pre-create: a symlink, a non-directory, a directory
// owned by another uid, or one with group or other permission bits.
var ErrUnsafeRuntimeDir = errors.New("layout: unsafe runtime directory")

// RuntimeDir resolves the per-user runtime root without creating it
// (ADR-024 § 1, § 10). Resolution, highest first:
//
//  1. NIGHTGAUGE_RUNTIME_DIR (must be absolute);
//  2. $XDG_RUNTIME_DIR/nightgauge (a relative value is ignored);
//  3. <os.TempDir()>/nightgauge-<uid>.
func RuntimeDir() (string, error) {
	return RuntimeDirFrom(os.LookupEnv, os.TempDir(), os.Getuid())
}

// RuntimeDirFrom is RuntimeDir from explicit inputs, for tests and for a
// caller resolving the root another environment would see.
func RuntimeDirFrom(lookup func(string) (string, bool), tempDir string, uid int) (string, error) {
	get := func(k string) string {
		if lookup == nil {
			return ""
		}
		v, _ := lookup(k)
		return v
	}
	if v := get(EnvRuntimeDir); v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("layout: %s=%q is not an absolute path", EnvRuntimeDir, v)
		}
		return filepath.Clean(v), nil
	}
	if xdg := get("XDG_RUNTIME_DIR"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, stateDirName), nil
	}
	return filepath.Join(tempDir, stateDirName+"-"+strconv.Itoa(uid)), nil
}

// EnsureRuntimeDir creates dir with mode 0700 when absent and then verifies
// it: a directory, not a symlink (final component only; parent symlinks such
// as macOS /var -> /private/var resolve normally), owned by the current uid,
// with no group or other bits. An existing directory that fails any check is
// refused, never repaired: on a shared temp directory another user may have
// pre-created it to intercept IPC (ADR-024 § 10).
func EnsureRuntimeDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("layout: create runtime dir %s: %w", dir, err)
		}
		// MkdirAll applies the umask; make the leaf's mode exact.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("layout: chmod runtime dir %s: %w", dir, err)
		}
	}
	return VerifyRuntimeDir(dir)
}

// VerifyRuntimeDir applies EnsureRuntimeDir's checks without creating
// anything.
func VerifyRuntimeDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("layout: stat runtime dir %s: %w", dir, err)
	}
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s %s; remove it or set %s", ErrUnsafeRuntimeDir, dir, why, EnvRuntimeDir)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return refuse("is a symlink")
	case !info.IsDir():
		return refuse("is not a directory")
	case stateDirOwnedByOther(info):
		return refuse("is owned by another user")
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0:
		return refuse(fmt.Sprintf("has mode %#o (want 0700)", info.Mode().Perm()))
	}
	return nil
}
