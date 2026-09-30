package layout

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnsafeClassFileName reports a file name a confined writer refuses: empty,
// absolute, or with a component that is empty, "." or "..".
var ErrUnsafeClassFileName = errors.New("layout: unsafe per-clone file name")

// cleanClassFileName validates name, a slash-separated path relative to a
// class directory ("issue-42.json", "history/2026-09-29.jsonl"), and returns
// it in native form.
func cleanClassFileName(name string) (string, error) {
	slashed := filepath.ToSlash(name)
	if slashed == "" || strings.HasPrefix(slashed, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("%w: %q must be a relative path", ErrUnsafeClassFileName, name)
	}
	for _, part := range strings.Split(slashed, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: %q", ErrUnsafeClassFileName, name)
		}
	}
	return filepath.FromSlash(path.Clean(slashed)), nil
}

// ClassFilePath is the path of name inside class for the repository root is
// in, after the name is validated. It creates nothing beyond CLONE itself.
func ClassFilePath(root, class, name string) (string, error) {
	clean, err := cleanClassFileName(name)
	if err != nil {
		return "", err
	}
	dir, err := ClassDir(root, class)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, clean), nil
}

// openConfinedRoot creates dir and opens it as an os.Root, so every later
// operation is confined to it: a symlink anywhere under it that points
// outside is refused by the kernel-level walk, not by a check that a racing
// writer could invalidate. dir itself must not be a symlink.
func openConfinedRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := refuseUnsafeDir(dir, info); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return r, nil
}

// refuseSymlinkTarget reports an error when name exists in r as a symlink.
func refuseSymlinkTarget(r *os.Root, dir, name string) error {
	info, err := r.Lstat(name)
	if err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", ErrUnsafeCloneDir, filepath.Join(dir, name))
	}
	return nil
}

// WriteClassFile writes data to name inside class for the repository root is
// in: to a temporary file in the same directory, then renamed into place, so
// a reader never sees a partial file. Parent directories are created. The
// write is confined to the class directory (ADR-024 § 7, § 17). It returns
// the absolute path written.
func WriteClassFile(root, class, name string, data io.Reader) (string, error) {
	dir, err := ClassDir(root, class)
	if err != nil {
		return "", err
	}
	return writeConfined(dir, name, data, writeReplace)
}

// CreateClassFile is WriteClassFile that never replaces an existing file: it
// fails with an error wrapping fs.ErrExist when name is already present, so a
// check-then-write race cannot overwrite a file another writer created.
func CreateClassFile(root, class, name string, data io.Reader) (string, error) {
	dir, err := ClassDir(root, class)
	if err != nil {
		return "", err
	}
	return writeConfined(dir, name, data, writeCreate)
}

// AppendClassFile appends data to name inside class for the repository root
// is in, creating the file and its parents when absent. The append is
// confined to the class directory and refuses a symlinked target. It returns
// the absolute path appended to.
func AppendClassFile(root, class, name string, data io.Reader) (string, error) {
	dir, err := ClassDir(root, class)
	if err != nil {
		return "", err
	}
	return writeConfined(dir, name, data, writeAppend)
}

// writeMode selects how writeConfined installs data.
type writeMode int

const (
	writeReplace writeMode = iota // temporary file renamed into place
	writeAppend                   // append, creating the file when absent
	writeCreate                   // temporary file linked into place; never replaces
)

// writeConfined writes (or appends) data to name inside dir, confined to dir
// through os.Root. A write goes to a temporary file renamed into place, or,
// for writeCreate, hard-linked into place so an existing file is never
// replaced.
func writeConfined(dir, name string, data io.Reader, mode writeMode) (string, error) {
	clean, err := cleanClassFileName(name)
	if err != nil {
		return "", err
	}
	r, err := openConfinedRoot(dir)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if parent := filepath.Dir(clean); parent != "." {
		if err := r.MkdirAll(parent, 0o700); err != nil {
			return "", fmt.Errorf("create %s: %w", filepath.Join(dir, parent), err)
		}
	}
	if err := refuseSymlinkTarget(r, dir, clean); err != nil {
		return "", err
	}
	if mode == writeAppend {
		f, err := r.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", filepath.Join(dir, clean), err)
		}
		_, copyErr := io.Copy(f, data)
		closeErr := f.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", fmt.Errorf("append %s: %w", filepath.Join(dir, clean), err)
		}
		return filepath.Join(dir, clean), nil
	}
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	tmp := filepath.Join(filepath.Dir(clean), "."+filepath.Base(clean)+".tmp-"+hex.EncodeToString(suffix[:]))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Join(dir, tmp), err)
	}
	_, copyErr := io.Copy(f, data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		_ = r.Remove(tmp)
		return "", fmt.Errorf("write %s: %w", filepath.Join(dir, clean), err)
	}
	if mode == writeCreate {
		linkErr := r.Link(tmp, clean)
		_ = r.Remove(tmp)
		if linkErr != nil {
			return "", fmt.Errorf("create %s: %w", filepath.Join(dir, clean), linkErr)
		}
		return filepath.Join(dir, clean), nil
	}
	if err := r.Rename(tmp, clean); err != nil {
		_ = r.Remove(tmp)
		return "", fmt.Errorf("rename into %s: %w", filepath.Join(dir, clean), err)
	}
	return filepath.Join(dir, clean), nil
}
