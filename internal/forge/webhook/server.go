package webhook

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

const shutdownTimeout = 5 * time.Second

// Server wraps an http.Server and manages the bind-listen-serve-shutdown
// lifecycle. It mirrors internal/notifications/inbound/server.go but is
// forge-agnostic: any http.Handler can be mounted.
type Server struct {
	host string
	port int

	mu      sync.Mutex
	httpSrv *http.Server
	ln      net.Listener
}

// NewServer returns an unstarted Server. Call Start(ctx) to bind and serve.
func NewServer(host string, port int) *Server {
	return &Server{
		host: host,
		port: port,
	}
}

// Start binds the listener, mounts handler, and serves until ctx is cancelled.
// The listener is created synchronously so Addr() is readable immediately after
// Start is called (before it blocks on ctx.Done).
func (s *Server) Start(ctx context.Context, handler http.Handler) error {
	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("forge webhook: listen %s: %w", addr, err)
	}

	if ip := net.ParseIP(s.host); ip != nil && !ip.IsLoopback() {
		log.Printf("forge webhook: bound to non-loopback %s — ensure TLS terminates upstream", s.host)
	}

	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	s.mu.Lock()
	s.httpSrv = httpSrv
	s.ln = ln
	s.mu.Unlock()

	serveErr := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			serveErr <- nil
			return
		}
		serveErr <- err
	}()

	log.Printf("forge webhook: listening on http://%s", ln.Addr())

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-serveErr:
		return err
	}
}

// Shutdown stops the server gracefully using a fixed 5-second drain budget.
// Safe to call before Start or concurrently; subsequent calls are no-ops.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.httpSrv
	s.httpSrv = nil
	s.mu.Unlock()

	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return s.Shutdown(ctx)
}

// Addr returns the bound "host:port". Returns "" before Start has bound.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}
