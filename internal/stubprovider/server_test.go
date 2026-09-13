package stubprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// startServer boots a Server on an ephemeral loopback port and returns its
// base URL. The server is stopped via ctx cancellation in t.Cleanup.
func startServer(t *testing.T, cfg Config) string {
	t.Helper()

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, ln)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server did not stop within 3s of cleanup")
		}
	})

	return "http://" + addr
}

func postChatCompletion(t *testing.T, baseURL string, body map[string]any) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp, data
}

func userTurnRequest(content string) map[string]any {
	return map[string]any{
		"model":    "stub/stub-model",
		"messages": []map[string]any{{"role": "user", "content": content}},
		"stream":   false,
	}
}

// secondTurnRequest simulates a client that received turn 1's tool call and
// replied with the assistant tool-call message and the tool's result, so the
// server should now be one assistant message into the conversation.
func secondTurnRequest() map[string]any {
	return map[string]any{
		"model": "stub/stub-model",
		"messages": []map[string]any{
			{"role": "user", "content": "edit calc.py"},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{
					{"id": "call-1", "type": "function", "function": map[string]any{"name": "edit", "arguments": "{}"}},
				},
			},
			{"role": "tool", "tool_call_id": "call-1", "content": "ok"},
		},
		"stream": false,
	}
}

func decodeChatCompletion(t *testing.T, data []byte) chatCompletionResponse {
	t.Helper()
	var resp chatCompletionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("decode chat completion response: %v\nbody: %s", err, data)
	}
	return resp
}

func TestToolEditStopScript(t *testing.T) {
	baseURL := startServer(t, Config{Script: "tool-edit-stop"})

	resp, body := postChatCompletion(t, baseURL, userTurnRequest("edit calc.py"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn 1: status = %d, body = %s", resp.StatusCode, body)
	}
	turn1 := decodeChatCompletion(t, body)
	if len(turn1.Choices) != 1 {
		t.Fatalf("turn 1: expected 1 choice, got %d", len(turn1.Choices))
	}
	if got := turn1.Choices[0].FinishReason; got != "tool_calls" {
		t.Fatalf("turn 1: finish_reason = %q, want tool_calls", got)
	}
	toolCalls := turn1.Choices[0].Message.ToolCalls
	if len(toolCalls) != 1 || toolCalls[0].Function.Name != "edit" {
		t.Fatalf("turn 1: tool_calls = %+v, want one call named edit", toolCalls)
	}
	if toolCalls[0].Function.Arguments == "" {
		t.Fatalf("turn 1: tool call arguments empty")
	}

	// Byte-identical request -> byte-identical reply.
	_, body2 := postChatCompletion(t, baseURL, userTurnRequest("edit calc.py"))
	if !bytes.Equal(body, body2) {
		t.Fatalf("turn 1 replayed: bodies differ\nfirst:  %s\nsecond: %s", body, body2)
	}

	// Turn 2: prior assistant/tool messages present -> final text, stop.
	resp2, body3 := postChatCompletion(t, baseURL, secondTurnRequest())
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("turn 2: status = %d, body = %s", resp2.StatusCode, body3)
	}
	turn2 := decodeChatCompletion(t, body3)
	if got := turn2.Choices[0].FinishReason; got != "stop" {
		t.Fatalf("turn 2: finish_reason = %q, want stop", got)
	}
	if turn2.Choices[0].Message.Content == nil || *turn2.Choices[0].Message.Content == "" {
		t.Fatalf("turn 2: expected non-empty final text content")
	}
}

func TestBashThenStopScript(t *testing.T) {
	baseURL := startServer(t, Config{Script: "bash-then-stop"})

	resp, body := postChatCompletion(t, baseURL, userTurnRequest("run calc.py"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	turn1 := decodeChatCompletion(t, body)
	toolCalls := turn1.Choices[0].Message.ToolCalls
	if len(toolCalls) != 1 || toolCalls[0].Function.Name != "bash" {
		t.Fatalf("tool_calls = %+v, want one call named bash", toolCalls)
	}
}

func TestOverflowScript(t *testing.T) {
	baseURL := startServer(t, Config{Script: "overflow"})

	resp, body := postChatCompletion(t, baseURL, userTurnRequest("a very long prompt"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "context_length_exceeded") {
		t.Errorf("body missing OpenAI context_length_exceeded wording: %s", body)
	}
	if !strings.Contains(string(body), "n_ctx") {
		t.Errorf("body missing local-server n_ctx wording: %s", body)
	}
}

func TestErrorScript(t *testing.T) {
	baseURL := startServer(t, Config{Script: "error"})

	resp, body := postChatCompletion(t, baseURL, userTurnRequest("trigger failure"))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", resp.StatusCode, body)
	}
	var errResp errorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("decode error body: %v (body: %s)", err, body)
	}
	if errResp.Error.Message == "" {
		t.Errorf("error message empty")
	}
}

func TestSlowScriptDelaysFirstChunk(t *testing.T) {
	delay := 300 * time.Millisecond
	baseURL := startServer(t, Config{Script: "slow", DelayOverride: &delay})

	req := userTurnRequest("go slow")
	req["stream"] = true
	raw, _ := json.Marshal(req)

	start := time.Now()
	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	elapsed := time.Since(start)
	if err != nil && n == 0 {
		t.Fatalf("reading first chunk: %v", err)
	}
	if elapsed < delay {
		t.Errorf("first chunk arrived after %s, want at least %s", elapsed, delay)
	}
}

func TestModelsEndpoint(t *testing.T) {
	baseURL := startServer(t, Config{Script: "tool-edit-stop"})

	resp, err := http.Get(baseURL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var list modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	scripts, err := loadScripts()
	if err != nil {
		t.Fatalf("loadScripts: %v", err)
	}
	want := scripts["tool-edit-stop"].Models
	if len(list.Data) != len(want) {
		t.Fatalf("got %d models, want %d (%v)", len(list.Data), len(want), want)
	}
	for i, m := range list.Data {
		if m.ID != want[i] {
			t.Errorf("model[%d].ID = %q, want %q", i, m.ID, want[i])
		}
	}
}

func TestRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.0.2.1:0"} {
		t.Run(addr, func(t *testing.T) {
			ln, err := Listen(addr)
			if err == nil {
				ln.Close()
				t.Fatalf("Listen(%q) succeeded, want an error", addr)
			}
			if ln != nil {
				t.Fatalf("Listen(%q) returned a non-nil listener alongside an error", addr)
			}
		})
	}
}

func TestMaxRequestsStopsServing(t *testing.T) {
	srv, err := NewServer(Config{Script: "tool-edit-stop", MaxRequests: 2})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()

	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), ln) }()

	baseURL := "http://" + addr
	for i := 0; i < 2; i++ {
		resp, body := postChatCompletion(t, baseURL, userTurnRequest("hi"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, body = %s", i+1, resp.StatusCode, body)
		}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3s after max-requests reached")
	}

	// The third request must never be served: the listener is closed.
	if _, err := http.Post(baseURL+"/v1/chat/completions", "application/json", bytes.NewReader([]byte("{}"))); err == nil {
		t.Fatal("third request succeeded, want connection failure after max-requests")
	}
}

// TestMaxRequestsIsHardCapUnderConcurrency drives handleChatCompletions
// directly (no network hop, to remove dial/scheduling jitter) from many
// goroutines released at once against a server configured with
// MaxRequests: 1. The accept/reject decision and the request counter update
// must be a single atomic step, so at most one of the concurrent calls may
// be served; anything else means the cap was exceeded.
func TestMaxRequestsIsHardCapUnderConcurrency(t *testing.T) {
	srv, err := NewServer(Config{Script: "tool-edit-stop", MaxRequests: 1})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	raw, err := json.Marshal(userTurnRequest("hi"))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	const concurrency = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	statuses := make([]int, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		go func(i int, req *http.Request, rec *httptest.ResponseRecorder) {
			defer wg.Done()
			<-start
			srv.handleChatCompletions(rec, req)
			statuses[i] = rec.Code
		}(i, req, rec)
	}
	close(start)
	wg.Wait()

	served := 0
	for _, s := range statuses {
		if s == http.StatusOK {
			served++
		}
	}
	if served != 1 {
		t.Fatalf("MaxRequests: 1 but %d of %d concurrent calls to handleChatCompletions were served (statuses=%v), want exactly 1", served, concurrency, statuses)
	}
}

// TestNewServerRejectsScriptWithUnknownKind exercises NewServer's kind
// validation against the real embedded scripts.json. "invalid-kind-fixture"
// exists in scripts.json solely for this test: it has a kind that is none of
// "turns", "overflow" or "error", so NewServer must fail fast at
// construction time instead of the server later silently serving it as an
// ordinary turns script.
func TestNewServerRejectsScriptWithUnknownKind(t *testing.T) {
	_, err := NewServer(Config{Script: "invalid-kind-fixture"})
	if err == nil {
		t.Fatal("NewServer succeeded for a script with an unknown kind, want an error")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("error = %v, want it to mention the unknown kind", err)
	}
}

// TestHandleChatCompletionsRejectsUnknownScriptKind constructs a Server
// directly (bypassing NewServer's validation) with a script Kind that is not
// one of the known constants, to pin the handler's own dispatch as
// fail-closed: an unrecognized kind must never fall through to being served
// as an ordinary turns completion.
func TestHandleChatCompletionsRejectsUnknownScriptKind(t *testing.T) {
	srv := &Server{
		cfg: Config{
			Script:      "kind-typo",
			MaxRequests: DefaultMaxRequests,
			IdleTimeout: DefaultIdleTimeout,
			Log:         log.New(io.Discard, "", 0),
		},
		script:     Script{Models: []string{"stub/stub-model"}, Kind: "eror"},
		maxReached: make(chan struct{}, 1),
	}

	ts := httptest.NewServer(http.HandlerFunc(srv.handleChatCompletions))
	defer ts.Close()

	resp, body := postChatCompletion(t, ts.URL, userTurnRequest("hi"))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s, want 500: an unknown script kind must fail closed, not silently serve as turns", resp.StatusCode, body)
	}
}

func TestIdleTimeoutStopsServing(t *testing.T) {
	srv, err := NewServer(Config{Script: "tool-edit-stop", IdleTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), ln) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Serve did not return within 1s of the idle timeout")
	}
}

func TestNoRequestBodyPersistence(t *testing.T) {
	const marker = "SECRET-MARKER"

	var logBuf bytes.Buffer
	var mu sync.Mutex
	logger := log.New(&syncWriter{w: &logBuf, mu: &mu}, "", 0)

	tempDir := t.TempDir()

	baseURL := startServer(t, Config{Script: "tool-edit-stop", Log: logger})

	resp, body := postChatCompletion(t, baseURL, userTurnRequest(marker))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	// Give any (incorrect) async log write a moment to land before asserting.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	logged := logBuf.String()
	mu.Unlock()
	if strings.Contains(logged, marker) {
		t.Errorf("log buffer contains the request body marker: %s", logged)
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", tempDir, err)
	}
	for _, e := range entries {
		t.Errorf("unexpected file written to temp dir: %s", e.Name())
	}
	found, err := dirContainsString(tempDir, marker)
	if err != nil {
		t.Fatalf("scanning temp dir: %v", err)
	}
	if found {
		t.Errorf("temp dir contains the request body marker")
	}
}

type syncWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func dirContainsString(dir, needle string) (bool, error) {
	found := false
	err := filepathWalk(dir, func(path string, isDir bool) error {
		if isDir {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			found = true
		}
		return nil
	})
	return found, err
}

// filepathWalk is a tiny os.ReadDir-based recursive walk, avoiding an extra
// import for a single test helper.
func filepathWalk(dir string, fn func(path string, isDir bool) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := dir + string(os.PathSeparator) + e.Name()
		if err := fn(path, e.IsDir()); err != nil {
			return err
		}
		if e.IsDir() {
			if err := filepathWalk(path, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- subprocess SIGTERM test ------------------------------------------

var stubProviderBinary string

func TestMain(m *testing.M) {
	bin, cleanup, err := buildStubProviderBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stubprovider: building stub-provider for tests: %v\n", err)
		os.Exit(1)
	}
	stubProviderBinary = bin
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func buildStubProviderBinary() (string, func(), error) {
	dir, err := os.MkdirTemp("", "stub-provider-test-bin-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	bin := dir + "/stub-provider"
	cmd := exec.Command("go", "build", "-o", bin, "github.com/nightgauge/nightgauge/cmd/stub-provider")
	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build: %w\n%s", err, out)
	}
	return bin, cleanup, nil
}

// TestSIGTERMStopsWithinTwoSeconds spawns the real binary as a subprocess,
// captures its PID at spawn, sends SIGTERM, and verifies the process is dead
// within 2s via kill -0. It always kills the PID it captured before
// returning, even on failure.
func TestSIGTERMStopsWithinTwoSeconds(t *testing.T) {
	if stubProviderBinary == "" {
		t.Skip("stub-provider binary not built")
	}

	cmd := exec.Command(stubProviderBinary, "--script", "tool-edit-stop", "--listen", "127.0.0.1:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	pid := cmd.Process.Pid

	// Reap the child as soon as it exits. kill(pid, 0) succeeds on a zombie
	// until the parent calls Wait, so without this concurrent reaper the
	// liveness poll below could never observe the process as gone.
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	killAndWait := func() {
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
	}
	defer killAndWait()

	// Wait for the base_url line so the server has actually bound.
	lineCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := stdout.Read(buf)
		lineCh <- string(buf[:n])
	}()
	select {
	case line := <-lineCh:
		if !strings.Contains(line, "base_url") {
			t.Fatalf("unexpected stdout: %s", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess never printed base_url")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		select {
		case <-waitDone:
			return
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive 2s after SIGTERM", pid)
}
