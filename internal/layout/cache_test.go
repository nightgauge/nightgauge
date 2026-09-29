package layout

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCacheHomePathResolution(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	xdg := filepath.Join(t.TempDir(), "xdg")
	base := filepath.Join(t.TempDir(), "base")

	cases := []struct {
		name, goos, override, xdg, base, want string
	}{
		{"override beats xdg", "linux", override, xdg, base, override},
		{"override on windows", "windows", override, xdg, base, override},
		{"xdg on linux", "linux", "", xdg, base, filepath.Join(xdg, "nightgauge")},
		{"xdg on darwin", "darwin", "", xdg, base, filepath.Join(xdg, "nightgauge")},
		{"xdg on windows", "windows", "", xdg, base, filepath.Join(xdg, "nightgauge")},
		{"linux default", "linux", "", "", base, filepath.Join(base, "nightgauge")},
		{"darwin default", "darwin", "", "", base, filepath.Join(base, "nightgauge")},
		{"windows default is apart from STATE", "windows", "", "", base, filepath.Join(base, "nightgauge", "cache")},
		{"relative xdg is ignored", "linux", "", "rel/xdg", base, filepath.Join(base, "nightgauge")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{EnvCacheHome: tc.override, "XDG_CACHE_HOME": tc.xdg}
			lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
			got, err := CacheHomePathFrom(tc.goos, tc.base, lookup)
			if err != nil || got != tc.want {
				t.Fatalf("CacheHomePathFrom = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCacheHomePathRefusesWhatItCannotUse(t *testing.T) {
	rel := func(k string) (string, bool) {
		if k == EnvCacheHome {
			return "relative", true
		}
		return "", false
	}
	if _, err := CacheHomePathFrom("linux", "/abs", rel); !errors.Is(err, ErrNoCacheHome) {
		t.Fatalf("relative override: err = %v; want ErrNoCacheHome", err)
	}
	if _, err := CacheHomePathFrom("linux", "", nil); !errors.Is(err, ErrNoCacheHome) {
		t.Fatalf("no user cache dir: err = %v; want ErrNoCacheHome", err)
	}
}

func TestCacheDirCreatesPrivateDirectories(t *testing.T) {
	home := filepath.Join(t.TempDir(), "cache-home")
	t.Setenv(EnvCacheHome, home)
	dir, err := CacheDir("recall", "abc")
	if err != nil || dir != filepath.Join(home, "recall", "abc") {
		t.Fatalf("CacheDir = %q, %v", dir, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, d := range []string{home, filepath.Join(home, "recall"), dir} {
		info, err := os.Stat(d)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, %v; want 0700", d, info.Mode().Perm(), err)
		}
	}
	// An existing looser directory is narrowed.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CacheDir("recall", "abc"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("existing dir not narrowed: %v", info.Mode().Perm())
	}
}

func TestCacheDirRefusesSymlinksAndBadComponents(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "recall")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv(EnvCacheHome, home)
	if _, err := CacheDir("recall", "abc"); !errors.Is(err, ErrNoCacheHome) {
		t.Fatalf("symlinked component: err = %v; want ErrNoCacheHome", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("CacheDir created through a symlink: %v", entries)
	}

	linkedHome := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(outside, linkedHome); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCacheHome, linkedHome)
	if _, err := CacheDir("x"); !errors.Is(err, ErrNoCacheHome) {
		t.Fatalf("symlinked root: err = %v; want ErrNoCacheHome", err)
	}

	t.Setenv(EnvCacheHome, t.TempDir())
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := CacheDir(bad); !errors.Is(err, ErrNoCacheHome) {
			t.Fatalf("component %q: err = %v; want ErrNoCacheHome", bad, err)
		}
	}
}
