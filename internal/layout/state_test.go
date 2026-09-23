package layout

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// isolateState clears every variable StateHome reads and points HOME at a
// temp dir, so no test resolves the operator's real state root.
func isolateState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvStateHome, "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("LOCALAPPDATA", "")
	return home
}

func TestStateHomePathResolution(t *testing.T) {
	home := isolateState(t)
	override := filepath.Join(t.TempDir(), "override")
	xdg := filepath.Join(t.TempDir(), "xdg")
	local := filepath.Join(t.TempDir(), "Local")

	cases := []struct {
		name     string
		goos     string
		override string
		xdg      string
		local    string
		want     string
	}{
		{"override beats xdg (linux)", "linux", override, xdg, "", override},
		{"override beats xdg (darwin)", "darwin", override, xdg, "", override},
		{"override beats xdg (windows)", "windows", override, xdg, local, override},
		{"xdg on linux", "linux", "", xdg, "", filepath.Join(xdg, "nightgauge")},
		{"xdg on darwin", "darwin", "", xdg, "", filepath.Join(xdg, "nightgauge")},
		{"xdg on windows", "windows", "", xdg, local, filepath.Join(xdg, "nightgauge")},
		{"linux default", "linux", "", "", "", filepath.Join(home, ".local", "state", "nightgauge")},
		{"darwin default", "darwin", "", "", "", filepath.Join(home, ".nightgauge", "state")},
		{"windows default", "windows", "", "", local, filepath.Join(local, "nightgauge", "state")},
		{"windows without LOCALAPPDATA", "windows", "", "", "", filepath.Join(home, "AppData", "Local", "nightgauge", "state")},
		{"relative xdg is ignored", "linux", "", "rel/xdg", "", filepath.Join(home, ".local", "state", "nightgauge")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvStateHome, tc.override)
			t.Setenv("XDG_STATE_HOME", tc.xdg)
			t.Setenv("LOCALAPPDATA", tc.local)
			got, err := StateHomePathForGOOS(tc.goos)
			if err != nil {
				t.Fatalf("StateHomePathForGOOS(%q): %v", tc.goos, err)
			}
			if got != tc.want {
				t.Errorf("StateHomePathForGOOS(%q) = %q, want %q", tc.goos, got, tc.want)
			}
		})
	}
}

func TestStateHomeRejectsRelativeOverride(t *testing.T) {
	isolateState(t)
	t.Setenv(EnvStateHome, "relative/state")
	_, err := StateHome()
	if !errors.Is(err, ErrNoStateHome) || !strings.Contains(err.Error(), EnvStateHome) {
		t.Fatalf("StateHome with a relative override = %v, want ErrNoStateHome naming %s", err, EnvStateHome)
	}
}

func TestStateHomeWithoutHomeNamesOverride(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("os.UserHomeDir reads a different variable here")
	}
	isolateState(t)
	t.Setenv("HOME", "")
	for _, goos := range []string{"linux", "darwin"} {
		_, err := StateHomeForGOOS(goos)
		if !errors.Is(err, ErrNoStateHome) || !strings.Contains(err.Error(), EnvStateHome) {
			t.Errorf("%s with no HOME: err = %v, want ErrNoStateHome naming %s", goos, err, EnvStateHome)
		}
	}
}

func TestStateHomeCreatesPrivateDir(t *testing.T) {
	isolateState(t)
	root := filepath.Join(t.TempDir(), "a", "state")
	t.Setenv(EnvStateHome, root)
	got, err := StateHome()
	if err != nil {
		t.Fatalf("StateHome: %v", err)
	}
	if got != root {
		t.Fatalf("StateHome = %q, want %q", got, root)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("root not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("root mode = %v, want 0700", info.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".write-probe-*")); len(left) != 0 {
		t.Errorf("write probe left behind: %v", left)
	}
}

func TestStateHomeRefusesSymlinkRoot(t *testing.T) {
	isolateState(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv(EnvStateHome, link)
	if _, err := StateHome(); !errors.Is(err, ErrNoStateHome) {
		t.Fatalf("StateHome over a symlink = %v, want ErrNoStateHome", err)
	}
}

func TestStateHomeUnwritableRootNamesOverride(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory write permission is not enforced here")
	}
	isolateState(t)
	root := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	t.Setenv(EnvStateHome, root)
	_, err := StateHome()
	if !errors.Is(err, ErrNoStateHome) || !strings.Contains(err.Error(), EnvStateHome) {
		t.Fatalf("StateHome on a read-only root = %v, want ErrNoStateHome naming %s", err, EnvStateHome)
	}
}

// stateAndLegacy isolates the state root and returns (root, legacy dir).
func stateAndLegacy(t *testing.T) (string, string) {
	t.Helper()
	home := isolateState(t)
	root := filepath.Join(t.TempDir(), "state")
	t.Setenv(EnvStateHome, root)
	legacyDir := filepath.Join(home, ".nightgauge")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, legacyDir
}

func TestStateFileMovesLegacyByteForByte(t *testing.T) {
	root, legacyDir := stateAndLegacy(t)
	legacy := filepath.Join(legacyDir, "machine-id")
	content := []byte("id-bytes\n\x00tail")
	if err := os.WriteFile(legacy, content, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := StateFile("machine-id")
	if err != nil {
		t.Fatalf("StateFile: %v", err)
	}
	if path != filepath.Join(root, "machine-id") {
		t.Fatalf("StateFile = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("moved content = %q (%v), want %q", got, err, content)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want the legacy 0600 preserved", info.Mode().Perm())
		}
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy file not removed: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, legacyTempPrefix+"*")); len(left) != 0 {
		t.Errorf("temporary copies left behind: %v", left)
	}
}

func TestStateFileNothingToMove(t *testing.T) {
	root, _ := stateAndLegacy(t)
	path, err := StateFile("rate-limit.json")
	if err != nil {
		t.Fatalf("StateFile: %v", err)
	}
	if path != filepath.Join(root, "rate-limit.json") {
		t.Fatalf("StateFile = %q", path)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("StateFile created the file: %v", err)
	}
}

// A crash after the target was installed but before the source was deleted
// leaves two equal files; the next call completes the move.
func TestStateFileCompletesAnInterruptedMove(t *testing.T) {
	root, legacyDir := stateAndLegacy(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(legacyDir, "machine-id"), filepath.Join(root, "machine-id")} {
		if err := os.WriteFile(p, []byte("same\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := StateFile("machine-id"); err != nil {
		t.Fatalf("StateFile: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(legacyDir, "machine-id")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy copy not removed: %v", err)
	}
}

func TestStateFileConflictOverwritesNothing(t *testing.T) {
	root, legacyDir := stateAndLegacy(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, target := filepath.Join(legacyDir, "machine-id"), filepath.Join(root, "machine-id")
	if err := os.WriteFile(legacy, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := StateFile("machine-id")
	if !errors.Is(err, ErrStateMoveConflict) || !strings.Contains(err.Error(), "nightgauge doctor --fix") {
		t.Fatalf("StateFile on a conflict = %v, want ErrStateMoveConflict naming doctor --fix", err)
	}
	for p, want := range map[string]string{legacy: "old\n", target: "new\n"} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("%s = %q, want %q untouched", p, got, want)
		}
	}
}

func TestStateFileNeverFollowsLegacySymlink(t *testing.T) {
	root, legacyDir := stateAndLegacy(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(legacyDir, "machine-id")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := StateFile("machine-id"); !errors.Is(err, ErrStateMoveConflict) {
		t.Fatalf("StateFile over a legacy symlink = %v, want ErrStateMoveConflict", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "machine-id")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a symlinked legacy file was copied: %v", err)
	}
}

// Many processes starting together after an upgrade (simulated with
// goroutines) all end with the one legacy value at the target, and nothing
// lost or duplicated.
func TestStateFileConcurrentMovesAgree(t *testing.T) {
	root, legacyDir := stateAndLegacy(t)
	legacy := filepath.Join(legacyDir, "machine-id")
	if err := os.WriteFile(legacy, []byte("the-one-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = StateFile("machine-id")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("StateFile[%d]: %v", i, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "machine-id")); string(got) != "the-one-id\n" {
		t.Errorf("target = %q, want the legacy bytes", got)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy file survived: %v", err)
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != "machine-id" {
			t.Errorf("unexpected leftover %q in the state root", e.Name())
		}
	}
}

// A hint file that conflicts is discarded, not reported: a missing hint is a
// cold start.
func TestStateHintFileDiscardsConflictingLegacy(t *testing.T) {
	root, legacyDir := stateAndLegacy(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, target := filepath.Join(legacyDir, "rate-limit.json"), filepath.Join(root, "rate-limit.json")
	if err := os.WriteFile(legacy, []byte(`{"old":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"new":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := StateHintFile("rate-limit.json")
	if err != nil || path != target {
		t.Fatalf("StateHintFile = %q, %v", path, err)
	}
	if got, _ := os.ReadFile(target); string(got) != `{"new":1}` {
		t.Errorf("target overwritten: %q", got)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("conflicting legacy hint not discarded: %v", err)
	}
}

func TestWriteStateFileExclusiveOneWinner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine-id")
	got, won, err := WriteStateFileExclusive(path, []byte("first"), 0o600)
	if err != nil || !won || string(got) != "first" {
		t.Fatalf("first write = %q, %v, %v", got, won, err)
	}
	got, won, err = WriteStateFileExclusive(path, []byte("second"), 0o600)
	if err != nil || won || string(got) != "first" {
		t.Fatalf("second write = %q, %v, %v; want the first value kept", got, won, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}
