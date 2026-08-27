package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

func seedSurvival(t *testing.T, root string, recs ...survival.Record) {
	t.Helper()
	store := survival.NewStore(root)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, r := range recs {
		if _, err := store.Append(r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func pendingRec(repo string, issue int, mergedAt time.Time) survival.Record {
	return survival.NewPending(repo, issue, issue+1000,
		fmt.Sprintf("sha%d", issue), mergedAt.UTC().Format(time.RFC3339), "")
}

// TestSurvivalBacklog_ReportsRecordsPastTwiceTheWindow is the observability half
// of #992. Finalization running is not enough — nothing reported that it had
// STOPPED running, which is how the sweep stayed dead for weeks while the
// writer kept appending.
func TestSurvivalBacklog_ReportsRecordsPastTwiceTheWindow(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	// 30 days old against a 7-day window: well past 2x, so its verdict is
	// already folding to "unobserved".
	seedSurvival(t, root, pendingRec("o/r", 42, now.AddDate(0, 0, -30)))

	item, warning := checkSurvivalBacklog(root, now, 7)

	if item.OK {
		t.Error("a record 30d past a 7d window reported OK — this is the exact state the " +
			"workspace was in for a month with every check green")
	}
	if warning == "" {
		t.Error("no warning surfaced; the arm is invisible to the operator")
	}
	if !strings.Contains(warning, "survival-backlog-stale") {
		t.Errorf("warning lacks the stable code: %q", warning)
	}
	if !strings.Contains(warning, "nightgauge survival sweep") {
		t.Errorf("warning does not name the remedy: %q", warning)
	}
	if !strings.Contains(warning, "o/r#42") {
		t.Errorf("warning does not name the affected record: %q", warning)
	}
}

// TestSurvivalBacklog_FreshRecordIsNotAFinding is the control. A pending record
// inside its window is the NORMAL state — every merge creates one. An arm that
// fired on those would be noise on every healthy repo and would be muted.
func TestSurvivalBacklog_FreshRecordIsNotAFinding(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seedSurvival(t, root,
		pendingRec("o/r", 1, now.AddDate(0, 0, -1)),  // 1d — fresh
		pendingRec("o/r", 2, now.AddDate(0, 0, -10)), // 10d — past window, under 2x
	)

	item, warning := checkSurvivalBacklog(root, now, 7)

	if !item.OK {
		t.Errorf("a record still inside 2x the window was reported as a finding: %s", item.Error)
	}
	if warning != "" {
		t.Errorf("unexpected warning for a healthy backlog: %q", warning)
	}
	if !strings.Contains(item.Detail, "2") {
		t.Errorf("detail should report how many are pending, got %q", item.Detail)
	}
}

// TestSurvivalBacklog_EmptyStoreIsHealthy guards the common case: a repo that
// has never merged anything through the pipeline.
func TestSurvivalBacklog_EmptyStoreIsHealthy(t *testing.T) {
	item, warning := checkSurvivalBacklog(t.TempDir(), time.Now(), 7)
	if !item.OK || warning != "" {
		t.Errorf("empty store reported a finding: ok=%v warning=%q", item.OK, warning)
	}
}

// TestSurvivalBacklog_UnparseableTimestampIsReported guards against silently
// skipping a record that can never be finalized either — the arm must not
// quietly drop the one record that is most stuck.
func TestSurvivalBacklog_UnparseableTimestampIsReported(t *testing.T) {
	root := t.TempDir()
	rec := pendingRec("o/r", 9, time.Now())
	rec.MergedAt = "not-a-timestamp"
	seedSurvival(t, root, rec)

	item, warning := checkSurvivalBacklog(root, time.Now(), 7)
	if item.OK {
		t.Error("a record with an unparseable merged_at reported OK — it can never be " +
			"finalized, so it is permanently stuck")
	}
	if !strings.Contains(warning, "o/r#9") {
		t.Errorf("warning does not name the stuck record: %q", warning)
	}
}

// TestSurvivalBacklog_WindowScalesTheThreshold proves the threshold tracks the
// configured window rather than a hardcoded number of days.
func TestSurvivalBacklog_WindowScalesTheThreshold(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	seedSurvival(t, root, pendingRec("o/r", 5, now.AddDate(0, 0, -20)))

	// 20d is past 2x7=14 → a finding.
	if item, _ := checkSurvivalBacklog(root, now, 7); item.OK {
		t.Error("20d old should exceed 2x a 7d window")
	}
	// 20d is inside 2x30=60 → not a finding.
	if item, _ := checkSurvivalBacklog(root, now, 30); !item.OK {
		t.Errorf("20d old should be inside 2x a 30d window: %s", item.Error)
	}
}
