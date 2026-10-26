package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// readPersistedPhases reads back the on-disk runtime snapshot for issue and
// returns its phase history, the file a run the extension did not spawn reads.
func readPersistedPhases(t *testing.T, root string, issue int) []state.PhaseRecord {
	t.Helper()
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("runtime-%d-*.json", issue)))
	if err != nil || len(matches) != 1 {
		t.Fatalf("runtime snapshots for #%d = %v (%v), want exactly one", issue, matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		PhaseHistory []state.PhaseRecord `json:"phaseHistory"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	return snap.PhaseHistory
}

// A phase event reaches disk while the stage is still running (#586). Before
// this, only stage boundaries persisted, so a CLI-started run read 0/18 in the
// extension until feature-dev ended.
func TestRecordPhaseProgress_PersistsMidStage(t *testing.T) {
	root := layouttest.Repo(t)
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	rs := state.NewRuntimeState("example-org/app", 586, "item", id)
	rs.BeginStage(state.StageFeatureDev)
	if err := persistPipelineState(rs, root); err != nil {
		t.Fatal(err)
	}

	recordPhaseProgress(rs, root, 586, state.StageFeatureDev, "validate-environment", 0, 18, false)
	recordPhaseProgress(rs, root, 586, state.StageFeatureDev, "read-planning-context", 1, 18, true)
	recordPhaseProgress(rs, root, 586, state.StageFeatureDev, "implementation", 8, 18, false)

	var got []state.PhaseRecord
	for _, p := range readPersistedPhases(t, root, 586) {
		if p.Stage == state.StageFeatureDev {
			got = append(got, p)
		}
	}
	if len(got) < 3 {
		t.Fatalf("persisted feature-dev phases = %+v, want the 3 recorded mid-stage", got)
	}
	last := got[len(got)-1]
	if last.Name != "implementation" || last.Index != 8 || last.Total != 18 {
		t.Errorf("latest persisted phase = %+v, want implementation 8/18", last)
	}
}
