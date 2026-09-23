package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// isolateMachineIDState points HOME (where the legacy ~/.nightgauge/machine-id
// lives) and the machine-state root at temp dirs, clears the env override, and
// returns (legacy path, new path).
func isolateMachineIDState(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home resolution
	t.Setenv(layout.EnvStateHome, state)
	t.Setenv(machineIDEnv, "")
	return filepath.Join(home, ".nightgauge", machineIDFileName), filepath.Join(state, machineIDFileName)
}

func writeLegacyMachineID(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The pre-ADR-024 writer used 0644.
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveMachineID_PersistsAndReuses verifies a UUID is minted once under
// the state root, with mode 0600, and reused on subsequent calls.
func TestResolveMachineID_PersistsAndReuses(t *testing.T) {
	legacy, path := isolateMachineIDState(t)

	first := ResolveMachineID()
	if first == "" {
		t.Fatal("ResolveMachineID() returned empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("machine-id file not persisted under the state root: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("machine-id mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a legacy ~/.nightgauge/machine-id was written: %v", err)
	}
	if second := ResolveMachineID(); second != first {
		t.Errorf("machine id not stable: first=%q second=%q", first, second)
	}
}

// TestMachineIDCopiedFromLegacyAndKept: a pre-ADR-024 id is copied byte for
// byte and kept, never regenerated (a new id is a new device to the platform,
// #1883); the legacy file stays, narrowed to 0600, for an older binary.
func TestMachineIDCopiedFromLegacyAndKept(t *testing.T) {
	legacy, path := isolateMachineIDState(t)
	const content = "3f2c9a1e-legacy-id\n"
	writeLegacyMachineID(t, legacy, content)

	id, err := MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	if id != strings.TrimSpace(content) {
		t.Fatalf("MachineID = %q, want the legacy id %q", id, strings.TrimSpace(content))
	}
	for _, p := range []string{path, legacy} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != content {
			t.Errorf("%s = %q (%v), want %q", p, got, err, content)
		}
		if runtime.GOOS != "windows" {
			if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", p, info.Mode().Perm())
			}
		}
	}
}

// TestMachineIDNotRegeneratedWhenLegacyPresent: when the legacy id and a
// different id at the new location both exist, the new file wins, nothing is
// rewritten, no id is minted, and the lookup does not fail (a failed lookup
// silently stops queue sync and agent registration).
func TestMachineIDNotRegeneratedWhenLegacyPresent(t *testing.T) {
	legacy, path := isolateMachineIDState(t)
	writeLegacyMachineID(t, legacy, "legacy-id\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := MachineID()
	if err != nil || id != "new-id" {
		t.Fatalf("MachineID = %q, %v; want the authoritative new-location id", id, err)
	}
	if got := ResolveMachineID(); got != "new-id" {
		t.Errorf("ResolveMachineID = %q, want new-id", got)
	}
	for p, want := range map[string]string{legacy: "legacy-id\n", path: "new-id\n"} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("%s = %q, want it untouched (%q)", p, got, want)
		}
	}
}

// An existing but empty id file is an error, never a reason to mint: with the
// exclusive-create fallback a concurrent writer's file can be briefly empty.
func TestMachineIDEmptyFileIsNotRegenerated(t *testing.T) {
	_, path := isolateMachineIDState(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := MachineID(); err == nil {
		t.Fatalf("MachineID = %q over an empty file, want an error", id)
	}
	if got, _ := os.ReadFile(path); len(got) != 0 {
		t.Errorf("an id was minted over the empty file: %q", got)
	}
}

// A fresh id is also written as the legacy compatibility copy when
// ~/.nightgauge exists, so an older binary on the machine reads the same id.
func TestMachineIDMintWritesCompatibilityCopy(t *testing.T) {
	legacy, path := isolateMachineIDState(t)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	for _, p := range []string{path, legacy} {
		if got, _ := os.ReadFile(p); strings.TrimSpace(string(got)) != id {
			t.Errorf("%s = %q, want %q", p, got, id)
		}
	}
}

// TestMachineIDConcurrentFirstUseAgrees: processes starting together on a
// fresh machine (simulated by goroutines) all get one id.
func TestMachineIDConcurrentFirstUseAgrees(t *testing.T) {
	_, _ = isolateMachineIDState(t)
	const n = 16
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = MachineID()
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("MachineID[%d]: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent first use minted two ids: %q and %q", ids[0], ids[i])
		}
	}
}
