package followup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// TestMaterialize_ReportOnlyAppliesParentBody: report mode reads the
// parent's body, so a restating record is tracked-by-parent, and makes no
// other Filer call.
func TestMaterialize_ReportOnlyAppliesParentBody(t *testing.T) {
	f := newFake()
	f.criteria = map[int][]string{42: {"Waiter honours the configured ceiling on retry"}}
	capt := capture(t, "Waiter honours the configured ceiling on retry", "Logger drops the run id")
	res, err := Materialize(context.Background(), capt, filing.Gate{Mode: filing.ModeReport, Reason: "--dry-run"},
		false, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(res); got[0] != OutcomeTrackedByParent || got[1] != OutcomeWouldFile {
		t.Fatalf("outcomes = %v", got)
	}
	if f.calls != 0 || len(f.created) != 0 || len(f.comments) != 0 {
		t.Fatalf("report mode reached the forge: calls=%d created=%d comments=%d", f.calls, len(f.created), len(f.comments))
	}
}

// TestReviewFiling_ReportModeAppliesParentBody: the pr-merge filer reads
// the parent's body in report mode, so a restating finding is
// tracked-by-parent, and writes nothing; with a repository that is not
// owner/name it reports without the read.
func TestReviewFiling_ReportModeAppliesParentBody(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{
		42: {Body: "- [ ] Waiter honours the configured ceiling on retry\n"},
	})
	cfg := &config.Config{Owner: githubtest.Owner, DefaultRepo: githubtest.Repo}
	file := ReviewFiling(fake.Client(), cfg)
	capt := capture(t, "Waiter honours the configured ceiling on retry")

	res, err := file(context.Background(), t.TempDir(), githubtest.Owner+"/"+githubtest.Repo, capt)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(res); got[0] != OutcomeTrackedByParent {
		t.Fatalf("outcomes = %v", got)
	}
	if m := fake.Mutations(); len(m) != 0 {
		t.Fatalf("report mode mutated the forge: %v", m)
	}

	res, err = file(context.Background(), t.TempDir(), "not-a-repo", capt)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(res); got[0] != OutcomeWouldFile {
		t.Fatalf("outcomes without a repository = %v", got)
	}
}

// TestReviewFiling_ReportModeUnreadableParentReports: the read is best effort,
// so a parent that cannot be read leaves the finding would-file.
func TestReviewFiling_ReportModeUnreadableParentReports(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{42: {}})
	fake.Unreadable = []int{42}
	file := ReviewFiling(fake.Client(), &config.Config{Owner: githubtest.Owner, DefaultRepo: githubtest.Repo})
	res, err := file(context.Background(), t.TempDir(), "", capture(t, "Waiter honours the configured ceiling on retry"))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(res); got[0] != OutcomeWouldFile {
		t.Fatalf("outcomes = %v", got)
	}
}

// TestReviewFiling_FileModeGuardsStayFatal: what report mode tolerates, a
// malformed repository or an unresolved project, still fails when the gate
// files.
func TestReviewFiling_FileModeGuardsStayFatal(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nightgauge", "config.yaml"),
		[]byte("follow_up_filing:\n  mode: file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := githubtest.New(t, map[int]githubtest.Issue{})
	file := ReviewFiling(fake.Client(), &config.Config{Owner: githubtest.Owner, DefaultRepo: githubtest.Repo})
	for _, repo := range []string{"not-a-repo", "someone/undeclared"} {
		res, err := file(context.Background(), workdir, repo, capture(t, "Logger drops the run id"))
		if err == nil || res != nil {
			t.Fatalf("%s: res = %+v, err = %v, want a refusal before filing", repo, res, err)
		}
	}
}

// TestMaterialize_MarkerInParentBodyIsTrackedByParent: a record whose marker
// is in its parent's current body is tracked-by-parent in file mode and in
// report mode, whether the criterion under the marker is checked or unchecked
// and even though it does not restate the summary. Nothing is filed, folded or
// commented (#2872).
func TestMaterialize_MarkerInParentBodyIsTrackedByParent(t *testing.T) {
	for _, box := range []string{"[x]", "[ ]"} {
		for name, gate := range map[string]filing.Gate{
			"file":   fileGate,
			"report": {Mode: filing.ModeReport, Reason: "--dry-run"},
		} {
			t.Run(name+" "+box, func(t *testing.T) {
				c := capture(t, "Logger drops the run id")
				f := newFake()
				f.bodies[42] = "## Part B\n\n" + Marker(c.Records[0]) + "\n\n- " + box +
					" Stage lines carry their correlation field\n"
				f.titles[NormalizeTitle("Logger drops the run id")] = 61
				f.open = []SubsystemCandidate{{Number: 60, Body: "x.go"}}
				res, err := Materialize(context.Background(), c, gate, true, Tracking{}, f)
				if err != nil {
					t.Fatal(err)
				}
				it := res.Items[0]
				if it.Outcome != OutcomeTrackedByParent || it.Detail != "marker in #42's body" || !res.Consumed(it.ID) {
					t.Fatalf("item = %+v, want tracked-by-parent", it)
				}
				if f.calls != 0 || len(f.created) != 0 || len(f.comments) != 0 || strings.Contains(f.bodies[60], "## Part") {
					t.Fatalf("reached the forge: calls=%d created=%d comments=%d", f.calls, len(f.created), len(f.comments))
				}
			})
		}
	}
}

// TestMaterialize_MarkerOnlyOnAnotherIssueIsAlreadyExists: a marker in an open
// issue other than the parent is already-exists, not tracked-by-parent.
func TestMaterialize_MarkerOnlyOnAnotherIssueIsAlreadyExists(t *testing.T) {
	c := capture(t, "Logger drops the run id")
	f := newFake()
	f.bodies[42] = "parent without the marker\n- [ ] Stage lines carry their correlation field\n"
	f.bodies[70] = "folded earlier\n" + Marker(c.Records[0])
	res, err := Materialize(context.Background(), c, fileGate, true, Tracking{}, f)
	if err != nil {
		t.Fatal(err)
	}
	if it := res.Items[0]; it.Outcome != OutcomeAlreadyExists || it.IssueNumber != 70 || len(f.created) != 0 {
		t.Fatalf("item = %+v created=%d, want already-exists on #70", it, len(f.created))
	}
}

// TestReviewFiling_ReportModeMarkerInParentBody: the pr-merge filer in report
// mode reads the parent's body, so a record whose marker is there is
// tracked-by-parent, not would-file, and nothing is written.
func TestReviewFiling_ReportModeMarkerInParentBody(t *testing.T) {
	capt := capture(t, "Logger drops the run id")
	fake := githubtest.New(t, map[int]githubtest.Issue{
		42: {Body: Marker(capt.Records[0]) + "\n- [x] Stage lines carry their correlation field\n"},
	})
	file := ReviewFiling(fake.Client(), &config.Config{Owner: githubtest.Owner, DefaultRepo: githubtest.Repo})
	res, err := file(context.Background(), t.TempDir(), githubtest.Owner+"/"+githubtest.Repo, capt)
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(res); got[0] != OutcomeTrackedByParent {
		t.Fatalf("outcomes = %v", got)
	}
	if m := fake.Mutations(); len(m) != 0 {
		t.Fatalf("report mode mutated the forge: %v", m)
	}
}

// TestMaterialize_UnreadableParentBodyFiles: a parent body that cannot be read
// tracks nothing, so the record is filed in file mode and would-file in report
// mode: the safe direction.
func TestMaterialize_UnreadableParentBodyFiles(t *testing.T) {
	for gate, want := range map[filing.Gate]string{
		fileGate: OutcomeFiled,
		{Mode: filing.ModeReport, Reason: "--dry-run"}: OutcomeWouldFile,
	} {
		c := capture(t, "Logger drops the run id")
		f := newFake()
		f.bodyErr = errors.New("rate limited")
		res, err := Materialize(context.Background(), c, gate, true, Tracking{}, f)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Items[0].Outcome; got != want {
			t.Fatalf("%s: outcome = %s, want %s", gate.Mode, got, want)
		}
	}
}
