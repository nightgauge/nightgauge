package ipc

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// newSocketTestServer starts a Server's socket listener on a temp-dir socket
// path and returns the server plus that path. Mirrors newAttentionTestServer
// (attention_test.go) so socket-transport tests exercise the same store
// wiring as the stdio-transport tests.
func newSocketTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), t.TempDir())
	s := &Server{autonomousScheduler: as, writer: io.Discard, methods: make(map[string]Handler)}
	s.methods["attention.list"] = s.handleAttentionList

	sockPath := filepath.Join(t.TempDir(), "daemon.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = s.ListenSocket(ctx, sockPath)
	}()
	<-ready
	// ListenSocket's net.Listen happens synchronously relative to the
	// goroutine scheduling above being unreliable — poll briefly for the
	// socket file to appear rather than assuming it's ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := DialClient(ctx, sockPath, 50*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s, sockPath
}

func TestDialClientRoundTrip(t *testing.T) {
	_, sockPath := newSocketTestServer(t)

	ctx := context.Background()
	client, err := DialClient(ctx, sockPath, time.Second)
	if err != nil {
		t.Fatalf("DialClient: %v", err)
	}
	defer client.Close()

	var res AttentionListResult
	if err := client.Call(ctx, "attention.list", AttentionListParams{}, &res); err != nil {
		t.Fatalf("Call attention.list: %v", err)
	}
	if res.Requests == nil {
		t.Error("Requests should be an empty slice, not nil, for an empty store")
	}
	if len(res.Requests) != 0 {
		t.Errorf("Requests = %d, want 0 for an empty store", len(res.Requests))
	}
}

func TestDialClientNoDaemonFailsPromptly(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "no-daemon.sock")

	start := time.Now()
	_, err := DialClient(context.Background(), sockPath, 300*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a dial error against a nonexistent socket path")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("dial to nonexistent path took %v, want well under the 300ms timeout bound", elapsed)
	}
}

func TestDialClientUnknownMethod(t *testing.T) {
	_, sockPath := newSocketTestServer(t)

	ctx := context.Background()
	client, err := DialClient(ctx, sockPath, time.Second)
	if err != nil {
		t.Fatalf("DialClient: %v", err)
	}
	defer client.Close()

	if err := client.Call(ctx, "no.such.method", struct{}{}, nil); err == nil {
		t.Fatal("expected an error calling an unregistered method")
	}
}
