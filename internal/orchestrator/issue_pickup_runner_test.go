package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

func fakePickupRunner(ensureErr error) (*deterministicIssuePickup, *int32) {
	var pushes int32
	return &deterministicIssuePickup{
		ensure: func(_ string, branch string, _ int, _ func() (string, error)) (git.IssueBranchResult, error) {
			if ensureErr != nil {
				return git.IssueBranchResult{}, ensureErr
			}
			return git.IssueBranchResult{Branch: branch, BaseBranch: "main", Action: "created"}, nil
		},
		push: func(string, string) error { atomic.AddInt32(&pushes, 1); return nil },
		now:  time.Now,
	}, &pushes
}

func pickupInput(dir string) IssuePickupInput {
	return IssuePickupInput{
		Issue: &types.Issue{
			Number: 1904,
			Title:  "issue-pickup has no deterministic runner on the Go path",
			Body:   "## Summary\nline\n\n- [ ] first criterion\n- [x] second criterion\n",
			Labels: []string{"type:bug", "size:M"},
		},
		Dir: dir,
		Routing: routing.Derive(routing.DeriveInput{
			Title:  "issue-pickup has no deterministic runner",
			Labels: []string{"type:bug", "size:M"},
		}),
		DevModel: "sonnet",
	}
}

// The runner produces a context file the real IssuePickupGate accepts, with no
// model anywhere in the path.
func TestDeterministicIssuePickup_WritesGatePassingContext(t *testing.T) {
	dir := t.TempDir()
	r, pushes := fakePickupRunner(nil)
	res, err := r.Run(context.Background(), pickupInput(dir))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasPrefix(res.Branch, "fix/1904-") {
		t.Errorf("branch = %q, want the shared fix/1904-… derivation", res.Branch)
	}
	if atomic.LoadInt32(pushes) != 1 {
		t.Errorf("expected the branch to be pushed once")
	}
	gr := (gates.IssuePickupGate{}).Verify(context.Background(), 1904, dir)
	if !gr.Passed {
		t.Fatalf("gate failed on the deterministic context: %+v", gr)
	}

	data, err := os.ReadFile(res.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["branch"].(string); !ok {
		t.Fatalf("branch must be a JSON string, got %T", raw["branch"])
	}
	if raw["type"] != "bug" || raw["base_branch"] != "main" {
		t.Errorf("type/base_branch = %v/%v", raw["type"], raw["base_branch"])
	}
	rt, ok := raw["routing"].(map[string]interface{})
	if !ok {
		t.Fatal("routing must be populated deterministically")
	}
	if cs, _ := rt["complexity_score"].(float64); cs < 1 || cs > 8 {
		t.Errorf("complexity_score = %v, want 1..8", rt["complexity_score"])
	}
	pr, _ := rt["pickup_recommendation"].(map[string]interface{})
	if pr == nil || pr["dev_model"] != "sonnet" {
		t.Errorf("pickup_recommendation = %v", rt["pickup_recommendation"])
	}
	req := raw["requirements"].(map[string]interface{})
	if ac, _ := req["acceptance_criteria"].([]interface{}); len(ac) != 2 {
		t.Errorf("acceptance_criteria = %v", req["acceptance_criteria"])
	}
	// The score and dev model the outcome recorder reads come back out.
	score, _, model := loadIssueContext(dir, "", "", 1904)
	if score < 1 || model != "sonnet" {
		t.Errorf("loadIssueContext = %d/%q", score, model)
	}
	if loadFeatureBranch(dir, 1904) != res.Branch {
		t.Errorf("loadFeatureBranch disagrees with the runner")
	}
}

// Keys the runner does not author (knowledge_path stamped on a prior attempt)
// survive; keys it does author are replaced, including a malformed branch.
func TestDeterministicIssuePickup_MergesOverExistingContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "issue-1904.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The 2026-09-20 failure shape: branch as an object.
	if err := os.WriteFile(path, []byte(`{"branch":{"name":"x"},"knowledge_path":"/kb/1904"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := fakePickupRunner(nil)
	if _, err := r.Run(context.Background(), pickupInput(dir)); err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["branch"].(string); !ok {
		t.Fatalf("branch = %T, want string", raw["branch"])
	}
	if raw["knowledge_path"] != "/kb/1904" {
		t.Errorf("knowledge_path not preserved: %v", raw["knowledge_path"])
	}
	if gr := (gates.IssuePickupGate{}).Verify(context.Background(), 1904, dir); !gr.Passed {
		t.Error("gate must pass after the runner rewrote a malformed file")
	}
}

// A reader polling the file while the runner rewrites it never sees it empty
// or unparseable.
func TestDeterministicIssuePickup_ContextNeverObservedPartial(t *testing.T) {
	dir := t.TempDir()
	r, _ := fakePickupRunner(nil)
	in := pickupInput(dir)
	in.Issue.Body = strings.Repeat("body line\n", 20000) // large enough to span writes
	if _, err := r.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".nightgauge", "pipeline", "issue-1904.json")

	stop := make(chan struct{})
	var bad atomic.Value
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				bad.Store("read error: " + err.Error())
				return
			}
			var v struct {
				Branch string `json:"branch"`
			}
			if len(data) == 0 || json.Unmarshal(data, &v) != nil || v.Branch == "" {
				bad.Store("observed empty or partial context")
				return
			}
		}
	}()
	for i := 0; i < 30; i++ {
		if _, err := r.Run(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if msg, _ := bad.Load().(string); msg != "" {
		t.Fatal(msg)
	}
}

// A branch-creation failure is an error (the scheduler punts to the skill),
// and no context file is written for a branch that does not exist.
func TestDeterministicIssuePickup_BranchFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	r, _ := fakePickupRunner(errors.New("boom"))
	if _, err := r.Run(context.Background(), pickupInput(dir)); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".nightgauge", "pipeline", "issue-1904.json")); !os.IsNotExist(err) {
		t.Fatalf("no context file may be written when the branch was not created (stat err %v)", err)
	}
}

// An untitled issue cannot name a branch (#1915): the runner refuses.
func TestDeterministicIssuePickup_RefusesUnnameableIssue(t *testing.T) {
	r, _ := fakePickupRunner(nil)
	in := pickupInput(t.TempDir())
	in.Issue.Title = ""
	if _, err := r.Run(context.Background(), in); err == nil {
		t.Fatal("expected an error for an issue with no title")
	}
}

func TestIssueTypeFromBranch(t *testing.T) {
	for branch, want := range map[string]string{
		"fix/1-a": "bug", "docs/1-a": "docs", "chore/1-a": "chore",
		"refactor/1-a": "refactor", "feat/1-a": "feature", "": "feature",
	} {
		if got := issueTypeFromBranch(branch); got != want {
			t.Errorf("%q → %q, want %q", branch, got, want)
		}
	}
	_ = state.StageIssuePickup
}
