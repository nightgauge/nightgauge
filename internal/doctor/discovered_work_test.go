package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
)

func TestDiscoveredWorkOrphans_NoFilesIsQuiet(t *testing.T) {
	now := time.Now()
	if fs, _ := discoveredWorkOrphanFindings(filepath.Join(t.TempDir(), "absent"), now); len(fs) != 0 {
		t.Fatalf("absent dir: %+v", fs)
	}
	if fs, _ := discoveredWorkOrphanFindings(t.TempDir(), now); len(fs) != 0 {
		t.Fatalf("empty dir: %+v", fs)
	}
}

func TestDiscoveredWorkOrphans_ReportsAndKeepsFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	rec := discoveredwork.Record{Summary: "x", Evidence: []string{"y"}, SourceStage: "feature-dev",
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "XS"}
	if _, _, err := discoveredwork.Append(dir, rec, now); err != nil {
		t.Fatal(err)
	}
	fs, detail := discoveredWorkOrphanFindings(dir, now)
	if len(fs) != 1 {
		t.Fatalf("findings = %+v (%s)", fs, detail)
	}
	f := fs[0]
	if f.Code != "NGD050" || f.Severity != SeverityWarning || f.Evidence["issue"] != "42" ||
		len(f.Remedies) != 1 || f.Remedies[0].Kind != RemedyManual {
		t.Fatalf("finding = %+v", f)
	}
	if _, err := os.Stat(filepath.Join(dir, "discovered-work-42.json")); err != nil {
		t.Fatal("the check deleted the capture file")
	}
	// A live run (issue context present, file fresh) is not an orphan.
	os.WriteFile(filepath.Join(dir, "issue-42.json"), []byte("{}"), 0o644)
	if fs, _ := discoveredWorkOrphanFindings(dir, now); len(fs) != 0 {
		t.Fatalf("live run reported: %+v", fs)
	}
}
