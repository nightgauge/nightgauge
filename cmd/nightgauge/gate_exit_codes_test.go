package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/intelligence/baselineGate"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// The pickup gates' exit codes are a verdict contract: 0 allow, 1 deferred,
// 2 could not run. issue-pickup posts a "paused" comment on exit 1, so an
// error that leaves through cobra's default exit 1 claims a pause that never
// happened (#1700 for deps-gate, #2442 for baseline-gate).

// failingFetcher is an issue reader whose every read fails, as a rate limit or
// a relationship read that could not complete does.
type failingFetcher struct{}

func (failingFetcher) GetIssueWithRelations(context.Context, string, string, int, gh.IssueRelations) (*types.Issue, error) {
	return nil, errors.New("injected read failure")
}

// runGateCmd executes cmd with args and returns the process exit code main
// would use, and what main would print to stderr.
func runGateCmd(t *testing.T, cmd *cobra.Command, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	var stderr bytes.Buffer
	return exitCodeFor(&stderr, cmd, err), stderr.String()
}

func withDepsGateFetcher(t *testing.T, f hooks.IssueFetcher, err error) {
	t.Helper()
	prev := depsGateFetcher
	depsGateFetcher = func() (hooks.IssueFetcher, error) { return f, err }
	t.Cleanup(func() { depsGateFetcher = prev })
}

func TestDepsGateCheck_ExitCodes(t *testing.T) {
	args := []string{"--issue", "5", "--owner", "o", "--repo", "r", "--json", "--pause-queue=false"}

	t.Run("a read error exits 2, not the deferral's 1", func(t *testing.T) {
		withDepsGateFetcher(t, failingFetcher{}, nil)
		code, stderr := runGateCmd(t, depsGateCheckCmd(), args...)
		if code != exitCouldNotRun {
			t.Fatalf("exit = %d, want %d", code, exitCouldNotRun)
		}
		if !strings.Contains(stderr, "could not run:") || !strings.Contains(stderr, "injected read failure") {
			t.Errorf("stderr = %q, want a could-not-run line naming the read failure", stderr)
		}
	})

	t.Run("a client that cannot be built exits 2", func(t *testing.T) {
		withDepsGateFetcher(t, nil, errors.New("no token"))
		if code, _ := runGateCmd(t, depsGateCheckCmd(), args...); code != exitCouldNotRun {
			t.Fatalf("exit = %d, want %d", code, exitCouldNotRun)
		}
	})

	t.Run("an open blocker exits 1", func(t *testing.T) {
		withDepsGateFetcher(t, &depsMockFetcher{issues: map[string]*types.Issue{
			"o/r#5": {Number: 5, BlockedBy: []types.BlockingRef{
				{Number: 4, Title: "blocker", State: "OPEN", Repo: "o/r"},
			}},
		}}, nil)
		if code, _ := runGateCmd(t, depsGateCheckCmd(), args...); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
	})

	t.Run("no open blocker exits 0", func(t *testing.T) {
		withDepsGateFetcher(t, &depsMockFetcher{issues: map[string]*types.Issue{
			"o/r#5": {Number: 5},
		}}, nil)
		if code, _ := runGateCmd(t, depsGateCheckCmd(), args...); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
	})
}

// gateStubRunner lists the same runs for every workflow.
type gateStubRunner struct{ runs []gh.WorkflowRun }

func (s gateStubRunner) ListBaselineRuns(context.Context, string, string, string, string, int) ([]gh.WorkflowRun, error) {
	return s.runs, nil
}

func (gateStubRunner) ListRunJobs(context.Context, string, string, int64) ([]gh.WorkflowRunJob, error) {
	return nil, nil
}

func withBaselineGateServices(t *testing.T, f hooks.IssueFetcher, r baselineGate.WorkflowRunsLister, err error) {
	t.Helper()
	prev := baselineGateServices
	baselineGateServices = func() (hooks.IssueFetcher, baselineGate.WorkflowRunsLister, error) {
		return f, r, err
	}
	t.Cleanup(func() { baselineGateServices = prev })
}

func TestBaselineGateCheck_ExitCodes(t *testing.T) {
	// No file there: the gate runs on its defaults, which enable it.
	config := filepath.Join(t.TempDir(), "config.yaml")
	args := []string{"--issue", "5", "--owner", "o", "--repo", "r", "--config", config, "--json", "--pause-queue=false"}
	trigger := "- [ ] Make `.github/workflows/ci.yml` a required check"
	runs := func(conclusions ...string) []gh.WorkflowRun {
		out := make([]gh.WorkflowRun, len(conclusions))
		for i, c := range conclusions {
			out[i] = gh.WorkflowRun{ID: int64(i + 1), Status: "completed", Conclusion: c}
		}
		return out
	}

	t.Run("a read error exits 2, not the deferral's 1", func(t *testing.T) {
		withBaselineGateServices(t, failingFetcher{}, gateStubRunner{}, nil)
		code, stderr := runGateCmd(t, baselineGateCheckCmd(), args...)
		if code != exitCouldNotRun {
			t.Fatalf("exit = %d, want %d", code, exitCouldNotRun)
		}
		if !strings.Contains(stderr, "could not run:") || !strings.Contains(stderr, "injected read failure") {
			t.Errorf("stderr = %q, want a could-not-run line naming the read failure", stderr)
		}
	})

	t.Run("a client that cannot be built exits 2", func(t *testing.T) {
		withBaselineGateServices(t, nil, nil, errors.New("no token"))
		if code, _ := runGateCmd(t, baselineGateCheckCmd(), args...); code != exitCouldNotRun {
			t.Fatalf("exit = %d, want %d", code, exitCouldNotRun)
		}
	})

	t.Run("a red baseline exits 1", func(t *testing.T) {
		withBaselineGateServices(t, &depsMockFetcher{issues: map[string]*types.Issue{
			"o/r#5": {Number: 5, Body: trigger},
		}}, gateStubRunner{runs: runs("failure", "failure", "failure", "success", "failure")}, nil)
		if code, _ := runGateCmd(t, baselineGateCheckCmd(), args...); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
	})

	t.Run("a green baseline exits 0", func(t *testing.T) {
		withBaselineGateServices(t, &depsMockFetcher{issues: map[string]*types.Issue{
			"o/r#5": {Number: 5, Body: trigger},
		}}, gateStubRunner{runs: runs("success", "success", "success", "success", "success")}, nil)
		if code, _ := runGateCmd(t, baselineGateCheckCmd(), args...); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
	})
}
