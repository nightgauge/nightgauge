package followup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/filing"
)

type created struct{ title, body, typ, priority, size string }

type fakeFiler struct {
	bodies   map[int]string   // issue number -> body (marker search)
	titles   map[string]int   // normalized open title -> number
	comments map[int][]string // issue number -> comments
	created  []created
	calls    int
	next     int
}

func newFake() *fakeFiler {
	return &fakeFiler{bodies: map[int]string{}, titles: map[string]int{}, comments: map[int][]string{}, next: 100}
}

func (f *fakeFiler) FindByMarker(_ context.Context, marker string) (int, string, error) {
	f.calls++
	for n, b := range f.bodies {
		if strings.Contains(b, marker) {
			return n, "", nil
		}
	}
	return 0, "", nil
}

func (f *fakeFiler) FindOpenByTitle(_ context.Context, title string) (int, string, error) {
	f.calls++
	return f.titles[NormalizeTitle(title)], "", nil
}

func (f *fakeFiler) HasComment(_ context.Context, n int, marker string) (bool, error) {
	f.calls++
	for _, c := range f.comments[n] {
		if strings.Contains(c, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeFiler) AddComment(_ context.Context, n int, body string) error {
	f.calls++
	f.comments[n] = append(f.comments[n], body)
	return nil
}

func (f *fakeFiler) CreateIssue(_ context.Context, title, body, typ, pri, size string) (int, string, error) {
	f.calls++
	f.next++
	f.bodies[f.next] = body
	f.titles[NormalizeTitle(title)] = f.next
	f.created = append(f.created, created{title, body, typ, pri, size})
	return f.next, "", nil
}

var fileGate = filing.Gate{Mode: filing.ModeFile, Cap: 10}

func capture(t *testing.T, summaries ...string) *discoveredwork.Capture {
	t.Helper()
	dir := t.TempDir()
	run := "run-1"
	for _, s := range summaries {
		rec := discoveredwork.Record{
			Summary: s, Evidence: []string{"seen at x.go:1"}, SourceStage: "feature-dev",
			IssueNumber: 42, RunID: &run, SuggestedType: "bug", SuggestedPriority: "high",
			SuggestedSize: "S", CandidateTargetFiles: []string{"x.go"},
		}
		if _, _, err := discoveredwork.Append(dir, rec, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := discoveredwork.Load(dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func outcomes(r *Result) []string {
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Outcome)
	}
	return out
}

func TestMaterialize_FilesWithBoardFieldsAndProvenance(t *testing.T) {
	f := newFake()
	res, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %d", len(f.created))
	}
	c := f.created[0]
	if c.typ != "bug" || c.priority != "high" || c.size != "S" {
		t.Fatalf("board fields = %+v", c)
	}
	if !strings.Contains(c.body, "Discovered in #42 (run run-1, stage feature-dev)") ||
		!strings.Contains(c.body, "<!-- discovered-work: id=dw-") {
		t.Fatalf("body missing provenance/marker:\n%s", c.body)
	}
	if !res.Consumed(res.Items[0].ID) || res.Items[0].Outcome != OutcomeFiled {
		t.Fatalf("items = %+v", res.Items)
	}
}

func TestMaterialize_DedupAgainstOpenBacklog_CommentsInsteadOfCreating(t *testing.T) {
	f := newFake()
	f.titles[NormalizeTitle("fix  the THING")] = 7
	res, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 || len(f.comments[7]) != 1 || res.Items[0].Outcome != OutcomeDeduped {
		t.Fatalf("created=%d comments=%v items=%+v", len(f.created), f.comments, res.Items)
	}
	// A second pass does not comment twice.
	if _, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, f); err != nil {
		t.Fatal(err)
	}
	if len(f.comments[7]) != 1 {
		t.Fatalf("comments = %d, want 1", len(f.comments[7]))
	}
}

func TestMaterialize_PerRunCap_RefusesLoudly(t *testing.T) {
	f := newFake()
	gate := filing.Gate{Mode: filing.ModeFile, Cap: 2}
	res, err := Materialize(context.Background(), capture(t, "one", "two", "three"), gate, true, f)
	var ce *filing.CapError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "refused: 1 of 3") {
		t.Fatalf("err = %v", err)
	}
	if len(f.created) != 2 || res.Refused != 1 {
		t.Fatalf("created=%d refused=%d", len(f.created), res.Refused)
	}
	last := res.Items[2]
	if last.Outcome != OutcomeRefused || res.Consumed(last.ID) {
		t.Fatalf("refused record must stay in the capture: %+v", last)
	}
}

func TestMaterialize_KillSwitchOff_FilesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "follow_up_filing:\n  enabled: false\n  mode: file\n"); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	res, err := Materialize(context.Background(), capture(t, "a", "b"), filing.ResolveGate(dir), true, f)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || !res.ReportOnly || !strings.Contains(res.Reason, "kill switch") {
		t.Fatalf("calls=%d res=%+v", f.calls, res)
	}
}

func TestMaterialize_MissingConfig_FailsClosed(t *testing.T) {
	f := newFake()
	res, err := Materialize(context.Background(), capture(t, "a"), filing.ResolveGate(t.TempDir()), true, f)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || !res.ReportOnly || !strings.Contains(res.Reason, "fail closed") {
		t.Fatalf("calls=%d res=%+v", f.calls, res)
	}
}

func TestMaterialize_ReportOnly_EmitsListNoMutations(t *testing.T) {
	f := newFake()
	gate := filing.Gate{Mode: filing.ModeReport, Cap: 10}
	res, err := Materialize(context.Background(), capture(t, "a", "b"), gate, true, f)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || strings.Join(outcomes(res), ",") != "would-file,would-file" {
		t.Fatalf("calls=%d outcomes=%v", f.calls, outcomes(res))
	}
	for _, it := range res.Items {
		if !res.Consumed(it.ID) {
			t.Fatalf("reported record %s not consumed", it.ID)
		}
	}
}

func TestMaterialize_UntrustedAuthor_ReportsOnly(t *testing.T) {
	f := newFake()
	res, err := Materialize(context.Background(), capture(t, "a"), fileGate, false, f)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || !res.ReportOnly || !strings.Contains(res.Reason, "not trusted") {
		t.Fatalf("calls=%d res=%+v", f.calls, res)
	}
	allow := fileGate
	allow.AllowUntrusted = true
	if _, err := Materialize(context.Background(), capture(t, "a"), allow, false, f); err != nil || len(f.created) != 1 {
		t.Fatalf("allow_untrusted_authors did not file: err=%v created=%d", err, len(f.created))
	}
}

func TestMaterialize_Idempotent_SecondRunCreatesNothing(t *testing.T) {
	f := newFake()
	c := capture(t, "alpha", "beta")
	if _, err := Materialize(context.Background(), c, fileGate, true, f); err != nil {
		t.Fatal(err)
	}
	// Make the title dedup unable to help: only the marker can stop a duplicate.
	f.titles = map[string]int{}
	res, err := Materialize(context.Background(), c, fileGate, true, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 2 || strings.Join(outcomes(res), ",") != "already-exists,already-exists" {
		t.Fatalf("created=%d outcomes=%v", len(f.created), outcomes(res))
	}
}

func TestMaterialize_ZeroRecords_SilentNoOp(t *testing.T) {
	f := newFake()
	for _, c := range []*discoveredwork.Capture{nil, {SchemaVersion: "1.0", IssueNumber: 42, Records: []discoveredwork.Record{}}} {
		res, err := Materialize(context.Background(), c, fileGate, true, f)
		if err != nil || len(res.Items) != 0 || f.calls != 0 {
			t.Fatalf("err=%v items=%d calls=%d", err, len(res.Items), f.calls)
		}
	}
}

func TestMaterialize_InvalidRecordKept(t *testing.T) {
	f := newFake()
	c := capture(t, "ok")
	bad := c.Records[0]
	bad.SuggestedType = "epic"
	bad.ID = "dw-000000000000"
	c.Records = append(c.Records, bad)
	res, err := Materialize(context.Background(), c, fileGate, true, f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[1].Outcome != OutcomeInvalid || res.Consumed(bad.ID) || len(f.created) != 1 {
		t.Fatalf("items=%+v created=%d", res.Items, len(f.created))
	}
}

func TestBody_UntrustedTextStaysFenced(t *testing.T) {
	c := capture(t, "x")
	rec := c.Records[0]
	rec.Evidence = []string{"```\n@everyone <img src=x>"}
	body := Body(rec)
	if strings.Contains(body, "```\n@everyone") {
		t.Fatalf("evidence escaped its fence:\n%s", body)
	}
}

func writeFile(dir, body string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filing.ConfigPath(dir), []byte(body), 0o644)
}
