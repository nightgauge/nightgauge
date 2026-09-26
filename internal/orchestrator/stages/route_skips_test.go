package stages

import (
	"context"
	"strings"
	"testing"
)

// routeSkippedSnap is a docs-only/trivial run: dev ran, the route skipped
// feature-validate, so no validate-{N}.json exists (#1968).
func routeSkippedSnap() PRCreateSnapshot {
	s := richSnap()
	s.IssueType = "docs"
	s.FilesCreated = nil
	s.FilesModified = []string{"docs/README.md"}
	s.HasValidate = false
	s.ValidationStatus = ""
	s.BuildPassed = false
	s.UnitTestsPassed = false
	s.ValidateSkippedByRoute = true
	return s
}

func TestDecideCreate_ValidateSkippedByRoute_Creates(t *testing.T) {
	d := DecideCreate(routeSkippedSnap())
	if !d.ShouldCreate || d.Reason != ReasonValidateSkippedByRoute {
		t.Fatalf("decision = %+v, want create with %q", d, ReasonValidateSkippedByRoute)
	}
}

func TestDecideCreate_ValidateMissingNotSkipped_StillPunts(t *testing.T) {
	s := routeSkippedSnap()
	s.ValidateSkippedByRoute = false
	if d := DecideCreate(s); d.Reason != ReasonMissingValidateContext {
		t.Fatalf("decision = %+v, want punt %q", d, ReasonMissingValidateContext)
	}
}

func TestDecideCreate_ValidateSkippedByRoute_DevFailuresStillPunt(t *testing.T) {
	for name, mut := range map[string]func(*PRCreateSnapshot){
		"build-failed": func(s *PRCreateSnapshot) { s.BuildStatus = "failed" },
		"tests-failed": func(s *PRCreateSnapshot) { s.TestsFailed = 1 },
		"no-changes":   func(s *PRCreateSnapshot) { s.FilesModified = nil },
		"spike":        func(s *PRCreateSnapshot) { s.IssueType = "spike" },
		"missing-dev":  func(s *PRCreateSnapshot) { s.HasDev = false },
	} {
		s := routeSkippedSnap()
		mut(&s)
		if d := DecideCreate(s); d.ShouldCreate {
			t.Errorf("%s: decision = %+v, want punt", name, d)
		}
	}
}

// A docs-only-routed issue reaches pr-create on its deterministic path when
// the scheduler reports the route skipped feature-validate (#1968).
func TestRunner_RouteSkippedValidate_CreatesDeterministically(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 7, URL: "https://github.com/owner/repo/pull/7", NodeID: "PR_7"}}
	snap := routeSkippedSnap()
	snap.ValidateSkippedByRoute = false // the runner must derive it from ctx
	r := newTestRunner(snap, prc, &fakeGit{})

	ctx := WithRouteSkippedStages(context.Background(), []string{"feature-planning", "feature-validate"})
	res, err := r.Run(ctx, 42, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != CreatePathCreated || res.Reason != ReasonValidateSkippedByRoute {
		t.Fatalf("result = %+v, want created with %q", res, ReasonValidateSkippedByRoute)
	}
	if !strings.Contains(res.Body, "feature-validate: skipped by the route") {
		t.Errorf("PR body does not record the route skip:\n%s", res.Body)
	}

	// Without the route signal the same context still punts.
	r2 := newTestRunner(snap, &fakePRClient{}, &fakeGit{})
	res2, _ := r2.Run(context.Background(), 42, "owner/repo", "/tmp")
	if res2.Reason != ReasonMissingValidateContext {
		t.Errorf("no-route-skip reason = %q, want %q", res2.Reason, ReasonMissingValidateContext)
	}
}
