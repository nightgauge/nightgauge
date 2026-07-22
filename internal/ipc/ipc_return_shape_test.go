// Package ipc — IPC return shape tests.
//
// These tests verify that IPC method responses contain the expected JSON fields
// without requiring a real GitHub API. They use the real binary via ipcTestHarness
// with a fake GITHUB_TOKEN so GitHub-calling methods are excluded.
//
// Methods tested here succeed with only a fake token:
//   - intelligence.*: run purely in-process
//   - queue.*: in-memory scheduler
//   - execution.list: returns empty list (no active executions)
//   - pipeline.getState: returns null for unknown issues
//   - config.*: reads from on-disk config
//   - git.*: operates on paths passed as params
package ipc

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// assertResultShape verifies that result (after JSON round-trip) contains all
// required top-level fields and can be unmarshalled into target.
func assertResultShape(t *testing.T, result interface{}, target interface{}, fields []string) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("unmarshal result into target: %v", err)
		}
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal result into map: %v", err)
	}
	for _, f := range fields {
		if _, ok := m[f]; !ok {
			t.Errorf("result missing required field %q", f)
		}
	}
}

// ─── Intelligence method shape tests ──────────────────────────────────────

func TestShape_Intelligence_Complexity(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("intelligence.complexity", map[string]interface{}{
		"title":  "Add photo upload feature with drag-and-drop",
		"body":   "Implement upload with validation",
		"labels": []string{"type:feature", "size:M"},
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("intelligence.complexity error: %+v", resp.Error)
	}

	// complexity.Score fields: value (int), sizeLabel, confidence, reasoning
	var target struct {
		Value     int    `json:"value"`
		Reasoning string `json:"reasoning"`
	}
	assertResultShape(t, resp.Result, &target, []string{"value", "reasoning"})
	if target.Value < 1 {
		t.Errorf("value = %v, want >= 1", target.Value)
	}
	if target.Value < 1 {
		t.Error("reasoning must not be empty")
	}
}

func TestShape_Intelligence_Route(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("intelligence.route", map[string]interface{}{
		"complexityScore": 3,
		"stage":           "feature-dev",
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("intelligence.route error: %+v", resp.Error)
	}

	var target struct {
		Model string `json:"model"`
	}
	assertResultShape(t, resp.Result, &target, []string{"model"})
	if target.Model == "" {
		t.Error("model must not be empty")
	}
}

func TestShape_Intelligence_Classify(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("intelligence.classify", map[string]interface{}{
		"error":  "test suite failed: assertion error in TestFoo",
		"output": "Expected true but got false",
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("intelligence.classify error: %+v", resp.Error)
	}

	var target struct {
		Category string `json:"category"`
	}
	assertResultShape(t, resp.Result, &target, []string{"category"})
	if target.Category == "" {
		t.Error("category must not be empty")
	}
}

func TestShape_Intelligence_Cost(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("intelligence.cost", map[string]interface{}{
		"stages":          []string{"feature-dev", "feature-validate"},
		"complexityScore": 3,
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("intelligence.cost error: %+v", resp.Error)
	}

	// tokens.CostEstimate fields: totalCostUsd, totalDurationMinutes, stageBreakdown, confidence
	assertResultShape(t, resp.Result, nil, []string{"totalCostUsd", "confidence"})
}

func TestShape_Intelligence_Health(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("intelligence.health", map[string]interface{}{})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("intelligence.health error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"overallScore"})
}

// ─── Queue method shape tests ──────────────────────────────────────────────

func TestShape_Queue_List_Empty(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("queue.list", nil)
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("queue.list error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"status", "items"})

	data, _ := json.Marshal(resp.Result)
	var result struct {
		Status string        `json:"status"`
		Items  []interface{} `json:"items"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal queue.list result: %v", err)
	}
	if result.Status == "" {
		t.Error("queue.list status must not be empty")
	}
	if result.Items == nil {
		t.Error("queue.list items must be an array (not null)")
	}
}

func TestShape_Queue_Add_Then_List(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	const testIssue = 777

	addID := h.sendRequest("queue.add", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": testIssue,
	})
	addResp := h.readResponseFor(addID, nil)
	if addResp.Error != nil {
		t.Fatalf("queue.add error: %+v", addResp.Error)
	}

	listID := h.sendRequest("queue.list", nil)
	listResp := h.readResponseFor(listID, nil)
	if listResp.Error != nil {
		t.Fatalf("queue.list after add error: %+v", listResp.Error)
	}

	data, _ := json.Marshal(listResp.Result)
	var state struct {
		Items []struct {
			IssueNumber int `json:"issueNumber"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal queue state: %v", err)
	}

	found := false
	for _, item := range state.Items {
		if item.IssueNumber == testIssue {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("issue %d not found in queue after queue.add: %s", testIssue, string(data))
	}
}

// TestShape_Queue_Add_RemoteRunID_Then_List verifies that a remoteRunId passed
// to queue.add (the dashboard-trigger ack runId) is stored on the queue item
// and surfaced by queue.list. The scheduler prefers this RemoteRunID over a
// freshly-minted run id (#3557), so threading it through here is what keeps the
// command's ack runId identical to the synced pipeline-run id (#4120).
func TestShape_Queue_Add_RemoteRunID_Then_List(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	const testIssue = 778
	const wantRunID = "49b2019e-6ab7-4866-935e-235a32765bc7"

	addID := h.sendRequest("queue.add", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": testIssue,
		"remoteRunId": wantRunID,
	})
	addResp := h.readResponseFor(addID, nil)
	if addResp.Error != nil {
		t.Fatalf("queue.add error: %+v", addResp.Error)
	}

	listID := h.sendRequest("queue.list", nil)
	listResp := h.readResponseFor(listID, nil)
	if listResp.Error != nil {
		t.Fatalf("queue.list after add error: %+v", listResp.Error)
	}

	data, _ := json.Marshal(listResp.Result)
	var state struct {
		Items []struct {
			IssueNumber int    `json:"issueNumber"`
			RemoteRunID string `json:"remoteRunId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal queue state: %v", err)
	}

	var got string
	found := false
	for _, item := range state.Items {
		if item.IssueNumber == testIssue {
			found = true
			got = item.RemoteRunID
			break
		}
	}
	if !found {
		t.Fatalf("issue %d not found in queue after queue.add: %s", testIssue, string(data))
	}
	if got != wantRunID {
		t.Errorf("remoteRunId not preserved: got %q, want %q (state: %s)", got, wantRunID, string(data))
	}
}

// ─── Execution method shape tests ─────────────────────────────────────────

func TestShape_Execution_List(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("execution.list", nil)
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("execution.list error: %+v", resp.Error)
	}

	// execution.list returns an array (possibly empty or null when no executions)
	if resp.Result != nil {
		data, _ := json.Marshal(resp.Result)
		var result []interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Errorf("execution.list must return array or null, got: %s", string(data))
		}
	}
}

// ─── Pipeline method shape tests ──────────────────────────────────────────

func TestShape_Pipeline_GetState_Unknown(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("pipeline.getState", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": 88888,
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("pipeline.getState error: %+v", resp.Error)
	}
	if resp.Result != nil {
		t.Errorf("pipeline.getState for unknown issue must return null, got: %v", resp.Result)
	}
}

// ─── Workspace method shape tests ─────────────────────────────────────────

func TestShape_Workspace_SetRoot(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("workspace.setRoot", map[string]string{"root": "/tmp"})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("workspace.setRoot error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"ok"})
}

// ─── Config method shape tests ────────────────────────────────────────────

func TestShape_Config_GetProjectConfig(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("config.getProjectConfig", nil)
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("config.getProjectConfig error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"owner", "projectNumber"})

	data, _ := json.Marshal(resp.Result)
	var result struct {
		Owner         string `json:"owner"`
		ProjectNumber int    `json:"projectNumber"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal config result: %v", err)
	}
	if result.Owner == "" {
		t.Error("config.getProjectConfig owner must not be empty")
	}
	if result.ProjectNumber <= 0 {
		t.Errorf("config.getProjectConfig projectNumber = %d, want > 0", result.ProjectNumber)
	}
}

func TestShape_Config_GetHealthThresholds(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("config.getHealthThresholds", nil)
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("config.getHealthThresholds error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"warningThreshold", "criticalThreshold"})
}

// ─── Git method shape tests (with real temp git repo) ─────────────────────

// initTempGitRepo creates a temp directory with a real git repository.
// Returns the path to the repo root.
func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@example.com"},
		{"git", "-C", dir, "config", "user.name", "Test User"},
	}
	for _, c := range cmds {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("cmd %v: %v\n%s", c, err, out)
		}
	}

	// Create an initial commit so HEAD and branch exist
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, c := range [][]string{
		{"git", "-C", dir, "add", "."},
		{"git", "-C", dir, "commit", "-m", "init"},
	} {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("cmd %v: %v\n%s", c, err, out)
		}
	}

	return dir
}

func TestShape_Git_CurrentBranch(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	gitDir := initTempGitRepo(t)

	id := h.sendRequest("git.currentBranch", map[string]interface{}{
		"workDir": gitDir,
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("git.currentBranch error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"branch"})

	data, _ := json.Marshal(resp.Result)
	var result struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal git.currentBranch result: %v", err)
	}
	if result.Branch == "" {
		t.Error("git.currentBranch branch must not be empty")
	}
}

func TestShape_Git_Root(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	gitDir := initTempGitRepo(t)

	id := h.sendRequest("git.root", map[string]interface{}{
		"workDir": gitDir,
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("git.root error: %+v", resp.Error)
	}

	data, _ := json.Marshal(resp.Result)
	var result struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal git.root result: %v", err)
	}
	if result.Root == "" {
		t.Error("git.root must return non-empty path")
	}
}

func TestShape_Git_Status(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	gitDir := initTempGitRepo(t)

	id := h.sendRequest("git.status", map[string]interface{}{
		"workDir": gitDir,
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("git.status error: %+v", resp.Error)
	}

	assertResultShape(t, resp.Result, nil, []string{"isClean"})
}

func TestShape_Git_Log(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	gitDir := initTempGitRepo(t)

	id := h.sendRequest("git.log", map[string]interface{}{
		"workDir": gitDir,
		"limit":   5,
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("git.log error: %+v", resp.Error)
	}

	// git.log returns an array of log entries
	if resp.Result != nil {
		data, _ := json.Marshal(resp.Result)
		var result []interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Errorf("git.log must return array, got: %s", string(data))
		}
	}
}
