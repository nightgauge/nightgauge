package baselineGate

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// GateConfig holds the runtime knobs for the baseline-CI gate.
//
// Defaults match the AC: lookback of 5 runs, defer when ≥2 fail, promote when
// the last 2 are green. See docs/CONFIGURATION.md for user-facing docs.
type GateConfig struct {
	// Enabled toggles the gate at runtime. Default: true.
	Enabled bool
	// LookbackRuns is the number of recent completed runs to inspect.
	// Default: 5. Capped at 20 to keep API usage bounded.
	LookbackRuns int
	// RedThreshold is the minimum number of failed runs in the lookback
	// window that triggers a defer. Default: 2.
	RedThreshold int
	// GreenThreshold is the minimum number of consecutive most-recent green
	// runs required for `promote` to lift a deferral. Default: 2.
	GreenThreshold int
}

// DefaultGateConfig returns a GateConfig with the AC's documented defaults.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		Enabled:        true,
		LookbackRuns:   5,
		RedThreshold:   2,
		GreenThreshold: 2,
	}
}

// Decision is the gate's allow/defer outcome.
type Decision string

const (
	// DecisionAllow means dispatch should proceed.
	DecisionAllow Decision = "allow"
	// DecisionDefer means the issue depends on a CI baseline that is
	// currently failing — pause the queue item and post a deferral comment.
	DecisionDefer Decision = "defer"
	// DecisionUnparseable means the AC text triggered the gate but the
	// referenced workflow/job could not be extracted. Per ADR-003 the
	// caller should treat this as `allow` and log the unparseable reference.
	DecisionUnparseable Decision = "unparseable"
)

// GateResult is the structured outcome of an evaluation.
type GateResult struct {
	Decision    Decision
	Reason      string
	Workflow    string
	Job         string
	FailedRuns  int
	SampledRuns int
	RunIDs      []int64
	// TriggerText is the AC phrase that fired the gate. Empty when no AC
	// triggered.
	TriggerText string
}

// WorkflowRunsLister abstracts the GitHub Actions list-runs/list-jobs surface
// the gate calls into. Backed by `github.CIService` in production and by a
// stub in tests.
type WorkflowRunsLister interface {
	ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile, branch string, perPage int) ([]gh.WorkflowRun, error)
	ListRunJobs(ctx context.Context, owner, repo string, runID int64) ([]gh.WorkflowRunJob, error)
}

// Evaluator wraps a GateConfig and a runner; it owns the decision logic.
type Evaluator struct {
	cfg    GateConfig
	runner WorkflowRunsLister
}

// NewEvaluator builds an Evaluator. Passing a nil runner is allowed for
// classification-only callers but `EvaluateForBody` will panic on classified
// matches in that case — the CLI always wires a real runner.
func NewEvaluator(cfg GateConfig, runner WorkflowRunsLister) *Evaluator {
	if cfg.LookbackRuns <= 0 {
		cfg.LookbackRuns = 5
	}
	if cfg.LookbackRuns > 20 {
		cfg.LookbackRuns = 20
	}
	if cfg.RedThreshold <= 0 {
		cfg.RedThreshold = 2
	}
	if cfg.GreenThreshold <= 0 {
		cfg.GreenThreshold = 2
	}
	return &Evaluator{cfg: cfg, runner: runner}
}

// EvaluateForBody parses the issue body, classifies each AC, and queries
// workflow runs for the first matched AC. Returns DecisionAllow when no AC
// triggers; DecisionUnparseable when the matched AC has no extractable
// workflow path; DecisionDefer / DecisionAllow based on red threshold
// otherwise.
func (e *Evaluator) EvaluateForBody(ctx context.Context, issueBody, owner, repo, branch string) (*GateResult, error) {
	items := SplitACList(issueBody)
	for _, ac := range items {
		m := ClassifyAC(ac)
		if !m.Triggered {
			continue
		}
		return e.evaluateMatch(ctx, m, owner, repo, branch)
	}
	return &GateResult{Decision: DecisionAllow, Reason: "no baseline-CI dependency detected in AC text"}, nil
}

// evaluateMatch handles the post-classification path: workflow query + threshold check.
func (e *Evaluator) evaluateMatch(ctx context.Context, m ACMatch, owner, repo, branch string) (*GateResult, error) {
	if m.Workflow == "" {
		return &GateResult{
			Decision:    DecisionUnparseable,
			Reason:      fmt.Sprintf("AC mentions %q but no .github/workflows/ path was extractable — allowing dispatch", m.TriggerText),
			TriggerText: m.TriggerText,
		}, nil
	}
	if e.runner == nil {
		return nil, fmt.Errorf("baselineGate: runner is nil — cannot query workflow runs for %q", m.Workflow)
	}

	runs, err := e.runner.ListWorkflowRuns(ctx, owner, repo, m.Workflow, branch, e.cfg.LookbackRuns)
	if err != nil {
		return nil, fmt.Errorf("list runs for %s on %s: %w", m.Workflow, branch, err)
	}

	failed, sampled, runIDs := e.countFailures(ctx, runs, m.Job, owner, repo)

	res := &GateResult{
		Workflow:    m.Workflow,
		Job:         m.Job,
		FailedRuns:  failed,
		SampledRuns: sampled,
		RunIDs:      runIDs,
		TriggerText: m.TriggerText,
	}

	if sampled == 0 {
		// No completed runs at all on the branch — let dispatch proceed; the
		// downstream PR pipeline will surface real CI status.
		res.Decision = DecisionAllow
		res.Reason = fmt.Sprintf("no completed runs found for %s on %s — allowing dispatch", m.Workflow, branch)
		return res, nil
	}

	if failed >= e.cfg.RedThreshold {
		res.Decision = DecisionDefer
		jobNote := ""
		if m.Job != "" {
			jobNote = " job=" + m.Job
		}
		res.Reason = fmt.Sprintf("%s%s failed %d/%d recent runs on %s (threshold: %d)",
			m.Workflow, jobNote, failed, sampled, branch, e.cfg.RedThreshold)
		return res, nil
	}

	res.Decision = DecisionAllow
	res.Reason = fmt.Sprintf("%s passed enough recent runs on %s (%d/%d failed; threshold: %d)",
		m.Workflow, branch, failed, sampled, e.cfg.RedThreshold)
	return res, nil
}

// countFailures inspects the recent runs and returns (failedCount,
// sampledCount, runIDs). When `job` is non-empty, each run's jobs are
// fetched and only runs where that named job concluded `failure` count.
// Job-level errors degrade gracefully — a failed jobs lookup falls back to
// run-level conclusion for that run only.
func (e *Evaluator) countFailures(ctx context.Context, runs []gh.WorkflowRun, job, owner, repo string) (int, int, []int64) {
	var failed, sampled int
	runIDs := make([]int64, 0, len(runs))
	for _, run := range runs {
		if !strings.EqualFold(run.Status, "completed") {
			continue
		}
		sampled++
		runIDs = append(runIDs, run.ID)

		runFailed := strings.EqualFold(run.Conclusion, "failure")
		if job != "" {
			jobs, err := e.runner.ListRunJobs(ctx, owner, repo, run.ID)
			if err == nil {
				runFailed = false
				for _, j := range jobs {
					if strings.EqualFold(j.Name, job) && strings.EqualFold(j.Conclusion, "failure") {
						runFailed = true
						break
					}
				}
			}
			// On error: keep run-level conclusion already computed above.
		}
		if runFailed {
			failed++
		}
	}
	return failed, sampled, runIDs
}

// IsLastNGreen returns true when the most-recent N completed runs are all
// `success`. Used by the `promote` command to lift a deferral when the
// baseline has stabilized. When `job` is non-empty, the same job-level
// filtering used by countFailures applies.
//
// Returns false when fewer than N completed runs exist.
func (e *Evaluator) IsLastNGreen(ctx context.Context, owner, repo, workflow, branch, job string, n int) (bool, []int64, error) {
	if n <= 0 {
		n = e.cfg.GreenThreshold
	}
	runs, err := e.runner.ListWorkflowRuns(ctx, owner, repo, workflow, branch, n)
	if err != nil {
		return false, nil, err
	}
	completed := 0
	ids := make([]int64, 0, n)
	for _, run := range runs {
		if !strings.EqualFold(run.Status, "completed") {
			continue
		}
		completed++
		ids = append(ids, run.ID)

		ok := strings.EqualFold(run.Conclusion, "success")
		if job != "" {
			jobs, jerr := e.runner.ListRunJobs(ctx, owner, repo, run.ID)
			if jerr == nil {
				ok = false
				for _, j := range jobs {
					if strings.EqualFold(j.Name, job) && strings.EqualFold(j.Conclusion, "success") {
						ok = true
						break
					}
				}
			}
		}
		if !ok {
			return false, ids, nil
		}
		if completed >= n {
			return true, ids, nil
		}
	}
	return false, ids, nil
}
