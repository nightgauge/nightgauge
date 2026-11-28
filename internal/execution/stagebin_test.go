package execution

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubHostExecutableForTests points hostExecutable at a script that exits 0,
// for the whole test binary (TestMain), and returns the restore.
func stubHostExecutableForTests() (restore func()) {
	dir, err := os.MkdirTemp("", "nightgauge-host-exe-")
	if err != nil {
		panic(err)
	}
	path := filepath.Join(dir, "nightgauge")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		panic(err)
	}
	previous := hostExecutable
	hostExecutable = func() (string, error) { return path, nil }
	return func() {
		hostExecutable = previous
		_ = os.RemoveAll(dir)
	}
}

// useStageBinSource makes hostExecutable a fresh script with body for one test
// and returns its path.
func useStageBinSource(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nightgauge")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := hostExecutable
	hostExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { hostExecutable = previous })
	return path
}

func preparedStageBinary(t *testing.T) (*stageBinary, string) {
	t.Helper()
	useStageBinSource(t, "exit 0")
	dir := filepath.Join(t.TempDir(), "bin")
	sb, err := prepareStageBinary(dir)
	if err != nil {
		t.Fatalf("prepareStageBinary: %v", err)
	}
	t.Cleanup(func() { _ = sb.Remove() })
	if err := sb.Verify(); err != nil {
		t.Fatalf("a copy nothing touched failed verification: %v", err)
	}
	return sb, dir
}

func wantTamper(t *testing.T, sb *stageBinary, field string) {
	t.Helper()
	err := sb.Verify()
	if err == nil {
		t.Fatalf("Verify passed, want a difference in %s", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Errorf("Verify = %v, want it to name %s", err, field)
	}
}

func TestPrepareStageBinaryMakesAnIndependentCopy(t *testing.T) {
	src := useStageBinSource(t, "exit 0")
	dir := filepath.Join(t.TempDir(), "bin")
	sb, err := prepareStageBinary(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Remove()
	if sb.Path() != filepath.Join(dir, "nightgauge") {
		t.Errorf("Path = %s, want %s", sb.Path(), filepath.Join(dir, "nightgauge"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "nightgauge" {
		t.Fatalf("the directory holds %v (%v), want the copy alone", entries, err)
	}
	fi, err := os.Lstat(sb.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o500 {
		t.Errorf("the copy is %s, want a regular file with mode 0500", fi.Mode())
	}
	srcFi, _ := os.Lstat(src)
	if os.SameFile(fi, srcFi) {
		t.Error("the copy is the source file itself (a hard link), want a copy")
	}
	dfi, _ := os.Lstat(dir)
	if dfi.Mode().Perm() != 0o700 {
		t.Errorf("the directory is %s, want 0700", dfi.Mode())
	}
	if err := sb.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("Remove left %s: %v", dir, err)
	}
}

func TestStageBinaryVerifyDetectsTampering(t *testing.T) {
	t.Run("in-place overwrite of the same size", func(t *testing.T) {
		sb, _ := preparedStageBinary(t)
		data, _ := os.ReadFile(sb.Path())
		flipped := bytes.Clone(data)
		flipped[len(flipped)-2] ^= 1
		if err := os.Chmod(sb.Path(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sb.Path(), flipped, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sb.Path(), 0o500); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "sha256")
	})
	t.Run("rename-replace with identical bytes", func(t *testing.T) {
		sb, dir := preparedStageBinary(t)
		data, _ := os.ReadFile(sb.Path())
		tmp := filepath.Join(dir, ".x")
		if err := os.WriteFile(tmp, data, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, sb.Path()); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "inode")
	})
	t.Run("removal", func(t *testing.T) {
		sb, _ := preparedStageBinary(t)
		if err := os.Remove(sb.Path()); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "could not be read")
	})
	t.Run("an added sibling", func(t *testing.T) {
		sb, dir := preparedStageBinary(t)
		if err := os.WriteFile(filepath.Join(dir, "helper"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "directory entries")
	})
	t.Run("chmod", func(t *testing.T) {
		sb, _ := preparedStageBinary(t)
		if err := os.Chmod(sb.Path(), 0o700); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "mode")
	})
	t.Run("replace then restore identical bytes", func(t *testing.T) {
		sb, _ := preparedStageBinary(t)
		data, _ := os.ReadFile(sb.Path())
		waitPastCtimeTick()
		if err := os.Remove(sb.Path()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sb.Path(), data, 0o500); err != nil {
			t.Fatal(err)
		}
		// A filesystem may reuse the freed inode number; the ctime still moves.
		wantTamper(t, sb, "ctime")
	})
	t.Run("move away and back keeps the inode", func(t *testing.T) {
		sb, dir := preparedStageBinary(t)
		waitPastCtimeTick()
		away := filepath.Join(dir, ".away")
		if err := os.Rename(sb.Path(), away); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(away, sb.Path()); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "ctime")
	})
	t.Run("the directory replaced by a symlink", func(t *testing.T) {
		sb, dir := preparedStageBinary(t)
		moved := dir + ".moved"
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(moved) })
		if err := os.Symlink(moved, dir); err != nil {
			t.Fatal(err)
		}
		wantTamper(t, sb, "could not be read")
	})
}

// The operator replacing their own installed binary mid-stage changes nothing
// the stage runs or the parent checks (#3688 AC 6).
// Remove restores a locked directory's write bit, but never chmods through a
// symlink a stage put in the directory's place.
func TestStageBinaryRemove(t *testing.T) {
	t.Run("a locked directory", func(t *testing.T) {
		sb, dir := preparedStageBinary(t)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := sb.Remove(); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("Remove left %s: %v", dir, err)
		}
	})
	t.Run("a symlink in the directory's place", func(t *testing.T) {
		sb, dir := preparedStageBinary(t)
		target := t.TempDir()
		if err := os.Chmod(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		if err := sb.Remove(); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatalf("Remove deleted the symlink's target: %v", err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("Remove changed the symlink target's mode to %s", fi.Mode().Perm())
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("Remove left the symlink %s: %v", dir, err)
		}
	})
}

// waitPastCtimeTick lets the clock pass the recorded ctime: Linux stamps
// ctime from a coarse kernel clock that ticks every few milliseconds, so a
// change made within the same tick as the record would carry the same ctime.
func waitPastCtimeTick() { time.Sleep(20 * time.Millisecond) }

func TestStageBinaryIgnoresTheSourceBeingReplaced(t *testing.T) {
	src := useStageBinSource(t, "exit 0")
	dir := filepath.Join(t.TempDir(), "bin")
	sb, err := prepareStageBinary(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Remove()
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho upgraded\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sb.Verify(); err != nil {
		t.Errorf("replacing the source failed the copy's verification: %v", err)
	}
}

func TestPrepareStageBinaryRefusesAndLeavesNothing(t *testing.T) {
	t.Run("unreadable source", func(t *testing.T) {
		src := useStageBinSource(t, "exit 0")
		if err := os.Chmod(src, 0o100); err != nil {
			t.Fatal(err)
		}
		if os.Geteuid() == 0 {
			t.Skip("root reads any file")
		}
		dir := filepath.Join(t.TempDir(), "bin")
		if _, err := prepareStageBinary(dir); err == nil {
			t.Fatal("prepareStageBinary copied an unreadable source")
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("a refused copy left %s behind: %v", dir, err)
		}
	})
	t.Run("missing source", func(t *testing.T) {
		src := useStageBinSource(t, "exit 0")
		_ = os.Remove(src)
		dir := filepath.Join(t.TempDir(), "bin")
		if _, err := prepareStageBinary(dir); err == nil {
			t.Fatal("prepareStageBinary copied a missing source")
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("a refused copy left %s behind: %v", dir, err)
		}
	})
	t.Run("a directory that is not empty", func(t *testing.T) {
		useStageBinSource(t, "exit 0")
		dir := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "planted"), []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := prepareStageBinary(dir)
		if err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("prepareStageBinary = %v, want a not-empty refusal", err)
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("a refused copy left %s behind: %v", dir, err)
		}
	})
	t.Run("a copy that cannot run version", func(t *testing.T) {
		useStageBinSource(t, "exit 3")
		dir := filepath.Join(t.TempDir(), "bin")
		_, err := prepareStageBinary(dir)
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("prepareStageBinary = %v, want the settle exec's failure", err)
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("a refused copy left %s behind: %v", dir, err)
		}
	})
}

// The record is taken after the one settle exec: the copy executed by the
// stage afterwards still verifies.
func TestStageBinaryVerifiesAfterTheStageExecutesIt(t *testing.T) {
	sb, _ := preparedStageBinary(t)
	for range 3 {
		if out, err := runCopy(sb.Path()); err != nil {
			t.Fatalf("running the copy: %v %s", err, out)
		}
	}
	if err := sb.Verify(); err != nil {
		t.Errorf("executing the copy failed its verification: %v", err)
	}
}

func runCopy(path string) ([]byte, error) {
	return exec.Command(path, "version").CombinedOutput()
}
