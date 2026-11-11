package followup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/filing"
)

const manifest = "internal/adaptercompat/manifests/opencode.json"

// criterion4 is #3186's stand-in for the parent's own criterion that #3184
// restated.
const criterion4 = "`opencode.json` records the newest tested version."

type spec struct {
	stage, summary, evidence, file, criterion string
	run                                       *string
}

func strp(s string) *string { return &s }

// mixed writes specs to a capture through the real append path.
func mixed(t *testing.T, specs ...spec) *discoveredwork.Capture {
	t.Helper()
	dir := t.TempDir()
	for _, s := range specs {
		rec := discoveredwork.Record{
			Summary: s.summary, Evidence: []string{s.evidence}, SourceStage: s.stage,
			IssueNumber: 42, RunID: s.run, SuggestedType: "bug", SuggestedPriority: "medium",
			SuggestedSize: "S", CandidateTargetFiles: []string{s.file}, Criterion: s.criterion,
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

// review3184 is #3184's shape: a plan-compliance major naming the parent's
// criterion on the manifest.
func review3184(run *string) spec {
	return spec{
		stage:     reviewProducer,
		summary:   "plan-compliance: Criterion 4: max_tested in opencode.json is not the newest tested version",
		evidence:  manifest + ":5: Criterion 4: max_tested in opencode.json is not the newest tested version",
		file:      manifest,
		criterion: criterion4,
		run:       run,
	}
}

// devRecord3183 is #3183's shape: feature-dev's record of the same gap.
func devRecord3183(run *string) spec {
	return spec{
		stage:    "feature-dev",
		summary:  "Raise max_tested for OpenCode to the version the matrix ran",
		evidence: manifest + " still pins max_tested below the newest tested release",
		file:     manifest,
		run:      run,
	}
}

func unmet(run string, criteria ...string) Tracking {
	t := Tracking{RunID: run, Unmet: map[string]bool{}}
	for _, c := range criteria {
		t.Unmet[NormalizeTitle(c)] = true
	}
	return t
}

func byStage(r *Result) map[string]Item {
	out := map[string]Item{}
	for _, it := range r.Items {
		out[it.SourceStage] = it
	}
	return out
}

// TestReviewRestatingTheParentsUnmetCriterionIsNotFiled is AC1 with #3184's
// shape: the parent's PR holds the criterion unmet, so the review record files
// nothing and is reported and consumed as tracked by the parent.
func TestReviewRestatingTheParentsUnmetCriterionIsNotFiled(t *testing.T) {
	f := newFake()
	c := mixed(t, review3184(strp("run-1")))
	// The PR text differs in spacing and case only.
	track := unmet("run-1", "  `OPENCODE.json`   records the newest tested version. ")
	res, err := Materialize(context.Background(), c, fileGate, true, track, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 || f.calls != 0 {
		t.Fatalf("filed or looked up: %+v (%d calls)", f.created, f.calls)
	}
	it := res.Items[0]
	if it.Outcome != OutcomeTrackedByParent || it.Detail != "criterion unmet on #42; the PR says Refs" {
		t.Fatalf("%+v", it)
	}
	if !res.Consumed(it.ID) {
		t.Fatal("a tracked record must leave the capture")
	}
}

// TestTrackedByParentIsReportedInReportMode: report mode shows the decision
// instead of a would-file.
func TestTrackedByParentIsReportedInReportMode(t *testing.T) {
	res, err := Materialize(context.Background(), mixed(t, review3184(nil)),
		filing.Gate{Mode: filing.ModeReport}, true, unmet("", criterion4), nil)
	if err != nil || !reflect.DeepEqual(outcomes(res), []string{OutcomeTrackedByParent}) {
		t.Fatalf("%v, %v", outcomes(res), err)
	}
}

// TestReviewRecordsTheParentDoesNotTrackAreFiled: AC2 and every way the
// parent may not track the criterion.
func TestReviewRecordsTheParentDoesNotTrackAreFiled(t *testing.T) {
	noCriterion := review3184(strp("run-1"))
	noCriterion.criterion = ""
	noCriterion.summary = "plan-compliance: the change also rewrites the installer, which no criterion asks for"
	noCriterion.evidence = "install.sh:1: unrequested installer rewrite"
	noCriterion.file = "install.sh"
	for name, tc := range map[string]struct {
		rec   spec
		track Tracking
	}{
		"outside the criteria": {noCriterion, unmet("run-1", criterion4)},
		"criterion met":        {review3184(strp("run-1")), unmet("run-1", "Some other criterion.")},
		"another run":          {review3184(strp("run-0")), unmet("run-1", criterion4)},
		"run only on one side": {review3184(nil), unmet("run-1", criterion4)},
		"no pr context":        {review3184(strp("run-1")), Tracking{}},
		"not the review's own": {spec{"feature-dev", "Criterion 4 gap", manifest + ":5", manifest, criterion4, strp("run-1")}, unmet("run-1", criterion4)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			res, err := Materialize(context.Background(), mixed(t, tc.rec), fileGate, true, tc.track, f)
			if err != nil || len(f.created) != 1 || res.Items[0].Outcome != OutcomeFiled {
				t.Fatalf("%v, %v, created %d", outcomes(res), err, len(f.created))
			}
		})
	}
}

// TestReviewRestatingAnotherProducersRecordIsADuplicate is AC3 with #3183 and
// #3184's pair: one issue is filed, and the review record is reported as a
// duplicate of feature-dev's. The pair shares the manifest and max_tested.
func TestReviewRestatingAnotherProducersRecordIsADuplicate(t *testing.T) {
	for name, run := range map[string]*string{"same run": strp("run-1"), "no run on either": nil} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			c := mixed(t, devRecord3183(run), review3184(run))
			res, err := Materialize(context.Background(), c, fileGate, true, Tracking{}, f)
			if err != nil || len(f.created) != 1 {
				t.Fatalf("%v, %v, created %d", outcomes(res), err, len(f.created))
			}
			got := byStage(res)
			dev, rev := got["feature-dev"], got[reviewProducer]
			if dev.Outcome != OutcomeFiled || rev.Outcome != OutcomeDuplicate || rev.Detail != "duplicates "+dev.ID {
				t.Fatalf("dev %+v, review %+v", dev, rev)
			}
			if !res.Consumed(rev.ID) {
				t.Fatal("a duplicate must leave the capture")
			}
		})
	}
}

// TestDuplicateNeedsASharedCodeIdentifier: the same file alone, or prose
// tokens such as e.g., i.e., 404 or 2nd, never make a duplicate.
func TestDuplicateNeedsASharedCodeIdentifier(t *testing.T) {
	run := strp("run-1")
	dev := spec{"feature-dev", "Handle the 404 path, e.g. a missing manifest, on the 2nd retry",
		"see opencode.json, i.e. the manifest", manifest, "", run}
	security := spec{reviewProducer, "security: the loader trusts the path, e.g. a symlink, on the 2nd read",
		manifest + " returns 404 bodies verbatim, i.e. unescaped", manifest, "", run}
	f := newFake()
	res, err := Materialize(context.Background(), mixed(t, dev, security), fileGate, true, Tracking{}, f)
	if err != nil || len(f.created) != 2 {
		t.Fatalf("%v, %v, created %d", outcomes(res), err, len(f.created))
	}
}

// TestDuplicateNeedsAnotherProducerTheSameRunAndFile.
func TestDuplicateNeedsAnotherProducerTheSameRunAndFile(t *testing.T) {
	sameProducer := devRecord3183(strp("run-1"))
	sameProducer.stage = reviewProducer
	sameProducer.summary = "security: max_tested is read without a bound"
	otherFile := devRecord3183(strp("run-1"))
	otherFile.file = "internal/adaptercompat/check.go"
	for name, first := range map[string]spec{
		"same producer": sameProducer,
		"another run":   devRecord3183(strp("run-0")),
		"another file":  otherFile,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			res, err := Materialize(context.Background(), mixed(t, first, review3184(strp("run-1"))),
				fileGate, true, Tracking{}, f)
			if err != nil || len(f.created) != 2 {
				t.Fatalf("%v, %v, created %d", outcomes(res), err, len(f.created))
			}
		})
	}
}

func TestIdentifiers(t *testing.T) {
	got := identifiers([]string{"Use `Foo()` at internal/x.go:12, max_tested and opencode.json; e.g. 404, 2nd, I.E. 12:30"}, nil)
	want := map[string]bool{"Foo()": true, "internal/x.go:12": true, "x.go": true,
		"max_tested": true, "opencode.json": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func writePR(t *testing.T, dir string, doc any) {
	t.Helper()
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "pr-42.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTracking(t *testing.T) {
	dir := t.TempDir()
	if got := LoadTracking(dir, 42); got.RunID != "" || len(got.Unmet) != 0 {
		t.Fatalf("a missing pr context tracks nothing: %+v", got)
	}
	writePR(t, dir, map[string]any{"issue_number": 42, "run_id": "run-1", "acceptance_criteria": []map[string]string{
		{"text": criterion4, "status": "unmet"},
		{"text": "Met one.", "status": "met"},
		{"text": "Unverifiable one.", "status": "not_verifiable"},
	}})
	got := LoadTracking(dir, 42)
	if want := unmet("run-1", criterion4); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got := LoadTracking(dir, 43); got.RunID != "" || len(got.Unmet) != 0 {
		t.Fatalf("another issue's file tracks nothing: %+v", got)
	}
	writePR(t, dir, map[string]any{"issue_number": 7})
	if got := LoadTracking(dir, 42); len(got.Unmet) != 0 {
		t.Fatalf("a foreign pr context tracks nothing: %+v", got)
	}
}

func TestLoadTrackingMatchesAParaphrasedVerdictByIndex(t *testing.T) {
	dir := t.TempDir()
	issue, _ := json.Marshal(map[string]any{"issue_number": 42, "requirements": map[string]any{
		"acceptance_criteria": []string{"First criterion.", criterion4},
	}})
	if err := os.WriteFile(filepath.Join(dir, "issue-42.json"), issue, 0o600); err != nil {
		t.Fatal(err)
	}
	writePR(t, dir, map[string]any{"issue_number": 42, "run_id": "run-1", "acceptance_criteria": []map[string]any{
		{"index": 0, "text": "First criterion.", "status": "met"},
		{"index": 1, "text": "Paraphrased wording of the newest tested version.", "status": "unmet"},
	}})
	got := LoadTracking(dir, 42)
	if !got.Unmet[NormalizeTitle(criterion4)] {
		t.Fatalf("the issue's own wording of an unmet criterion must be tracked: %+v", got)
	}
	if got.Unmet[NormalizeTitle("First criterion.")] {
		t.Fatalf("a met criterion must not be tracked: %+v", got)
	}
}
