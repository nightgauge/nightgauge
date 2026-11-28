package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// runEndFiler is a Filer that finds nothing and files every record.
type runEndFiler struct{ created int }

func (f *runEndFiler) FindByMarker(context.Context, string) (followup.Found, error) {
	return followup.Found{}, nil
}
func (f *runEndFiler) ParentBody(context.Context, int) (string, error) { return "", nil }
func (f *runEndFiler) FindOpenByTitle(context.Context, string) (int, string, error) {
	return 0, "", nil
}
func (f *runEndFiler) HasComment(context.Context, int, string) (bool, error) { return false, nil }
func (f *runEndFiler) AddComment(context.Context, int, string) error         { return nil }
func (f *runEndFiler) CreateIssue(context.Context, string, string, string, string, string) (int, string, error) {
	f.created++
	return 1000 + f.created, "", nil
}
func (f *runEndFiler) FindOpenInSubsystem(context.Context, int, []string, string) (int, string, error) {
	return 0, "", nil
}
func (f *runEndFiler) AppendPart(context.Context, int, discoveredwork.Record) (string, error) {
	return "", nil
}

func runEndWorkspace(t *testing.T, stage string) (string, string) {
	t.Helper()
	ws := t.TempDir()
	gittest.InitRepo(t, ws, "-q")
	dir, err := layout.PipelineStateDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := mkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	rec := discoveredwork.Record{Summary: "Waiter ignores the ceiling", Evidence: []string{"e"}, SourceStage: stage,
		IssueNumber: 9, SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "XS"}
	if _, _, err := discoveredwork.Append(dir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	return ws, dir
}

// A review-capped or halted run's capture is decided and consumed at run end
// (#3685), so doctor's NGD050 scan finds no orphan for the issue.
func TestMaterializeOnRunEnd_ConsumesUnsuccessfulRunCapture(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"producer:independent-review", "feature-dev"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ws, dir := runEndWorkspace(t, stage)
			calls := 0
			s := &Scheduler{runEndFilerFactory: func(workspace, repo string, issue int) followup.FilerFactory {
				return func(context.Context, filing.Gate) (followup.Filer, bool, error) {
					calls++
					return &runEndFiler{}, true, nil
				}
			}}
			s.materializeOnRunEnd(ws, "o/r", 9)
			if calls != 1 {
				t.Fatalf("factory calls = %d, want 1", calls)
			}
			if followupCaptureHasRecords(ws, 9) {
				t.Fatal("capture not consumed")
			}
			orphans, err := discoveredwork.Orphans(dir, time.Now().Add(time.Hour), 0)
			if err != nil || len(orphans) != 0 {
				t.Fatalf("orphans = %+v, err = %v", orphans, err)
			}
		})
	}
}

// A failing materialize leaves the records and cannot panic or change state.
func TestMaterializeOnRunEnd_FailureKeepsRecords(t *testing.T) {
	t.Parallel()
	ws, _ := runEndWorkspace(t, "feature-validate")
	s := &Scheduler{runEndFilerFactory: func(string, string, int) followup.FilerFactory {
		return func(context.Context, filing.Gate) (followup.Filer, bool, error) {
			return nil, false, errors.New("forge down")
		}
	}}
	s.materializeOnRunEnd(ws, "o/r", 9)
	if !followupCaptureHasRecords(ws, 9) {
		t.Fatal("records lost on a failed materialize")
	}
}

// runEndFailingRunner fails one stage and delegates the rest.
type runEndFailingRunner struct {
	inner *partialRerunRunner
	fail  state.PipelineStage
}

func (r *runEndFailingRunner) RunStage(ctx context.Context, p StageRunParams) (*StageRunResult, error) {
	res, err := r.inner.RunStage(ctx, p)
	if p.Stage == r.fail {
		return &StageRunResult{ExitCode: 1}, nil
	}
	return res, err
}

// A run that fails at feature-validate with discovered work on disk files it
// through the terminal defer (#3685): the real runPipeline, not a direct call.
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestRunPipeline_FailedRunMaterializesCapture(t *testing.T) {
	inner := &partialRerunRunner{}
	runner := &runEndFailingRunner{inner: inner, fail: state.StageFeatureValidate}
	s, root := partialRerunScheduler(t, runner, false)
	inner.onStage = func(p StageRunParams) {
		if p.Stage != state.StageFeatureDev {
			return
		}
		dir, err := layout.PipelineStateDir(root)
		if err != nil {
			t.Error(err)
			return
		}
		rec := discoveredwork.Record{Summary: "Waiter ignores the ceiling", Evidence: []string{"e"},
			SourceStage: "feature-dev", IssueNumber: p.IssueNumber, SuggestedType: "bug",
			SuggestedPriority: "low", SuggestedSize: "XS"}
		if _, _, err := discoveredwork.Append(dir, rec, time.Now()); err != nil {
			t.Error(err)
		}
	}
	calls := 0
	s.runEndFilerFactory = func(string, string, int) followup.FilerFactory {
		return func(context.Context, filing.Gate) (followup.Filer, bool, error) {
			calls++
			return &runEndFiler{}, true, nil
		}
	}

	ok, _ := s.runPipeline(context.Background(), types.BoardItem{Number: 3685, Repo: "acme/platform", ID: "item-3685", NodeID: "I_3685"})

	if ok {
		t.Fatal("run reported success")
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
	if followupCaptureHasRecords(root, 3685) {
		t.Fatal("failed run's capture not consumed")
	}
}
