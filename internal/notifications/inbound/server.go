package inbound

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// shutdownTimeout caps how long Shutdown(ctx) waits for in-flight
// requests to drain before forcing close. Mattermost outgoing webhooks
// are short-lived; 5 seconds is generous.
const shutdownTimeout = 5 * time.Second

// Server wraps a *http.Server bound to a single TokenStore + dispatcher
// pair. It is owned by the cmd/ wiring layer and started in a
// goroutine alongside the IPC server.
type Server struct {
	cfg   *config.InboundConfig
	store *TokenStore
	disp  CommandDispatcher

	mu      sync.Mutex
	httpSrv *http.Server
	ln      net.Listener
}

// New returns an unstarted Server bound to the supplied config. The
// caller must call Start(ctx) to bind the listener and serve requests.
//
// cfg may be nil — in that case the receiver uses the documented
// loopback defaults (127.0.0.1:8765/mattermost).
func New(cfg *config.InboundConfig, store *TokenStore, disp CommandDispatcher) *Server {
	if store == nil {
		store = NewTokenStore()
	}
	return &Server{
		cfg:   cfg,
		store: store,
		disp:  disp,
	}
}

// Start binds the listener, registers the handler, and begins serving.
// It blocks until ctx is cancelled, then performs a graceful Shutdown
// with a fixed 5-second drain budget. Returns nil on a clean shutdown
// or the underlying serve/listen error otherwise.
//
// The listener is created synchronously before launching the serve
// goroutine so callers (and tests) can read Addr() immediately after
// Start returns, even though Start itself blocks.
func (s *Server) Start(ctx context.Context) error {
	host := s.cfg.ResolvedHost()
	// Use the raw port — 0 means "let the OS pick", which is what tests
	// rely on. The documented default (8765) is applied by the cmd/
	// wiring layer before constructing the Server.
	port := 0
	if s.cfg != nil {
		port = s.cfg.Port
	}
	path := s.cfg.ResolvedPath()

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("inbound listen %s: %w", addr, err)
	}

	// Loopback-bind sanity check: warn (don't error) when host is not a
	// loopback address. Operators sometimes intentionally bind to a
	// LAN interface behind a firewall, so this is documentation, not
	// enforcement.
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		log.Printf("inbound webhook: bound to non-loopback host %s — ensure a reverse proxy with TLS terminates this address", host)
	}

	httpSrv := &http.Server{
		Handler:           NewHandler(path, s.store, s.disp, time.Now),
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

	log.Printf("inbound webhook: listening on http://%s%s (channels=%d)", ln.Addr(), path, s.store.Len())

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-serveErr:
		return err
	}
}

// Shutdown stops the server using a bounded context. Safe to call
// before Start, after Start, or concurrently with the goroutine that
// invoked Start. Subsequent calls are no-ops.
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

// Addr returns the address the server is bound to as a "host:port"
// string. Returns "" when Start has not yet bound a listener. Useful
// for tests that bind to :0 and then need to construct request URLs.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Path returns the configured base path (e.g. "/mattermost") so callers
// can construct request URLs without re-resolving the config defaults.
func (s *Server) Path() string {
	return s.cfg.ResolvedPath()
}

// TokenStore exposes the underlying store so the IPC reload handler
// can refresh credentials in place.
func (s *Server) TokenStore() *TokenStore {
	return s.store
}
