// Command stub-provider serves a deterministic, scripted, OpenAI-compatible
// chat-completions API on loopback for adapter contract runs. It answers
// POST /v1/chat/completions (streamed and non-streamed) and GET /v1/models
// from a named script; see internal/stubprovider for the script format and
// the loopback-only, bounded-lifetime behaviour.
//
// On success it prints exactly one JSON line to stdout with the bound
// address:
//
//	{"base_url":"http://127.0.0.1:<port>/v1"}
//
// and then serves until --max-requests requests have been handled,
// --idle-timeout elapses with no request, or the process receives
// SIGINT/SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/stubprovider"
)

func main() {
	script := flag.String("script", "", "Script name to serve (required)")
	listen := flag.String("listen", "127.0.0.1:0", "Loopback host:port to bind (127.0.0.1 or ::1 only)")
	maxRequests := flag.Int("max-requests", stubprovider.DefaultMaxRequests,
		"Serve stops accepting requests and returns after this many /v1/chat/completions requests")
	idleTimeout := flag.Duration("idle-timeout", stubprovider.DefaultIdleTimeout,
		"Serve returns after this much time with no request")
	delayMS := flag.Int("delay-ms", -1, "Override the script's first-token delay, in milliseconds (-1: no override)")
	verbose := flag.Bool("verbose", false, "Log request sizes and script turn indices to stderr")
	flag.Parse()

	if *script == "" {
		fail("stub-provider: --script is required")
	}

	logOut := io.Discard
	if *verbose {
		logOut = os.Stderr
	}
	logger := log.New(logOut, "", log.LstdFlags)

	cfg := stubprovider.Config{
		Script:      *script,
		MaxRequests: *maxRequests,
		IdleTimeout: *idleTimeout,
		Log:         logger,
	}
	if *delayMS >= 0 {
		d := time.Duration(*delayMS) * time.Millisecond
		cfg.DelayOverride = &d
	}

	srv, err := stubprovider.NewServer(cfg)
	if err != nil {
		fail("stub-provider: %v", err)
	}

	ln, err := stubprovider.Listen(*listen)
	if err != nil {
		fail("stub-provider: %v", err)
	}

	fmt.Printf("{\"base_url\":\"http://%s/v1\"}\n", ln.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx, ln); err != nil {
		fail("stub-provider: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
