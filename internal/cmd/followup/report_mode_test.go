package followup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// TestMaterialize_ReportOnlyAppliesParentCriteria: report mode reads the
// parent's criteria, so a restating record is tracked-by-parent, and makes no
// other Filer call.
func TestMaterialize_ReportOnlyAppliesParentCriteria(t *testing.T) {
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

// TestReviewFiling_ReportModeAppliesParentCriteria: the pr-merge filer reads
// the parent's body in report mode, so a restating finding is
// tracked-by-parent, and writes nothing; with a repository that is not
// owner/name it reports without the read.
func TestReviewFiling_ReportModeAppliesParentCriteria(t *testing.T) {
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
