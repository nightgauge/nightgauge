package github

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// ghScenario configures the stubbed survivalExecGh responses for one test.
type ghScenario struct {
	commitsList string            // JSON array for the revert-scan commits call
	headSHA     string            // resolved base HEAD sha (refSHA)
	checkRuns   map[string]string // sha -> gh output: one JSON check-run per line, every page
}

func installGhStub(t *testing.T, sc ghScenario) {
	t.Helper()
	orig := survivalExecGh
	t.Cleanup(func() { survivalExecGh = orig })

	survivalExecGh = func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "/check-runs"):
			// #1681: a check-runs read without --paginate sees page 1 only.
			// The stub refuses it outright rather than modelling the page.
			if !containsArg(args, "--paginate") || !strings.Contains(joined, "per_page=100") {
				return nil, fmt.Errorf("stub: check-runs read must be paginated at per_page=100: %s", joined)
			}
			sha := shaFromCheckRunsArgs(args)
			if body, ok := sc.checkRuns[sha]; ok {
				return []byte(body), nil
			}
			return nil, nil
		case containsArg(args, "GET"):
			return []byte(sc.commitsList), nil
		default: // refSHA: repos/o/r/commits/<ref> --jq .sha
			return []byte(sc.headSHA + "\n"), nil
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func shaFromCheckRunsArgs(args []string) string {
	for _, a := range args {
		if strings.Contains(a, "/check-runs") {
			// repos/o/r/commits/<sha>/check-runs?per_page=100
			parts := strings.Split(strings.SplitN(a, "?", 2)[0], "/")
			for i, p := range parts {
				if p == "check-runs" && i > 0 {
					return parts[i-1]
				}
			}
		}
	}
	return ""
}

// checkRunsJSON renders what `gh api --paginate --jq '.check_runs[] | ... |
// tojson'` prints: one compact JSON object per line, pages concatenated.
func checkRunsJSON(runs ...[2]string) string {
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "{\"name\":%q,\"status\":\"completed\",\"conclusion\":%q}\n", r[0], r[1])
	}
	return b.String()
}

func rec(mergeSHA string) survival.Record {
	return survival.NewPending("nightgauge/nightgauge", 1, 2, mergeSHA, "2026-06-01T12:00:00Z", "main")
}

func TestSurvivalDetector_RevertFound(t *testing.T) {
	installGhStub(t, ghScenario{
		commitsList: `[{"sha":"revertSHA","commit":{"message":"Revert \"feat\"\n\nThis reverts commit mergeSHA111."}}]`,
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !obs.RevertFound || obs.RevertSHA != "revertSHA" {
		t.Errorf("got %+v, want RevertFound w/ sha=revertSHA", obs)
	}
}

func TestSurvivalDetector_RevertOfDifferentShaIgnored(t *testing.T) {
	installGhStub(t, ghScenario{
		commitsList: `[{"sha":"x","commit":{"message":"This reverts commit SOMEOTHER."}}]`,
		headSHA:     "mergeSHA111", // no descendant → no breakage either
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.RevertFound {
		t.Errorf("a revert of a different sha must not match, got %+v", obs)
	}
}

func TestSurvivalDetector_AncestryBreakage(t *testing.T) {
	installGhStub(t, ghScenario{
		commitsList: `[]`,
		headSHA:     "headSHA222",
		checkRuns: map[string]string{
			"mergeSHA111": checkRunsJSON([2]string{"build", "success"}, [2]string{"lint", "success"}),
			"headSHA222":  checkRunsJSON([2]string{"build", "failure"}),
		},
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !obs.Broke {
		t.Errorf("expected broke=true (build was green@merge, failing@head), got %+v", obs)
	}
}

func TestSurvivalDetector_NoDescendantNoBreakage(t *testing.T) {
	installGhStub(t, ghScenario{
		commitsList: `[]`,
		headSHA:     "mergeSHA111", // HEAD == merge → no descendant
		checkRuns: map[string]string{
			"mergeSHA111": checkRunsJSON([2]string{"build", "success"}),
		},
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Broke {
		t.Errorf("no descendant must not yield breakage, got %+v", obs)
	}
}

func TestSurvivalDetector_FailingCheckNotGreenAtMergeIsNotAttributed(t *testing.T) {
	installGhStub(t, ghScenario{
		commitsList: `[]`,
		headSHA:     "headSHA222",
		checkRuns: map[string]string{
			// "build" was NOT success at merge (it was already failing), so a
			// failing "build" at head is NOT attributable to this merge.
			"mergeSHA111": checkRunsJSON([2]string{"build", "failure"}),
			"headSHA222":  checkRunsJSON([2]string{"build", "failure"}),
		},
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Broke {
		t.Errorf("a check not green@merge must not be attributed, got %+v", obs)
	}
}

func TestSurvivalDetector_NoBaselineNoBreakage(t *testing.T) {
	installGhStub(t, ghScenario{
		commitsList: `[]`,
		headSHA:     "headSHA222",
		checkRuns: map[string]string{
			"mergeSHA111": "", // no green-at-merge baseline
			"headSHA222":  checkRunsJSON([2]string{"build", "failure"}),
		},
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Broke {
		t.Errorf("no baseline → cannot attribute, got %+v", obs)
	}
}

func TestSurvivalDetector_MalformedRepo(t *testing.T) {
	installGhStub(t, ghScenario{})
	bad := rec("mergeSHA111")
	bad.Repo = "not-a-repo"
	if _, err := NewSurvivalDetector().Observe(context.Background(), bad); err == nil {
		t.Error("expected error on malformed repo slug")
	}
}

// TestSurvivalDetector_BreakagePastTheFirstPage is the #1681 regression: the
// check that broke is the 31st run on the head commit, past the 30-run first
// page an un-paginated read returned. Every page's lines must be parsed.
func TestSurvivalDetector_BreakagePastTheFirstPage(t *testing.T) {
	var headRuns, mergeRuns [][2]string
	for i := 1; i <= 30; i++ {
		name := fmt.Sprintf("job-%02d", i)
		headRuns = append(headRuns, [2]string{name, "success"})
		mergeRuns = append(mergeRuns, [2]string{name, "success"})
	}
	headRuns = append(headRuns, [2]string{"late", "failure"})
	mergeRuns = append(mergeRuns, [2]string{"late", "success"})

	installGhStub(t, ghScenario{
		commitsList: `[]`,
		headSHA:     "headSHA222",
		checkRuns: map[string]string{
			"mergeSHA111": checkRunsJSON(mergeRuns...),
			"headSHA222":  checkRunsJSON(headRuns...),
		},
	})
	obs, err := NewSurvivalDetector().Observe(context.Background(), rec("mergeSHA111"))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if !obs.Broke || !strings.Contains(obs.BrokeDetail, "late") {
		t.Errorf("a failure on page 2 must be attributed, got %+v", obs)
	}
}
