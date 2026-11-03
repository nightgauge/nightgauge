package stages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// TestRetireMergedRun (#2878): a merged PR's pr-{N}.json and dev-{N}.json go, so
// a squash-merged branch's leftover commits cannot resume at feature-validate.
// An open PR rules nothing out, and an unreadable one rules it out but keeps
// the files.
func TestRetireMergedRun(t *testing.T) {
	cases := []struct {
		name      string
		state     string
		err       error
		closed    bool
		wantBlock bool
		wantKept  bool
	}{
		{name: "merged", state: "MERGED", wantBlock: true},
		{name: "merged on a closed issue", state: "MERGED", closed: true, wantBlock: true, wantKept: true},
		{name: "open", state: "OPEN", wantKept: true},
		{name: "unreadable", err: errors.New("boom"), wantBlock: true, wantKept: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := layouttest.Repo(t)
			prPath := pipelineContextPath(dir, "pr-7.json")
			devPath := pipelineContextPath(dir, "dev-7.json")
			for _, p := range []string{prPath, devPath} {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(`{"pr_number":42}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			orig := prStateOnForge
			t.Cleanup(func() { prStateOnForge = orig })
			prStateOnForge = func(context.Context, string, int) (string, error) { return tc.state, tc.err }
			origOpen := issueOpenOnForge
			t.Cleanup(func() { issueOpenOnForge = origOpen })
			issueOpenOnForge = func(context.Context, string, int) bool { return !tc.closed }

			why := retireMergedRun(context.Background(), dir, 7)
			if (why != "") != tc.wantBlock {
				t.Errorf("why = %q, want block=%v", why, tc.wantBlock)
			}
			for _, p := range []string{prPath, devPath} {
				if _, err := os.Stat(p); (err == nil) != tc.wantKept {
					t.Errorf("%s kept=%v, want %v", p, err == nil, tc.wantKept)
				}
			}
		})
	}
}

// TestResolvePreservedResume_MergedPRDeclines (#2878): a squash merge leaves the
// branch ahead of main with dev-{N}.json present and the criteria unchanged,
// which is every other condition for a resume. The merged PR alone rules it out.
func TestResolvePreservedResume_MergedPRDeclines(t *testing.T) {
	dir := resumeRepo(t)
	commitOnBranch(t, dir)
	writeFileT(t, pipelineContextPath(dir, "pr-42.json"), `{"pr_number":99}`)
	orig := prStateOnForge
	t.Cleanup(func() { prStateOnForge = orig })
	prStateOnForge = func(context.Context, string, int) (string, error) { return "MERGED", nil }
	origOpen := issueOpenOnForge
	t.Cleanup(func() { issueOpenOnForge = origOpen })
	issueOpenOnForge = func(context.Context, string, int) bool { return true }

	got := ResolvePreservedResume(context.Background(), dir, 42, bodyReader(resumeIssueBody))
	if got.Resume {
		t.Fatalf("got %+v, want no resume on a merged PR", got)
	}
}
