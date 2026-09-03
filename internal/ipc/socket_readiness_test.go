package ipc

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestBindSocketIsListenReady pins the readiness contract of the socket
// transport (#1158): when BindSocket returns, the socket is ACCEPTING — not
// merely present on disk. The dial happens with no accept loop running at
// all, so a bind()-without-listen() implementation (or any "the file exists
// so it's ready" shortcut) would be refused here rather than merely racing.
//
// Run with -race -count=50: there is no poll and no sleep between bind and
// dial, so any residual window shows up as a hard ECONNREFUSED.
func TestBindSocketIsListenReady(t *testing.T) {
	srv := NewServer(nil)
	path := shortTempSocket(t, "r.sock")

	ln, err := srv.BindSocket(path)
	if err != nil {
		t.Fatalf("BindSocket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// Deliberately NOT serving yet: the connection must be queued in the
	// listen backlog, which only exists after listen() — the exact syscall
	// the old os.Stat readiness poll could not see.
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial immediately after BindSocket returned: %v", err)
	}
	_ = conn.Close()

	// And the queued connection is then served, not dropped, once the
	// accept loop starts.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ServeSocket(ctx, ln, path) }()

	c, err := DialClient(ctx, path, time.Second)
	if err != nil {
		t.Fatalf("DialClient after ServeSocket: %v", err)
	}
	_ = c.Close()
}
