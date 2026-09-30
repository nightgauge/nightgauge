package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

const (
	legacyMachineID = "11111111-2222-4333-8444-555555555555\n" // newMachineStateFixture's
	stateMachineID  = "99999999-2222-4333-8444-555555555555\n"
)

// machineIDConflictFixture is the machine-state fixture with a different id
// at the new location, and known modification times on both files.
func machineIDConflictFixture(t *testing.T) (machineStateFixture, time.Time, time.Time) {
	t.Helper()
	f := newMachineStateFixture(t)
	writeLayoutFile(t, filepath.Join(f.state, "machine-id"), stateMachineID, 0o600)
	legacyAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	stateAt := time.Date(2026, 9, 29, 10, 11, 12, 0, time.UTC)
	for path, at := range map[string]time.Time{
		filepath.Join(f.legacy, "machine-id"): legacyAt, filepath.Join(f.state, "machine-id"): stateAt,
	} {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return f, legacyAt, stateAt
}

// TestMachineIDConflictFinding (#2308): the NGD045 finding for machine-id
// shows both ids, each file's modification time, the one the running binary
// uses, and the exact command that keeps each.
func TestMachineIDConflictFinding(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "")
	f, legacyAt, stateAt := machineIDConflictFixture(t)
	conflicts := findingsWith(mustMachineFindings(t, f.migrator()), codeLayoutConflict)
	if len(conflicts) != 1 {
		t.Fatalf("conflict findings = %s, want one for machine-id", findingsText(conflicts))
	}
	c := conflicts[0]
	want := map[string]string{
		"legacy_id": strings.TrimSpace(legacyMachineID), "target_id": strings.TrimSpace(stateMachineID),
		"legacy_modified": legacyAt.Format(time.RFC3339), "target_modified": stateAt.Format(time.RFC3339),
		"keep_state":  "nightgauge doctor resolve machine-id --keep state",
		"keep_legacy": "nightgauge doctor resolve machine-id --keep legacy",
	}
	for k, v := range want {
		if c.Evidence[k] != v {
			t.Errorf("evidence %s = %q, want %q", k, c.Evidence[k], v)
		}
	}
	if !strings.Contains(c.Evidence["in_use"], strings.TrimSpace(stateMachineID)) {
		t.Errorf("in_use = %q, want it to name the state id", c.Evidence["in_use"])
	}
	for _, s := range []string{want["keep_state"], want["keep_legacy"], strings.TrimSpace(legacyMachineID),
		strings.TrimSpace(stateMachineID), "new device"} {
		if !strings.Contains(c.Cause, s) {
			t.Errorf("cause does not contain %q:\n%s", s, c.Cause)
		}
	}
	if len(c.Remedies) != 3 || c.Remedies[0].Verb != verbLayoutMigrate ||
		c.Remedies[1].Kind != RemedyManual || c.Remedies[2].Kind != RemedyManual {
		t.Fatalf("remedies = %+v, want migrate then the two manual choices", c.Remedies)
	}
	if !strings.Contains(c.Remedies[0].Summary, want["keep_state"]) || !strings.Contains(c.Remedies[0].Summary, want["keep_legacy"]) {
		t.Errorf("the first remedy's summary (the list report's fix line) = %q, want both commands", c.Remedies[0].Summary)
	}
}

// TestResolveMachineIDConflict (#2308): choosing a side leaves that id at the
// new path byte for byte, mode 0600, the other id in a timestamped backup,
// and the legacy path clear; `doctor --fix` then completes the machine-state
// migration and a second run is a no-op. The backup is never taken for
// legacy state.
func TestResolveMachineIDConflict(t *testing.T) {
	for _, tc := range []struct{ side, kept, other string }{
		{MachineIDKeepState, stateMachineID, legacyMachineID},
		{MachineIDKeepLegacy, legacyMachineID, stateMachineID},
	} {
		t.Run(tc.side, func(t *testing.T) {
			t.Setenv("NIGHTGAUGE_AGENT_ID", "")
			f, _, _ := machineIDConflictFixture(t)
			m := f.migrator()
			// An automatic run met the conflict and recorded the attempt.
			if _, ran := autoMigrateMachineState(context.Background(), m, time.Now()); !ran {
				t.Fatal("the automatic run did not run")
			}
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			res, err := resolveMachineID(m, tc.side, now)
			if err != nil {
				t.Fatalf("resolve %s: %v", tc.side, err)
			}
			idPath := filepath.Join(f.state, "machine-id")
			backup := filepath.Join(f.state, "machine-id.replaced-20260930T120000Z")
			if res.Path != idPath || res.Backup != backup || res.Legacy != filepath.Join(f.legacy, "machine-id") ||
				res.ID != strings.TrimSpace(tc.kept) || res.BackupID != strings.TrimSpace(tc.other) {
				t.Errorf("resolution = %+v", res)
			}
			for path, content := range map[string]string{idPath: tc.kept, backup: tc.other} {
				if got := readLayoutFile(t, path); got != content {
					t.Errorf("%s = %q, want %q", path, got, content)
				}
				if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
					t.Errorf("%s mode = %v (%v), want 0600", path, info.Mode().Perm(), err)
				}
			}
			assertGone(t, filepath.Join(f.legacy, "machine-id"))
			assertGone(t, filepath.Join(f.state, layoutAttemptName))
			if c := findingsWith(mustMachineFindings(t, m), codeLayoutConflict); len(c) != 0 {
				t.Errorf("NGD045 after resolution:\n%s", findingsText(c))
			}

			// doctor --fix now moves the rest of the machine state.
			rep := f.fixer(m).Run(context.Background(), FixOptions{})
			if rep.ExitCode != 0 || rep.Counts.Conflict != 0 || rep.Counts.Blocked != 0 {
				t.Fatalf("fix after resolution: exit %d, counts %+v; results %+v", rep.ExitCode, rep.Counts, rep.Results)
			}
			if got := readLayoutFile(t, filepath.Join(f.state, layoutMarkerName)); got != strconv.Itoa(MachineStateLayoutVersion)+"\n" {
				t.Errorf("marker = %q, want the machine-state migration complete", got)
			}
			assertGone(t, filepath.Join(f.legacy, "usage"))
			if got := readLayoutFile(t, idPath); got != tc.kept {
				t.Errorf("the migration changed the kept id: %q", got)
			}
			if got := readLayoutFile(t, backup); got != tc.other {
				t.Errorf("the migration changed the backup: %q", got)
			}
			if id, err := platform.MachineID(); err != nil || id != strings.TrimSpace(tc.kept) {
				t.Errorf("MachineID = %q, %v; want the kept id", id, err)
			}

			// A second run is a no-op, and there is nothing left to resolve.
			stateBefore, legacyBefore := treeDigest(t, f.state), treeDigest(t, f.legacy)
			if again := f.fixer(m).Run(context.Background(), FixOptions{}); again.ExitCode != 0 || len(again.Results) != 0 {
				t.Errorf("second fix: exit %d, %d result(s)", again.ExitCode, len(again.Results))
			}
			if _, err := resolveMachineID(m, tc.side, now); !errors.Is(err, ErrNoMachineIDConflict) {
				t.Errorf("a second resolution returned %v, want ErrNoMachineIDConflict", err)
			}
			if treeDigest(t, f.state) != stateBefore || treeDigest(t, f.legacy) != legacyBefore {
				t.Error("the second run changed the tree")
			}
		})
	}
}

// TestResolveMachineIDConflictChangesNothingWithoutAChoice (#2308): no side,
// an unknown side, or no conflict changes nothing and never mints an id.
func TestResolveMachineIDConflictChangesNothingWithoutAChoice(t *testing.T) {
	t.Run("no or an unknown choice", func(t *testing.T) {
		f, _, _ := machineIDConflictFixture(t)
		m := f.migrator()
		stateBefore, legacyBefore := treeDigest(t, f.state), treeDigest(t, f.legacy)
		for _, side := range []string{"", "both", "State", "new"} {
			if _, err := resolveMachineID(m, side, time.Now()); err == nil || !strings.Contains(err.Error(), "no default") {
				t.Errorf("side %q: err %v, want a refusal naming both choices", side, err)
			}
		}
		if treeDigest(t, f.state) != stateBefore || treeDigest(t, f.legacy) != legacyBefore {
			t.Error("a refused resolution changed the tree")
		}
	})
	t.Run("no conflict: only the legacy id", func(t *testing.T) {
		f := newMachineStateFixture(t)
		m := f.migrator()
		legacyBefore := treeDigest(t, f.legacy)
		if _, err := resolveMachineID(m, MachineIDKeepState, time.Now()); !errors.Is(err, ErrNoMachineIDConflict) {
			t.Errorf("err = %v, want ErrNoMachineIDConflict", err)
		}
		assertGone(t, filepath.Join(f.state, "machine-id"))
		if treeDigest(t, f.legacy) != legacyBefore {
			t.Error("the legacy tree changed")
		}
	})
	t.Run("no conflict: the same id at both paths", func(t *testing.T) {
		f := newMachineStateFixture(t)
		writeLayoutFile(t, filepath.Join(f.state, "machine-id"), legacyMachineID, 0o600)
		stateBefore, legacyBefore := treeDigest(t, f.state), treeDigest(t, f.legacy)
		if _, err := resolveMachineID(f.migrator(), MachineIDKeepLegacy, time.Now()); !errors.Is(err, ErrNoMachineIDConflict) {
			t.Errorf("err = %v, want ErrNoMachineIDConflict", err)
		}
		if treeDigest(t, f.state) != stateBefore || treeDigest(t, f.legacy) != legacyBefore {
			t.Error("the tree changed")
		}
	})
	t.Run("an empty chosen id is refused", func(t *testing.T) {
		f, _, _ := machineIDConflictFixture(t)
		writeLayoutFile(t, filepath.Join(f.legacy, "machine-id"), "\n", 0o644)
		stateBefore, legacyBefore := treeDigest(t, f.state), treeDigest(t, f.legacy)
		if _, err := resolveMachineID(f.migrator(), MachineIDKeepLegacy, time.Now()); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("err = %v, want a refusal of the empty id", err)
		}
		if treeDigest(t, f.state) != stateBefore || treeDigest(t, f.legacy) != legacyBefore {
			t.Error("the tree changed")
		}
	})
}

// TestMachineIDBackupNeverReplaces: a second backup in the same second gains
// a suffix instead of replacing the first.
func TestMachineIDBackupNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	a, err := writeMachineIDBackup(dir, []byte("a\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := writeMachineIDBackup(dir, []byte("b\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || readLayoutFile(t, a) != "a\n" || readLayoutFile(t, b) != "b\n" || !strings.HasSuffix(b, "-1") {
		t.Errorf("backups %s and %s", a, b)
	}
}
