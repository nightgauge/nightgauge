package followup

import (
	"context"
	"strings"
	"testing"
	"time"

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
		"<!-- discovered-work: id=" + it.ID, "- [ ] Resolved: `Fix the thing`", "- [ ] No longer true: `seen at x.go:1`"} {
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
	sub := Subsystem{Parent: 42, Files: []string{"internal/a/b.go"}, Components: []string{"component:cli", "component:sdk"}}
	cases := []struct {
		name  string
		cands []SubsystemCandidate
		want  int
	}{
		{"nothing matches", []SubsystemCandidate{{Number: 5, Body: "other"}}, 0},
		{"one shared label never qualifies", []SubsystemCandidate{{Number: 9, Labels: []string{"Component:CLI"}}}, 0},
		{"two shared labels qualify", []SubsystemCandidate{{Number: 9, Labels: []string{"Component:CLI", "component:sdk"}}}, 9},
		{"file match outranks label match", []SubsystemCandidate{
			{Number: 3, Labels: []string{"component:cli", "component:sdk"}}, {Number: 8, Body: "see internal/a/b.go"}}, 8},
		{"lowest number breaks ties", []SubsystemCandidate{
			{Number: 12, Body: "internal/a/b.go"}, {Number: 11, Body: "internal/a/b.go"}}, 11},
		{"parent only when nothing else", []SubsystemCandidate{
			{Number: 42, Body: "internal/a/b.go"}, {Number: 50, Labels: []string{"component:cli", "component:sdk"}}}, 50},
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

// TestPickSubsystemIssue_Issue3300 pins #3300's real candidates (labels as on
// GitHub; the #3301-#3307 siblings under epic #3197 were open then, so their
// board Status is the open-time one): no record folds into the unrelated old
// #1734 or #1755, which share only component:go-binary and program:opencode
// and mention package-lock.json in prose (#2872).
func TestPickSubsystemIssue_Issue3300(t *testing.T) {
	feat := []string{"component:go-binary", "type:feature", "pipeline:refined", "size:M", "program:opencode"}
	cands := []SubsystemCandidate{
		{Number: 1734, Labels: []string{"component:go-binary", "type:chore", "size:M", "program:opencode"},
			BoardStatus: "Backlog", Body: "Follow-ups: the package-lock.json drift and the stream parser"},
		{Number: 1755, Labels: []string{"component:go-binary", "type:chore", "program:opencode"},
			BoardStatus: "Backlog", Body: "terminalkind follow-ups"},
	}
	for n := 3301; n <= 3306; n++ {
		cands = append(cands, SubsystemCandidate{Number: n, Labels: feat, Parent: 3197, BoardStatus: "Backlog"})
	}
	cands = append(cands, SubsystemCandidate{Number: 3307, Parent: 3197, BoardStatus: "Ready",
		Labels: []string{"component:docs", "type:docs", "pipeline:refined", "program:opencode", "size:S"}})
	sub := Subsystem{Parent: 3300, Epic: 3197, Components: []string{"component:go-binary"},
		Programs: []string{"program:opencode"}, Broad: map[string]bool{"component:go-binary": true}}
	for _, files := range [][]string{{"package-lock.json"}, {"internal/execution/opencode.go"}, nil} {
		sub.Files = files
		got, _ := PickSubsystemIssue(sub, cands)
		if got == 1734 || got == 1755 {
			t.Fatalf("files %v folded into unrelated #%d", files, got)
		}
		if got != 3307 {
			t.Errorf("files %v: picked #%d, want the Ready sibling #3307", files, got)
		}
	}
}

func TestPickSubsystemIssue_Qualification(t *testing.T) {
	broad := Subsystem{Parent: 42, Components: []string{"component:go-binary", "component:cli"},
		Broad: map[string]bool{"component:go-binary": true}}
	if got, _ := PickSubsystemIssue(broad, []SubsystemCandidate{
		{Number: 5, Labels: []string{"component:go-binary", "component:cli"}}}); got != 0 {
		t.Errorf("a broad label counted toward the two shared: #%d", got)
	}
	single := Subsystem{Parent: 42, Components: []string{"component:go-binary"}}
	if got, _ := PickSubsystemIssue(single, []SubsystemCandidate{
		{Number: 5, Labels: []string{"component:go-binary"}}}); got != 0 {
		t.Errorf("a single shared component:go-binary qualified #%d", got)
	}
	files := Subsystem{Parent: 42, Files: []string{"package-lock.json", "a/x.go"}}
	for _, body := range []string{"see b/a/x.go", "a/x.go.bak", "package-lock.json drifted", "wa/x.go"} {
		if got, _ := PickSubsystemIssue(files, []SubsystemCandidate{{Number: 5, Body: body}}); got != 0 {
			t.Errorf("body %q qualified", body)
		}
	}
	for _, body := range []string{"see a/x.go.", "`a/x.go:12`", "(a/x.go)", "a/x.go, b.go"} {
		if got, _ := PickSubsystemIssue(files, []SubsystemCandidate{{Number: 5, Body: body}}); got != 5 {
			t.Errorf("body %q did not qualify", body)
		}
	}
	ranked := Subsystem{Parent: 42, Files: []string{"a/x.go"}}
	if got, _ := PickSubsystemIssue(ranked, []SubsystemCandidate{
		{Number: 3, Body: "a/x.go", BoardStatus: "Backlog"}, {Number: 9, Body: "a/x.go", BoardStatus: "Ready"}}); got != 9 {
		t.Errorf("Ready must beat Backlog: #%d", got)
	}
	if got, _ := PickSubsystemIssue(ranked, []SubsystemCandidate{
		{Number: 9, Body: "a/x.go", BoardStatus: "Ready"}, {Number: 3, Body: "a/x.go", BoardStatus: "Ready"}}); got != 3 {
		t.Errorf("number decides among equals: #%d", got)
	}
}

func TestOutranks(t *testing.T) {
	for _, tc := range []struct {
		pri, board string
		want       bool
	}{{"high", "P2", true}, {"medium", "P2", false}, {"low", "P1", false}, {"critical", "", false}} {
		if got := Outranks(tc.pri, tc.board); got != tc.want {
			t.Errorf("Outranks(%q, %q) = %v", tc.pri, tc.board, got)
		}
	}
}

// A record more urgent than the target's board Priority is filed, not folded.
func TestMaterialize_PriorityGuardFiles(t *testing.T) {
	f := newFake()
	f.open = []SubsystemCandidate{{Number: 60, Body: "x.go", Priority: "P3"}}
	res, err := Materialize(context.Background(), capture(t, "Fix the thing"), fileGate, true, Tracking{}, f)
	if err != nil || res.Items[0].Outcome != OutcomeFiled {
		t.Fatalf("%+v, %v", res.Items, err)
	}
}

// A marker found only on a closed issue is already-exists: nothing is folded
// or filed (#2872).
func TestMaterialize_MarkerOnClosedIssue(t *testing.T) {
	c := capture(t, "Fix the thing")
	f := newFake()
	f.bodies[70] = "done\n" + Marker(c.Records[0])
	f.closed = map[int]bool{70: true}
	f.open = []SubsystemCandidate{{Number: 60, Body: "x.go"}}
	res, err := Materialize(context.Background(), c, fileGate, true, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	it := res.Items[0]
	if it.Outcome != OutcomeAlreadyExists || it.IssueNumber != 70 || it.Detail != "marker on closed #70" ||
		len(f.created) != 0 || strings.Contains(f.bodies[60], "## Part") {
		t.Fatalf("item=%+v created=%d", it, len(f.created))
	}
}

// One parent body holding a feature-dev record's and a review record's markers
// leaves both tracked-by-parent (#2872).
func TestMaterialize_MarkersInParentBodyAreTrackedByParent(t *testing.T) {
	dir := t.TempDir()
	run := "run-1"
	for i, stage := range []string{"feature-dev", reviewProducer} {
		file := []string{"x.go", "y.go"}[i]
		if _, _, err := discoveredwork.Append(dir, discoveredwork.Record{
			Summary: "gap from " + stage, Evidence: []string{file + ":1"}, SourceStage: stage, IssueNumber: 42,
			RunID: &run, SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "S",
			CandidateTargetFiles: []string{file},
		}, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := discoveredwork.Load(dir, 42)
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	f.bodies[42] = "parent\n" + Marker(c.Records[0]) + "\n" + Marker(c.Records[1])
	f.open = []SubsystemCandidate{{Number: 60, Body: "x.go"}}
	res, err := Materialize(context.Background(), c, fileGate, true, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(outcomes(res), ","); got != "tracked-by-parent,tracked-by-parent" {
		t.Fatalf("outcomes = %s", got)
	}
}

// Any producer's record restating a parent criterion is tracked-by-parent: an
// unchecked criterion in the parent's body, or an unmet one in pr-<N>.json
// (the #3323 shape, a feature-dev record).
func TestMaterialize_TrackedByParentForEveryProducer(t *testing.T) {
	crit := "The stream parser bounds provider errors to one line"
	for name, tc := range map[string]struct {
		stage    string
		track    Tracking
		criteria []string
	}{
		"review record, parent body": {reviewProducer, Tracking{}, []string{crit}},
		"feature-dev, unmet in pr":   {"feature-dev", Tracking{RunID: "run-1", Unmet: map[string]bool{NormalizeTitle(crit): true}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			run := "run-1"
			if _, _, err := discoveredwork.Append(dir, discoveredwork.Record{
				Summary: "plan-compliance: " + crit + ".", Evidence: []string{"x.go:1"}, SourceStage: tc.stage,
				IssueNumber: 42, RunID: &run, SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "S",
			}, time.Unix(0, 0)); err != nil {
				t.Fatal(err)
			}
			c, err := discoveredwork.Load(dir, 42)
			if err != nil {
				t.Fatal(err)
			}
			f := newFake()
			f.criteria = map[int][]string{42: tc.criteria}
			res, err := Materialize(context.Background(), c, fileGate, true, tc.track, f)
			if err != nil || res.Items[0].Outcome != OutcomeTrackedByParent || len(f.created) != 0 {
				t.Fatalf("%+v, %v", res.Items, err)
			}
		})
	}
}

func TestUncheckedCriteria(t *testing.T) {
	got := UncheckedCriteria("- [ ] one\n- [x] done\n  * [ ] two  \ntext")
	if strings.Join(got, "|") != "one|two" {
		t.Fatalf("%q", got)
	}
}
