package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// TestDaemonSocketPath pins ADR-024 § 10 (#2039): the socket lives in the
// per-user runtime root, keyed by a short hash of the workspace, never in the
// working tree, and always fits sun_path.
func TestDaemonSocketPath(t *testing.T) {
	longRoot := "/" + strings.Repeat("d", 199)

	t.Run("XDG_RUNTIME_DIR unset uses the temp dir per uid", func(t *testing.T) {
		t.Setenv(layout.EnvRuntimeDir, "")
		t.Setenv("XDG_RUNTIME_DIR", "")
		got, err := DaemonSocketPath(longRoot)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(os.TempDir(), "nightgauge-"+strconv.Itoa(os.Getuid()), SocketKey(longRoot)+".sock")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if len(got) >= 100 {
			t.Errorf("a 200-character root gave a %d-byte path: %q", len(got), got)
		}
	})

	t.Run("XDG_RUNTIME_DIR set", func(t *testing.T) {
		xdg := "/run/user/1000"
		t.Setenv(layout.EnvRuntimeDir, "")
		t.Setenv("XDG_RUNTIME_DIR", xdg)
		got, err := DaemonSocketPath(longRoot)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(xdg, "nightgauge", SocketKey(longRoot)+".sock"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("NIGHTGAUGE_RUNTIME_DIR beats XDG_RUNTIME_DIR", func(t *testing.T) {
		t.Setenv(layout.EnvRuntimeDir, "/r")
		t.Setenv("XDG_RUNTIME_DIR", "/x")
		got, err := DaemonSocketPath(longRoot)
		if err != nil {
			t.Fatal(err)
		}
		if want := "/r/" + SocketKey(longRoot) + ".sock"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("an override too long is an error, not a truncation", func(t *testing.T) {
		t.Setenv(layout.EnvRuntimeDir, "/"+strings.Repeat("r", 100))
		if _, err := DaemonSocketPath(longRoot); !errors.Is(err, ErrSocketPathTooLong) {
			t.Errorf("err = %v, want ErrSocketPathTooLong", err)
		}
	})

	t.Run("key is stable per root and distinct across roots", func(t *testing.T) {
		a, b := SocketKey("/work/a"), SocketKey("/work/b")
		if a == b {
			t.Errorf("two roots share key %q", a)
		}
		if SocketKey("/work/a") != a || SocketKey("/work/a/") != a {
			t.Error("the same root gave different keys")
		}
		if len(a) != 12 {
			t.Errorf("key %q is not 12 hex characters", a)
		}
	})

	t.Run("a symlinked spelling of a root shares its key", func(t *testing.T) {
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if SocketKey(link) != SocketKey(real) {
			t.Error("a symlink to the workspace resolved to a different daemon")
		}
	})

	t.Run("never inside the working tree", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(layout.EnvRuntimeDir, "")
		got, err := DaemonSocketPath(root)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Errorf("socket %q is inside the workspace %q", got, root)
		}
	})
}

// TestClientSocketPathPrefersTheSpawningDaemon pins NIGHTGAUGE_DAEMON_SOCKET.
func TestClientSocketPathPrefersTheSpawningDaemon(t *testing.T) {
	t.Setenv(EnvDaemonSocket, "/run/spawner.sock")
	if got, _ := ClientSocketPath("/any"); got != "/run/spawner.sock" {
		t.Errorf("got %q, want the spawning daemon's socket", got)
	}
	t.Setenv(EnvDaemonSocket, "")
	t.Setenv(layout.EnvRuntimeDir, "/r")
	if got, _ := ClientSocketPath("/any"); got != "/r/"+SocketKey("/any")+".sock" {
		t.Errorf("got %q, want the computed path", got)
	}
}

// TestSocketDirPermissions pins the security constraint: the socket's parent
// is created 0700, and a pre-existing parent that another user could have
// planted (symlink, group/other bits) makes BindSocket refuse.
func TestSocketDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix sockets")
	}
	base := shortSocketDir(t)

	t.Run("created 0700", func(t *testing.T) {
		dir := filepath.Join(base, "fresh")
		ln, err := (&Server{}).BindSocket(filepath.Join(dir, "s.sock"))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("socket dir mode = %#o, want 0700", perm)
		}
	})

	t.Run("group or other bits are refused", func(t *testing.T) {
		dir := filepath.Join(base, "loose")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := (&Server{}).BindSocket(filepath.Join(dir, "s.sock"))
		if !errors.Is(err, layout.ErrUnsafeRuntimeDir) {
			t.Errorf("err = %v, want ErrUnsafeRuntimeDir", err)
		}
	})

	t.Run("a symlinked directory is refused", func(t *testing.T) {
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := (&Server{}).BindSocket(filepath.Join(link, "s.sock"))
		if !errors.Is(err, layout.ErrUnsafeRuntimeDir) {
			t.Errorf("err = %v, want ErrUnsafeRuntimeDir", err)
		}
	})

	t.Run("a non-directory is refused", func(t *testing.T) {
		file := filepath.Join(base, "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := layout.VerifyRuntimeDir(file); !errors.Is(err, layout.ErrUnsafeRuntimeDir) {
			t.Errorf("err = %v, want ErrUnsafeRuntimeDir", err)
		}
	})
}
