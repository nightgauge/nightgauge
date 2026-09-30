package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// logRetentionFixture makes a workspace root with a logs directory, pins the
// machine tier and the machine-state root to temp paths, and writes cfg (if
// any) as the machine config.
func logRetentionFixture(t *testing.T, machineYAML string) (root, logs string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if machineYAML != "" {
		if err := os.WriteFile(cfg, []byte(machineYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(config.SwapMachineConfigPathForTest(func() (string, error) { return cfg, nil }))
	t.Setenv("NIGHTGAUGE_STATE_HOME", t.TempDir())
	root = layouttest.Repo(t)
	return root, layouttest.MkLogsDir(t, root)
}

func writeAged(t *testing.T, dir, name string, size int, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLogRetentionReportsSizeAndCaps: the detail line carries the directory
// size and the configured caps with their source; under the cap there is no
// finding.
func TestLogRetentionReportsSizeAndCaps(t *testing.T) {
	root, logs := logRetentionFixture(t, "pipeline:\n  logs:\n    max_size_mb: 1\n")
	now := time.Now()
	writeAged(t, logs, "a.log", 2048, now.Add(-48*time.Hour))
	fs, detail := logRetentionFindings(root, now)
	if len(fs) != 0 {
		t.Fatalf("findings under the cap: %s", findingsText(fs))
	}
	for _, want := range []string{"clone logs 2.0 KiB in 1 file(s)", "caps 1.0 MiB (from ", "30 days (default)"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
}

// TestLogRetentionOverCapFindings: a directory over its cap is housekeeping
// when a prune would fix it and a warning when only kept files remain. Doctor
// never deletes anything.
func TestLogRetentionOverCapFindings(t *testing.T) {
	root, logs := logRetentionFixture(t, "pipeline:\n  logs:\n    max_size_mb: 1\n")
	now := time.Now()
	old := writeAged(t, logs, "old.log", 1<<20, now.Add(-48*time.Hour))
	writeAged(t, logs, "new.log", 1<<19, now.Add(-2*time.Hour))
	fs, _ := logRetentionFindings(root, now)
	if len(fs) != 1 || fs[0].Code != codeLogRetention || fs[0].Severity != SeverityHousekeeping {
		t.Fatalf("want one housekeeping %s, got %s", codeLogRetention, findingsText(fs))
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("doctor deleted a log file")
	}

	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	writeAged(t, logs, "go-backend.log", 2<<20, now.Add(-72*time.Hour)) // live: never pruned
	fs, _ = logRetentionFindings(root, now)
	if len(fs) != 1 || fs[0].Severity != SeverityWarning || !strings.Contains(fs[0].Title, "log-dir-over-cap") {
		t.Fatalf("want one over-cap warning, got %s", findingsText(fs))
	}
	if !strings.Contains(fs[0].Evidence["kept"], "go-backend.log") {
		t.Errorf("evidence kept = %q, want it to name go-backend.log", fs[0].Evidence["kept"])
	}
}
