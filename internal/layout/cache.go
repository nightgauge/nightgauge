package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvCacheHome overrides the cache root (ADR-024 § 1, § 6). PR #2020
// introduced it for the GitHub conditional-request store; #2028 lifted its
// resolver here so every cache shares one.
const EnvCacheHome = "NIGHTGAUGE_CACHE_HOME"

// ErrNoCacheHome reports that no usable cache root could be resolved or
// prepared. A cache is disposable, so a caller that gets it keeps the cache
// in memory for the call and writes nothing (ADR-024 § 6); it never falls
// back into the working tree.
var ErrNoCacheHome = errors.New("no usable cache directory")

// CacheHomePath resolves the cache root without creating it (ADR-024 § 1,
// § 6). Resolution, highest first:
//
//  1. NIGHTGAUGE_CACHE_HOME (must be absolute);
//  2. $XDG_CACHE_HOME/nightgauge on every OS (a relative value is ignored, as
//     the XDG specification requires);
//  3. the platform default: os.UserCacheDir()/nightgauge on Linux and macOS
//     (~/.cache/nightgauge, ~/Library/Caches/nightgauge), and
//     %LOCALAPPDATA%\nightgauge\cache on Windows, so deleting the cache never
//     deletes STATE, which shares %LOCALAPPDATA%\nightgauge.
func CacheHomePath() (string, error) {
	base, _ := os.UserCacheDir()
	return CacheHomePathFrom(runtime.GOOS, base, os.LookupEnv)
}

// CacheHomePathFrom is CacheHomePath from explicit inputs: the OS whose
// default applies, the OS user cache directory ("" when it has none), and an
// environment lookup. Tests use it to cover every OS from one host.
func CacheHomePathFrom(goos, userCacheDir string, lookup func(string) (string, bool)) (string, error) {
	get := func(k string) string {
		if lookup == nil {
			return ""
		}
		v, _ := lookup(k)
		return v
	}
	if v := get(EnvCacheHome); v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%w: %s=%q is not an absolute path; set it to an absolute directory",
				ErrNoCacheHome, EnvCacheHome, v)
		}
		return filepath.Clean(v), nil
	}
	if xdg := get("XDG_CACHE_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, stateDirName), nil
	}
	if userCacheDir == "" || !filepath.IsAbs(userCacheDir) {
		return "", fmt.Errorf("%w: the OS reports no user cache directory; set %s to an absolute directory",
			ErrNoCacheHome, EnvCacheHome)
	}
	if goos == "windows" {
		return filepath.Join(userCacheDir, stateDirName, "cache"), nil
	}
	return filepath.Join(userCacheDir, stateDirName), nil
}

// CacheHome returns the cache root (CacheHomePath), creating it with mode
// 0700 when it is absent and refusing one that is a symlink, not a directory
// or owned by another user.
func CacheHome() (string, error) {
	root, err := CacheHomePath()
	if err != nil {
		return "", err
	}
	if err := ensureCacheComponent(root, true); err != nil {
		return "", err
	}
	return root, nil
}

// CacheDir returns <CacheHome>/<elem...>, creating the root and every
// component below it with mode 0700 (ADR-024 § 6). Each elem must be a single
// path component. No symlink is followed out of the cache home: the root's
// final component and every component below it are refused when they are a
// symlink or not a directory, and an existing one of ours looser than 0700 is
// narrowed. Parents of the root resolve normally (macOS /var ->
// /private/var).
func CacheDir(elem ...string) (string, error) {
	root, err := CacheHomePath()
	if err != nil {
		return "", err
	}
	return CacheDirUnder(root, elem...)
}

// CacheDirUnder is CacheDir for an explicit, already-resolved cache root.
func CacheDirUnder(root string, elem ...string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: cache root %q is not absolute", ErrNoCacheHome, root)
	}
	if err := ensureCacheComponent(root, true); err != nil {
		return "", err
	}
	dir := root
	for _, e := range elem {
		if e == "" || e == "." || e == ".." || strings.ContainsAny(e, `/\`) || filepath.Base(e) != e {
			return "", fmt.Errorf("%w: %q is not a single path component", ErrNoCacheHome, e)
		}
		dir = filepath.Join(dir, e)
		if err := ensureCacheComponent(dir, false); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// ensureCacheComponent creates dir with mode 0700 when it is absent (the root
// with its parents; a component below it by itself, so a symlinked parent is
// never traversed), then refuses a symlink, a non-directory or a directory
// owned by another user, and narrows ours to 0700.
func ensureCacheComponent(dir string, isRoot bool) error {
	fail := func(what string, err error) error {
		return fmt.Errorf("%w: %s %s: %v; set %s to a writable absolute directory",
			ErrNoCacheHome, what, dir, err, EnvCacheHome)
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		mk := os.Mkdir
		if isRoot {
			mk = os.MkdirAll
		}
		if err := mk(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fail("cannot create", err)
		}
		info, err = os.Lstat(dir)
	}
	switch {
	case err != nil:
		return fail("cannot stat", err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fail("refusing", errors.New("the directory is a symlink"))
	case !info.IsDir():
		return fail("refusing", errors.New("not a directory"))
	case stateDirOwnedByOther(info):
		return fail("refusing", errors.New("the directory is owned by another user"))
	case runtime.GOOS != "windows" && info.Mode().Perm() != 0o700:
		// MkdirAll applies the umask; an existing one may be looser.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fail("cannot set mode 0700 on", err)
		}
	}
	return nil
}
