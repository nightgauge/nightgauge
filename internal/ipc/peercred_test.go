package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
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
	prev := peerUID.Load()
	next := peerUIDFunc(fn)
	peerUID.Store(&next)
	t.Cleanup(func() { peerUID.Store(prev) })
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

	// Readiness is BindSocket returning, not the socket file existing (#1158).
	// net.Listen("unix", …) is socket(); bind(); listen() — the file appears
	// at bind(), one syscall BEFORE the listener exists, so a fixture that
	// polled os.Stat handed back a path a dial could still be refused on.
	// That window only opened wide enough to hit under the whole-tree -race
	// gate (#493), where ~108 packages compete for the machine. Binding
	// synchronously here means the listen backlog exists before this
	// function returns; the accept loop may start whenever it likes.
	ln, err := srv.BindSocket(path)
	if err != nil {
		t.Fatalf("bind socket: %v", err)
	}
	go func() { _ = srv.ServeSocket(ctx, ln, path) }()

	return path, &invocations
}

// isPeerClosed reports whether err is the server having closed the connection
// — the expected shape of a refusal that never read the request.
func isPeerClosed(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed)
}

// call dials the socket, sends one request, and returns the response line.
//
// The second result reports whether a response was actually read. On the
// REFUSAL paths the server authenticates before reading (socket.go — the
// ordering is the security property), so it writes its error and closes
// without ever consuming the request. The client's write therefore races the
// close and may legitimately land as EPIPE/ECONNRESET, and the response may be
// gone before it can be read. Treating either as a test failure made
// TestSocketRefusesForeignUID a coin flip that passed every PR and failed on
// main with `write: broken pipe` (#1123).
//
// A refused connection is the same verdict whether it is delivered as a typed
// error or as a closed socket, so both are reported here and the caller
// decides. What must never vary — and what the refusal tests really assert —
// is that the handler did not run.
func call(t *testing.T, path, method string) (Response, bool) {
	t.Helper()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := fmt.Sprintf(`{"id":1,"method":%q,"params":{}}`, method)
	if _, err := fmt.Fprintf(conn, "%s\n", req); err != nil {
		if isPeerClosed(err) {
			return Response{}, false
		}
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err == nil || isPeerClosed(err) {
			return Response{}, false
		}
		t.Fatalf("no response: %v", scanner.Err())
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", scanner.Text(), err)
	}
	return resp, true
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
	resp, got := call(t, path, "test.mutate")

	// Either delivery is a refusal; only a SUCCESS response would be a defect.
	if got {
		if resp.Error == nil {
			t.Fatal("expected an error response for a foreign uid, got success")
		}
		if resp.Error.Code != ErrUnauthorized {
			t.Errorf("code = %d, want ErrUnauthorized (%d)", resp.Error.Code, ErrUnauthorized)
		}
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
	resp, got := call(t, path, "test.mutate")

	// No race on the accept path: the server reads the request before
	// responding, so a missing response here is a real failure.
	if !got {
		t.Fatal("own uid got no response; the connection was closed unread")
	}
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
	resp, got := call(t, path, "test.mutate")

	if !got {
		t.Fatal("unsupported-platform fallback got no response; the connection was closed unread")
	}
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
	resp, got := call(t, path, "test.mutate")

	if got {
		if resp.Error == nil {
			t.Fatal("expected refusal when the peer lookup fails")
		}
		if resp.Error.Code != ErrUnauthorized {
			t.Errorf("code = %d, want ErrUnauthorized (%d)", resp.Error.Code, ErrUnauthorized)
		}
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
