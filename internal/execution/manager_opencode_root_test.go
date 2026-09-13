package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// TestManagerCleanupOpenCodeRunRoot: the run's end deletes its OpenCode
// per-run root (ADR-022 § 22) through the manager, whatever adapter the run's
// last stage used. A run with no identity has no root, a root already gone is
// not an error, and an id that is not a run identity is refused rather than
// turned into a path.
func TestManagerCleanupOpenCodeRunRoot(t *testing.T) {
	home := isolateOpenCodeHome(t)
	const runID = "01890a5d-ac96-774b-bcce-b302099a8057"
	root, _, err := adapters.EnsureOpenCodeRunRoot(home, runID, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir(), adapters.NewClaudeAdapter())
	if err := m.CleanupOpenCodeRunRoot(runID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("CleanupOpenCodeRunRoot left the root behind: %v", err)
	}
	if err := m.CleanupOpenCodeRunRoot(runID); err != nil {
		t.Errorf("cleaning up a root that is already gone = %v, want nil", err)
	}
	if err := m.CleanupOpenCodeRunRoot(""); err != nil {
		t.Errorf("CleanupOpenCodeRunRoot for a run with no identity = %v, want nil", err)
	}
	sibling := filepath.Join(home, ".nightgauge", "opencode", "x")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.CleanupOpenCodeRunRoot("../x"); err == nil {
		t.Error("CleanupOpenCodeRunRoot accepted ../x")
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("CleanupOpenCodeRunRoot(../x) deleted a directory outside the runs directory: %v", err)
	}
}

// TestComposeStageEnv_WithholdFiltersOnlyTheInheritedEnv: the adapter's
// withhold decision applies to the host's environment and not to its own
// exports, so the opencode adapter can withhold every inherited OPENCODE_*
// variable and still set its own (ADR-022 § 8).
func TestComposeStageEnv_WithholdFiltersOnlyTheInheritedEnv(t *testing.T) {
	inherited := []string{"PATH=/usr/bin", "OPENCODE_CONFIG=/operator/opencode.json", "OPENCODE_DISABLE_SHARE=0"}
	withhold := func(key string) bool { return strings.HasPrefix(key, "OPENCODE_") }
	env := composeStageEnv(inherited, withhold, map[string]string{"OPENCODE_DISABLE_SHARE": "1"}, "", "")
	if v, ok := lookupEnv(env, "OPENCODE_CONFIG"); ok {
		t.Errorf("the withheld inherited OPENCODE_CONFIG reached the env as %q", v)
	}
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_DISABLE_SHARE=") {
			n++
		}
	}
	if v, _ := lookupEnv(env, "OPENCODE_DISABLE_SHARE"); n != 1 || v != "1" {
		t.Errorf("OPENCODE_DISABLE_SHARE appears %d times with value %q; want the adapter's export once", n, v)
	}
	if v, ok := lookupEnv(env, "PATH"); !ok || !strings.Contains(v, "/usr/bin") {
		t.Errorf("PATH = %q (present=%v); the filter removed a variable it does not withhold", v, ok)
	}
}
