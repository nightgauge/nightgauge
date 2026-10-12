package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// RecentRuns reads the history most recent first, bounded, with each run's
// repository and visibility, and writes nothing; a repository with no
// history answers empty (#1587).
func TestRecentRuns_ReadsTheHistoryWithoutWritingIt(t *testing.T) {
	dir := layouttest.Repo(t)
	if got, err := RecentRuns(dir, 10); err != nil || len(got) != 0 {
		t.Fatalf("no history: %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(layouttest.PipelineDir(t, dir), "history")); !os.IsNotExist(err) {
		t.Fatalf("reading created the history directory: %v", err)
	}

	hw := NewHistoryWriter(dir)
	for i, id := range []string{mustRunID(t), mustRunID(t), mustRunID(t)} {
		rec := makeRunRec(id, 100+i, fixedNow.Format("2006-01-02T15:04:05Z"), "feature-dev")
		if i == 2 {
			rec.Visibility = VisibilityPrivate
		}
		if err := hw.WriteV2Record(rec, fixedNow); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	historyDir := filepath.Join(layouttest.PipelineDir(t, dir), "history")
	before := dirState(t, historyDir)
	got, err := RecentRuns(dir, 2)
	if err != nil || len(got) != 2 || got[0].IssueNumber != 102 || got[1].IssueNumber != 101 {
		t.Fatalf("RecentRuns = %+v, %v; want #102 then #101", got, err)
	}
	if got[0].Visibility != VisibilityPrivate || got[0].Repo != "nightgauge/nightgauge" {
		t.Errorf("the record lost its visibility or repository: %+v", got[0])
	}
	if after := dirState(t, historyDir); after != before {
		t.Errorf("reading changed the history:\nbefore %s\nafter  %s", before, after)
	}
	if got, err := RecentRuns(dir, 0); err != nil || len(got) != 0 {
		t.Errorf("limit 0 = %v, %v", got, err)
	}
	if _, err := RecentRuns(t.TempDir(), 2); err == nil {
		t.Error("a root that is no repository answered")
	}
}

func dirState(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for _, e := range entries {
		info, _ := e.Info()
		out += e.Name() + "@" + info.ModTime().String() + ";"
	}
	return out
}
