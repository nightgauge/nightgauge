// Package ipc — Integration tests for the real IPC server binary.
//
// These tests build the real nightgauge binary, start it as a subprocess
// over stdin/stdout pipes, and exercise the full request→dispatch→response
// cycle end-to-end. Unlike server_test.go (which constructs Server{} directly),
// these tests catch bugs in the real initialization path:
//
//   - NewServer() → SetScheduler() wiring
//   - Config loading from disk (real .nightgauge/config.yaml)
//   - ipc.ready event emission from server.Run()
//   - Queue method dispatch through the real scheduler
//   - Wire format invariants over actual JSON-over-stdio transport
//
// See: docs/GO_BINARY.md — IPC protocol section
// See: packages/nightgauge-vscode/src/services/IpcClient.ts
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// binaryPath is set by TestMain after building the binary.
var binaryPath string

// TestMain builds the real binary once before running any integration tests.
// The binary is placed in a temp directory and cleaned up after the suite.
func TestMain(m *testing.M) {
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go")

	dir, err := os.MkdirTemp("", "nightgauge-ipc-smoke-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TempDir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binaryPath = filepath.Join(dir, "nightgauge")

	repoRoot := findRepoRoot()

	cmd := exec.Command(goExe, "build", "-o", binaryPath, "./cmd/nightgauge/")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s\n", err, out)
		os.Exit(1)
	}

	// Point every spawned `serve` subprocess's machine-tier config lookup at
	// an empty, package-lifetime directory instead of the real developer's
	// ~/.nightgauge/config.yaml (config.MachineConfigPath honors
	// NIGHTGAUGE_CONFIG_HOME — see internal/config/merge.go). Every harness
	// in this package builds cmd.Env from os.Environ(), which inherits
	// whatever is set here for the lifetime of the test binary. Without
	// this, a developer/CI machine with a real platform.license_key
	// configured (now actually read by serve, #333) makes these subprocesses
	// dial the real production platform instead of exercising the intended
	// "no platform configured" / fixture-server code paths — nondeterministic
	// and dependent on whoever's machine runs the suite.
	machineConfigHome, err := os.MkdirTemp("", "nightgauge-ipc-machine-config-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TempDir (machine config isolation):", err)
		os.Exit(1)
	}
	defer os.RemoveAll(machineConfigHome)
	os.Setenv("NIGHTGAUGE_CONFIG_HOME", machineConfigHome)

	os.Exit(m.Run())
}

// findRepoRoot walks up from CWD to find the directory containing go.mod.
func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// ipcTestHarness starts a real nightgauge binary in serve mode and
// provides helpers for sending JSON requests and reading responses/events.
type ipcTestHarness struct {
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string // all stdout lines dispatched here
	nextID  int
	mu      sync.Mutex
	workDir string // workspace root passed to `serve --workspace`; used by tests that need to write skill outputs
}

// newIpcTestHarness creates a temp workspace with a minimal config, starts
// the binary with GITHUB_TOKEN=fake-token-for-integration-test, and returns
// a harness ready to send requests.
func newIpcTestHarness(t *testing.T) *ipcTestHarness {
	t.Helper()

	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	// Minimal nested config — satisfies cfg.Owner != "" and cfg.ProjectNumber > 0
	// so the scheduler is created in serveCmd().
	configYAML := "project:\n  owner: test-org\n  number: 1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binaryPath, "serve", "--workspace", workDir)
	// Supply fake GITHUB_TOKEN — satisfies gh.NewClient() without real API access.
	// Queue and pipeline.getState methods make no network calls.
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN=fake-token-for-integration-test")

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
		lines:   make(chan string, 64),
		nextID:  1,
		workDir: workDir,
	}

	// Background reader: dispatch all stdout lines to h.lines.
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

// nextLine reads the next line from the binary's stdout. Fails the test if
// the channel is closed or the timeout (10s) is exceeded.
func (h *ipcTestHarness) nextLine() string {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatal("binary stdout closed unexpectedly")
		}
		return line
	case <-time.After(10 * time.Second):
		h.t.Fatal("timeout waiting for binary output")
		return ""
	}
}

// awaitReady reads lines until the ipc.ready event is consumed and returns it.
// Call this at the start of every test that is NOT testing the ready event
// itself — it drains the startup event so subsequent assertions are clean.
func (h *ipcTestHarness) awaitReady() Event {
	h.t.Helper()
	for {
		line := h.nextLine()
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if _, ok := msg["event"]; ok {
			var evt Event
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				continue
			}
			if evt.Event == "ipc.ready" {
				return evt
			}
		}
	}
}

// sendRequest serialises a request and writes it to the binary's stdin.
// Returns the request ID so callers can match the response.
func (h *ipcTestHarness) sendRequest(method string, params interface{}) int {
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.mu.Unlock()

	req := Request{ID: id, Method: method}
	if params != nil {
		encoded, _ := json.Marshal(params)
		req.Params = encoded
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	h.stdin.Write(data) //nolint:errcheck
	return id
}

// readResponseFor reads lines until it finds a response matching id.
// Lines that are events (have "event" key, no "id" key) are appended to
// eventsOut when non-nil. Unrecognised non-JSON lines are logged and skipped.
func (h *ipcTestHarness) readResponseFor(id int, eventsOut *[]string) Response {
	h.t.Helper()
	for {
		line := h.nextLine()
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			h.t.Logf("skip non-JSON line: %q", line)
			continue
		}
		if _, hasID := msg["id"]; hasID {
			var resp Response
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				h.t.Logf("skip malformed response: %q", line)
				continue
			}
			if resp.ID == id {
				return resp
			}
		} else if eventsOut != nil {
			if _, hasEvent := msg["event"]; hasEvent {
				*eventsOut = append(*eventsOut, line)
			}
		}
	}
}

// readRawResponseFor reads lines until it finds a response matching id and
// returns the raw wire JSON string. Event lines are collected in eventsOut.
func (h *ipcTestHarness) readRawResponseFor(id int, eventsOut *[]string) string {
	h.t.Helper()
	for {
		line := h.nextLine()
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			h.t.Logf("skip non-JSON line: %q", line)
			continue
		}
		if rawID, hasID := msg["id"]; hasID {
			var parsedID int
			if err := json.Unmarshal(rawID, &parsedID); err != nil {
				continue
			}
			if parsedID == id {
				return line
			}
		} else if eventsOut != nil {
			if _, hasEvent := msg["event"]; hasEvent {
				*eventsOut = append(*eventsOut, line)
			}
		}
	}
}

// sendRaw writes a raw string (plus newline) to the binary's stdin.
// Use for testing invalid JSON handling.
func (h *ipcTestHarness) sendRaw(raw string) {
	h.stdin.Write([]byte(raw + "\n")) //nolint:errcheck
}

// ─── Integration tests ────────────────────────────────────────────────────

// TestIntegration_BinaryStartsAndEmitsReadyEvent verifies that the real
// binary emits an ipc.ready event immediately on startup with the correct
// protocol version.
func TestIntegration_BinaryStartsAndEmitsReadyEvent(t *testing.T) {
	h := newIpcTestHarness(t)

	evt := h.awaitReady()

	if evt.Event != "ipc.ready" {
		t.Errorf("event = %q, want %q", evt.Event, "ipc.ready")
	}

	// data.protocolVersion must equal ProtocolVersion (1).
	dataBytes, err := json.Marshal(evt.Data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(dataBytes, &data); err != nil {
		t.Fatalf("unmarshal event data: %v", err)
	}
	v, ok := data["protocolVersion"]
	if !ok {
		t.Fatal("ipc.ready data missing 'protocolVersion' key")
	}
	// JSON numbers unmarshal as float64 into interface{}.
	if int(v.(float64)) != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %d", v, ProtocolVersion)
	}
}

// TestIntegration_UnknownMethodReturnsMethodNotFound verifies that sending
// an unregistered method name returns ErrMethodNotFound (-32601) and that
// the error message contains the method name.
func TestIntegration_UnknownMethodReturnsMethodNotFound(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("does.not.exist", nil)
	resp := h.readResponseFor(id, nil)

	if resp.Error == nil {
		t.Fatal("expected error response, got nil error")
	}
	if resp.Error.Code != ErrMethodNotFound {
		t.Errorf("Code = %d, want ErrMethodNotFound (%d)", resp.Error.Code, ErrMethodNotFound)
	}
	if !strings.Contains(resp.Error.Message, "does.not.exist") {
		t.Errorf("Message %q should mention the unknown method", resp.Error.Message)
	}
}

// TestIntegration_InvalidJSONReturnsParseError verifies that sending malformed
// JSON over stdin causes the binary to emit an error response with id=0 and
// ErrInvalidParams (-32602).
func TestIntegration_InvalidJSONReturnsParseError(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	h.sendRaw(`{not valid json`)

	// The error response will have id=0 (parse error path in server.Run).
	raw := h.readRawResponseFor(0, nil)

	var resp Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ID != 0 {
		t.Errorf("parse error response ID = %d, want 0", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if resp.Error.Code != ErrInvalidParams {
		t.Errorf("Code = %d, want ErrInvalidParams (%d)", resp.Error.Code, ErrInvalidParams)
	}
}

// TestIntegration_ResponseIDMatchesRequest verifies that the response ID
// always echoes the request ID over the real binary transport.
func TestIntegration_ResponseIDMatchesRequest(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// Use an unknown method — simple and reliable, no scheduler dependency.
	h.mu.Lock()
	h.nextID = 42
	h.mu.Unlock()

	id := h.sendRequest("does.not.exist", nil)
	resp := h.readResponseFor(id, nil)

	if resp.ID != 42 {
		t.Errorf("response ID = %d, want 42", resp.ID)
	}
}

// TestIntegration_SuccessHasNoErrorInWireJSON verifies that the raw wire JSON
// of a successful response does not contain an "error" key.
func TestIntegration_SuccessHasNoErrorInWireJSON(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	// queue.add with the real scheduler — always succeeds and returns {"status":"ok"}.
	id := h.sendRequest("queue.add", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": 1,
	})

	raw := h.readRawResponseFor(id, nil)

	if strings.Contains(raw, `"error"`) {
		t.Errorf("success response wire JSON must not contain \"error\" key: %s", raw)
	}
}

// TestIntegration_ErrorHasNoResultInWireJSON verifies that the raw wire JSON
// of an error response does not contain a "result" key.
func TestIntegration_ErrorHasNoResultInWireJSON(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("does.not.exist", nil)
	raw := h.readRawResponseFor(id, nil)

	if strings.Contains(raw, `"result"`) {
		t.Errorf("error response wire JSON must not contain \"result\" key: %s", raw)
	}
}

// TestIntegration_QueueAddEmitsQueueChangedEvent verifies that adding an item
// to the queue via queue.add causes the real scheduler to emit a queue.changed
// event before the response is written.
func TestIntegration_QueueAddEmitsQueueChangedEvent(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("queue.add", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": 42,
	})

	var events []string
	resp := h.readResponseFor(id, &events)

	if resp.Error != nil {
		t.Fatalf("queue.add returned error: %+v", resp.Error)
	}

	// At least one queue.changed event must have been emitted.
	found := false
	for _, e := range events {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(e), &msg); err != nil {
			continue
		}
		var evtName string
		if err := json.Unmarshal(msg["event"], &evtName); err != nil {
			continue
		}
		if evtName == "queue.changed" {
			found = true
			break
		}
	}
	if !found {
		// The event may have been emitted before the response window — check
		// if the response itself indicates success and accept that as sufficient.
		// Under heavy load or slow I/O the event may arrive after the response.
		// Re-read one more line to catch a late event.
		select {
		case line, ok := <-h.lines:
			if ok {
				var msg map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &msg); err == nil {
					var evtName string
					if rawEvt, hasEvt := msg["event"]; hasEvt {
						if err := json.Unmarshal(rawEvt, &evtName); err == nil {
							if evtName == "queue.changed" {
								found = true
							}
						}
					}
				}
			}
		case <-time.After(2 * time.Second):
		}
	}

	if !found {
		t.Errorf("expected queue.changed event after queue.add, collected events: %v", events)
	}
}

// TestIntegration_QueueListAfterAdd verifies that after adding an issue to
// the queue with queue.add, queue.list returns the issue in its item list.
func TestIntegration_QueueListAfterAdd(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	const testIssueNumber = 99

	// Add issue 99 to the queue.
	addID := h.sendRequest("queue.add", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": testIssueNumber,
	})
	addResp := h.readResponseFor(addID, nil) // discard events
	if addResp.Error != nil {
		t.Fatalf("queue.add failed: %+v", addResp.Error)
	}

	// List the queue.
	listID := h.sendRequest("queue.list", nil)
	listResp := h.readResponseFor(listID, nil)
	if listResp.Error != nil {
		t.Fatalf("queue.list failed: %+v", listResp.Error)
	}

	// Unmarshal the result into QueueState to inspect items.
	resultBytes, err := json.Marshal(listResp.Result)
	if err != nil {
		t.Fatalf("marshal queue.list result: %v", err)
	}
	var state struct {
		Items []struct {
			IssueNumber int `json:"issueNumber"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resultBytes, &state); err != nil {
		t.Fatalf("unmarshal queue state: %v", err)
	}

	found := false
	for _, item := range state.Items {
		if item.IssueNumber == testIssueNumber {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("issue %d not found in queue.list result: %s", testIssueNumber, string(resultBytes))
	}
}

// TestIntegration_PipelineGetStateUnknownReturnsNull verifies that
// pipeline.getState for an issue that has never been run returns a null
// result (no error, nil result) from the real binary.
func TestIntegration_PipelineGetStateUnknownReturnsNull(t *testing.T) {
	h := newIpcTestHarness(t)
	h.awaitReady()

	id := h.sendRequest("pipeline.getState", map[string]interface{}{
		"owner":       "test-org",
		"repo":        "test-repo",
		"issueNumber": 9999,
	})
	resp := h.readResponseFor(id, nil)

	if resp.Error != nil {
		t.Errorf("expected no error, got: %+v", resp.Error)
	}
	// Result should be nil — no state persisted for issue 9999.
	if resp.Result != nil {
		t.Errorf("expected nil result for unknown issue, got: %v", resp.Result)
	}
}
