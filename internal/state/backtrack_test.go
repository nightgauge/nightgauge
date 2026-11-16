package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// The run record keeps the planned file list, so plan fidelity survives the
// planning context being pruned (#3081).
func TestBuildV2Record_CarriesPlannedFiles(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.PipelineDir(t, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	planning := `{"files_to_create":["a/new.go"],"files_to_modify":["b/old.go","c/old.ts"]}`
	if err := os.WriteFile(filepath.Join(dir, "planning-77.json"), []byte(planning), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := NewRuntimeState("o/r", 77, "", "")
	rec := NewHistoryWriter(root).BuildV2Record(snap, true, "", V2RunInput{}, time.Now())
	want := []string{"a/new.go", "b/old.go", "c/old.ts"}
	if !reflect.DeepEqual(rec.PlannedFiles, want) {
		t.Fatalf("planned_files = %v, want %v", rec.PlannedFiles, want)
	}

	// No planning context: the field is omitted, not invented.
	other := NewHistoryWriter(root).BuildV2Record(NewRuntimeState("o/r", 78, "", ""), true, "", V2RunInput{}, time.Now())
	if other.PlannedFiles != nil {
		t.Fatalf("planned_files without a planning context = %v", other.PlannedFiles)
	}
}

func TestRuntimeState_RecordBacktrackIsSnapshotted(t *testing.T) {
	rs := NewRuntimeState("o/r", 5, "", "")
	rs.RecordBacktrack("feature-validate", "feature-dev", "TEST_FAILURE", "red", time.Unix(0, 0))
	rs.RecordBacktrack("", "feature-dev", "x", "", time.Now()) // no from stage: ignored
	snap := rs.Snapshot()
	if len(snap.Backtracks) != 1 || snap.Backtracks[0].At != "1970-01-01T00:00:00Z" {
		t.Fatalf("snapshot backtracks = %+v", snap.Backtracks)
	}
	rs.RecordBacktrack("pr-create", "feature-dev", "", "", time.Now())
	if len(snap.Backtracks) != 1 {
		t.Fatal("snapshot shares its backtrack slice with the live state")
	}
}
