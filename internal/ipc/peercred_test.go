package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// withPeerUID replaces the peer-credential lookup for one test.
//
// The rejection path cannot be reached otherwise: the test process and the
// daemon it starts share a uid by construction, so without this seam the
// refusal branch would be unreachable and the security property untested.
func withPeerUID(t *testing.T, fn func(net.Conn) (uint32, error)) {
	t.Helper()
	prev := peerUID
	peerUID = fn
	t.Cleanup(func() { peerUID = prev })
}

// shortTempSocket returns a socket path short enough to bind.
//
// A Unix socket path is capped near 104 bytes, and t.TempDir() embeds the full
// test name - long test names silently push the path past the limit and bind
// fails with "invalid argument", which reads like a permissions problem rather
// than a length one.
func shortTempSocket(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ng")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// startSocketServer brings up a daemon socket with one instrumented verb and
// returns its path plus a counter of how many times that verb actually ran.
func startSocketServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	var invocations atomic.Int32
	srv := NewServer(nil)
	srv.methods["test.mutate"] = func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		invocations.Add(1)
		return map[string]string{"ok": "true"}, nil
	}

	path := shortTempSocket(t, "d.sock")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = srv.ListenSocket(ctx, path)
	}()
	<-ready

	// ListenSocket creates the socket asynchronously; wait for the file.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	return path, &invocations
}

// call dials the socket, sends one request, and returns the response line.
func call(t *testing.T, path, method string) Response {
	t.Helper()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := fmt.Sprintf(`{"id":1,"method":%q,"params":{}}`, method)
	if _, err := fmt.Fprintf(conn, "%s\n", req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("no response: %v", scanner.Err())
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", scanner.Text(), err)
	}
	return resp
}

// The acceptance criterion for #378: a caller outside the trust boundary
// cannot invoke a verb.
//
// The assertion that matters is the invocation counter, not the error. An
// implementation that refused the RESPONSE while still dispatching the handler
// would satisfy an error-only assertion and leave the state mutated - which is
// the whole defect, since these verbs mint runs and write records.
func TestSocketRefusesForeignUID(t *testing.T) {
	withPeerUID(t, func(net.Conn) (uint32, error) {
		return uint32(os.Getuid() + 1), nil
	})

	path, invocations := startSocketServer(t)
	resp := call(t, path, "test.mutate")

	if resp.Error == nil {
		t.Fatal("expected an error response for a foreign uid, got success")
	}
	if resp.Error.Code != ErrUnauthorized {
		t.Errorf("code = %d, want ErrUnauthorized (%d)", resp.Error.Code, ErrUnauthorized)
	}
	if got := invocations.Load(); got != 0 {
		t.Errorf("handler ran %d times; a refused connection must never reach a verb", got)
	}
}

// The other half: the legitimate path is unaffected.
func TestSocketAcceptsOwnUID(t *testing.T) {
	withPeerUID(t, func(net.Conn) (uint32, error) {
		return uint32(os.Getuid()), nil
	})

	path, invocations := startSocketServer(t)
	resp := call(t, path, "test.mutate")

	if resp.Error != nil {
		t.Fatalf("own uid refused: %+v", resp.Error)
	}
	if got := invocations.Load(); got != 1 {
		t.Errorf("handler ran %d times, want 1", got)
	}
}

// A platform with no peer-credential mechanism falls back to the 0600 socket
// mode rather than refusing every connection, which would make the daemon
// unusable there. Pinned because it is a deliberate weaker posture, not an
// oversight - if someone later "tightens" it to a refusal, that is a product
// decision and this test should be the thing that surfaces it.
func TestSocketAllowsWhenPeerCredUnsupported(t *testing.T) {
	withPeerUID(t, func(net.Conn) (uint32, error) {
		return 0, ErrPeerCredUnsupported
	})

	path, invocations := startSocketServer(t)
	resp := call(t, path, "test.mutate")

	if resp.Error != nil {
		t.Fatalf("unsupported-platform fallback refused the connection: %+v", resp.Error)
	}
	if got := invocations.Load(); got != 1 {
		t.Errorf("handler ran %d times, want 1", got)
	}
}

// A failed lookup on a platform that DOES support it is anomalous, and
// "I cannot tell who you are" is not a reason to admit someone. This is the
// case that must not be folded into the unsupported sentinel.
func TestSocketRefusesWhenPeerCredLookupFails(t *testing.T) {
	withPeerUID(t, func(net.Conn) (uint32, error) {
		return 0, errors.New("getsockopt exploded")
	})

	path, invocations := startSocketServer(t)
	resp := call(t, path, "test.mutate")

	if resp.Error == nil {
		t.Fatal("expected refusal when the peer lookup fails")
	}
	if resp.Error.Code != ErrUnauthorized {
		t.Errorf("code = %d, want ErrUnauthorized (%d)", resp.Error.Code, ErrUnauthorized)
	}
	if got := invocations.Load(); got != 0 {
		t.Errorf("handler ran %d times; a failed credential check must not dispatch", got)
	}
}

// The real, un-stubbed lookup must work on this machine. Without this the
// entire suite could pass against a seam that never matches production
// behaviour - the stub proves the policy, this proves the mechanism.
func TestPlatformPeerUIDMatchesSelfOverRealSocket(t *testing.T) {
	path := shortTempSocket(t, "p.sock")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := net.Dial("unix", path)
		if err == nil {
			defer c.Close()
			time.Sleep(200 * time.Millisecond)
		}
	}()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer conn.Close()

	uid, err := platformPeerUID(conn)
	if errors.Is(err, ErrPeerCredUnsupported) {
		t.Skipf("peer credentials unsupported on %s", os.Getenv("GOOS"))
	}
	if err != nil {
		t.Fatalf("platformPeerUID: %v", err)
	}
	if int(uid) != os.Getuid() {
		t.Errorf("peer uid = %d, want this process's uid %d", uid, os.Getuid())
	}
}
