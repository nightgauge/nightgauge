package followup

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
)

// #3278: a follow-up whose subsystem has an open issue is appended to it as a
// new part, with its own criteria and a back-link, instead of being filed; a
// re-run finds the marker; and with no match it is filed.
func TestMaterialize_FoldsIntoOpenSubsystemIssue(t *testing.T) {
	f := newFake()
	f.bodies[60] = "## Summary\n\nWork on x.go\n"
	f.open = []SubsystemCandidate{{Number: 60, Body: "Work on x.go"}}
	res, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	it := res.Items[0]
	if len(f.created) != 0 || it.Outcome != OutcomeFolded || it.IssueNumber != 60 || it.Detail != "appended as Part B of #60" {
		t.Fatalf("created=%d item=%+v, want folded into #60 as Part B", len(f.created), it)
	}
	if !res.Consumed(it.ID) {
		t.Error("a folded record must leave the capture file")
	}
	body := f.bodies[60]
	for _, want := range []string{"## Part B: follow-up discovered in #42", "Discovered in #42 (run run-1, stage feature-dev)",
		"<!-- discovered-work: id=" + it.ID, "- [ ] The follow-up summarized in Part B"} {
		if !strings.Contains(body, want) {
			t.Errorf("folded body missing %q:\n%s", want, body)
		}
	}

	again, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Items[0]; got.Outcome != OutcomeAlreadyExists || got.IssueNumber != 60 {
		t.Fatalf("re-run item = %+v, want already-exists on #60", got)
	}
	if strings.Count(f.bodies[60], "## Part B") != 1 {
		t.Fatal("re-run appended the part twice")
	}
}

func TestMaterialize_FilesWhenNoSubsystemIssueMatches(t *testing.T) {
	f := newFake()
	f.open = []SubsystemCandidate{{Number: 60, Body: "unrelated"}}
	res, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Outcome != OutcomeFiled || len(f.created) != 1 {
		t.Fatalf("items=%+v created=%d, want filed", res.Items, len(f.created))
	}
}

// Folding creates no issue, so the per-run cap does not count it.
func TestMaterialize_FoldingIsNotCapped(t *testing.T) {
	f := newFake()
	f.open = []SubsystemCandidate{{Number: 60, Body: "x.go"}}
	gate := fileGate
	gate.Cap = 0
	res, err := Materialize(context.Background(), capture(t, "One", "Two"), gate, true, Tracking{}, f)
	if err != nil {
		t.Fatalf("folding hit the cap: %v", err)
	}
	if got := outcomes(res); strings.Join(got, ",") != "folded,folded" {
		t.Fatalf("outcomes = %v", got)
	}
	if strings.Count(f.bodies[60], "## Part ") != 2 || !strings.Contains(f.bodies[60], "## Part C") {
		t.Fatalf("want Parts B and C:\n%s", f.bodies[60])
	}
}

func TestPickSubsystemIssue(t *testing.T) {
	sub := Subsystem{Parent: 42, Files: []string{"internal/a/b.go"}, Components: []string{"component:cli"}}
	cases := []struct {
		name  string
		cands []SubsystemCandidate
		want  int
	}{
		{"nothing matches", []SubsystemCandidate{{Number: 5, Body: "other"}}, 0},
		{"label match", []SubsystemCandidate{{Number: 9, Labels: []string{"Component:CLI"}}}, 9},
		{"file match outranks label match", []SubsystemCandidate{
			{Number: 3, Labels: []string{"component:cli"}}, {Number: 8, Body: "see internal/a/b.go"}}, 8},
		{"lowest number breaks ties", []SubsystemCandidate{
			{Number: 12, Body: "internal/a/b.go"}, {Number: 11, Body: "internal/a/b.go"}}, 11},
		{"parent only when nothing else", []SubsystemCandidate{
			{Number: 42, Body: "internal/a/b.go"}, {Number: 50, Labels: []string{"component:cli"}}}, 50},
		{"parent alone", []SubsystemCandidate{{Number: 42, Body: "internal/a/b.go"}}, 42},
		{"epic excluded", []SubsystemCandidate{{Number: 7, Body: "internal/a/b.go", Labels: []string{"type:epic"}}}, 0},
		{"owner-action excluded", []SubsystemCandidate{{Number: 7, Body: "internal/a/b.go", Labels: []string{"owner-action"}}}, 0},
		{"in progress excluded", []SubsystemCandidate{{Number: 7, Body: "internal/a/b.go", BoardStatus: "In progress"}}, 0},
		{"in review excluded", []SubsystemCandidate{{Number: 7, Body: "internal/a/b.go", BoardStatus: "In review"}}, 0},
	}
	for _, tc := range cases {
		if got, _ := PickSubsystemIssue(sub, tc.cands); got != tc.want {
			t.Errorf("%s: picked #%d, want #%d", tc.name, got, tc.want)
		}
	}
}

func TestAppendPartToBody_LabelsAndIdempotency(t *testing.T) {
	run := "r"
	rec := discoveredwork.Record{ID: "dw-1", IssueNumber: 4, RunID: &run, SourceStage: "feature-dev",
		Summary: "s @someone `rm -rf`", Evidence: []string{"e"}}
	body, label, changed := AppendPartToBody("intro\n\n## Part C: earlier\n", rec)
	if !changed || label != "D" || !strings.Contains(body, "## Part D: follow-up discovered in #4") {
		t.Fatalf("label=%q changed=%v body=\n%s", label, changed, body)
	}
	// Record text stays inside the untrusted fence, backticks neutralized.
	if strings.Contains(body, "`rm -rf`") {
		t.Error("record text escaped its fence")
	}
	if _, _, again := AppendPartToBody(body, rec); again {
		t.Error("appending the same record twice changed the body")
	}
	if got := nextPartLabel("## Part Z: last"); got != "27" {
		t.Errorf("after Z = %q", got)
	}
}
