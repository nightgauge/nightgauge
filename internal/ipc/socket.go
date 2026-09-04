package ipc

// Workspace-scoped Unix domain socket transport for `nightgauge serve`
// (#263). Additive to the existing stdio JSON-RPC loop (Run): a standalone
// terminal CLI process has no access to the private stdio pipe the VSCode
// extension owns, so it dials this socket instead to reach the same
// VerbExecutor/handler set. One connection per request/response round-trip —
// there is no push/subscribe traffic over this transport; that stays
// stdio-only (Emit).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrSocketInUse reports that a LIVE daemon is already accepting on the
// socket path, so this process must not bind it.
//
// It is not an error the caller should treat as fatal. `serve` is two
// transports: the stdio JSON-RPC loop, which is private to one extension
// host and may legitimately run once per VS Code window, and this socket,
// which is the workspace's single terminal-facing endpoint. A second window's
// daemon losing the socket is correct and expected — it keeps its stdio pipe
// and the terminal CLI keeps reaching the first daemon. Refusing to start
// `serve` over this would be the outage #1349 already backed out of once.
var ErrSocketInUse = errors.New("ipc: a live daemon is already listening on the socket")

// bindProbeTimeout bounds the liveness probe in BindSocket. It only ever
// dials a path on the local filesystem, where connect() either completes or
// is refused immediately; the timeout exists so a socket file whose peer is
// wedged (bound and listening but never accepting) cannot hang daemon
// startup forever.
const bindProbeTimeout = 250 * time.Millisecond

// DaemonSocketPath returns the workspace-scoped Unix socket path a co-located
// `nightgauge serve` daemon listens on. Cross-machine/cross-workspace
// discovery is explicitly out of scope (#263) — this is scoped to the same
// `.nightgauge/` directory as every other workspace-local state file.
func DaemonSocketPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "daemon.sock")
}

// ListenSocket binds a Unix domain socket at path and serves request/response
// JSON-RPC calls (reusing the Request/Response envelope from protocol.go)
// until ctx is done. It is BindSocket followed by ServeSocket; the daemon
// (`nightgauge serve`) runs it on a goroutine and never needs to know when
// the socket became reachable. Anything that DOES need that — a test fixture,
// a supervisor that hands the path to a client — must call the two halves
// itself, because ListenSocket blocks for the daemon's lifetime and has no
// moment at which "ready" could be reported.
//
// It is additive to Run's stdio loop — stdio push events (Emit) are
// unaffected; socket clients receive only the response to their own request,
// never the broadcast stream.
//
// Windows is unsupported (net.Listen("unix", ...) is POSIX-only); callers on
// Windows get the listen error back and should log it non-fatally, matching
// today's "no daemon reachable" fallback behavior on every OS (no
// regression).
func (s *Server) ListenSocket(ctx context.Context, path string) error {
	ln, err := s.BindSocket(path)
	if err != nil {
		return err
	}
	return s.ServeSocket(ctx, ln, path)
}

// BindSocket creates the socket at path and returns the listener. It is the
// readiness signal for the socket transport (#1158): when it returns nil
// error, the kernel is accepting connections on path — bind() AND listen()
// have both happened — and a client may dial immediately. Connections made
// before ServeSocket starts accepting simply wait in the listen backlog; they
// are not refused.
//
// This matters because the socket FILE is not that signal. net.Listen("unix")
// is socket(); bind(); listen(), and the file appears at bind(), one syscall
// before anyone is listening. A client that dials in that window gets
// ECONNREFUSED. Readiness observed by polling os.Stat therefore lies under
// load — exactly when the window is widest — which is how the peercred tests
// failed on a loaded gate while passing in isolation. Callers that need to
// know the socket is up call BindSocket synchronously and only then start
// ServeSocket; nobody should poll the filesystem for it.
func (s *Server) BindSocket(path string) (net.Listener, error) {
	// A stale socket file left by a crashed prior daemon must be cleared —
	// net.Listen fails with "address already in use" otherwise. But the file
	// alone cannot tell stale from live, and unlinking unconditionally STEALS
	// the path from a running daemon.
	//
	// The comment that used to sit here claimed "a genuinely live listener
	// holding the path still fails Listen correctly below." That is false on
	// POSIX and was verified false: unlink() detaches the NAME from the
	// listening socket's inode. The first daemon keeps accepting on an inode
	// nothing can reach any more, net.Listen on the freed name succeeds, and
	// every subsequent dial lands on the second daemon. The first becomes
	// silently unreachable — with the CLI's dial-with-fallback template
	// (gate.go, attention.go) reporting no daemon rather than the wrong one.
	//
	// So probe first and only unlink what does not answer. The probe is a
	// dial, which is the same question a client asks: a refused or absent
	// path is stale and safe to clear; a path that accepts belongs to someone.
	//
	// This is not mutual exclusion — two daemons starting inside the probe
	// window can still both bind, and closing THAT race is the serve lease's
	// job (#1349), not this function's. What it closes is the common and
	// entirely silent case: a second daemon starting while the first is up.
	if c, err := net.DialTimeout("unix", path, bindProbeTimeout); err == nil {
		c.Close()
		return nil, fmt.Errorf("%w: %s", ErrSocketInUse, path)
	}
	_ = os.Remove(path)

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("ipc: create socket dir: %w", err)
		}
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen on socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("ipc: chmod socket %s: %w", path, err)
	}
	return ln, nil
}

// ServeSocket accepts connections on ln — a listener from BindSocket — and
// serves each one until ctx is done, then closes the listener and removes
// the socket file at path. It returns nil on a ctx-driven shutdown and the
// accept error otherwise.
func (s *Server) ServeSocket(ctx context.Context, ln net.Listener, path string) error {
	go func() {
		<-ctx.Done()
		ln.Close()
		_ = os.Remove(path)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go s.serveSocketConn(ctx, conn)
	}
}

// serveSocketConn handles one client connection: it reads exactly one
// newline-delimited JSON-RPC request, dispatches it, writes exactly one
// response, and closes — matching the CLI's one-shot dial-per-invocation use
// (no connection pooling, no multi-request sessions over one connection).
func (s *Server) serveSocketConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Authenticate BEFORE the request is read or dispatched (ADR 017 R-2,
	// #378). Ordering is the whole point: a check that ran after dispatch
	// would refuse the response while the verb had already mutated state.
	if err := s.authorizeConn(conn); err != nil {
		log.Printf("WARNING: IPC socket rejected a connection: %v", err)
		s.sendErrorOnWriter(conn, 0, ErrUnauthorized, "unauthorized: "+err.Error())
		return
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	if !scanner.Scan() {
		return
	}
	line := scanner.Bytes()
	if len(line) == 0 {
		return
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.sendErrorOnWriter(conn, 0, ErrInvalidParams, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	s.handleRequestOnWriter(ctx, req, conn)
}

// handleRequestOnWriter is handleRequest's socket-transport counterpart: it
// dispatches to the same method table but writes the response to w (a
// per-connection socket) rather than the shared s.writer (stdout). This
// keeps stdio push-event behavior (Emit) untouched — socket clients never see
// the broadcast stream.
func (s *Server) handleRequestOnWriter(ctx context.Context, req Request, w writerFlusher) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WARNING: PANIC in IPC socket handler %q (id=%d): %v", req.Method, req.ID, r)
			s.sendErrorOnWriter(w, req.ID, ErrInternal, fmt.Sprintf("internal panic in %s: %v", req.Method, r))
		}
	}()

	handler, ok := s.methods[req.Method]
	if !ok {
		s.sendErrorOnWriter(w, req.ID, ErrMethodNotFound, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}

	result, err := handler(ctx, req.Params)
	if err != nil {
		s.sendErrorOnWriter(w, req.ID, rpcCodeFor(err), err.Error())
		return
	}

	s.sendJSONOnWriter(w, Response{ID: req.ID, Result: result})
}

// writerFlusher is the minimal interface handleRequestOnWriter needs — any
// io.Writer works (net.Conn satisfies it).
type writerFlusher interface {
	Write(p []byte) (int, error)
}

func (s *Server) sendErrorOnWriter(w writerFlusher, id int, code int, message string) {
	s.sendJSONOnWriter(w, Response{ID: id, Error: &RPCError{Code: code, Message: message}})
}

func (s *Server) sendJSONOnWriter(w writerFlusher, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "%s\n", data)
}
