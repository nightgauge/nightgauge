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
	checkRuns   map[string]string // sha -> check-runs JSON ({"check_runs":[...]})
}

func installGhStub(t *testing.T, sc ghScenario) {
	t.Helper()
	orig := survivalExecGh
	t.Cleanup(func() { survivalExecGh = orig })

	survivalExecGh = func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "/check-runs"):
			sha := shaFromCheckRunsArgs(args)
			if body, ok := sc.checkRuns[sha]; ok {
				return []byte(body), nil
			}
			return []byte(`{"check_runs":[]}`), nil
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
			// repos/o/r/commits/<sha>/check-runs
			parts := strings.Split(a, "/")
			for i, p := range parts {
				if p == "check-runs" && i > 0 {
					return parts[i-1]
				}
			}
		}
	}
	return ""
}

func checkRunsJSON(runs ...[2]string) string {
	var b strings.Builder
	b.WriteString(`{"check_runs":[`)
	for i, r := range runs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":%q,"status":"completed","conclusion":%q}`, r[0], r[1])
	}
	b.WriteString("]}")
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
			"mergeSHA111": `{"check_runs":[]}`, // no green-at-merge baseline
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
