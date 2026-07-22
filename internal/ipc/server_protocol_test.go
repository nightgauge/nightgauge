// Package ipc — Phase 3 IPC protocol tests.
//
// These tests validate the real JSON-over-stdio wire format used between the
// TypeScript VSCode extension and the Go binary. Unlike the existing
// server_test.go (which tests struct marshaling in isolation), these tests
// exercise the full request→dispatch→response cycle at the server level,
// verifying:
//   - Newline-delimited message framing
//   - Request/response ID matching
//   - Structured error responses (ErrMethodNotFound, ErrInvalidParams, ErrInternal)
//   - Concurrent request handling with out-of-order ID matching
//   - Wire format: result and error are mutually exclusive
//
// See: docs/GO_BINARY.md — IPC protocol section
// See: packages/nightgauge-vscode/src/services/IpcClient.ts
package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// newProtocolTestServer creates a minimal IPC server with test handlers
// writing to buf. The client (gh.Client) is nil — test handlers must not
// call it.
func newProtocolTestServer(buf *bytes.Buffer) *Server {
	s := &Server{
		writer:  buf,
		methods: make(map[string]Handler),
	}

	// test.echo — returns params as-is, used for ID-matching tests.
	s.methods["test.echo"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var v interface{}
		if err := json.Unmarshal(params, &v); err != nil {
			return nil, fmt.Errorf("echo: %w", err)
		}
		return v, nil
	}

	// test.fail — always returns an error, used for ErrInternal tests.
	s.methods["test.fail"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("intentional handler error")
	}

	// test.value — returns a fixed value, used for result-field tests.
	s.methods["test.value"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"key": "value"}, nil
	}

	return s
}

// parseResponse parses one JSON line into a Response struct.
func parseResponse(t *testing.T, line string) Response {
	t.Helper()
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("parseResponse: invalid JSON %q: %v", line, err)
	}
	return resp
}

// readLine reads one line from the buffer (up to the first \n).
func readLine(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	line, err := buf.ReadString('\n')
	if err != nil {
		t.Fatalf("readLine: %v (buffer: %q)", err, buf.String())
	}
	return strings.TrimRight(line, "\n")
}

// ─── Newline framing ──────────────────────────────────────────────────────

// TestProtocol_ResponseNewlineTerminated verifies that every IPC response
// ends with a single newline character, as required by the JSON-over-stdio
// newline-delimited protocol.
func TestProtocol_ResponseNewlineTerminated(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     1,
		Method: "test.value",
	})

	raw := buf.String()
	if !strings.HasSuffix(raw, "\n") {
		t.Errorf("response must end with \\n, got: %q", raw)
	}
	if strings.Count(raw, "\n") != 1 {
		t.Errorf("response must contain exactly one newline, got: %q", raw)
	}
}

// TestProtocol_ErrorResponseNewlineTerminated verifies that error responses
// are also newline-terminated.
func TestProtocol_ErrorResponseNewlineTerminated(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     2,
		Method: "unknown.method",
	})

	raw := buf.String()
	if !strings.HasSuffix(raw, "\n") {
		t.Errorf("error response must end with \\n, got: %q", raw)
	}
}

// ─── ID matching ──────────────────────────────────────────────────────────

// TestProtocol_ResponseIDEchosRequest verifies that the response ID always
// equals the request ID. The TypeScript client routes responses to pending
// promises using this ID.
func TestProtocol_ResponseIDEchosRequest(t *testing.T) {
	cases := []int{1, 42, 9999, 0}
	for _, id := range cases {
		t.Run(fmt.Sprintf("id=%d", id), func(t *testing.T) {
			buf := &bytes.Buffer{}
			s := newProtocolTestServer(buf)

			s.handleRequest(context.Background(), Request{
				ID:     id,
				Method: "test.value",
			})

			resp := parseResponse(t, readLine(t, buf))
			if resp.ID != id {
				t.Errorf("ID = %d, want %d", resp.ID, id)
			}
		})
	}
}

// TestProtocol_ErrorResponseIDEchosRequest verifies that error responses also
// echo the request ID.
func TestProtocol_ErrorResponseIDEchosRequest(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     17,
		Method: "nonexistent.method",
	})

	resp := parseResponse(t, readLine(t, buf))
	if resp.ID != 17 {
		t.Errorf("error response ID = %d, want 17", resp.ID)
	}
}

// ─── Error codes ──────────────────────────────────────────────────────────

// TestProtocol_UnknownMethodReturnsMethodNotFound verifies that calling an
// unregistered method returns error code ErrMethodNotFound (-32601).
func TestProtocol_UnknownMethodReturnsMethodNotFound(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     1,
		Method: "does.not.exist",
	})

	resp := parseResponse(t, readLine(t, buf))
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

// TestProtocol_HandlerErrorReturnsInternalError verifies that when a handler
// returns an error, the server returns ErrInternal (-32603) with the error
// message in the response.
func TestProtocol_HandlerErrorReturnsInternalError(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     1,
		Method: "test.fail",
	})

	resp := parseResponse(t, readLine(t, buf))
	if resp.Error == nil {
		t.Fatal("expected error response, got nil error")
	}
	if resp.Error.Code != ErrInternal {
		t.Errorf("Code = %d, want ErrInternal (%d)", resp.Error.Code, ErrInternal)
	}
	if !strings.Contains(resp.Error.Message, "intentional handler error") {
		t.Errorf("Message %q should contain handler error text", resp.Error.Message)
	}
}

// TestProtocol_InvalidJSONInputReturnsParseError verifies that sending
// malformed JSON over stdio causes the server to emit an error response
// with id=0 and ErrInvalidParams code.
func TestProtocol_InvalidJSONInputReturnsParseError(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	// Simulate what Run() does when it encounters bad JSON.
	invalidLine := []byte(`{not valid json`)
	var req Request
	if err := json.Unmarshal(invalidLine, &req); err != nil {
		s.sendError(0, ErrInvalidParams, fmt.Sprintf("invalid JSON: %v", err))
	}

	resp := parseResponse(t, readLine(t, buf))
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

// ─── Wire format invariants ───────────────────────────────────────────────

// TestProtocol_SuccessResponseHasNoError verifies that a successful response
// has a nil error field (omitted from JSON), satisfying the mutual-exclusivity
// invariant expected by the TypeScript client.
func TestProtocol_SuccessResponseHasNoError(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     1,
		Method: "test.value",
	})

	resp := parseResponse(t, readLine(t, buf))
	if resp.Error != nil {
		t.Errorf("success response should not have error field, got: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Error("success response should have a non-nil result")
	}
}

// TestProtocol_ErrorResponseHasNoResult verifies that an error response
// does not include a result field, maintaining the mutual-exclusivity
// invariant.
func TestProtocol_ErrorResponseHasNoResult(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.handleRequest(context.Background(), Request{
		ID:     1,
		Method: "unknown.method",
	})

	resp := parseResponse(t, readLine(t, buf))
	if resp.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if resp.Result != nil {
		t.Errorf("error response should not have result field, got: %v", resp.Result)
	}
}

// TestProtocol_ErrorFieldsInWireJSON verifies that the "error" key is absent
// from the raw JSON wire bytes of a successful response (not just nil after
// unmarshal), and that "result" is absent from error responses.
func TestProtocol_ErrorFieldsInWireJSON(t *testing.T) {
	t.Run("success omits error key", func(t *testing.T) {
		buf := &bytes.Buffer{}
		s := newProtocolTestServer(buf)
		s.handleRequest(context.Background(), Request{ID: 1, Method: "test.value"})
		raw := readLine(t, buf)
		if strings.Contains(raw, `"error"`) {
			t.Errorf("success response wire JSON must not contain \"error\" key: %s", raw)
		}
	})

	t.Run("error omits result key", func(t *testing.T) {
		buf := &bytes.Buffer{}
		s := newProtocolTestServer(buf)
		s.handleRequest(context.Background(), Request{ID: 1, Method: "unknown.method"})
		raw := readLine(t, buf)
		if strings.Contains(raw, `"result"`) {
			t.Errorf("error response wire JSON must not contain \"result\" key: %s", raw)
		}
	})
}

// ─── Concurrent request handling ─────────────────────────────────────────

// TestProtocol_ConcurrentRequestsMatchedByID fires N concurrent requests and
// verifies that each response carries the correct ID, validating that the
// server's goroutine-per-request model maintains ID integrity when multiple
// requests are in-flight simultaneously.
func TestProtocol_ConcurrentRequestsMatchedByID(t *testing.T) {
	const n = 10
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	// Register a handler that echoes the id field from params.
	s.methods["test.id.echo"] = func(_ context.Context, params json.RawMessage) (interface{}, error) {
		var p struct {
			ReqID int `json:"reqId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]int{"reqId": p.ReqID}, nil
	}

	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			params, _ := json.Marshal(map[string]int{"reqId": id})
			s.handleRequest(context.Background(), Request{
				ID:     id,
				Method: "test.id.echo",
				Params: params,
			})
		}(i)
	}
	wg.Wait()

	// Parse all n responses and build an id→result map.
	scanner := bufio.NewScanner(buf)
	responses := make(map[int]Response, n)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Errorf("invalid response JSON: %q — %v", line, err)
			continue
		}
		responses[resp.ID] = resp
	}

	if len(responses) != n {
		t.Errorf("expected %d responses, got %d", n, len(responses))
	}
	for id := 1; id <= n; id++ {
		resp, ok := responses[id]
		if !ok {
			t.Errorf("missing response for id=%d", id)
			continue
		}
		if resp.Error != nil {
			t.Errorf("id=%d: unexpected error: %+v", id, resp.Error)
		}
	}
}

// ─── Event wire format ────────────────────────────────────────────────────

// TestProtocol_EventNewlineTerminated verifies that unsolicited events (which
// have no ID field) are also newline-terminated valid JSON.
func TestProtocol_EventNewlineTerminated(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	s.sendJSON(Event{
		Event: "stage.complete",
		Data:  map[string]interface{}{"issue": 42, "stage": "feature-dev"},
	})

	raw := buf.String()
	if !strings.HasSuffix(raw, "\n") {
		t.Errorf("event must end with \\n, got: %q", raw)
	}

	// Verify it's valid JSON with the expected event key.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimRight(raw, "\n")), &parsed); err != nil {
		t.Fatalf("event is not valid JSON: %v", err)
	}
	if _, ok := parsed["event"]; !ok {
		t.Error("event JSON missing 'event' key")
	}
	if _, ok := parsed["id"]; ok {
		t.Error("event JSON must not have 'id' key (events are unsolicited)")
	}
}

// ─── Multiple sequential requests ────────────────────────────────────────

// TestProtocol_SequentialRequestsProduceOneResponseEach verifies that
// sending N sequential requests produces exactly N responses (one per
// request), each with the correct ID.
func TestProtocol_SequentialRequestsProduceOneResponseEach(t *testing.T) {
	const n = 5
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	for i := 1; i <= n; i++ {
		s.handleRequest(context.Background(), Request{
			ID:     i,
			Method: "test.value",
		})
	}

	scanner := bufio.NewScanner(buf)
	seen := make(map[int]bool)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Errorf("invalid JSON: %q", line)
			continue
		}
		if seen[resp.ID] {
			t.Errorf("duplicate response for id=%d", resp.ID)
		}
		seen[resp.ID] = true
	}

	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Errorf("no response for id=%d", i)
		}
	}
}

// ─── Params handling ──────────────────────────────────────────────────────

// TestProtocol_EchoHandlerReturnsParamsAsResult validates the test.echo
// handler that underpins the ID-matching tests — it should return whatever
// params it receives as its result.
func TestProtocol_EchoHandlerReturnsParamsAsResult(t *testing.T) {
	buf := &bytes.Buffer{}
	s := newProtocolTestServer(buf)

	params, _ := json.Marshal(map[string]string{"hello": "world"})
	s.handleRequest(context.Background(), Request{
		ID:     1,
		Method: "test.echo",
		Params: params,
	})

	resp := parseResponse(t, readLine(t, buf))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// Result should be a map with the echoed key.
	resultMap, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result type = %T, want map[string]interface{}", resp.Result)
	}
	if resultMap["hello"] != "world" {
		t.Errorf("result[\"hello\"] = %v, want \"world\"", resultMap["hello"])
	}
}

// TestProtocol_AllErrorCodesAreNegative verifies that the three defined IPC
// error codes follow the convention of negative integers, matching the
// JSON-RPC style used in LSP and this protocol.
func TestProtocol_AllErrorCodesAreNegative(t *testing.T) {
	codes := []struct {
		name string
		code int
	}{
		{"ErrMethodNotFound", ErrMethodNotFound},
		{"ErrInvalidParams", ErrInvalidParams},
		{"ErrInternal", ErrInternal},
	}
	for _, c := range codes {
		if c.code >= 0 {
			t.Errorf("%s = %d, want negative integer", c.name, c.code)
		}
	}
}
