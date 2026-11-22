// Package cleaninstall replays release gate G1's fixture offline.
package cleaninstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/preflight"
)

// TestG1ReplayClosesIssue feeds the G1 issue and the diff of its rc.25 run to
// the pr-create judge, with no validate verdicts (#3501).
func TestG1ReplayClosesIssue(t *testing.T) {
	const slugifyTest = "test/slugify.test.js"
	for _, tc := range []struct {
		name      string
		editTests bool
	}{
		{"recorded diff closes the issue", false},
		{"edited slugify test keeps it open", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := os.ReadFile("issue.md")
			if err != nil {
				t.Fatal(err)
			}
			// Read here, not only by git, so the test cache sees an edit to it.
			recorded, err := os.ReadFile("testdata/g1-rc25.diff")
			if err != nil {
				t.Fatal(err)
			}
			patch := filepath.Join(t.TempDir(), "g1.diff")
			if err := os.WriteFile(patch, recorded, 0o600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS("fixture")); err != nil {
				t.Fatal(err)
			}
			gittest.InitRepo(t, dir, "-q", "-b", "main")
			gittest.Run(t, dir, "config", "user.email", "fixture@example.invalid")
			gittest.Run(t, dir, "config", "user.name", "Fixture")
			gittest.Run(t, dir, "add", "-A")
			gittest.Run(t, dir, "commit", "-q", "-m", "base")
			gittest.Run(t, dir, "checkout", "-q", "-b", "feat/1-truncate")
			gittest.Run(t, dir, "apply", patch)
			if tc.editTests {
				p := filepath.Join(dir, slugifyTest)
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, append(data, "// edited\n"...), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			criteria := preflight.ACTexts(string(body))
			if len(criteria) != 3 {
				t.Fatalf("got %d criteria %q, want 3", len(criteria), criteria)
			}
			det := preflight.ReconcileACs(dir, 1, string(body))
			diff, err := stages.CollectACDiff(context.Background(), dir, "main")
			if err != nil {
				t.Fatal(err)
			}
			got := stages.ResolveACVerdicts(criteria, nil, det, diff.Checker(stages.ACTestState{}))
			if len(got) != len(criteria) {
				t.Fatalf("got %d verdicts, want %d", len(got), len(criteria))
			}

			for i, v := range got {
				want := stages.ACMet
				if tc.editTests && i == 1 {
					want = stages.ACUnmet
					if !strings.Contains(v.Evidence, slugifyTest) {
						t.Errorf("evidence %q does not name %s", v.Evidence, slugifyTest)
					}
				}
				if v.Status != want {
					t.Errorf("criterion %d = %s (%s), want %s", i+1, v.Status, v.Evidence, want)
				}
			}
			if closes := stages.IssueClosedBy(got); closes == tc.editTests {
				t.Errorf("closes_issue = %v, want %v", closes, !tc.editTests)
			}
		})
	}
}
