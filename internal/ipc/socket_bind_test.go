package ipc

// BindSocket must not take the socket path away from a daemon that is still
// serving on it (#1349 follow-up).
//
// The unconditional `os.Remove(path)` this replaced was defended by a comment
// asserting that "a genuinely live listener holding the path still fails
// Listen correctly below." It does not, and the reason is a POSIX property
// rather than a Go one: unlink() detaches the NAME from the listening
// socket's inode. The first daemon goes on accepting on an inode no client
// can name, the second binds the freed path, and every dial from then on
// reaches the second. Nothing errors and nothing logs.
//
// TestBindSocketRefusesToStealALiveSocket is the direct regression. It is
// written against a raw net.Listener rather than a second Server so it
// reproduces the exact syscall sequence and cannot pass because of some
// higher-level guard.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSocketDir returns a temp dir short enough for a Unix socket path.
// t.TempDir() embeds the full test name, and these test names are long
// enough to blow macOS's ~104-byte sun_path limit — the failure is a bare
// "bind: invalid argument" that looks nothing like a length problem.
// cmd/nightgauge's attention tests carry the same helper for the same reason.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ngbind")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// liveListener starts a real accepting Unix listener at path and records who
// answered each dial. Closed on cleanup.
func liveListener(t *testing.T, path string, name string, answered chan<- string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s as %s: %v", path, name, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case answered <- name:
			default:
			}
			_ = c.Close()
		}
	}()
	return ln
}

// whoAnswers dials path and returns the name of the listener that accepted.
func whoAnswers(t *testing.T, path string, answered <-chan string) string {
	t.Helper()
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	_ = c.Close()
	select {
	case who := <-answered:
		return who
	case <-time.After(2 * time.Second):
		t.Fatal("nobody accepted the dial")
		return ""
	}
}

func TestBindSocketRefusesToStealALiveSocket(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "daemon.sock")
	answered := make(chan string, 8)

	liveListener(t, path, "first", answered)
	if who := whoAnswers(t, path, answered); who != "first" {
		t.Fatalf("before the second bind, dial reached %q, want \"first\"", who)
	}

	// The second daemon starts. It must refuse, not take the path.
	second := &Server{}
	ln, err := second.BindSocket(path)
	if err == nil {
		_ = ln.Close()
		t.Fatal("BindSocket bound a path a live daemon was already accepting on — " +
			"the first daemon is now unreachable and nothing reported it")
	}
	if !errors.Is(err, ErrSocketInUse) {
		t.Errorf("error = %v, want it to wrap ErrSocketInUse so `serve` can tell "+
			"this apart from a real bind failure", err)
	}

	// The decisive assertion: the first daemon still owns the path.
	if who := whoAnswers(t, path, answered); who != "first" {
		t.Errorf("after the refused bind, dial reached %q, want \"first\"", who)
	}
}

// TestBindSocketClearsAStaleSocketFile is the other half of the contract. A
// crashed daemon leaves the file behind with nothing accepting on it, and
// net.Listen fails with EADDRINUSE unless it is cleared — so the probe must
// not be so cautious that it strands every workspace whose daemon was killed.
func TestBindSocketClearsAStaleSocketFile(t *testing.T) {
	dir := shortSocketDir(t)
	path := filepath.Join(dir, "daemon.sock")

	// A dead daemon's leftovers: the file exists, nothing is accepting.
	dead, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := dead.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file should still exist: %v", err)
	}

	s := &Server{}
	ln, err := s.BindSocket(path)
	if err != nil {
		t.Fatalf("BindSocket refused a STALE socket file — every workspace whose "+
			"daemon was killed would be stranded: %v", err)
	}
	defer ln.Close()

	answered := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case answered <- "new":
			default:
			}
			_ = c.Close()
		}
	}()
	if who := whoAnswers(t, path, answered); who != "new" {
		t.Errorf("dial reached %q, want the newly bound listener", who)
	}
}

// TestBindSocketBindsAFreshPath pins the ordinary case — no file, no daemon.
func TestBindSocketBindsAFreshPath(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "daemon.sock")
	s := &Server{}
	ln, err := s.BindSocket(path)
	if err != nil {
		t.Fatalf("BindSocket on a fresh path: %v", err)
	}
	defer ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("socket file missing after a successful bind: %v", err)
	}
}
