package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
)

// teeReporter fans one runner's phase calls to both reporters, so the two
// records being compared come from ONE run rather than from two runs that could
// have diverged for unrelated reasons.
type teeReporter struct{ a, b pmstages.PhaseReporter }

func (t teeReporter) PhaseStart(stage, name string, index, total int) {
	t.a.PhaseStart(stage, name, index, total)
	t.b.PhaseStart(stage, name, index, total)
}
func (t teeReporter) PhaseComplete(stage, name string) {
	t.a.PhaseComplete(stage, name)
	t.b.PhaseComplete(stage, name)
}
func (t teeReporter) PhaseFail(stage, name string, index, total int) {
	t.a.PhaseFail(stage, name, index, total)
	t.b.PhaseFail(stage, name, index, total)
}
func (t teeReporter) PhaseSkip(stage, name string, index, total int) {
	t.a.PhaseSkip(stage, name, index, total)
	t.b.PhaseSkip(stage, name, index, total)
}

// replayInto feeds recorded transitions into a RuntimeState through the SAME
// methods deterministicPhaseReporter calls. It is a dispatch on the status
// field, not a second implementation of the folding rules: the start dedupe,
// the amend-the-running-record walk and the skip idempotence all live in
// RuntimeState and are exercised here rather than restated.
//
// This stands in for what HeadlessOrchestrator does with the `phases` array.
func replayInto(rt *state.RuntimeState, ts []PhaseTransition) {
	for _, t := range ts {
		stage := state.PipelineStage(t.Stage)
		switch t.Status {
		case "running":
			rt.BeginPhase(stage, t.Name, t.Index, t.Total)
		case "complete":
			rt.CompletePhase(stage, t.Name)
		case "failed":
			rt.FailPhase(stage, t.Name, t.Index, t.Total)
		case "skipped":
			rt.SkipPhase(stage, t.Name, t.Index, t.Total)
		}
	}
}

// phaseShape is a PhaseRecord without its timestamps — the only fields two
// sinks cannot be expected to agree on to the nanosecond.
type phaseShape struct {
	Stage, Name, Status string
	Index, Total        int
}

func shapes(recs []state.PhaseRecord) []phaseShape {
	out := make([]phaseShape, 0, len(recs))
	for _, r := range recs {
		out = append(out, phaseShape{string(r.Stage), r.Name, r.Status, r.Index, r.Total})
	}
	return out
}

func assertSameRecord(t *testing.T, gotRT, wantRT *state.RuntimeState) {
	t.Helper()
	got, want := shapes(gotRT.PhaseHistory), shapes(wantRT.PhaseHistory)
	if len(want) == 0 {
		t.Fatal("the scheduler path recorded no phases — this would prove nothing about parity")
	}
	if len(got) != len(want) {
		t.Fatalf("phase count differs — CLI replay %d, scheduler %d\n  CLI:       %+v\n  scheduler: %+v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("phase %d differs:\n  CLI replay: %+v\n  scheduler:  %+v", i, got[i], want[i])
		}
	}
}

// schedulerSink returns the reporter the Go scheduler uses, writing to rt. The
// Scheduler is bare: its live callbacks are nil, and the DURABLE record is what
// parity is about.
func schedulerSink(rt *state.RuntimeState) pmstages.PhaseReporter {
	return &deterministicPhaseReporter{sched: &Scheduler{}, rt: rt, repo: "owner/repo", issue: 300}
}

// TestPhaseRecorderMatchesTheSchedulerReporter is #1397 AC3, and it also
// discharges AC4 (the punt case) because the scenario it drives IS a punt.
//
// EQUIVALENCE, NOT LIVENESS. It is not enough that each path produces some
// phases. #1247 instrumented the scheduler's route and left the extension's
// `nightgauge pr-stage --json` route — a separate process with no RuntimeState
// and no live callbacks — reporting nothing, so an operator saw the fixed
// behaviour or the broken one depending on which route the run took, with
// nothing on screen explaining the difference.
//
// The runner is the real one on its hermetic punt path: with no pr-{N}.json in
// the workdir it fails at read-pr-context before any GitHub call, which is
// exactly the shape AC4 names — no skips written, and the phase the runner was
// inside recorded `failed`.
func TestPhaseRecorderMatchesTheSchedulerReporter(t *testing.T) {
	direct := &state.RuntimeState{}
	rec := NewPhaseRecorder()

	runner := pmstages.NewDeterministicRunner()
	res, err := runner.Run(
		pmstages.WithPhaseReporter(context.Background(), teeReporter{a: schedulerSink(direct), b: rec}),
		300, "owner/repo", t.TempDir())
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if res.Path != pmstages.PathPunt {
		t.Fatalf("setup: Path=%q, want a punt (no pr-{N}.json in the workdir)", res.Path)
	}

	replayed := &state.RuntimeState{}
	replayInto(replayed, rec.Transitions())
	assertSameRecord(t, replayed, direct)

	// AC4, stated as its own assertion rather than left implicit in the
	// comparison: a punt writes no skips, and the phase the runner was inside
	// is `failed` — so the skill's own markers continue without contradiction.
	for _, p := range shapes(replayed.PhaseHistory) {
		if p.Status == "skipped" {
			t.Errorf("punt recorded a skip for %q — a punt hands the stage to the skill, "+
				"so claiming a phase was deliberately skipped contradicts it", p.Name)
		}
	}
	if got := shapes(replayed.PhaseHistory); got[len(got)-1].Status != "failed" {
		t.Errorf("the phase the runner punted inside is %q, want failed: %+v",
			got[len(got)-1].Status, got)
	}
}

// TestPhaseRecorderMatchesAcrossTheWholeStatusVocabulary covers the statuses a
// punt cannot reach. The runner's success path needs a fake forge client, and
// that interface is package-private to `stages`, so the transitions are driven
// through the reporter interface directly here — which is the exact surface the
// runner uses, in the order prmerge_phases_test.go proves it uses.
func TestPhaseRecorderMatchesAcrossTheWholeStatusVocabulary(t *testing.T) {
	direct := &state.RuntimeState{}
	rec := NewPhaseRecorder()
	tee := teeReporter{a: schedulerSink(direct), b: rec}

	idx := func(n string) (int, int) { return pmstages.PhasePosition("pr-merge", n) }

	i, n := idx("read-pr-context")
	tee.PhaseStart("pr-merge", "read-pr-context", i, n)
	tee.PhaseComplete("pr-merge", "read-pr-context")

	i, n = idx("ci-gate")
	tee.PhaseStart("pr-merge", "ci-gate", i, n)
	tee.PhaseComplete("pr-merge", "ci-gate")

	i, n = idx("merge")
	tee.PhaseStart("pr-merge", "merge", i, n)
	tee.PhaseFail("pr-merge", "merge", i, n)

	i, n = idx("output-summary")
	tee.PhaseSkip("pr-merge", "output-summary", i, n)

	replayed := &state.RuntimeState{}
	replayInto(replayed, rec.Transitions())
	assertSameRecord(t, replayed, direct)

	// The vocabulary really was exercised — otherwise this test could pass by
	// comparing two empty records.
	seen := map[string]bool{}
	for _, p := range shapes(replayed.PhaseHistory) {
		seen[p.Status] = true
	}
	for _, want := range []string{"complete", "failed", "skipped"} {
		if !seen[want] {
			t.Errorf("status %q never reached the record; got %+v", want, shapes(replayed.PhaseHistory))
		}
	}
}

// TestStreamingPhaseRecorderEmitsAsItGoes pins the half the accumulated array
// cannot provide.
//
// The deterministic pr-merge waits out in-flight CI on a 30s x 30 budget, so a
// consumer that only learns the phases when the process EXITS sits at 0/14 for
// up to fifteen minutes and then jumps to the end — the reported symptom, not a
// fix for it. Each transition must be on the wire when it is reported.
func TestStreamingPhaseRecorderEmitsAsItGoes(t *testing.T) {
	var buf bytes.Buffer
	rec := NewStreamingPhaseRecorder(&buf)

	idx, total := pmstages.PhasePosition("pr-merge", "ci-gate")
	rec.PhaseStart("pr-merge", "ci-gate", idx, total)

	// Read the stream BEFORE the run is over — that is the whole point.
	first := strings.TrimSpace(buf.String())
	if first == "" {
		t.Fatal("nothing was written when the phase started — the caller cannot advance its count " +
			"until the process exits, which is the defect (#1397)")
	}
	if !strings.HasPrefix(first, PhaseStreamPrefix) {
		t.Fatalf("line %q is not sentinel-prefixed — a consumer cannot tell it from ordinary log output", first)
	}

	var got PhaseTransition
	if err := json.Unmarshal([]byte(strings.TrimPrefix(first, PhaseStreamPrefix)), &got); err != nil {
		t.Fatalf("streamed line is not JSON: %v (%q)", err, first)
	}
	want := PhaseTransition{Stage: "pr-merge", Name: "ci-gate", Index: idx, Total: total, Status: "running"}
	if got != want {
		t.Errorf("streamed %+v, want %+v", got, want)
	}

	rec.PhaseComplete("pr-merge", "ci-gate")
	if n := len(strings.Split(strings.TrimSpace(buf.String()), "\n")); n != 2 {
		t.Errorf("stream carries %d lines after two transitions, want 2", n)
	}

	// The stream is an ADDITION to the array, never a replacement: the array
	// stays the durable authority the result is built from.
	if len(rec.Transitions()) != 2 {
		t.Errorf("streaming recorder accumulated %d transitions, want 2", len(rec.Transitions()))
	}
}

// TestStreamingPhaseRecorderSurvivesABrokenPipe: a closed progress channel must
// never fail the merge that was reporting progress.
func TestStreamingPhaseRecorderSurvivesABrokenPipe(t *testing.T) {
	rec := NewStreamingPhaseRecorder(failingWriter{})
	rec.PhaseStart("pr-merge", "merge", 9, 14)
	rec.PhaseComplete("pr-merge", "merge")
	if len(rec.Transitions()) != 2 {
		t.Errorf("a failing stream lost transitions from the array: %+v", rec.Transitions())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// TestPhaseStreamPrefixParityWithTypeScript pins the sentinel across the two
// languages that have to agree on it.
//
// The prefix is a literal in Go (PhaseStreamPrefix) and a literal in TypeScript
// (PHASE_STREAM_PREFIX in prStagePhaseStream.ts). That is the dual-path-drift
// shape this repo keeps getting bitten by, and its failure mode here is
// SILENT: a changed prefix does not error — the reader simply stops recognising
// phase lines, logs them as ordinary output, and the tree goes back to 0/14,
// which is the exact bug #1397 fixes. Nothing else would fail.
//
// Same direction as TestPhaseRegistryParityWithTypeScript: Go reads the TS
// source and compares.
func TestPhaseStreamPrefixParityWithTypeScript(t *testing.T) {
	root := phaseRecorderRepoRoot(t)
	path := filepath.Join(root, "packages", "nightgauge-vscode", "src", "services", "prStagePhaseStream.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prStagePhaseStream.ts: %v", err)
	}

	m := regexp.MustCompile(`PHASE_STREAM_PREFIX\s*=\s*"([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("no PHASE_STREAM_PREFIX literal found in %s — if it was renamed, this guard is "+
			"no longer pinning anything", path)
	}
	if got := string(m[1]); got != PhaseStreamPrefix {
		t.Errorf("phase stream prefix has drifted:\n  Go:         %q\n  TypeScript: %q\n"+
			"The reader would silently stop recognising phase lines and the tree would return to 0/14.",
			PhaseStreamPrefix, got)
	}
}

func phaseRecorderRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
