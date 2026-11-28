package followup

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/filing"
)

func appendRecord(t *testing.T, runDir, stage, summary string) {
	t.Helper()
	rec := discoveredwork.Record{
		Summary: summary, Evidence: []string{"x"}, SourceStage: stage,
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
	}
	if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func captureLen(t *testing.T, runDir string) int {
	t.Helper()
	c, err := discoveredwork.Load(runDir, 42)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		return 0
	}
	return len(c.Records)
}

func factoryOf(f Filer) FilerFactory {
	return func(context.Context, filing.Gate) (Filer, bool, error) { return f, true, nil }
}

// A run stopped by the review cap (an independent-review producer record) or
// halted mid-pipeline (a feature-dev record) files its records
// and consumes them (#3685), and a second run of the same issue files nothing.
func TestRunMaterialize_UnsuccessfulRunFilesAndIsIdempotent(t *testing.T) {
	for _, stage := range []string{"producer:independent-review", "feature-dev"} {
		t.Run(stage, func(t *testing.T) {
			runDir := t.TempDir()
			appendRecord(t, runDir, stage, "Waiter ignores the configured ceiling")
			fake := newFake()
			res, err := RunMaterialize(context.Background(), runDir, 42, fileGate, factoryOf(fake), false)
			if err != nil {
				t.Fatalf("RunMaterialize: %v", err)
			}
			if len(fake.created) != 1 || countOutcomeIn(res, OutcomeFiled) != 1 {
				t.Fatalf("want one filed issue, got created=%d items=%+v", len(fake.created), res.Items)
			}
			if n := captureLen(t, runDir); n != 0 {
				t.Fatalf("capture not consumed: %d records left", n)
			}
			res, err = RunMaterialize(context.Background(), runDir, 42, fileGate, factoryOf(fake), false)
			if err != nil || len(fake.created) != 1 || res.Reason != EmptyCaptureReason {
				t.Fatalf("second run filed again: err=%v created=%d res=%+v", err, len(fake.created), res)
			}
		})
	}
}

// An empty capture never builds a filer: no forge client for nothing.
func TestRunMaterialize_EmptyCaptureSkipsFactory(t *testing.T) {
	called := false
	res, err := RunMaterialize(context.Background(), t.TempDir(), 42, fileGate,
		func(context.Context, filing.Gate) (Filer, bool, error) { called = true; return nil, false, nil }, false)
	if err != nil || called || res.Reason != EmptyCaptureReason || res.Items == nil {
		t.Fatalf("err=%v called=%v res=%+v", err, called, res)
	}
}

// A filer that cannot be built is logged with the issue number and the
// records stay on disk for the next attempt.
func TestMaterializeOnRunEnd_FailureLogsAndKeepsRecords(t *testing.T) {
	runDir := t.TempDir()
	appendRecord(t, runDir, "feature-validate", "Logger drops the run id")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	MaterializeOnRunEnd(context.Background(), runDir, runDir, 42,
		func(context.Context, filing.Gate) (Filer, bool, error) { return nil, false, errors.New("forge down") })
	if n := captureLen(t, runDir); n != 1 {
		t.Fatalf("records not kept: %d", n)
	}
	if out := buf.String(); !strings.Contains(out, "#42: followup-materialize on unsuccessful run end failed") ||
		!strings.Contains(out, "forge down") {
		t.Fatalf("log missing issue and cause: %q", out)
	}
}

func countOutcomeIn(res *Result, outcome string) int {
	n := 0
	for _, it := range res.Items {
		if it.Outcome == outcome {
			n++
		}
	}
	return n
}
