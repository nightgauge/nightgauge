package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/state"
)

// newTestServer creates a minimal Server wired to a bytes.Buffer for output.
func newTestServer(buf *bytes.Buffer) *Server {
	s := &Server{
		writer:         buf,
		methods:        make(map[string]Handler),
		userClients:    make(map[string]*gh.Client),
		activeRuntimes: make(map[string]*state.RuntimeState),
	}
	s.resolver = NewClientResolver(nil, false)
	return s
}

// TestRecoverPanic_PanicInHandler verifies that recoverPanic catches a panic
// and returns a non-nil error containing "panic recovered".
func TestRecoverPanic_PanicInHandler(t *testing.T) {
	result, err := recoverPanic("test-handler", func() (interface{}, error) {
		panic("intentional panic for testing")
	})
	if result != nil {
		t.Errorf("expected nil result on panic, got %v", result)
	}
	if err == nil {
		t.Fatal("expected error on panic, got nil")
	}
	if !strings.Contains(err.Error(), "panic recovered") {
		t.Errorf("error %q does not contain 'panic recovered'", err.Error())
	}
	if !strings.Contains(err.Error(), "intentional panic for testing") {
		t.Errorf("error %q does not contain original panic message", err.Error())
	}
}

// TestRecoverPanic_NormalExecution verifies that recoverPanic passes through
// normal results without interference.
func TestRecoverPanic_NormalExecution(t *testing.T) {
	result, err := recoverPanic("test-handler", func() (interface{}, error) {
		return "success", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "success" {
		t.Errorf("result = %v, want %q", result, "success")
	}
}

// TestRecoverPanic_ErrorReturn verifies that recoverPanic propagates errors
// from the handler (not panics) unchanged.
func TestRecoverPanic_ErrorReturn(t *testing.T) {
	result, err := recoverPanic("test-handler", func() (interface{}, error) {
		return nil, fmt.Errorf("expected error")
	})
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
	if err == nil || err.Error() != "expected error" {
		t.Errorf("err = %v, want 'expected error'", err)
	}
}

// TestHandleRequest_PanicRecovery verifies that a panic in a registered
// handler is caught, an ErrInternal response is sent, and the server remains
// alive for subsequent requests.
func TestHandleRequest_PanicRecovery(t *testing.T) {
	var buf bytes.Buffer
	s := newTestServer(&buf)

	// Register a handler that panics.
	s.methods["test.panic"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		panic("test handler panic")
	}

	// Call the panicking handler — should not propagate the panic to the test.
	s.handleRequest(context.Background(), Request{ID: 42, Method: "test.panic"})

	// The server should have written an ErrInternal response.
	output := buf.String()
	if !strings.Contains(output, fmt.Sprintf("%d", ErrInternal)) {
		t.Errorf("expected ErrInternal (%d) in output, got: %s", ErrInternal, output)
	}

	// Server should still respond to subsequent requests.
	buf.Reset()
	s.methods["test.ok"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return map[string]string{"status": "ok"}, nil
	}
	s.handleRequest(context.Background(), Request{ID: 43, Method: "test.ok"})

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("failed to parse response after panic recovery: %v", err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error in response after recovery: %v", resp.Error)
	}
}

// TestHandleRequest_PanicEmitsEvent verifies that an ipc.panic event is
// emitted when a handler panics.
func TestHandleRequest_PanicEmitsEvent(t *testing.T) {
	var buf bytes.Buffer
	s := newTestServer(&buf)

	s.methods["test.panic"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		panic("event test panic")
	}

	s.handleRequest(context.Background(), Request{ID: 1, Method: "test.panic"})

	output := buf.String()
	if !strings.Contains(output, "ipc.panic") {
		t.Errorf("expected ipc.panic event in output, got: %s", output)
	}
	if !strings.Contains(output, "test.panic") {
		t.Errorf("expected handler method name in ipc.panic event, got: %s", output)
	}
}

// TestHandleRequest_NilPanicValue verifies recovery handles a nil panic value.
func TestHandleRequest_NilPanicValue(t *testing.T) {
	var buf bytes.Buffer
	s := newTestServer(&buf)

	s.methods["test.nilpanic"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		panic(nil)
	}

	// Must not propagate the panic.
	s.handleRequest(context.Background(), Request{ID: 99, Method: "test.nilpanic"})

	output := buf.String()
	if len(output) == 0 {
		t.Error("expected some output after nil panic recovery, got nothing")
	}
}
