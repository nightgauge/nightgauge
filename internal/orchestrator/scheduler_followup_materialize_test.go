package orchestrator

import (
	"os"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestStageList_NonSpikeIssueIncludesFollowupMaterialize(t *testing.T) {
	got := appendPostMergeStages(types.BoardItem{Number: 1, Labels: []string{"type:feature"}}, fullStageOrder())
	want := append(fullStageOrder(), state.StageFollowupMaterialize)
	if !equalStages(got, want) {
		t.Fatalf("feature stages = %v, want %v", stageNames(got), stageNames(want))
	}
	spike := appendPostMergeStages(types.BoardItem{Number: 2, Labels: []string{"type:spike"}}, fullStageOrder())
	wantSpike := append(fullStageOrder(), state.StageSpikeMaterialize, state.StageFollowupMaterialize)
	if !equalStages(spike, wantSpike) {
		t.Fatalf("spike stages = %v, want %v", stageNames(spike), stageNames(wantSpike))
	}
	if p, ok := stagePrerequisites[state.StageFollowupMaterialize]; !ok || p.ContextType != "pr" {
		t.Fatalf("followup-materialize prerequisite = %+v", p)
	}
}

func TestFollowupCaptureHasRecords_SkipsEmptyCapture(t *testing.T) {
	ws := t.TempDir()
	gittest.InitRepo(t, ws, "-q")
	if followupCaptureHasRecords(ws, 9) {
		t.Fatal("absent capture reported records")
	}
	dir, err := layout.PipelineStateDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	rec := discoveredwork.Record{Summary: "s", Evidence: []string{"e"}, SourceStage: "feature-dev",
		IssueNumber: 9, SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "XS"}
	if err := mkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := discoveredwork.Append(dir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !followupCaptureHasRecords(ws, 9) {
		t.Fatal("capture with a record reported empty")
	}
}

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

type capturedEmit struct {
	kind    trace.Kind
	stage   string
	payload any
}

type captureEmitter struct{ got []capturedEmit }

func (c *captureEmitter) Emit(kind trace.Kind, stage string, payload any) {
	c.got = append(c.got, capturedEmit{kind, stage, payload})
}

// The empty-capture skip leaves a trace (#2638); it used to leave nothing.
func TestRecordFollowupSkip_EmitsStageSkipTrace(t *testing.T) {
	var em captureEmitter
	recordFollowupSkip(&em, 9)
	if len(em.got) != 1 {
		t.Fatalf("emits = %d, want 1", len(em.got))
	}
	e := em.got[0]
	p, ok := e.payload.(trace.StageSkipPayload)
	if e.kind != trace.KindStageSkip || e.stage != string(state.StageFollowupMaterialize) || !ok ||
		p.Source != "followup-capture" || p.Reason != followupEmptyCaptureReason {
		t.Fatalf("emit = %+v", e)
	}
}
