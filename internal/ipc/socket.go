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
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
)

// DaemonSocketPath returns the workspace-scoped Unix socket path a co-located
// `nightgauge serve` daemon listens on. Cross-machine/cross-workspace
// discovery is explicitly out of scope (#263) — this is scoped to the same
// `.nightgauge/` directory as every other workspace-local state file.
func DaemonSocketPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "daemon.sock")
}

// ListenSocket starts a Unix domain socket listener at path and serves
// request/response JSON-RPC calls (reusing the Request/Response envelope
// from protocol.go) until ctx is done. It is additive to Run's stdio loop —
// stdio push events (Emit) are unaffected; socket clients receive only the
// response to their own request, never the broadcast stream.
//
// Windows is unsupported (net.Listen("unix", ...) is POSIX-only); callers on
// Windows get the listen error back and should log it non-fatally, matching
// today's "no daemon reachable" fallback behavior on every OS (no
// regression).
func (s *Server) ListenSocket(ctx context.Context, path string) error {
	// Clear a stale socket file left by a crashed prior daemon — net.Listen
	// fails with "address already in use" otherwise. A genuinely live
	// listener holding the path still fails Listen correctly below.
	_ = os.Remove(path)

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("ipc: create socket dir: %w", err)
		}
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("ipc: listen on socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("ipc: chmod socket %s: %w", path, err)
	}

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
		s.sendErrorOnWriter(w, req.ID, ErrInternal, err.Error())
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
