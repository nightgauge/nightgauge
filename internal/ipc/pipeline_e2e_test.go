// Package ipc — End-to-end tests for the full pipeline path.
//
// These tests exercise the real nightgauge binary's pipeline execution:
// config load → pipeline.runItem → scheduler dispatch → stage execution
// (via mock stage runner responding with pipeline.stageResult) → completion.
//
// All tests use GITHUB_TOKEN=fake-token-for-integration-test and require
// no real GitHub or Claude API credentials. They build on the ipcTestHarness
// infrastructure from server_integration_test.go.
//
// See: Issue #1940 — Pipeline end-to-end tests
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─── Helpers ───────────────────────────────────────────────────────────────

// newIpcTestHarnessWithSkills creates an ipcTestHarness with minimal SKILL.md
// stubs for all 6 pipeline stages. Returns the harness and the workspace
// directory so tests can write context files.
func newIpcTestHarnessWithSkills(t *testing.T) (*ipcTestHarness, string) {
	t.Helper()

	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	// Minimal config — satisfies cfg.Owner != "" and cfg.ProjectNumber > 0
	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Create pipeline directory
	pipelineDir := filepath.Join(workDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline dir: %v", err)
	}

	// Create minimal SKILL.md stubs for all 6 stages
	skillDirs := []string{
		"nightgauge-issue-pickup",
		"nightgauge-feature-planning",
		"nightgauge-feature-dev",
		"nightgauge-feature-validate",
		"nightgauge-pr-create",
		"nightgauge-pr-merge",
	}
	for _, dir := range skillDirs {
		skillPath := filepath.Join(workDir, "skills", dir)
		if err := os.MkdirAll(skillPath, 0o755); err != nil {
			t.Fatalf("mkdir skill dir %s: %v", dir, err)
		}
		stub := "---\nname: stub\nallowed-tools: Read\n---\nStub skill.\n"
		if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(stub), 0o644); err != nil {
			t.Fatalf("write skill stub %s: %v", dir, err)
		}
	}

	// Start binary with this workspace
	h := startHarness(t, &cmdSpec{workDir: workDir})
	return h, workDir
}

// cmdSpec holds the parameters for starting a binary.
type cmdSpec struct {
	workDir string
}

// startHarness starts the binary from a cmdSpec and returns a harness.
// This is separated from newIpcTestHarness to allow restarting with the same workDir.
func startHarness(t *testing.T, spec *cmdSpec) *ipcTestHarness {
	t.Helper()

	cmd := newCmd(spec.workDir)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	h := &ipcTestHarness{
		t:       t,
		cmd:     cmd,
		stdin:   stdinPipe,
		lines:   make(chan string, 256),
		nextID:  1,
		workDir: spec.workDir,
	}

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			h.lines <- scanner.Text()
		}
		close(h.lines)
	}()

	t.Cleanup(func() {
		stdinPipe.Close()
		if cmd.Process != nil {
			cmd.Process.Signal(os.Interrupt)
			cmd.Wait()
		}
	})

	return h
}

// newCmd creates an exec.Cmd for the binary with the given workspace.
func newCmd(workDir string) *exec.Cmd {
	cmd := exec.Command(binaryPath, "serve", "--workspace", workDir)
	// Hermetic HOME: point the machine-tier config dir (~/.nightgauge,
	// resolved via os.UserHomeDir → $HOME) at an empty dir inside the test
	// workspace so the developer's real ~/.nightgauge/config.yaml (which may
	// set github_user) cannot leak into the test. Without this the identity
	// preflight (#4068) would resolve a real github_user, query the FAKE
	// test-org/test-repo, get a 404, and correctly fail-closed — blocking the
	// mock pipeline on every machine that has a github_user configured. (The last
	// HOME= in the slice wins per os/exec duplicate-key semantics.)
	isolatedHome := filepath.Join(workDir, ".isolated-home")
	_ = os.MkdirAll(isolatedHome, 0o755)
	cmd.Env = append(os.Environ(),
		"HOME="+isolatedHome,
		"GITHUB_TOKEN=fake-token-for-integration-test",
		// Issue #3266: pr-create and pr-merge gates call `gh pr view`; the
		// E2E harness has no real gh access. The other 4 gates are file-
		// based and satisfied by writeGatePassingSkillOutput.
		"NIGHTGAUGE_DISABLE_GATES=pr-create,pr-merge",
	)
	return cmd
}

// writeContextFile writes a minimal context JSON file to the pipeline directory
// using the flat <prefix>-<N>.json convention shared by the skills, gates, and
// stagecontext.ContextPath. contextType is the prefix (e.g., "issue", "planning").
func writeContextFile(workDir string, issueNumber int, contextType string) {
	dir := filepath.Join(workDir, ".nightgauge", "pipeline")
	os.MkdirAll(dir, 0o755) //nolint:errcheck
	content := fmt.Sprintf(`{"issueNumber":%d,"repo":"test-org/test-repo","stage":"%s"}`,
		issueNumber, contextType)
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.json", contextType, issueNumber)), []byte(content), 0o644) //nolint:errcheck
}

// writeGatePassingSkillOutput emits the flat skill-output context file plus
// any auxiliary files (gate-metrics.jsonl, plan_file) that the matching
// post-condition gate (Issue #3266) requires to return passed=true. Called
// by the test stage responders after each pipeline.runStage so the gate
// hook in the scheduler does not flag the simulated stage as a no-op.
//
// The pr-merge gate inspects gh state. The E2E tests do not run gh, so we
// fake-pass by inserting a sentinel file the gate's exec stub honours when
// running under the test binary — but the binary in the test runs as a
// real process, so the simplest approach is to deregister pr-merge from
// the gate registry inside `serve` when GITHUB_TOKEN is the
// fake-token-for-integration-test sentinel (handled in main.go).
func writeGatePassingSkillOutput(workDir string, issueNumber int, stage string) {
	pipelineDir := filepath.Join(workDir, ".nightgauge", "pipeline")
	os.MkdirAll(pipelineDir, 0o755) //nolint:errcheck

	switch stage {
	case "issue-pickup":
		path := filepath.Join(pipelineDir, fmt.Sprintf("issue-%d.json", issueNumber))
		body := fmt.Sprintf(`{"issue_number":%d,"branch":"feat/%d-test"}`, issueNumber, issueNumber)
		os.WriteFile(path, []byte(body), 0o644) //nolint:errcheck

	case "feature-planning":
		planFile := filepath.Join(pipelineDir, fmt.Sprintf("plan-%d.md", issueNumber))
		os.WriteFile(planFile, []byte("# plan\n"), 0o644) //nolint:errcheck
		path := filepath.Join(pipelineDir, fmt.Sprintf("planning-%d.json", issueNumber))
		body := fmt.Sprintf(`{"issue_number":%d,"plan_file":%q}`, issueNumber, planFile)
		os.WriteFile(path, []byte(body), 0o644) //nolint:errcheck

	case "feature-dev":
		path := filepath.Join(pipelineDir, fmt.Sprintf("dev-%d.json", issueNumber))
		// build_verification is part of the dev completion contract — the
		// FeatureDevGate rejects a dev context without it (#55).
		body := fmt.Sprintf(`{"issue_number":%d,"files_changed":{"created":["foo.go"],"modified":[],"deleted":[]},"build_verification":{"ran":true,"status":"passed"}}`,
			issueNumber)
		os.WriteFile(path, []byte(body), 0o644) //nolint:errcheck

	case "feature-validate":
		// Emit gate-metrics.jsonl with all gates passing.
		healthDir := filepath.Join(workDir, ".nightgauge", "health")
		os.MkdirAll(healthDir, 0o755) //nolint:errcheck
		f, err := os.OpenFile(filepath.Join(healthDir, "gate-metrics.jsonl"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			defer f.Close()
			for _, gate := range []string{"build", "lint", "unit-tests"} {
				record := fmt.Sprintf(
					`{"schema_version":"1","timestamp":"2026-05-07T00:00:00Z","issue_number":%d,"gate_name":%q,"result":"pass"}`+"\n",
					issueNumber, gate)
				f.Write([]byte(record))
			}
		}
		path := filepath.Join(pipelineDir, fmt.Sprintf("validate-%d.json", issueNumber))
		os.WriteFile(path, []byte(fmt.Sprintf(`{"issue_number":%d}`, issueNumber)), 0o644) //nolint:errcheck

	case "pr-create":
		path := filepath.Join(pipelineDir, fmt.Sprintf("pr-%d.json", issueNumber))
		// Use a well-formed GitHub PR URL — the pr-merge verifier parses its
		// trailing segment as the PR number. (The old "https://example/x" only
		// "passed" because loadPrUrl read the wrong prefix and returned "".)
		body := fmt.Sprintf(`{"issue_number":%d,"pr_number":%d,"pr_url":"https://github.com/test-org/test-repo/pull/%d"}`,
			issueNumber, issueNumber+1000, issueNumber+1000)
		os.WriteFile(path, []byte(body), 0o644) //nolint:errcheck

	case "pr-merge":
		// pr-merge gate runs `gh pr view`; in the E2E binary we cannot
		// satisfy this without a real GitHub call. The serve subcommand
		// detects the test sentinel GITHUB_TOKEN and disables the gate
		// for pr-merge in that environment.
	}
}

// pipelineStageResponder runs in a goroutine, reading events from the harness.
// When it sees a pipeline.runStage event, it:
// 1. Records the stage name
// 2. Writes the output context file (from params.outputFile)
// 3. Sends a pipeline.stageResult request back with success=true
//
// The filter function, if non-nil, is called for each event and returns true
// to process it as a runStage event, false to skip.
//
// Returns channels for: dispatched stage names, all collected events, and done.
func pipelineStageResponder(
	h *ipcTestHarness,
	issueFilter func(issueNumber int) bool,
	timeout time.Duration,
) (stages chan stageDispatch, events chan string, done chan struct{}) {
	stages = make(chan stageDispatch, 64)
	events = make(chan string, 256)
	done = make(chan struct{})

	go func() {
		defer close(done)
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		for {
			select {
			case line, ok := <-h.lines:
				if !ok {
					return
				}

				// Forward all lines to events channel (non-blocking)
				select {
				case events <- line:
				default:
				}

				// Parse as JSON
				var msg map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &msg); err != nil {
					continue
				}

				// Check if this is an event
				rawEvt, hasEvent := msg["event"]
				if !hasEvent {
					continue
				}

				var evtName string
				if err := json.Unmarshal(rawEvt, &evtName); err != nil {
					continue
				}

				switch evtName {
				case "pipeline.runStage":
					// Parse runStage data
					var data RunStageParams
					if rawData, ok := msg["data"]; ok {
						if err := json.Unmarshal(rawData, &data); err != nil {
							continue
						}
					}

					// Apply issue filter
					if issueFilter != nil && !issueFilter(data.IssueNumber) {
						continue
					}

					// Record dispatched stage
					sd := stageDispatch{
						Stage:       data.Stage,
						IssueNumber: data.IssueNumber,
						OutputFile:  data.OutputFile,
					}
					select {
					case stages <- sd:
					default:
					}

					// Write the output context file so the next stage's prerequisite check passes
					if data.OutputFile != "" {
						dir := filepath.Dir(data.OutputFile)
						os.MkdirAll(dir, 0o755) //nolint:errcheck
						content := fmt.Sprintf(`{"issueNumber":%d,"repo":"%s","stage":"%s"}`,
							data.IssueNumber, data.Repo, data.Stage)
						os.WriteFile(data.OutputFile, []byte(content), 0o644) //nolint:errcheck
					}

					// Issue #3266: also write the skill-output context files
					// the post-condition stage gates inspect. Each stage gate
					// requires minimum fields matching what real skills emit.
					writeGatePassingSkillOutput(h.workDir, data.IssueNumber, data.Stage)

					// Send pipeline.stageResult back
					h.sendRequest("pipeline.stageResult", StageResultParams{
						Stage:       data.Stage,
						IssueNumber: data.IssueNumber,
						Success:     true,
						ExitCode:    0,
					})

				case "pipeline.complete":
					return // Pipeline done — stop listening
				}

			case <-timer.C:
				return
			}
		}
	}()

	return stages, events, done
}

// stageDispatch records a dispatched stage execution.
type stageDispatch struct {
	Stage       string
	IssueNumber int
	OutputFile  string
}

// collectEvents drains the events channel and returns all events as a slice.
func collectEvents(events chan string, done chan struct{}) []string {
	<-done
	var result []string
	for {
		select {
		case e := <-events:
			result = append(result, e)
		default:
			return result
		}
	}
}

// countEventsByName counts occurrences of a specific event name in a list of event JSON strings.
func countEventsByName(events []string, eventName string) int {
	count := 0
	for _, line := range events {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		rawEvt, ok := msg["event"]
		if !ok {
			continue
		}
		var name string
		if err := json.Unmarshal(rawEvt, &name); err != nil {
			continue
		}
		if name == eventName {
			count++
		}
	}
	return count
}

// findEventByName finds the first event matching the given name and returns its data.
func findEventByName(events []string, eventName string) (map[string]json.RawMessage, bool) {
	for _, line := range events {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		rawEvt, ok := msg["event"]
		if !ok {
			continue
		}
		var name string
		if err := json.Unmarshal(rawEvt, &name); err != nil {
			continue
		}
		if name == eventName {
			return msg, true
		}
	}
	return nil, false
}

// ─── E2E Tests ─────────────────────────────────────────────────────────────

// TestE2E_FullPipelineLifecycle verifies the full pipeline path:
// queue → run → all 6 stage events → completion event.
//
// AC: Full pipeline lifecycle test: queue → run → stage events → completion
func TestE2E_FullPipelineLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	h, workDir := newIpcTestHarnessWithSkills(t)
	h.awaitReady()

	const issueNumber = 1

	// Write the issue context prerequisite (issue-pickup needs no input,
	// but feature-planning needs issue-{N}/issue-context.json)
	writeContextFile(workDir, issueNumber, "issue")

	// Send pipeline.runItem and read immediate response BEFORE starting
	// the stage responder, so readResponseFor and the responder goroutine
	// don't compete for h.lines.
	id := h.sendRequest("pipeline.runItem", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": issueNumber,
		"title":       "E2E test issue",
		"id":          "item-1",
	})

	var preEvents []string
	resp := h.readResponseFor(id, &preEvents)
	if resp.Error != nil {
		t.Fatalf("pipeline.runItem returned error: %+v", resp.Error)
	}

	// Verify response status
	resultBytes, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(resultBytes, &result)
	if status, ok := result["status"].(string); !ok || status != "queued" {
		t.Errorf("expected status=queued, got %v", result["status"])
	}

	// Now launch stage responder goroutine (sole consumer of h.lines from here)
	stages, events, done := pipelineStageResponder(h, nil, 30*time.Second)

	// Wait for pipeline completion
	allEvents := collectEvents(events, done)
	allEvents = append(preEvents, allEvents...)

	// Collect all dispatched stages
	close(stages)
	var dispatchedStages []string
	for sd := range stages {
		dispatchedStages = append(dispatchedStages, sd.Stage)
	}

	// Assert: all 6 pipeline stages were dispatched
	expectedStages := []string{
		"issue-pickup", "feature-planning", "feature-dev",
		"feature-validate", "pr-create", "pr-merge",
	}
	if len(dispatchedStages) != 6 {
		t.Errorf("expected 6 dispatched stages, got %d: %v", len(dispatchedStages), dispatchedStages)
	} else {
		for i, expected := range expectedStages {
			if dispatchedStages[i] != expected {
				t.Errorf("stage[%d] = %q, want %q", i, dispatchedStages[i], expected)
			}
		}
	}

	// Assert: stage.start emitted 6 times
	startCount := countEventsByName(allEvents, "stage.start")
	if startCount != 6 {
		t.Errorf("expected 6 stage.start events, got %d", startCount)
	}

	// Assert: stage.complete emitted 6 times
	completeCount := countEventsByName(allEvents, "stage.complete")
	if completeCount != 6 {
		t.Errorf("expected 6 stage.complete events, got %d", completeCount)
	}

	// Assert: pipeline.complete event received with success=true
	pipelineComplete, found := findEventByName(allEvents, "pipeline.complete")
	if !found {
		t.Fatal("pipeline.complete event not received")
	}
	if rawData, ok := pipelineComplete["data"]; ok {
		var data map[string]interface{}
		if err := json.Unmarshal(rawData, &data); err == nil {
			if success, ok := data["success"].(bool); !ok || !success {
				t.Errorf("pipeline.complete success = %v, want true", data["success"])
			}
		}
	}

	// Assert: runtime state file exists
	runtimePath := filepath.Join(workDir, ".nightgauge", "pipeline",
		fmt.Sprintf("runtime-%d.json", issueNumber))
	if _, err := os.Stat(runtimePath); os.IsNotExist(err) {
		t.Errorf("expected runtime state file at %s", runtimePath)
	}
}

// TestE2E_StageExecutionRoundTrip verifies the stage execution round-trip:
// Go emits pipeline.runStage → test sends pipeline.stageResult → Go confirms.
//
// AC: Stage execution round-trip: Go request → stdin → stdout → Go result
func TestE2E_StageExecutionRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	h, workDir := newIpcTestHarnessWithSkills(t)
	h.awaitReady()

	const issueNumber = 2

	// Write issue context prerequisite
	writeContextFile(workDir, issueNumber, "issue")

	// Send pipeline.runItem
	id := h.sendRequest("pipeline.runItem", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": issueNumber,
		"title":       "Round-trip test",
		"id":          "item-2",
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("pipeline.runItem returned error: %+v", resp.Error)
	}

	// Wait for the first pipeline.runStage event (issue-pickup)
	var runStageEvent RunStageParams
	deadline := time.After(15 * time.Second)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatal("binary stdout closed while waiting for pipeline.runStage")
			}
			var msg map[string]json.RawMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}
			rawEvt, hasEvent := msg["event"]
			if !hasEvent {
				continue
			}
			var evtName string
			if err := json.Unmarshal(rawEvt, &evtName); err != nil {
				continue
			}
			if evtName == "pipeline.runStage" {
				if rawData, ok := msg["data"]; ok {
					json.Unmarshal(rawData, &runStageEvent)
				}
				goto foundRunStage
			}
		case <-deadline:
			t.Fatal("timeout waiting for pipeline.runStage event")
		}
	}
foundRunStage:

	// Verify event structure
	if runStageEvent.Stage == "" {
		t.Error("pipeline.runStage event missing 'stage'")
	}
	if runStageEvent.IssueNumber != issueNumber {
		t.Errorf("pipeline.runStage issueNumber = %d, want %d", runStageEvent.IssueNumber, issueNumber)
	}
	if runStageEvent.Stage != "issue-pickup" {
		t.Errorf("first stage = %q, want %q", runStageEvent.Stage, "issue-pickup")
	}

	// Write the output context file before sending result
	if runStageEvent.OutputFile != "" {
		dir := filepath.Dir(runStageEvent.OutputFile)
		os.MkdirAll(dir, 0o755) //nolint:errcheck
		content := fmt.Sprintf(`{"issueNumber":%d,"repo":"test-org/test-repo","stage":"issue-pickup"}`, issueNumber)
		os.WriteFile(runStageEvent.OutputFile, []byte(content), 0o644) //nolint:errcheck
	}

	// Send pipeline.stageResult
	resultID := h.sendRequest("pipeline.stageResult", StageResultParams{
		Stage:       runStageEvent.Stage,
		IssueNumber: issueNumber,
		Success:     true,
		ExitCode:    0,
	})

	// Verify response returns {status: "ok"}
	resultResp := h.readResponseFor(resultID, nil)
	if resultResp.Error != nil {
		t.Fatalf("pipeline.stageResult returned error: %+v", resultResp.Error)
	}
	resultBytes, _ := json.Marshal(resultResp.Result)
	var resultData map[string]interface{}
	json.Unmarshal(resultBytes, &resultData)
	if status, ok := resultData["status"].(string); !ok || status != "ok" {
		t.Errorf("pipeline.stageResult status = %v, want \"ok\"", resultData["status"])
	}
}

// TestE2E_QueueSurvivesRestart verifies that queue state persists across
// binary restarts. Items added via queue.add survive a SIGINT + restart.
//
// AC: State persistence test: kill and restart binary, verify queue survives
func TestE2E_QueueSurvivesRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	// Create shared workspace directory (persists across binary restarts)
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	pipelineDir := filepath.Join(workDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline dir: %v", err)
	}

	// --- First binary instance ---
	cmd1 := newCmd(workDir)
	stdinPipe1, _ := cmd1.StdinPipe()
	stdoutPipe1, _ := cmd1.StdoutPipe()
	if err := cmd1.Start(); err != nil {
		t.Fatalf("start binary (1st): %v", err)
	}

	h1 := &ipcTestHarness{
		t:      t,
		cmd:    cmd1,
		stdin:  stdinPipe1,
		lines:  make(chan string, 256),
		nextID: 1,
	}
	go func() {
		scanner := bufio.NewScanner(stdoutPipe1)
		for scanner.Scan() {
			h1.lines <- scanner.Text()
		}
		close(h1.lines)
	}()

	h1.awaitReady()

	// Add 2 items to queue
	id1 := h1.sendRequest("queue.add", map[string]interface{}{
		"owner": "test-org", "repo": "test-repo", "issueNumber": 100,
	})
	h1.readResponseFor(id1, nil)

	id2 := h1.sendRequest("queue.add", map[string]interface{}{
		"owner": "test-org", "repo": "test-repo", "issueNumber": 200,
	})
	h1.readResponseFor(id2, nil)

	// Verify both items are in queue
	listID := h1.sendRequest("queue.list", nil)
	listResp := h1.readResponseFor(listID, nil)
	if listResp.Error != nil {
		t.Fatalf("queue.list failed (1st instance): %+v", listResp.Error)
	}

	// Kill first binary
	stdinPipe1.Close()
	if cmd1.Process != nil {
		cmd1.Process.Signal(os.Interrupt)
		cmd1.Wait()
	}

	// Drain any remaining lines from closed channel
	for range h1.lines {
	}

	// --- Second binary instance (same workDir) ---
	cmd2 := newCmd(workDir)
	stdinPipe2, _ := cmd2.StdinPipe()
	stdoutPipe2, _ := cmd2.StdoutPipe()
	if err := cmd2.Start(); err != nil {
		t.Fatalf("start binary (2nd): %v", err)
	}

	h2 := &ipcTestHarness{
		t:      t,
		cmd:    cmd2,
		stdin:  stdinPipe2,
		lines:  make(chan string, 256),
		nextID: 1,
	}
	go func() {
		scanner := bufio.NewScanner(stdoutPipe2)
		for scanner.Scan() {
			h2.lines <- scanner.Text()
		}
		close(h2.lines)
	}()

	t.Cleanup(func() {
		stdinPipe2.Close()
		if cmd2.Process != nil {
			cmd2.Process.Signal(os.Interrupt)
			cmd2.Wait()
		}
	})

	h2.awaitReady()

	// Verify queue still has both items
	listID2 := h2.sendRequest("queue.list", nil)
	listResp2 := h2.readResponseFor(listID2, nil)
	if listResp2.Error != nil {
		t.Fatalf("queue.list failed (2nd instance): %+v", listResp2.Error)
	}

	resultBytes, _ := json.Marshal(listResp2.Result)
	var queueState struct {
		Items []struct {
			IssueNumber int `json:"issueNumber"`
		} `json:"items"`
	}
	json.Unmarshal(resultBytes, &queueState)

	foundIssues := make(map[int]bool)
	for _, item := range queueState.Items {
		foundIssues[item.IssueNumber] = true
	}

	if !foundIssues[100] {
		t.Errorf("issue 100 not found in queue after restart")
	}
	if !foundIssues[200] {
		t.Errorf("issue 200 not found in queue after restart")
	}
}

// TestE2E_ConcurrentPipelines verifies that 3 simultaneous pipelines
// (different repos) run without cross-talk.
//
// Historical flake (fixed in #3348): each pipeline.runItem call created a new
// IpcStageRunner and overwrote srv.methods["pipeline.stageResult"] with a
// closure capturing the new runner. On slow CI, pipeline goroutines started
// executing stages before all three handlers had been registered, so earlier
// runners' pending channels were orphaned and stage dispatch timed out after
// 150 s with "0 stages dispatched". Fix: create the shared IpcStageRunner once
// in registerMethods() and reuse it across all concurrent invocations.
//
// AC: Concurrent pipeline test: 3 simultaneous pipelines, no cross-talk
func TestE2E_ConcurrentPipelines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	h, workDir := newIpcTestHarnessWithSkills(t)
	h.awaitReady()

	// Use different repos to allow concurrent execution (maxPerRepo=1)
	type pipelineSpec struct {
		owner       string
		repo        string
		issueNumber int
	}
	pipelines := []pipelineSpec{
		{"test-org", "repo-a", 10},
		{"test-org", "repo-b", 20},
		{"test-org", "repo-c", 30},
	}

	// Write issue context prerequisites for all pipelines
	for _, p := range pipelines {
		writeContextFile(workDir, p.issueNumber, "issue")
	}

	// Track dispatched stages per issue
	var mu sync.Mutex
	issueStages := make(map[int][]string) // issueNumber → list of stage names
	lastStage := make(map[int]string)     // issueNumber → last dispatched stage (for diagnostics)
	allEvents := make([]string, 0, 256)

	// Single shared timeout for both the responder and the main thread.
	// 150s accounts for variable CI performance. With the file race eliminated,
	// 3 pipelines × 6 stages typically complete in 30-45s on slow CI.
	deadline := time.NewTimer(150 * time.Second)
	defer deadline.Stop()

	// WaitGroup tracks pipeline.complete events (replaces counter + channel).
	var wg sync.WaitGroup
	wg.Add(3)
	wgDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(wgDone)
	}()

	// Background goroutine: read events and respond to pipeline.runStage.
	// Uses the shared deadline — no independent timer.
	responderDone := make(chan struct{})
	go func() {
		defer close(responderDone)
		for {
			select {
			case line, ok := <-h.lines:
				if !ok {
					return
				}

				mu.Lock()
				allEvents = append(allEvents, line)
				mu.Unlock()

				var msg map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &msg); err != nil {
					continue
				}

				rawEvt, hasEvent := msg["event"]
				if !hasEvent {
					continue
				}
				var evtName string
				json.Unmarshal(rawEvt, &evtName)

				switch evtName {
				case "pipeline.runStage":
					var data RunStageParams
					if rawData, ok := msg["data"]; ok {
						json.Unmarshal(rawData, &data)
					}

					mu.Lock()
					issueStages[data.IssueNumber] = append(issueStages[data.IssueNumber], data.Stage)
					lastStage[data.IssueNumber] = data.Stage
					mu.Unlock()

					// Write output context file atomically (write-then-rename)
					// to eliminate the race where the scheduler's os.Stat check
					// runs before the file is fully visible on disk.
					if data.OutputFile != "" {
						dir := filepath.Dir(data.OutputFile)
						os.MkdirAll(dir, 0o755) //nolint:errcheck
						content := fmt.Sprintf(`{"issueNumber":%d,"repo":"%s","stage":"%s"}`,
							data.IssueNumber, data.Repo, data.Stage)
						tmpFile := data.OutputFile + ".tmp"
						if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
							t.Errorf("write tmp context file: %v", err)
							continue
						}
						if err := os.Rename(tmpFile, data.OutputFile); err != nil {
							t.Errorf("rename context file: %v", err)
							continue
						}
					}

					// Issue #3266: write skill-output files the post-condition
					// gates inspect (see writeGatePassingSkillOutput helper).
					writeGatePassingSkillOutput(workDir, data.IssueNumber, data.Stage)

					// Send stageResult
					h.sendRequest("pipeline.stageResult", StageResultParams{
						Stage:       data.Stage,
						IssueNumber: data.IssueNumber,
						Success:     true,
						ExitCode:    0,
					})

				case "pipeline.complete":
					wg.Done()
				}

			case <-deadline.C:
				// Shared deadline fired — stop reading. The main thread
				// will detect the timeout via the same channel.
				return
			}
		}
	}()

	// Launch 3 pipelines in quick succession.
	// Responses are consumed by the background goroutine reading h.lines
	// (responses are non-event lines, silently skipped by the event handler).
	for _, p := range pipelines {
		h.sendRequest("pipeline.runItem", map[string]interface{}{
			"owner":       p.owner,
			"repo":        p.repo,
			"issueNumber": p.issueNumber,
			"title":       fmt.Sprintf("Concurrent test #%d", p.issueNumber),
			"id":          fmt.Sprintf("item-%d", p.issueNumber),
		})
	}

	// Wait for all 3 pipeline.complete events OR the shared deadline.
	select {
	case <-wgDone:
		// All 3 pipelines completed — success path.
	case <-deadline.C:
		// Timeout — collect diagnostics before failing.
		mu.Lock()
		var diag string
		for _, p := range pipelines {
			stages := issueStages[p.issueNumber]
			last := lastStage[p.issueNumber]
			diag += fmt.Sprintf("\n  issue #%d (%s/%s): %d stages dispatched, last=%q, stages=%v",
				p.issueNumber, p.owner, p.repo, len(stages), last, stages)
		}
		mu.Unlock()
		t.Fatalf("timeout waiting for 3 concurrent pipelines to complete."+
			"\nPer-pipeline state:%s", diag)
	}

	mu.Lock()
	defer mu.Unlock()

	// Assert: each pipeline dispatched all 6 stages
	expectedStages := []string{
		"issue-pickup", "feature-planning", "feature-dev",
		"feature-validate", "pr-create", "pr-merge",
	}
	for _, p := range pipelines {
		stages := issueStages[p.issueNumber]
		if len(stages) != 6 {
			t.Errorf("issue #%d: expected 6 stages, got %d: %v",
				p.issueNumber, len(stages), stages)
			continue
		}
		for i, expected := range expectedStages {
			if stages[i] != expected {
				t.Errorf("issue #%d: stage[%d] = %q, want %q",
					p.issueNumber, i, stages[i], expected)
			}
		}
	}

	// Assert: no cross-talk — verify pipeline.complete events have correct issueNumbers
	completedIssues := make(map[int]bool)
	for _, line := range allEvents {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		rawEvt, ok := msg["event"]
		if !ok {
			continue
		}
		var evtName string
		json.Unmarshal(rawEvt, &evtName)
		if evtName == "pipeline.complete" {
			var data struct {
				IssueNumber int  `json:"issueNumber"`
				Success     bool `json:"success"`
			}
			if rawData, ok := msg["data"]; ok {
				json.Unmarshal(rawData, &data)
				completedIssues[data.IssueNumber] = true
				if !data.Success {
					t.Errorf("pipeline.complete for issue #%d: success=false", data.IssueNumber)
				}
			}
		}
	}

	for _, p := range pipelines {
		if !completedIssues[p.issueNumber] {
			t.Errorf("no pipeline.complete event for issue #%d", p.issueNumber)
		}
	}

	// Assert: no stageResult cross-pollination
	// Verify stage.start events have the correct issueNumber for their repo
	for _, line := range allEvents {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		rawEvt, ok := msg["event"]
		if !ok {
			continue
		}
		var evtName string
		json.Unmarshal(rawEvt, &evtName)
		if evtName == "stage.start" || evtName == "stage.complete" {
			var data struct {
				Repo        string `json:"repo"`
				IssueNumber int    `json:"issueNumber"`
			}
			if rawData, ok := msg["data"]; ok {
				json.Unmarshal(rawData, &data)
				// Verify issue→repo mapping is correct
				expectedRepo := ""
				for _, p := range pipelines {
					if p.issueNumber == data.IssueNumber {
						expectedRepo = p.owner + "/" + p.repo
						break
					}
				}
				if expectedRepo != "" && data.Repo != expectedRepo {
					t.Errorf("%s event: issue #%d had repo=%q, want %q (cross-talk!)",
						evtName, data.IssueNumber, data.Repo, expectedRepo)
				}
			}
		}
	}
}
