// Package ipc — Cross-language contract tests for all registered IPC methods.
//
// These tests verify that every method registered in server.go:
//  1. Exists in the compiled binary (does NOT return -32601 method-not-found)
//  2. Accepts valid-typed params without crashing the binary
//  3. Is enumerated in contractTestedMethods (enforced by TestContractCoverage)
//
// Binary is built once in TestMain (server_integration_test.go). This file
// shares binaryPath and ipcTestHarness from the same package.
//
// Test structure: one TestContract_* function per method group. Each function
// creates its own harness so groups are isolated. Within a group, t.Run
// subtests share a single harness and run sequentially.
//
// Key assertion: assertMethodRegistered — verifies the response is NOT
// ErrMethodNotFound (-32601). A -32603 (internal) error is acceptable: it
// means the method ran and failed due to a fake GitHub token or missing git
// repo, which is expected in the test environment.
package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// contractTestedMethods is the authoritative set of all registered IPC method
// names. TestContractCoverage parses server.go and fails if any s.methods[...]
// registration is absent from this map.
//
// When adding a new IPC method:
//  1. Add its name here.
//  2. Add a t.Run subtest in the appropriate TestContract_* function below.
var contractTestedMethods = map[string]bool{
	// Config
	"config.getProjectConfig":    true,
	"config.getHealthThresholds": true,
	"config.tierAudit":           true,
	// Board
	"board.counts":       true,
	"board.list":         true,
	"board.updateStatus": true,
	// Branch
	"branch.cleanup": true,
	// Epic
	"epic.appendContext":    true,
	"epic.checkCompletion":  true,
	"epic.createPR":         true,
	"epic.mergePR":          true,
	"epic.progress":         true,
	"epic.readContext":      true,
	"epic.transitionStatus": true,
	// Execution
	"execution.list": true,
	// Git
	"git.abortPipeline":         true,
	"git.branchCleanup":         true,
	"git.branchCreate":          true,
	"git.branchDelete":          true,
	"git.cleanupMergedBranches": true,
	"git.checkout":              true,
	"git.commit":                true,
	"git.currentBranch":         true,
	"git.diff":                  true,
	"git.fetch":                 true,
	"git.listRemoteBranches":    true,
	"git.log":                   true,
	"git.push":                  true,
	"git.resetPipeline":         true,
	"git.root":                  true,
	"git.status":                true,
	// Intelligence
	"intelligence.classify":   true,
	"intelligence.complexity": true,
	"intelligence.cost":       true,
	"intelligence.health":     true,
	"intelligence.route":      true,
	// Issue
	"issue.close":           true,
	"issue.create":          true,
	"issue.createSubIssue":  true,
	"issue.linkSubIssue":    true,
	"issue.list":            true,
	"issue.removeBlockedBy": true,
	"issue.reopen":          true,
	"issue.view":            true,
	"issue.viewMany":        true,
	// Action Center (ADR 015)
	"attention.list":        true,
	"attention.resolve":     true,
	"attention.acknowledge": true,
	// Pipeline
	"pipeline.cancelActiveForNetworkOutage": true,
	"pipeline.getState":                     true,
	"pipeline.notifyComplete":               true,
	"pipeline.notifyPhaseTransition":        true,
	"pipeline.notifyStageProgress":          true,
	"pipeline.notifyStageTransition":        true,
	"pipeline.pause":                        true,
	"pipeline.resume":                       true,
	"pipeline.run":                          true,
	"pipeline.runItem":                      true,
	"pipeline.setPaused":                    true,
	"pipeline.status":                       true,
	"pipeline.stop":                         true,
	// Auth
	"auth.deviceFlowPoll":  true,
	"auth.deviceFlowStart": true,
	"auth.exchangeGitHub":  true,
	"auth.refresh":         true,
	// Platform
	"platform.authDeviceCode":      true,
	"platform.authDeviceToken":     true,
	"platform.authGithub":          true,
	"platform.authRefresh":         true,
	"platform.authSignout":         true,
	"platform.createPortalSession": true,
	"platform.getTeamMembers":      true,
	"platform.getUsageSummary":     true,
	"platform.healthCheck":         true,
	"platform.syncTelemetry":       true,
	"platform.license":             true,
	"platform.resolveSkill":        true,
	"platform.status":              true,
	"platform.submitAnalytics":     true,
	"platform.validateLicense":     true,
	"platform.startTrial":          true,
	"platform.getCostAnalytics":    true,
	"platform.getAnalyticsHealth":  true,
	"platform.getAnalyticsRuns":    true,
	"platform.getAnalyticsTrends":  true,
	"platform.auditGenerateReport": true,
	"platform.auditListReports":    true,
	"platform.auditGetReport":      true,
	"audit.getRetentionConfig":     true,
	"audit.updateRetentionConfig":  true,
	"audit.verifyIntegrity":        true,
	// PR
	"pr.create": true,
	"pr.list":   true,
	"pr.merge":  true,
	"pr.view":   true,
	// Project
	"project.addItem":       true,
	"project.setHours":      true,
	"project.syncIteration": true,
	"project.syncStatus":    true,
	// Remote
	"remote.getCommandHistory": true,
	"remote.getPollingStatus":  true,
	// Workspace
	"workspace.setRoot":                true,
	"workspace.registerRepo":           true,
	"workspace.configureForgeInstance": true,
	// Wave orchestration
	"wave.status": true,
	// Queue
	"queue.add":                true,
	"queue.clear":              true,
	"queue.dequeueIndependent": true,
	"queue.enqueueEpic":        true,
	"queue.list":               true,
	"queue.remove":             true,
	// Autonomous
	"autonomous.start":              true,
	"autonomous.pause":              true,
	"autonomous.resume":             true,
	"autonomous.stop":               true,
	"autonomous.complete":           true,
	"autonomous.status":             true,
	"autonomous.stuckEpics":         true,
	"autonomous.rescan":             true,
	"autonomous.updateAllowlist":    true,
	"autonomous.clearIssueFailures": true,
	"autonomous.clearQuotaCooldown": true,
	// Pipeline config
	"pipeline.setMaxConcurrent": true,
	"pipeline.getMaxConcurrent": true,
	// Focus
	"focus.set":   true,
	"focus.show":  true,
	"focus.clear": true,
	"focus.list":  true,
	// Knowledge
	"knowledge.metrics":        true,
	"knowledge.search":         true, // Issue #2964
	"knowledge.backlinks":      true, // Issue #2964
	"knowledge.relatedToIssue": true, // Issue #2964
	// Diagnostics
	"diagnostics.recordStageExit": true, // Issue #3619
	// Forge
	"forge.list":           true,
	"forge.connectionTest": true,
	// GitHub
	"github.rateLimit": true,
	"github.authCheck": true,
	// Notifications
	"notifications.reloadTokens":       true,
	"notifications.checkAuthorization": true,
	// Agent
	"agent.acknowledgeCommand": true, // Issue #3551
	// Workflow
	"workflow.quotaState": true, // Issue #3909
}

// assertMethodRegistered fails the test if resp indicates the method was not
// found in the binary (-32601). A nil error (success) or -32603 (internal
// error — fake token, no git repo, missing upstream state) both pass, because
// they prove the method is registered and executed.
func assertMethodRegistered(t *testing.T, resp Response, method string) {
	t.Helper()
	if resp.Error != nil && resp.Error.Code == ErrMethodNotFound {
		t.Errorf("%s: method not found in binary (-32601) — codegen/registration drift", method)
	}
}

// ─── CI enforcement ────────────────────────────────────────────────────────

// TestContractCoverage parses server.go for all s.methods["xxx"] registrations
// and fails if any registered method is absent from contractTestedMethods.
// This ensures that adding a new IPC method without a contract test breaks CI.
func TestContractCoverage(t *testing.T) {
	repoRoot := findRepoRoot()
	serverFile := filepath.Join(repoRoot, "internal", "ipc", "server.go")

	content, err := os.ReadFile(serverFile)
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}

	// Match all s.methods["method.name"] = ... registrations.
	re := regexp.MustCompile(`s\.methods\["([^"]+)"\]`)
	matches := re.FindAllSubmatch(content, -1)

	var missing []string
	for _, m := range matches {
		name := string(m[1])
		if !contractTestedMethods[name] {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("IPC methods registered in server.go but absent from contractTestedMethods:\n  %s\n\n"+
			"Add each method to contractTestedMethods AND add a t.Run subtest in the\n"+
			"appropriate TestContract_* function in ipc_contract_test.go.",
			strings.Join(missing, "\n  "))
	}
}

// ─── Workspace ─────────────────────────────────────────────────────────────

func TestContract_Workspace(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("workspace.setRoot/registered", func(t *testing.T) {
		id := h.sendRequest("workspace.setRoot", map[string]string{"root": "/tmp"})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "workspace.setRoot")
	})

	t.Run("workspace.registerRepo/registered", func(t *testing.T) {
		id := h.sendRequest("workspace.registerRepo", map[string]interface{}{
			"owner": "test-org",
			"repo":  "test-repo",
			"path":  "/tmp",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "workspace.registerRepo")
	})

	t.Run("workspace.configureForgeInstance/registered", func(t *testing.T) {
		id := h.sendRequest("workspace.configureForgeInstance", map[string]interface{}{
			"owner": "test-org",
			"repo":  "test-repo",
			"kind":  "github",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "workspace.configureForgeInstance")
	})
}

// ─── Config ────────────────────────────────────────────────────────────────

func TestContract_Config(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("config.getProjectConfig/registered", func(t *testing.T) {
		id := h.sendRequest("config.getProjectConfig", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "config.getProjectConfig")
	})

	t.Run("config.getHealthThresholds/registered", func(t *testing.T) {
		id := h.sendRequest("config.getHealthThresholds", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "config.getHealthThresholds")
	})

	t.Run("config.tierAudit/registered", func(t *testing.T) {
		id := h.sendRequest("config.tierAudit", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "config.tierAudit")
	})
}

// ─── Notifications ─────────────────────────────────────────────────────────

func TestContract_Notifications(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("notifications.reloadTokens/registered", func(t *testing.T) {
		id := h.sendRequest("notifications.reloadTokens", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "notifications.reloadTokens")
	})

	t.Run("notifications.checkAuthorization/registered", func(t *testing.T) {
		id := h.sendRequest("notifications.checkAuthorization", map[string]interface{}{
			"mattermostUserId": "U04TEST000",
			"commandType":      "status",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "notifications.checkAuthorization")
	})
}

// ─── Board ─────────────────────────────────────────────────────────────────

func TestContract_Board(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("board.list/registered", func(t *testing.T) {
		id := h.sendRequest("board.list", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "board.list")
	})

	t.Run("board.counts/registered", func(t *testing.T) {
		id := h.sendRequest("board.counts", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "board.counts")
	})

	t.Run("board.updateStatus/registered", func(t *testing.T) {
		id := h.sendRequest("board.updateStatus", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
			"itemId": "PVI_contract_test", "status": "Ready",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "board.updateStatus")
	})
}

// ─── GitHub ────────────────────────────────────────────────────────────────

func TestContract_GitHub(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("github.rateLimit/registered", func(t *testing.T) {
		id := h.sendRequest("github.rateLimit", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "github.rateLimit")
	})

	t.Run("github.authCheck/registered", func(t *testing.T) {
		id := h.sendRequest("github.authCheck", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "github.authCheck")
	})
}

// ─── Workflow ──────────────────────────────────────────────────────────────

func TestContract_Workflow(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// #3909 — the workflow quota bridge the WorkflowExecutor (#3908) consults.
	t.Run("workflow.quotaState/registered", func(t *testing.T) {
		id := h.sendRequest("workflow.quotaState", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "workflow.quotaState")
	})
}

// ─── Forge ─────────────────────────────────────────────────────────────────

func TestContract_Forge(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("forge.list/registered", func(t *testing.T) {
		id := h.sendRequest("forge.list", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "forge.list")
	})

	t.Run("forge.connectionTest/registered", func(t *testing.T) {
		id := h.sendRequest("forge.connectionTest", map[string]interface{}{
			"instance_id": "test-forge",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "forge.connectionTest")
	})
}

// ─── Issue ─────────────────────────────────────────────────────────────────

func TestContract_Issue(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("issue.view/registered", func(t *testing.T) {
		id := h.sendRequest("issue.view", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "number": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.view")
	})

	t.Run("issue.viewMany/registered", func(t *testing.T) {
		id := h.sendRequest("issue.viewMany", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "numbers": []int{1, 2, 3},
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.viewMany")
	})

	t.Run("issue.list/registered", func(t *testing.T) {
		id := h.sendRequest("issue.list", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.list")
	})

	t.Run("issue.create/registered", func(t *testing.T) {
		id := h.sendRequest("issue.create", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
			"title": "Contract test issue", "body": "Created by contract tests",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.create")
	})

	t.Run("issue.close/registered", func(t *testing.T) {
		id := h.sendRequest("issue.close", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "number": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.close")
	})

	t.Run("issue.removeBlockedBy/registered", func(t *testing.T) {
		id := h.sendRequest("issue.removeBlockedBy", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "blockedNumber": 2, "blockerNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.removeBlockedBy")
	})

	t.Run("issue.reopen/registered", func(t *testing.T) {
		id := h.sendRequest("issue.reopen", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "number": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.reopen")
	})

	t.Run("issue.createSubIssue/registered", func(t *testing.T) {
		id := h.sendRequest("issue.createSubIssue", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
			"epicNumber": 1, "title": "Sub-issue", "body": "Contract test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.createSubIssue")
	})

	t.Run("issue.linkSubIssue/registered", func(t *testing.T) {
		id := h.sendRequest("issue.linkSubIssue", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
			"epicNumber": 1, "issueNumber": 2,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "issue.linkSubIssue")
	})
}

// ─── PR ────────────────────────────────────────────────────────────────────

func TestContract_PR(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("pr.view/registered", func(t *testing.T) {
		id := h.sendRequest("pr.view", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "number": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pr.view")
	})

	t.Run("pr.list/registered", func(t *testing.T) {
		id := h.sendRequest("pr.list", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pr.list")
	})

	t.Run("pr.create/registered", func(t *testing.T) {
		id := h.sendRequest("pr.create", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
			"title": "Contract test PR", "body": "Contract test",
			"headRef": "feat/contract-test", "baseRef": "main",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pr.create")
	})

	t.Run("pr.merge/registered", func(t *testing.T) {
		id := h.sendRequest("pr.merge", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "prNodeId": "PR_FAKE",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pr.merge")
	})
}

// ─── Epic ──────────────────────────────────────────────────────────────────

func TestContract_Epic(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("epic.progress/registered", func(t *testing.T) {
		id := h.sendRequest("epic.progress", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "number": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.progress")
	})

	t.Run("epic.checkCompletion/registered", func(t *testing.T) {
		id := h.sendRequest("epic.checkCompletion", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "number": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.checkCompletion")
	})

	t.Run("epic.transitionStatus/registered", func(t *testing.T) {
		id := h.sendRequest("epic.transitionStatus", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
			"epicNumber": 1, "projectNumber": 1, "newStatus": "In Progress",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.transitionStatus")
	})

	t.Run("epic.createPR/registered", func(t *testing.T) {
		id := h.sendRequest("epic.createPR", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "epicNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.createPR")
	})

	t.Run("epic.mergePR/registered", func(t *testing.T) {
		id := h.sendRequest("epic.mergePR", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo",
			"epicNumber": 1, "prNodeId": "PR_contract_test",
			"epicBranch": "feat/epic-1",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.mergePR")
	})

	t.Run("epic.readContext/registered", func(t *testing.T) {
		id := h.sendRequest("epic.readContext", map[string]interface{}{
			"epicNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.readContext")
	})

	t.Run("epic.appendContext/registered", func(t *testing.T) {
		id := h.sendRequest("epic.appendContext", map[string]interface{}{
			"epicNumber":  1,
			"issueNumber": 42,
			"findings": map[string]interface{}{
				"files_touched": []string{"src/foo.ts"},
				"decisions":     []string{},
				"discoveries":   []string{},
				"patterns":      []string{},
				"recorded_at":   "2026-03-24T00:00:00Z",
			},
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "epic.appendContext")
	})
}

// ─── Pipeline ──────────────────────────────────────────────────────────────

func TestContract_Pipeline(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("pipeline.status/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.status", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1, "itemId": "PVI_contract_test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.status")
	})

	// pipeline.run, stop, pause, resume, setPaused: send nil params to trigger
	// a fast json.Unmarshal error (-32603) without spawning a real Claude process.
	t.Run("pipeline.run/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.run", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.run")
	})

	t.Run("pipeline.runItem/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.runItem", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.runItem")
	})

	t.Run("pipeline.cancelActiveForNetworkOutage/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.cancelActiveForNetworkOutage", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.cancelActiveForNetworkOutage")
	})

	t.Run("pipeline.stop/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.stop", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.stop")
	})

	t.Run("pipeline.pause/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.pause", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.pause")
	})

	t.Run("pipeline.resume/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.resume", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.resume")
	})

	t.Run("pipeline.setPaused/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.setPaused", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.setPaused")
	})

	// pipeline.getState for an unknown issue returns nil (no state) — success.
	t.Run("pipeline.getState/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.getState", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "issueNumber": 9999,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.getState")
	})

	// pipeline.notifyStageTransition — local state management, no network.
	t.Run("pipeline.notifyStageTransition/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.notifyStageTransition", map[string]interface{}{
			"repo":        "test-repo",
			"issueNumber": 1,
			"stage":       "feature-dev",
			"status":      "initialized",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.notifyStageTransition")
	})

	// pipeline.notifyComplete — terminal pipeline_done signal from the
	// extension/HeadlessOrchestrator path. Succeeds even with no active runtime
	// (telemetry is skipped when the run UUID can't be resolved).
	t.Run("pipeline.notifyComplete/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.notifyComplete", map[string]interface{}{
			"repo":            "owner/test-repo",
			"issueNumber":     1,
			"success":         true,
			"totalDurationMs": 1000,
			"stagesRun":       []string{"issue-pickup", "pr-merge"},
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.notifyComplete")
	})

	// pipeline.notifyPhaseTransition — succeeds even without an active runtime
	// (Go-scheduler mode has no activeRuntime entry — it still emits phase.start).
	t.Run("pipeline.notifyPhaseTransition/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.notifyPhaseTransition", map[string]interface{}{
			"repo":        "test-repo",
			"issueNumber": 8888,
			"stage":       "feature-dev",
			"name":        "implementation",
			"index":       1,
			"total":       5,
			"eventType":   "start",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.notifyPhaseTransition")
	})

	// pipeline.notifyStageProgress — live in-stage token/cost estimate (#233).
	// Best-effort: succeeds even without an active runtime (telemetry is skipped
	// when the run UUID can't be resolved) and never creates one.
	t.Run("pipeline.notifyStageProgress/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.notifyStageProgress", map[string]interface{}{
			"repo":            "test-repo",
			"issueNumber":     8889,
			"stage":           "feature-dev",
			"inputTokens":     1500,
			"outputTokens":    800,
			"cacheReadTokens": 200,
			"costUsd":         0.42,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.notifyStageProgress")
	})
}

// ─── Queue ─────────────────────────────────────────────────────────────────

func TestContract_Queue(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("queue.list/registered", func(t *testing.T) {
		id := h.sendRequest("queue.list", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "queue.list")
	})

	t.Run("queue.add/registered", func(t *testing.T) {
		id := h.sendRequest("queue.add", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "issueNumber": 42,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "queue.add")
	})

	t.Run("queue.dequeueIndependent/registered", func(t *testing.T) {
		id := h.sendRequest("queue.dequeueIndependent", map[string]interface{}{
			"maxSlots": 1, "runningIssues": []int{},
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "queue.dequeueIndependent")
	})

	t.Run("queue.remove/registered", func(t *testing.T) {
		id := h.sendRequest("queue.remove", map[string]interface{}{
			"issueNumber": 42,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "queue.remove")
	})

	t.Run("queue.clear/registered", func(t *testing.T) {
		id := h.sendRequest("queue.clear", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "queue.clear")
	})

	// queue.enqueueEpic fetches sub-issues from GitHub → -32603 with fake token.
	t.Run("queue.enqueueEpic/registered", func(t *testing.T) {
		id := h.sendRequest("queue.enqueueEpic", map[string]interface{}{
			"owner": "test-org", "repo": "test-repo", "epicNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "queue.enqueueEpic")
	})

	// wave.status reads persisted wave plan/status → -32603 with no data on disk.
	t.Run("wave.status/registered", func(t *testing.T) {
		id := h.sendRequest("wave.status", map[string]interface{}{
			"epicNumber": 999,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "wave.status")
	})
}

// ─── Git ───────────────────────────────────────────────────────────────────

// TestContract_Git verifies that all git.* and branch.cleanup methods are
// registered. All will return -32603 (internal error) because the harness
// temp dir has no git repository — that is expected and acceptable.
func TestContract_Git(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("git.currentBranch/registered", func(t *testing.T) {
		id := h.sendRequest("git.currentBranch", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.currentBranch")
	})

	t.Run("git.root/registered", func(t *testing.T) {
		id := h.sendRequest("git.root", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.root")
	})

	t.Run("git.status/registered", func(t *testing.T) {
		id := h.sendRequest("git.status", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.status")
	})

	t.Run("git.checkout/registered", func(t *testing.T) {
		id := h.sendRequest("git.checkout", map[string]interface{}{
			"branch": "main",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.checkout")
	})

	t.Run("git.branchCreate/registered", func(t *testing.T) {
		id := h.sendRequest("git.branchCreate", map[string]interface{}{
			"name": "feat/contract-test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.branchCreate")
	})

	t.Run("git.branchDelete/registered", func(t *testing.T) {
		id := h.sendRequest("git.branchDelete", map[string]interface{}{
			"name": "feat/contract-test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.branchDelete")
	})

	t.Run("git.branchCleanup/registered", func(t *testing.T) {
		id := h.sendRequest("git.branchCleanup", map[string]interface{}{
			"name": "feat/contract-test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.branchCleanup")
	})

	t.Run("git.cleanupMergedBranches/registered", func(t *testing.T) {
		id := h.sendRequest("git.cleanupMergedBranches", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.cleanupMergedBranches")
	})

	t.Run("git.listRemoteBranches/registered", func(t *testing.T) {
		id := h.sendRequest("git.listRemoteBranches", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.listRemoteBranches")
	})

	t.Run("git.commit/registered", func(t *testing.T) {
		id := h.sendRequest("git.commit", map[string]interface{}{
			"message": "contract test commit",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.commit")
	})

	t.Run("git.log/registered", func(t *testing.T) {
		id := h.sendRequest("git.log", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.log")
	})

	t.Run("git.diff/registered", func(t *testing.T) {
		id := h.sendRequest("git.diff", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.diff")
	})

	t.Run("git.fetch/registered", func(t *testing.T) {
		id := h.sendRequest("git.fetch", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.fetch")
	})

	t.Run("git.push/registered", func(t *testing.T) {
		id := h.sendRequest("git.push", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.push")
	})

	t.Run("git.abortPipeline/registered", func(t *testing.T) {
		id := h.sendRequest("git.abortPipeline", map[string]interface{}{
			"featureBranch": "feat/contract-test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.abortPipeline")
	})

	t.Run("git.resetPipeline/registered", func(t *testing.T) {
		id := h.sendRequest("git.resetPipeline", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "git.resetPipeline")
	})

	t.Run("branch.cleanup/registered", func(t *testing.T) {
		id := h.sendRequest("branch.cleanup", map[string]interface{}{
			"branch": "feat/contract-test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "branch.cleanup")
	})
}

// ─── Intelligence ──────────────────────────────────────────────────────────

// TestContract_Intelligence verifies intelligence.* methods. These perform
// local computation (no network), so they should return successful results.
func TestContract_Intelligence(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("intelligence.complexity/registered", func(t *testing.T) {
		id := h.sendRequest("intelligence.complexity", map[string]interface{}{
			"title":  "Contract test issue",
			"body":   "Test body for complexity estimation",
			"labels": []string{"type:feature"},
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "intelligence.complexity")
	})

	t.Run("intelligence.route/registered", func(t *testing.T) {
		id := h.sendRequest("intelligence.route", map[string]interface{}{
			"stage": "feature-dev", "complexityScore": 3,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "intelligence.route")
	})

	t.Run("intelligence.classify/registered", func(t *testing.T) {
		id := h.sendRequest("intelligence.classify", map[string]interface{}{
			"stage": "feature-dev", "exitCode": 1, "stderr": "test error",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "intelligence.classify")
	})

	t.Run("intelligence.cost/registered", func(t *testing.T) {
		id := h.sendRequest("intelligence.cost", map[string]interface{}{
			"stages": []string{"feature-dev", "feature-validate"}, "complexityScore": 3,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "intelligence.cost")
	})

	t.Run("intelligence.health/registered", func(t *testing.T) {
		id := h.sendRequest("intelligence.health", map[string]interface{}{
			"workspaceRoot": ".",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "intelligence.health")
	})
}

// ─── Platform ──────────────────────────────────────────────────────────────

func TestContract_Platform(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// Platform service may not be running — -32603 is acceptable.
	t.Run("platform.status/registered", func(t *testing.T) {
		id := h.sendRequest("platform.status", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.status")
	})

	t.Run("platform.license/registered", func(t *testing.T) {
		id := h.sendRequest("platform.license", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.license")
	})

	t.Run("platform.resolveSkill/registered", func(t *testing.T) {
		id := h.sendRequest("platform.resolveSkill", map[string]interface{}{
			"skillId": "feature-dev",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.resolveSkill")
	})

	t.Run("platform.validateLicense/registered", func(t *testing.T) {
		id := h.sendRequest("platform.validateLicense", map[string]interface{}{
			"licenseKey": "ib_test_contract",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.validateLicense")
	})

	t.Run("platform.startTrial/registered", func(t *testing.T) {
		id := h.sendRequest("platform.startTrial", map[string]interface{}{
			"accessToken": "jwt_test_contract",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.startTrial")
	})

	t.Run("platform.submitAnalytics/registered", func(t *testing.T) {
		id := h.sendRequest("platform.submitAnalytics", map[string]interface{}{
			"eventType": "test_event",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.submitAnalytics")
	})

	t.Run("platform.getUsageSummary/registered", func(t *testing.T) {
		id := h.sendRequest("platform.getUsageSummary", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.getUsageSummary")
	})

	// platform.syncTelemetry — analyticsSvc nil → -32603 "platform client not configured".
	t.Run("platform.syncTelemetry/registered", func(t *testing.T) {
		id := h.sendRequest("platform.syncTelemetry", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.syncTelemetry")
	})

	t.Run("platform.getTeamMembers/registered", func(t *testing.T) {
		id := h.sendRequest("platform.getTeamMembers", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.getTeamMembers")
	})

	t.Run("platform.createPortalSession/registered", func(t *testing.T) {
		id := h.sendRequest("platform.createPortalSession", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.createPortalSession")
	})

	t.Run("platform.healthCheck/registered", func(t *testing.T) {
		id := h.sendRequest("platform.healthCheck", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.healthCheck")
	})

	// Auth methods — platform client may not be configured, -32603 is acceptable.
	t.Run("platform.authDeviceCode/registered", func(t *testing.T) {
		id := h.sendRequest("platform.authDeviceCode", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.authDeviceCode")
	})

	t.Run("platform.authDeviceToken/registered", func(t *testing.T) {
		id := h.sendRequest("platform.authDeviceToken", map[string]interface{}{
			"deviceCode": "test-code",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.authDeviceToken")
	})

	t.Run("platform.authGithub/registered", func(t *testing.T) {
		id := h.sendRequest("platform.authGithub", map[string]interface{}{
			"githubAccessToken": "gho_test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.authGithub")
	})

	t.Run("platform.authRefresh/registered", func(t *testing.T) {
		id := h.sendRequest("platform.authRefresh", map[string]interface{}{
			"refreshToken": "rt_test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.authRefresh")
	})

	t.Run("platform.authSignout/registered", func(t *testing.T) {
		id := h.sendRequest("platform.authSignout", map[string]interface{}{
			"refreshToken": "rt_test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.authSignout")
	})

	t.Run("platform.getCostAnalytics/registered", func(t *testing.T) {
		id := h.sendRequest("platform.getCostAnalytics", map[string]interface{}{
			"workspaceId": "ws_test",
			"range":       "7d",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.getCostAnalytics")
	})

	t.Run("platform.getAnalyticsRuns/registered", func(t *testing.T) {
		id := h.sendRequest("platform.getAnalyticsRuns", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.getAnalyticsRuns")
	})

	t.Run("platform.getAnalyticsTrends/registered", func(t *testing.T) {
		id := h.sendRequest("platform.getAnalyticsTrends", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.getAnalyticsTrends")
	})

	t.Run("platform.auditGenerateReport/registered", func(t *testing.T) {
		id := h.sendRequest("platform.auditGenerateReport", map[string]interface{}{
			"reportType": "soc2",
			"startDate":  "2026-01-01",
			"endDate":    "2026-03-31",
			"format":     "pdf",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.auditGenerateReport")
	})

	t.Run("platform.auditListReports/registered", func(t *testing.T) {
		id := h.sendRequest("platform.auditListReports", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.auditListReports")
	})

	t.Run("platform.auditGetReport/registered", func(t *testing.T) {
		id := h.sendRequest("platform.auditGetReport", map[string]interface{}{
			"reportId": "rpt-test",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "platform.auditGetReport")
	})

	t.Run("audit.getRetentionConfig/registered", func(t *testing.T) {
		id := h.sendRequest("audit.getRetentionConfig", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "audit.getRetentionConfig")
	})

	t.Run("audit.updateRetentionConfig/registered", func(t *testing.T) {
		id := h.sendRequest("audit.updateRetentionConfig", map[string]interface{}{
			"retentionDays": 365,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "audit.updateRetentionConfig")
	})

	t.Run("audit.verifyIntegrity/registered", func(t *testing.T) {
		id := h.sendRequest("audit.verifyIntegrity", map[string]interface{}{
			"windowDays": 30,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "audit.verifyIntegrity")
	})
}

// ─── Auth ──────────────────────────────────────────────────────────────────

func TestContract_Auth(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// Auth service not configured in plain harness — -32603 is acceptable.
	t.Run("auth.exchangeGitHub/registered", func(t *testing.T) {
		id := h.sendRequest("auth.exchangeGitHub", map[string]interface{}{
			"github_token": "test-token",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "auth.exchangeGitHub")
	})

	t.Run("auth.deviceFlowStart/registered", func(t *testing.T) {
		id := h.sendRequest("auth.deviceFlowStart", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "auth.deviceFlowStart")
	})

	t.Run("auth.deviceFlowPoll/registered", func(t *testing.T) {
		id := h.sendRequest("auth.deviceFlowPoll", map[string]interface{}{
			"device_code": "test-code",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "auth.deviceFlowPoll")
	})

	t.Run("auth.refresh/registered", func(t *testing.T) {
		id := h.sendRequest("auth.refresh", map[string]interface{}{
			"refresh_token": "test-refresh",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "auth.refresh")
	})
}

// ─── Project ───────────────────────────────────────────────────────────────

func TestContract_Project(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("project.syncStatus/registered", func(t *testing.T) {
		id := h.sendRequest("project.syncStatus", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
			"repo": "test-repo", "issueNumber": 1, "status": "Ready",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "project.syncStatus")
	})

	t.Run("project.syncIteration/registered", func(t *testing.T) {
		id := h.sendRequest("project.syncIteration", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
			"repo": "test-repo", "issueNumber": 1, "iteration": "2025-01",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "project.syncIteration")
	})

	t.Run("project.setHours/registered", func(t *testing.T) {
		id := h.sendRequest("project.setHours", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
			"repo": "test-repo", "issueNumber": 1, "hours": 2.5,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "project.setHours")
	})

	t.Run("project.addItem/registered", func(t *testing.T) {
		id := h.sendRequest("project.addItem", map[string]interface{}{
			"owner": "test-org", "projectNumber": 1,
			"repo": "test-repo", "issueNumber": 1,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "project.addItem")
	})
}

// ─── Remote ────────────────────────────────────────────────────────────────

func TestContract_Remote(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("remote.getCommandHistory/registered", func(t *testing.T) {
		id := h.sendRequest("remote.getCommandHistory", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "remote.getCommandHistory")
	})

	t.Run("remote.getPollingStatus/registered", func(t *testing.T) {
		id := h.sendRequest("remote.getPollingStatus", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "remote.getPollingStatus")
	})
}

// ─── Execution ─────────────────────────────────────────────────────────────

func TestContract_Execution(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// execution.list takes no params and returns the list of active executions
	// (empty in a fresh binary).
	t.Run("execution.list/registered", func(t *testing.T) {
		id := h.sendRequest("execution.list", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "execution.list")
	})
}

// ─── Autonomous ──────────────────────────────────────────────────────────────

// TestContract_Attention proves the Action Center methods (ADR 015) are
// registered. The store is unconfigured in the harness, so list returns an
// empty result and resolve/acknowledge return a not-configured error — either
// way the method is registered (not -32601).
func TestContract_Attention(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("attention.list/registered", func(t *testing.T) {
		id := h.sendRequest("attention.list", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "attention.list")
	})

	t.Run("attention.resolve/registered", func(t *testing.T) {
		id := h.sendRequest("attention.resolve", map[string]interface{}{"id": "dr_x", "optionId": "go"})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "attention.resolve")
	})

	t.Run("attention.acknowledge/registered", func(t *testing.T) {
		id := h.sendRequest("attention.acknowledge", map[string]interface{}{"id": "dr_x"})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "attention.acknowledge")
	})
}

func TestContract_Autonomous(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// autonomous.status takes no params — returns "not configured" error
	// because no autonomous scheduler is attached (proves method is registered).
	t.Run("autonomous.status/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.status", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.status")
	})

	// #4073 — stalled-epic watchdog snapshot.
	t.Run("autonomous.stuckEpics/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.stuckEpics", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.stuckEpics")
	})

	t.Run("autonomous.start/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.start", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.start")
	})

	t.Run("autonomous.pause/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.pause", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.pause")
	})

	t.Run("autonomous.resume/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.resume", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.resume")
	})

	t.Run("autonomous.stop/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.stop", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.stop")
	})

	t.Run("autonomous.complete/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.complete", map[string]interface{}{
			"owner": "nightgauge", "repo": "nightgauge", "issueNumber": 1, "success": true,
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.complete")
	})

	// #3020 — clear lifetime failure cap for triaged issues.
	t.Run("autonomous.clearIssueFailures/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.clearIssueFailures", map[string]interface{}{
			"key": "nightgauge/nightgauge#1",
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.clearIssueFailures")
	})

	// #3446 — clear the global Anthropic-quota cooldown so dispatch resumes.
	t.Run("autonomous.clearQuotaCooldown/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.clearQuotaCooldown", map[string]interface{}{})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.clearQuotaCooldown")
	})

	// #3023 phase 1 — instant rescan trigger from local actions.
	t.Run("autonomous.rescan/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.rescan", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.rescan")
	})

	// #3429 — live-apply repo allowlist to a running scheduler without
	// restart. Replaces the previous "Restart Autonomous?" modal.
	t.Run("autonomous.updateAllowlist/registered", func(t *testing.T) {
		id := h.sendRequest("autonomous.updateAllowlist", map[string]interface{}{
			"workspaceRepos": []string{"nightgauge/nightgauge"},
		})
		assertMethodRegistered(t, h.readResponseFor(id, nil), "autonomous.updateAllowlist")
	})
}

// ─── Pipeline Config ─────────────────────────────────────────────────────────

func TestContract_PipelineConfig(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("pipeline.setMaxConcurrent/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.setMaxConcurrent", json.RawMessage(`{"maxConcurrent":5,"persist":false}`))
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.setMaxConcurrent")
	})

	t.Run("pipeline.getMaxConcurrent/registered", func(t *testing.T) {
		id := h.sendRequest("pipeline.getMaxConcurrent", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "pipeline.getMaxConcurrent")
	})
}

// ─── Focus ───────────────────────────────────────────────────────────────────

func TestContract_Focus(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("focus.show/registered", func(t *testing.T) {
		id := h.sendRequest("focus.show", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "focus.show")
	})

	t.Run("focus.set/registered", func(t *testing.T) {
		id := h.sendRequest("focus.set", json.RawMessage(`{"lens":"quality"}`))
		assertMethodRegistered(t, h.readResponseFor(id, nil), "focus.set")
	})

	t.Run("focus.clear/registered", func(t *testing.T) {
		id := h.sendRequest("focus.clear", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "focus.clear")
	})

	t.Run("focus.list/registered", func(t *testing.T) {
		id := h.sendRequest("focus.list", nil)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "focus.list")
	})
}

// ─── Knowledge ───────────────────────────────────────────────────────────────

func TestContract_Knowledge(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("knowledge.metrics/registered", func(t *testing.T) {
		id := h.sendRequest("knowledge.metrics", json.RawMessage(`{"windowDays":7,"staleDays":30}`))
		assertMethodRegistered(t, h.readResponseFor(id, nil), "knowledge.metrics")
	})

	// Issue #2964 — registration probes for the three new search/backlinks
	// handlers. Behavioral coverage lives in their dedicated tests; here we
	// only assert the methods exist on the wire.
	t.Run("knowledge.search/registered", func(t *testing.T) {
		id := h.sendRequest("knowledge.search", json.RawMessage(`{"query":"smoke","limit":1}`))
		assertMethodRegistered(t, h.readResponseFor(id, nil), "knowledge.search")
	})
	t.Run("knowledge.backlinks/registered", func(t *testing.T) {
		id := h.sendRequest("knowledge.backlinks", json.RawMessage(`{"path":"unused.md"}`))
		assertMethodRegistered(t, h.readResponseFor(id, nil), "knowledge.backlinks")
	})
	t.Run("knowledge.relatedToIssue/registered", func(t *testing.T) {
		id := h.sendRequest("knowledge.relatedToIssue", json.RawMessage(`{"issueNumber":1,"limit":1}`))
		assertMethodRegistered(t, h.readResponseFor(id, nil), "knowledge.relatedToIssue")
	})
}

// ─── Diagnostics ─────────────────────────────────────────────────────────────

// Contract test for the #3619 IPC method that closes the TS-dispatch
// blackbox where #3340-style failures used to write zero diagnostic records.
// Validates the handler is registered and reachable — full behavioral
// coverage (record contents, sentinel-file path, classifier fallback) lives
// in diagnostics_stage_exit_test.go alongside the handler implementation.
func TestContract_Diagnostics(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("diagnostics.recordStageExit/registered", func(t *testing.T) {
		payload := json.RawMessage(`{"repo":"nightgauge/nightgauge","issueNumber":1,"stage":"feature-dev","success":true}`)
		id := h.sendRequest("diagnostics.recordStageExit", payload)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "diagnostics.recordStageExit")
	})
}

// ─── Agent ───────────────────────────────────────────────────────────────────

// TestContract_Agent verifies agent.* IPC methods are registered and reachable.
// Behavioral coverage lives in agent_commands_test.go.
func TestContract_Agent(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	t.Run("agent.acknowledgeCommand/registered", func(t *testing.T) {
		// Send with empty params — expect method-not-found (-32601) to be absent;
		// any other error (missing platform client, bad params) is acceptable.
		payload := json.RawMessage(`{"agentId":"test-agent","commandId":"test-cmd"}`)
		id := h.sendRequest("agent.acknowledgeCommand", payload)
		assertMethodRegistered(t, h.readResponseFor(id, nil), "agent.acknowledgeCommand")
	})
}
