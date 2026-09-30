package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// TestMachineStateRefusesAnOverriddenStateWithTheRealHome (#2311): a process
// with the operator's HOME and a sandbox STATE (a test's temp directory) must
// not take the legacy machine state. Nothing is moved or deleted by the scan,
// the migration or the automatic run, and the finding says why.
func TestMachineStateRefusesAnOverriddenStateWithTheRealHome(t *testing.T) {
	f := newMachineStateFixture(t)
	def, err := layout.DefaultStateHomePath()
	if err != nil {
		t.Fatal(err)
	}
	// The default root exists: this HOME's own processes use it.
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	before := treeDigest(t, f.legacy)
	m := newMachineStateMigrator()

	refused := findingsWith(mustMachineFindings(t, m), codeLayoutRefused)
	if len(refused) == 0 {
		t.Fatal("no layout-refused finding")
	}
	for _, r := range refused {
		if !strings.Contains(r.Evidence["reason"], layout.EnvStateHome) {
			t.Errorf("the finding does not say why: %q", r.Evidence["reason"])
		}
	}
	if rep := m.Migrate(context.Background()); len(rep.Refused) == 0 || rep.Moved+rep.Removed+rep.CachesDeleted > 0 {
		t.Errorf("Migrate = %+v, want every row refused and nothing moved", rep)
	}
	rep, ran := autoMigrateMachineState(context.Background(), m, time.Now())
	if !ran || !strings.Contains(rep.Blocked, layout.EnvStateHome) {
		t.Errorf("automatic run = %+v (ran %v), want a report naming the override", rep, ran)
	}
	if after := treeDigest(t, f.legacy); after != before {
		t.Errorf("the legacy root changed:\n%s\nwant\n%s", after, before)
	}
	if _, err := os.Lstat(filepath.Join(f.state, "machine-id")); err == nil {
		t.Error("machine-id reached the sandbox STATE")
	}
}

// TestMachineStateAutoRunOnlyReportsUnderAnOverride (#2311): with an override
// and no default root, `doctor --fix` may migrate, but the automatic run at
// CLI start only reports.
func TestMachineStateAutoRunOnlyReportsUnderAnOverride(t *testing.T) {
	f := newMachineStateFixture(t)
	before := treeDigest(t, f.legacy)
	m := f.migrator()
	m.override = func() string { return "XDG_STATE_HOME" }
	rep, ran := autoMigrateMachineState(context.Background(), m, time.Now())
	if !ran || rep.Blocked == "" || rep.Changed() {
		t.Errorf("automatic run = %+v (ran %v), want a report and no change", rep, ran)
	}
	if after := treeDigest(t, f.legacy); after != before {
		t.Error("the automatic run changed the legacy root under an override")
	}
}

// TestMachineIDConflictConsidersTheDefaultState (#2311): a legacy machine-id
// that differs from the one in the default machine-state directory is a
// conflict even when STATE is elsewhere; nothing moves.
func TestMachineIDConflictConsidersTheDefaultState(t *testing.T) {
	f := newMachineStateFixture(t)
	def := filepath.Join(t.TempDir(), "default-state")
	writeLayoutFile(t, filepath.Join(def, "machine-id"), "99999999-2222-4333-8444-555555555555\n", 0o600)
	m := f.migrator()
	m.defaultState = func() (string, error) { return def, nil }

	conflicts := findingsWith(mustMachineFindings(t, m), codeLayoutConflict)
	if len(conflicts) == 0 {
		t.Fatal("no conflict against the default machine-state directory")
	}
	rep := m.Migrate(context.Background())
	if len(rep.Conflicts) == 0 {
		t.Errorf("Migrate = %+v, want the conflict", rep)
	}
	if got := readLayoutFile(t, filepath.Join(f.legacy, "machine-id")); got != "11111111-2222-4333-8444-555555555555\n" {
		t.Errorf("legacy machine-id = %q", got)
	}
	assertGone(t, filepath.Join(f.state, "machine-id"))
}

// TestMachineStateMoveBacksUpTheLegacyMachineID (#2311): the migration never
// removes the legacy machine-id without leaving machine-id.migrated-<UTC>.
func TestMachineStateMoveBacksUpTheLegacyMachineID(t *testing.T) {
	f := newMachineStateFixture(t)
	if rep := f.migrator().Migrate(context.Background()); rep.Version != MachineStateLayoutVersion {
		t.Fatalf("Migrate = %+v", rep)
	}
	backups, _ := filepath.Glob(filepath.Join(f.legacy, "machine-id.migrated-*"))
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	if got := readLayoutFile(t, backups[0]); got != "11111111-2222-4333-8444-555555555555\n" {
		t.Errorf("backup = %q", got)
	}
	if got := readLayoutFile(t, filepath.Join(f.state, "machine-id")); got != "11111111-2222-4333-8444-555555555555\n" {
		t.Errorf("moved id = %q", got)
	}
}
