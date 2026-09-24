package github

import (
	"context"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// restMaxInFlight bounds the REST requests one process has in flight. GitHub's
// secondary limits count concurrent requests per user; a daemon sweeping four
// repositories while the tree reads boards and relationship lists could
// otherwise reach dozens at once.
const restMaxInFlight = 8

// restGate is the process-wide throttle every REST request passes through: a
// bound on requests in flight, and one shared pause set by a rate-limited
// answer, so a Retry-After holds every caller rather than only the goroutine
// that received it.
type restGate struct {
	sem         chan struct{}
	mu          sync.Mutex
	pausedUntil time.Time
}

func newRESTGate(n int) *restGate { return &restGate{sem: make(chan struct{}, n)} }

var processRESTGate = newRESTGate(restMaxInFlight)

// acquire waits out any shared pause, then takes an in-flight slot.
func (g *restGate) acquire(ctx context.Context) error {
	for {
		g.mu.Lock()
		wait := time.Until(g.pausedUntil)
		g.mu.Unlock()
		if wait <= 0 {
			break
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	select {
	case g.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *restGate) release() { <-g.sem }

// pause holds every caller's next request for d (never shortening a longer
// pause already set).
func (g *restGate) pause(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := time.Now().Add(d); until.After(g.pausedUntil) {
		g.pausedUntil = until
	}
}

func isGatewayStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// gatewayRetryDelay is 200–700 ms, jittered so callers that failed together
// do not retry together.
var gatewayRetryDelay = func() time.Duration {
	return 200*time.Millisecond + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
}
