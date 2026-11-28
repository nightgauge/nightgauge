package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The per-stage binary copy (#3688, ADR-022 amendment 2026-11-27).
//
// An OpenCode stage runs the nightgauge binary NIGHTGAUGE_BIN names: its
// plugin spawns it for every gate and hook. A stage that could overwrite that
// binary would run its own gates. The shell parser (internal/hooks'
// careful_bindir.go) and the permission map's deny globs are an advisory first
// filter with known bypasses. The boundary is here, in the parent: each
// OpenCode stage gets its own full copy of this process's executable in an
// otherwise empty directory under the run's root. The parent records the
// copy's identity before the spawn and re-checks it in-process, without
// executing it, once the stage is reaped and before any of its output is
// read. Any difference fails the stage as bin_tamper.

const (
	// stageBinTamperMarker starts the error of a stage whose copy changed. The
	// bin-tamper rule of internal/terminalkind/table.json matches it.
	stageBinTamperMarker = "[bin-tamper]"
	// stageBinTamperKind is the terminal kind the marker classifies to,
	// orchestrator.TerminalKindBinTamper.
	stageBinTamperKind = "bin_tamper"
	// stageBinaryName is the copy's file name, its directory's only entry.
	stageBinaryName = "nightgauge"
	// stageBinSettleTimeout bounds the one exec of the copy made before its
	// identity is recorded.
	stageBinSettleTimeout = 10 * time.Second
	// stageBinSettleWaitDelay bounds the wait for the settle exec's output
	// pipes once it has been killed.
	stageBinSettleWaitDelay = time.Second
	// stageBinDirMode is the copy's directory: the owner alone.
	stageBinDirMode fs.FileMode = 0o700
	// stageBinFileMode is the copy: read and execute, no write bit.
	stageBinFileMode fs.FileMode = 0o500
	// nightgaugeBinEnv is the variable a stage finds its nightgauge binary by.
	nightgaugeBinEnv = "NIGHTGAUGE_BIN"
)

// stageBinaryIdentity is what the parent records of the copy and its
// directory: the entries from ReadDir, the hash from the bytes, and every
// other field from Lstat.
type stageBinaryIdentity struct {
	sha256   string
	size     int64
	mode     fs.FileMode
	dev      uint64
	ino      uint64
	ctime    time.Time
	entries  []string
	dirMode  fs.FileMode
	dirDev   uint64
	dirIno   uint64
	dirCtime time.Time
}

// stageBinary is one stage's copy: where it is and the identity it was given.
type stageBinary struct {
	dir  string
	path string
	want stageBinaryIdentity
}

// Path is the copy's path, the stage's NIGHTGAUGE_BIN.
func (b *stageBinary) Path() string { return b.path }

// prepareStageBinary creates dir, which must be empty, copies this process's
// executable (hostExecutable, symlinks resolved) into it as a new regular file
// with mode 0500, executes the copy once (`version`) so a first exec's own
// metadata change happens before the record, and records the identity. It
// never links: a hard link would share the operator's inode, and a symlink
// would name the operator's file. Any error removes dir.
func prepareStageBinary(dir string) (_ *stageBinary, err error) {
	if dir == "" {
		return nil, errors.New("no directory was given for the copy")
	}
	if err := os.MkdirAll(dir, stageBinDirMode); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = removeStageBinaryDir(dir)
		}
	}()
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if err := os.Chmod(dir, stageBinDirMode); err != nil {
		return nil, fmt.Errorf("restrict %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf("%s is not empty: %s", dir, strings.Join(entryNames(entries), ", "))
	}
	self, err := hostExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve this process's executable: %w", err)
	}
	src, err := filepath.EvalSymlinks(self)
	if err != nil {
		return nil, fmt.Errorf("resolve this process's executable: %w", err)
	}
	path := filepath.Join(dir, stageBinaryName)
	if err := copyStageBinary(src, path); err != nil {
		return nil, err
	}
	if err := settleStageBinary(path); err != nil {
		return nil, err
	}
	want, err := snapshotStageBinary(dir, path)
	if err != nil {
		return nil, fmt.Errorf("record the copy's identity: %w", err)
	}
	return &stageBinary{dir: dir, path: path, want: want}, nil
}

// copyStageBinary writes src's bytes into a file dst it creates exclusively.
func copyStageBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, stageBinFileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	// No fsync: the copy is removed when the stage returns, and the identity
	// is read back through the same page cache.
	if err := out.Chmod(stageBinFileMode); err != nil {
		_ = out.Close()
		return fmt.Errorf("set the mode of %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// settleStageBinary executes the copy once. A system that records a first
// exec on the file (an extended attribute on macOS) changes its ctime then,
// so the record is taken after it and the stage's own execs leave it alone.
// A copy that cannot run `version` cannot serve the stage either.
func settleStageBinary(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), stageBinSettleTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = stageBinSettleWaitDelay
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s version: %w", path, err)
	}
	return nil
}

// snapshotStageBinary reads the identity of the copy at path and of dir.
func snapshotStageBinary(dir, path string) (stageBinaryIdentity, error) {
	var id stageBinaryIdentity
	dfi, err := os.Lstat(dir)
	if err != nil {
		return id, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !dfi.IsDir() {
		return id, fmt.Errorf("%s is no longer a directory", dir)
	}
	var ok bool
	if id.dirDev, id.dirIno, id.dirCtime, ok = statIdentity(dfi); !ok {
		return id, errors.New("this platform does not report a file's device, inode and ctime")
	}
	id.dirMode = dfi.Mode()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return id, fmt.Errorf("list %s: %w", dir, err)
	}
	id.entries = entryNames(entries)
	fi, err := os.Lstat(path)
	if err != nil {
		return id, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return id, fmt.Errorf("%s is not a regular file (%s)", path, fi.Mode().Type())
	}
	if id.dev, id.ino, id.ctime, ok = statIdentity(fi); !ok {
		return id, fmt.Errorf("%s reports no device, inode and ctime", path)
	}
	id.size = fi.Size()
	id.mode = fi.Mode()
	f, err := os.Open(path)
	if err != nil {
		return id, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return id, fmt.Errorf("hash %s: %w", path, err)
	}
	id.sha256 = hex.EncodeToString(h.Sum(nil))
	return id, nil
}

// Verify re-reads the identity in-process, never executing the copy, and
// returns an error naming every field that differs from the record, or the
// reason the copy could not be read at all.
func (b *stageBinary) Verify() error {
	got, err := snapshotStageBinary(b.dir, b.path)
	if err != nil {
		return fmt.Errorf("the copy could not be read: %w", err)
	}
	var diff []string
	want := b.want
	check := func(name string, same bool) {
		if !same {
			diff = append(diff, name)
		}
	}
	check("sha256", got.sha256 == want.sha256)
	check("size", got.size == want.size)
	check("mode", got.mode == want.mode)
	check("device", got.dev == want.dev)
	check("inode", got.ino == want.ino)
	check("ctime", got.ctime.Equal(want.ctime))
	check("directory entries", slices.Equal(got.entries, want.entries))
	check("directory mode", got.dirMode == want.dirMode)
	check("directory device", got.dirDev == want.dirDev)
	check("directory inode", got.dirIno == want.dirIno)
	check("directory ctime", got.dirCtime.Equal(want.dirCtime))
	if len(diff) > 0 {
		return fmt.Errorf("%s differ", strings.Join(diff, ", "))
	}
	return nil
}

// Remove deletes the copy and its directory.
func (b *stageBinary) Remove() error { return removeStageBinaryDir(b.dir) }

// removeStageBinaryDir deletes dir. A stage that took the owner's write bit
// off it would otherwise keep the copy from being removed, so a real
// directory gets it back first. Only a real one: dir is a path the stage can
// write, and chmod follows a symlink a stage put in its place to wherever it
// points. RemoveAll removes such a symlink without following it.
func removeStageBinaryDir(dir string) error {
	if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
		_ = os.Chmod(dir, stageBinDirMode)
	}
	return os.RemoveAll(dir)
}

func entryNames(entries []fs.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
