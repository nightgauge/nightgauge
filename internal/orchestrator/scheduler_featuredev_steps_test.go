package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// steppedPipelineRunner plays every stage of a run whose feature-dev is split
// into sub-sessions: planning writes a three-task plan, each feature-dev
// session writes one file and checks its task, and feature-validate fails so
// the run ends with feature-dev's record in place.
type steppedPipelineRunner struct {
	mu       sync.Mutex
	root     string
	devCalls int
}

func (r *steppedPipelineRunner) HonoursPrompt() bool { return true }

func (r *steppedPipelineRunner) RunStage(_ context.Context, p StageRunParams) (*StageRunResult, error) {
	switch p.Stage {
	case state.StageFeatureDev:
		r.mu.Lock()
		r.devCalls++
		k := r.devCalls
		r.mu.Unlock()
		if err := os.WriteFile(filepath.Join(r.root, fmt.Sprintf("step%d.go", k)), []byte("package x\n"), 0o644); err != nil {
			return nil, err
		}
		plan := filepath.Join(r.root, "docs", "plan.md")
		body, _ := os.ReadFile(plan)
		_ = os.WriteFile(plan, []byte(strings.Replace(string(body), "- [ ]", "- [x]", 1)), 0o644)
		return &StageRunResult{InputTokens: 1000 * k, OutputTokens: 100 * k, CacheReadTokens: 10 * k}, nil
	case state.StageFeatureValidate:
		return &StageRunResult{ExitCode: 1, ErrorText: "validation failed (fixture)"}, nil
	}
	payload := map[string]any{
		"schema_version": "1.0",
		"issue_number":   p.IssueNumber,
		"ok":             true,
	}
	if p.Stage == state.StageFeaturePlanning {
		plan := filepath.Join(r.root, "docs", "plan.md")
		_ = os.MkdirAll(filepath.Dir(plan), 0o755)
		_ = os.WriteFile(plan, []byte("# Plan\n- [ ] First\n- [ ] Second\n- [ ] Third\n"), 0o644)
		payload["plan_file"] = "docs/plan.md"
	}
	if p.OutputFile != "" {
		_ = os.MkdirAll(filepath.Dir(p.OutputFile), 0o755)
		data, _ := json.Marshal(payload)
		_ = os.WriteFile(p.OutputFile, data, 0o644)
	}
	return &StageRunResult{}, nil
}

var subSessionPhaseRE = regexp.MustCompile(`^sub-session-(\d+) \(in=(\d+) out=(\d+) cache_read=(\d+)\)$`)

// AC6: the durable run record carries one phase entry per sub-session, with a
// duration and its tokens, attributed to feature-dev, and the tokens sum to
// the stage's own total. The feature-dev gate runs once, after the last step.
func TestScheduler_FeatureDevSubSessions_RecordedAsPhases(t *testing.T) {
	stubReconcileGhUnreachable(t)
	orig := resolveFeatureDevStepPolicy
	resolveFeatureDevStepPolicy = func(int) featureDevStepPolicy {
		return featureDevStepPolicy{Enabled: true, HardCap: featureDevSubSessionHardCap}
	}
	t.Cleanup(func() { resolveFeatureDevStepPolicy = orig })

	root := gitWorkspace(t)
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "fixture")

	runner := &steppedPipelineRunner{root: root}
	s := &Scheduler{
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(root, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
	s.WithStageGates(map[state.PipelineStage]gates.StageGate{
		state.StageFeatureDev: gates.FeatureDevGate{},
	})

	item := types.BoardItem{Number: 1651, Repo: "nightgauge/test", ID: "item-1651"}
	s.runPipeline(context.Background(), item)

	if runner.devCalls != 3 {
		t.Fatalf("feature-dev sessions = %d, want 3", runner.devCalls)
	}
	var rec *state.V2RunRecord
	records := readDailyJSONLRecords(t, root)
	for i := range records {
		if records[i].IssueNumber == item.Number {
			rec = &records[i]
		}
	}
	if rec == nil {
		t.Fatalf("no run record for #%d", item.Number)
	}
	detail, ok := rec.Stages[string(state.StageFeatureDev)]
	if !ok {
		t.Fatal("run record has no feature-dev stage")
	}
	if n := len(detail.GateResults); n != 1 {
		t.Errorf("feature-dev gate ran %d times, want once after the last step", n)
	}

	var in, out, cr int
	seen := map[int]bool{}
	for _, ph := range detail.Phases {
		m := subSessionPhaseRE.FindStringSubmatch(ph.Name)
		if m == nil {
			continue
		}
		k, _ := strconv.Atoi(m[1])
		seen[k] = true
		if ph.Status != "complete" {
			t.Errorf("%s status = %q, want complete", ph.Name, ph.Status)
		}
		if ph.StartedAt == "" || ph.CompletedAt == "" {
			t.Errorf("%s has no measured bounds", ph.Name)
		}
		a, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		c, _ := strconv.Atoi(m[4])
		in, out, cr = in+a, out+b, cr+c
	}
	if len(seen) != 3 || !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("sub-session phases = %v, want sub-session-1..3; phases: %+v", seen, detail.Phases)
	}
	tok := rec.Tokens.PerStage[string(state.StageFeatureDev)]
	if in != tok.Input || out != tok.Output || cr != tok.CacheRead {
		t.Errorf("sub-session tokens %d/%d/%d != stage total %d/%d/%d", in, out, cr, tok.Input, tok.Output, tok.CacheRead)
	}
	if tok.Input != 6000 {
		t.Errorf("stage input tokens = %d, want 6000 (1000+2000+3000)", tok.Input)
	}
}
