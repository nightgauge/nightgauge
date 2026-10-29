package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Recovering the issue context a skill-path pickup left broken (#1919).
//
// #1930 covered the first mechanism below; the rest of #1919's failures were a
// context file the model never wrote, or wrote as invalid or schema-mismatched
// JSON, and recoverIssueContextAtPickup now rebuilds those deterministically.
//
// The skill assigns BRANCH_NAME in its branch-creation phase and reads it back
// several phases later, in a DIFFERENT shell. A shell variable does not survive
// between tool calls, so `${BRANCH_NAME:-}` wrote "" and the post-condition gate
// called an otherwise-complete stage a no-op — then the orchestrator escalated
// and paid for the same stage on a larger model, for a branch that existed in
// git the whole time.
//
// The fixture is a REAL linked worktree checked out on the feature branch,
// because "what HEAD names in the run's worktree" is the entire mechanism; two
// plain temp directories cannot tell the fix from the bug.

// branchStampFixture returns (mainCheckout, worktree) with the worktree checked
// out on branchName.
func branchStampFixture(t *testing.T, branchName string) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "init", "-b", "main")
	gittest.Run(t, root, "config", "user.email", "test@test")
	gittest.Run(t, root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-m", "initial")
	wt := filepath.Join(root, ".worktrees", "issue-under-test")
	gittest.Run(t, root, "worktree", "add", wt, "-b", branchName)
	return root, wt
}

var recoveryItem = types.BoardItem{Number: 1919, Repo: "nightgauge/nightgauge"}

// recoveryScheduler serves issue #1919 from a mock, the read recovery rebuilds
// a missing or broken context from.
func recoveryScheduler() *Scheduler {
	issues := newMockIssueSvc()
	issues.addIssue("nightgauge", "nightgauge", 1919, &types.Issue{
		Number: 1919,
		Title:  "fix(pipeline): issue-pickup fails more often than it succeeds",
		Body:   "Summary line\n\n- [ ] every exit carries a gate reason",
		Labels: []string{"type:bug"},
	})
	return &Scheduler{issueSvc: issues}
}

func contextPathIn(t *testing.T, worktree string) string {
	t.Helper()
	dir := layouttest.PipelineDir(t, worktree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "issue-1919.json")
}

func plantContext(t *testing.T, worktree, body string) string {
	t.Helper()
	path := contextPathIn(t, worktree)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readContextMap(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read context file: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("context file is not valid JSON: %v\n%s", err, data)
	}
	return m
}

func pickupGate(worktree string) gates.GateResult {
	return gates.IssuePickupGate{}.Verify(context.Background(), 1919, worktree)
}

// assertRebuilt checks the document is the deterministic builder's, on HEAD's
// branch, and that the gate the stage is judged by now passes it.
func assertRebuilt(t *testing.T, worktree, path, branch string) map[string]interface{} {
	t.Helper()
	m := readContextMap(t, path)
	if m["branch"] != branch {
		t.Errorf("branch = %v, want %q from HEAD", m["branch"], branch)
	}
	if m["issue_number"] != float64(1919) {
		t.Errorf("issue_number = %v, want 1919", m["issue_number"])
	}
	if m["created_by"] != "issue-pickup-recovery" {
		t.Errorf("created_by = %v, want the recovery's own marker", m["created_by"])
	}
	if !strings.HasPrefix(m["title"].(string), "fix(pipeline): issue-pickup") {
		t.Errorf("title = %v, want the fetched issue's title", m["title"])
	}
	if res := pickupGate(worktree); !res.Passed {
		t.Errorf("gate still fails after recovery: %s", res.Reason)
	}
	return m
}

func TestRecoverIssueContext_RebuildsAMissingFile(t *testing.T) {
	root, wt := branchStampFixture(t, "fix/1919-pickup-context")
	path := contextPathIn(t, wt)
	if res := pickupGate(wt); res.Passed || res.Reason != "issue context file missing" {
		t.Fatalf("precondition: gate = (%t, %q), want it failing on the missing file", res.Passed, res.Reason)
	}

	if got := recoveryScheduler().recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != issueContextMissing {
		t.Errorf("fault = %q, want %q", got, issueContextMissing)
	}
	m := assertRebuilt(t, wt, path, "fix/1919-pickup-context")
	if m["type"] != "bug" {
		t.Errorf("type = %v, want bug from the fix/ prefix", m["type"])
	}
}

func TestRecoverIssueContext_RewritesInvalidJSON(t *testing.T) {
	root, wt := branchStampFixture(t, "fix/1919-pickup-context")
	path := plantContext(t, wt, `{"schema_version":"1.5","issue_number":1919,"branch":`)

	if got := recoveryScheduler().recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != issueContextInvalidJSON {
		t.Errorf("fault = %q, want %q", got, issueContextInvalidJSON)
	}
	assertRebuilt(t, wt, path, "fix/1919-pickup-context")
}

func TestRecoverIssueContext_RewritesASchemaMismatchAndKeepsOtherKeys(t *testing.T) {
	// The #1904 shape: `branch` serialised as an object, not a string.
	root, wt := branchStampFixture(t, "fix/1919-pickup-context")
	path := plantContext(t, wt, `{"issue_number":1919,"branch":{"name":"fix/1919-pickup-context"},"knowledge_path":"/kb/1919"}`)

	if got := recoveryScheduler().recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != issueContextSchema {
		t.Errorf("fault = %q, want %q", got, issueContextSchema)
	}
	m := assertRebuilt(t, wt, path, "fix/1919-pickup-context")
	if m["knowledge_path"] != "/kb/1919" {
		t.Errorf("knowledge_path = %v, want a key the builder does not own preserved", m["knowledge_path"])
	}
}

func TestRecoverIssueContext_StampsOnlyTheBranchOnAnOtherwiseValidFile(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", `{"schema_version":"1.5","issue_number":1919,"title":"the skill's own title","branch":""}`},
		{"absent", `{"schema_version":"1.5","issue_number":1919,"title":"the skill's own title"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, wt := branchStampFixture(t, "feat/1919-pickup-branch")
			path := plantContext(t, wt, tc.body)
			// No issue service: a stamp must not need the fetch.
			s := &Scheduler{}

			if got := s.recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != issueContextNoBranch {
				t.Errorf("fault = %q, want %q", got, issueContextNoBranch)
			}
			m := readContextMap(t, path)
			if m["branch"] != "feat/1919-pickup-branch" {
				t.Errorf("branch = %v, want it recovered from HEAD", m["branch"])
			}
			if m["title"] != "the skill's own title" {
				t.Errorf("title = %v, want everything else the skill wrote preserved", m["title"])
			}
			if res := pickupGate(wt); !res.Passed {
				t.Errorf("gate still fails after the stamp: %s", res.Reason)
			}
		})
	}
}

func TestRecoverIssueContext_NeverTouchesAValidFile(t *testing.T) {
	// Including a wrong-looking branch: an empty slug is #1915's defect, and
	// silently rewriting it here would hide that bug rather than fix it.
	root, wt := branchStampFixture(t, "fix/1919-full-slug")
	body := `{"schema_version":"1.5","issue_number":1919,"branch":"fix/1919-"}`
	path := plantContext(t, wt, body)

	if got := recoveryScheduler().recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != "" {
		t.Errorf("fault = %q, want none", got)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, []byte(body)) {
		t.Errorf("valid context rewritten:\n%s", data)
	}
}

func TestRecoverIssueContext_RecoversNothingWhenHEADIsNotThisIssuesBranch(t *testing.T) {
	// No branch for this issue was ever created, so the stage really did
	// produce nothing and the gate must still fail it.
	for _, tc := range []struct{ name, body string }{
		{"missing", ""},
		{"no-branch", `{"schema_version":"1.5","issue_number":1919,"branch":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, wt := branchStampFixture(t, "chore/4242-unrelated")
			path := contextPathIn(t, wt)
			if tc.body != "" {
				plantContext(t, wt, tc.body)
			}

			if got := recoveryScheduler().recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != "" {
				t.Errorf("fault = %q, want nothing recovered", got)
			}
			if tc.body == "" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("context file written for another issue's branch (stat err=%v)", err)
				}
			} else if m := readContextMap(t, path); m["branch"] != "" {
				t.Errorf("branch = %v, want nothing stamped from another issue's branch", m["branch"])
			}
			if res := pickupGate(wt); res.Passed {
				t.Error("gate passed a stage that created no branch for this issue")
			}
		})
	}
}

func TestRecoverIssueContext_IssueFetchFailureWritesNothing(t *testing.T) {
	root, wt := branchStampFixture(t, "fix/1919-pickup-context")
	path := contextPathIn(t, wt)
	s := &Scheduler{issueSvc: newMockIssueSvc()} // serves no issues

	if got := s.recoverIssueContextAtPickup(context.Background(), root, wt, recoveryItem); got != "" {
		t.Errorf("fault = %q, want nothing recovered without the issue", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("context file written without the issue (stat err=%v)", err)
	}
	if res := pickupGate(wt); res.Reason != "issue context file missing" {
		t.Errorf("gate reason = %q, want the original verdict left to the gate", res.Reason)
	}
}

// The call-site guard: only a skill-path pickup that reported success is
// repaired, and the repair is recorded on the stage's evidence.
func TestRecoverPickupAfterSkill_Guards(t *testing.T) {
	stage := state.StageIssuePickup
	cases := []struct {
		name      string
		execPath  string
		stageErr  error
		exitCode  int
		wantFault string
	}{
		{name: "skill path, success", execPath: "llm", wantFault: issueContextMissing},
		{name: "deterministic path", execPath: "deterministic"},
		{name: "stage error", execPath: "llm", stageErr: errors.New("adapter failed")},
		{name: "non-zero exit", execPath: "llm", exitCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, wt := branchStampFixture(t, "fix/1919-pickup-context")
			rs := state.NewRuntimeState("nightgauge/nightgauge", 1919, "item-1", testRunID())
			rs.SetWorktree(wt)
			rs.RecordExecutionPath(stage, tc.execPath)

			got := recoveryScheduler().recoverPickupAfterSkill(context.Background(), rs, tc.stageErr, tc.exitCode, root, recoveryItem)
			if got != tc.wantFault {
				t.Errorf("fault = %q, want %q", got, tc.wantFault)
			}
			_, statErr := os.Stat(contextPathIn(t, wt))
			if wrote := statErr == nil; wrote != (tc.wantFault != "") {
				t.Errorf("context written = %t, want %t", wrote, tc.wantFault != "")
			}
			tail := rs.StageOutputTails[string(stage)]
			if hasNote := strings.Contains(tail, "issue context recovered (missing)"); hasNote != (tc.wantFault != "") {
				t.Errorf("stage evidence = %q, want the recovery recorded only when it ran", tail)
			}
		})
	}
}
