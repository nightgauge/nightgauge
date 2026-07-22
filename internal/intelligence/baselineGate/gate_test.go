package baselineGate

import (
	"context"
	"errors"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// stubRunner implements WorkflowRunsLister with canned responses.
type stubRunner struct {
	runs    []gh.WorkflowRun
	jobs    map[int64][]gh.WorkflowRunJob
	runsErr error
	jobsErr error
}

func (s *stubRunner) ListWorkflowRuns(_ context.Context, _, _, _, _ string, perPage int) ([]gh.WorkflowRun, error) {
	if s.runsErr != nil {
		return nil, s.runsErr
	}
	if perPage > 0 && perPage < len(s.runs) {
		return s.runs[:perPage], nil
	}
	return s.runs, nil
}

func (s *stubRunner) ListRunJobs(_ context.Context, _, _ string, runID int64) ([]gh.WorkflowRunJob, error) {
	if s.jobsErr != nil {
		return nil, s.jobsErr
	}
	return s.jobs[runID], nil
}

func makeRuns(conclusions ...string) []gh.WorkflowRun {
	out := make([]gh.WorkflowRun, len(conclusions))
	for i, c := range conclusions {
		out[i] = gh.WorkflowRun{
			ID:         int64(i + 1),
			Name:       "CI",
			HeadBranch: "main",
			Status:     "completed",
			Conclusion: c,
		}
	}
	return out
}

func TestDefaultGateConfig(t *testing.T) {
	cfg := DefaultGateConfig()
	if !cfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.LookbackRuns != 5 {
		t.Errorf("LookbackRuns = %d, want 5", cfg.LookbackRuns)
	}
	if cfg.RedThreshold != 2 {
		t.Errorf("RedThreshold = %d, want 2", cfg.RedThreshold)
	}
	if cfg.GreenThreshold != 2 {
		t.Errorf("GreenThreshold = %d, want 2", cfg.GreenThreshold)
	}
}

func TestNewEvaluator_DefaultsCappedAndFloored(t *testing.T) {
	e := NewEvaluator(GateConfig{LookbackRuns: -3, RedThreshold: 0, GreenThreshold: 0}, nil)
	if e.cfg.LookbackRuns != 5 {
		t.Errorf("LookbackRuns floor: got %d, want 5", e.cfg.LookbackRuns)
	}
	if e.cfg.RedThreshold != 2 {
		t.Errorf("RedThreshold floor: got %d, want 2", e.cfg.RedThreshold)
	}

	e2 := NewEvaluator(GateConfig{LookbackRuns: 1000}, nil)
	if e2.cfg.LookbackRuns != 20 {
		t.Errorf("LookbackRuns cap: got %d, want 20", e2.cfg.LookbackRuns)
	}
}

func TestEvaluateForBody_NoTriggerAllows(t *testing.T) {
	e := NewEvaluator(DefaultGateConfig(), &stubRunner{})
	body := "Add a button to the settings page. No CI dependency here."
	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Errorf("Decision = %s, want allow", res.Decision)
	}
}

func TestEvaluateForBody_UnparseableAllows(t *testing.T) {
	// Trigger fires but no workflow path → unparseable (treated as allow by callers).
	e := NewEvaluator(DefaultGateConfig(), &stubRunner{})
	body := "- [ ] Make CI a required check on main"
	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != DecisionUnparseable {
		t.Errorf("Decision = %s, want unparseable", res.Decision)
	}
	if res.TriggerText == "" {
		t.Error("TriggerText empty on unparseable result")
	}
}

func TestEvaluateForBody_DefersOnRedBaseline(t *testing.T) {
	body := "- [ ] Make `.github/workflows/ci.yml` a required check"
	runner := &stubRunner{
		runs: makeRuns("failure", "failure", "failure", "success", "failure"),
	}
	e := NewEvaluator(DefaultGateConfig(), runner)
	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != DecisionDefer {
		t.Errorf("Decision = %s, want defer (failed=%d/%d)", res.Decision, res.FailedRuns, res.SampledRuns)
	}
	if res.FailedRuns != 4 || res.SampledRuns != 5 {
		t.Errorf("counts: failed=%d sampled=%d, want 4/5", res.FailedRuns, res.SampledRuns)
	}
	if res.Workflow != "ci.yml" {
		t.Errorf("Workflow = %q, want ci.yml", res.Workflow)
	}
}

func TestEvaluateForBody_AllowsOnMostlyGreen(t *testing.T) {
	body := "- [ ] Make `.github/workflows/ci.yml` a required check"
	runner := &stubRunner{runs: makeRuns("success", "success", "failure", "success", "success")}
	e := NewEvaluator(DefaultGateConfig(), runner)
	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Errorf("Decision = %s, want allow", res.Decision)
	}
	if res.FailedRuns != 1 {
		t.Errorf("FailedRuns = %d, want 1", res.FailedRuns)
	}
}

func TestEvaluateForBody_NoCompletedRunsAllows(t *testing.T) {
	body := "- [ ] Make `.github/workflows/ci.yml` a required check"
	runner := &stubRunner{runs: []gh.WorkflowRun{
		{ID: 1, Status: "in_progress", Conclusion: ""},
		{ID: 2, Status: "queued", Conclusion: ""},
	}}
	e := NewEvaluator(DefaultGateConfig(), runner)
	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Errorf("Decision = %s, want allow (no completed runs)", res.Decision)
	}
}

func TestEvaluateForBody_RunsErrorPropagates(t *testing.T) {
	body := "- [ ] Make `.github/workflows/ci.yml` a required check"
	runner := &stubRunner{runsErr: errors.New("network dead")}
	e := NewEvaluator(DefaultGateConfig(), runner)
	_, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestEvaluateForBody_JobLevelFiltering(t *testing.T) {
	// Run 1: workflow conclusion=failure but the named job succeeded → not counted as failure.
	// Run 2: workflow conclusion=failure AND named job failed → counted as failure.
	body := "- [ ] Make `Integration & E2E Tests` from `.github/workflows/ci.yml` a required check"
	runner := &stubRunner{
		runs: makeRuns("failure", "failure", "failure"),
		jobs: map[int64][]gh.WorkflowRunJob{
			1: {{Name: "Integration & E2E Tests", Status: "completed", Conclusion: "success"}, {Name: "Other", Conclusion: "failure"}},
			2: {{Name: "Integration & E2E Tests", Status: "completed", Conclusion: "failure"}},
			3: {{Name: "Integration & E2E Tests", Status: "completed", Conclusion: "failure"}},
		},
	}
	cfg := DefaultGateConfig()
	cfg.RedThreshold = 2
	e := NewEvaluator(cfg, runner)

	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Decision != DecisionDefer {
		t.Errorf("Decision = %s, want defer", res.Decision)
	}
	if res.FailedRuns != 2 {
		t.Errorf("FailedRuns = %d, want 2 (job-filtered)", res.FailedRuns)
	}
	if res.Job != "Integration & E2E Tests" {
		t.Errorf("Job = %q, want 'Integration & E2E Tests'", res.Job)
	}
}

func TestEvaluateForBody_JobLookupErrorFallsBackToRunConclusion(t *testing.T) {
	body := "- [ ] Make `Integration` from `.github/workflows/ci.yml` a required check"
	runner := &stubRunner{
		runs:    makeRuns("failure", "failure", "failure"),
		jobsErr: errors.New("transient"),
	}
	e := NewEvaluator(DefaultGateConfig(), runner)
	res, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All 3 runs fall back to run conclusion=failure → defer.
	if res.Decision != DecisionDefer {
		t.Errorf("Decision = %s, want defer (job lookup failed → fallback)", res.Decision)
	}
	if res.FailedRuns != 3 {
		t.Errorf("FailedRuns = %d, want 3", res.FailedRuns)
	}
}

func TestIsLastNGreen_AllSuccess(t *testing.T) {
	runner := &stubRunner{runs: makeRuns("success", "success", "success")}
	e := NewEvaluator(DefaultGateConfig(), runner)
	ok, _, err := e.IsLastNGreen(context.Background(), "o", "r", "ci.yml", "main", "", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected last-2-green to be true")
	}
}

func TestIsLastNGreen_StopsOnFirstFailure(t *testing.T) {
	runner := &stubRunner{runs: makeRuns("success", "failure", "success")}
	e := NewEvaluator(DefaultGateConfig(), runner)
	ok, _, err := e.IsLastNGreen(context.Background(), "o", "r", "ci.yml", "main", "", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false when 2nd-most-recent run failed")
	}
}

func TestIsLastNGreen_FewerThanNRunsReturnsFalse(t *testing.T) {
	runner := &stubRunner{runs: makeRuns("success")}
	e := NewEvaluator(DefaultGateConfig(), runner)
	ok, _, err := e.IsLastNGreen(context.Background(), "o", "r", "ci.yml", "main", "", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false when only 1 of required 2 runs exists")
	}
}

func TestEvaluator_NilRunnerOnMatchErrors(t *testing.T) {
	e := NewEvaluator(DefaultGateConfig(), nil)
	body := "- [ ] Make `.github/workflows/ci.yml` a required check"
	_, err := e.EvaluateForBody(context.Background(), body, "o", "r", "main")
	if err == nil {
		t.Fatal("expected error from nil runner on classified match")
	}
}
