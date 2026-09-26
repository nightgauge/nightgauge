// model_stream_watchdog.go is the manager's own bound on a model request that
// never answers (#2176).
//
// OpenCode 1.18.32 does honour an endpoint's provider options headerTimeout
// and chunkTimeout (milliseconds, the names the per-run config writes), but
// only through a fetch wrapper: the header timer is cleared as soon as the
// response headers arrive, and the chunk timer is armed only for a response
// whose content-type includes text/event-stream, and is reset by every body
// read, SSE comments included. A server that answers the headers and then
// holds the body, answers with another content type, or sends keep-alive
// comments during a stuck request passes both, and the stage then waits until
// its stage timeout, hours away. That is what #1659 leg 1 run 10 did for 34
// minutes against a declared endpoint with header and chunk timeouts of 20m.
//
// So, independently of OpenCode, a stage dispatched to a declared endpoint is
// stopped once it has printed no JSON event on stdout for longer than the
// endpoint's larger timeout while its session database shows no progress
// either: no part updated within the bound and no tool call running. A tool
// that is running is not the model stream; that silence is left to the stage
// timeout and the stall handling, so the kind stays distinct from stall_kill.
// The stop ends stderr with ModelStreamStalledMarker, which classifies as the
// retryable model_stream_stalled terminal kind.
package execution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ModelStreamStalledMarker is the token the model_stream_stalled terminal
// rule matches (#2176).
const ModelStreamStalledMarker = "[model-stream-stalled]"

// modelStreamStallGrace is the time between SIGTERM and SIGKILL.
const modelStreamStallGrace = 5 * time.Second

// modelStreamWatchdog stops a stage whose model request went silent.
type modelStreamWatchdog struct {
	endpoint string
	bound    time.Duration
	poll     time.Duration
	grace    time.Duration
	// sessionDB is the directory holding the run's opencode.db, or "" when
	// the stage has none; then only the stream is read.
	sessionDB string
	// session reads the session database's progress; swapped in tests.
	session func(dir string) (sessionProgress, error)

	lastEvent atomic.Int64 // unix nanoseconds of the last JSON event
	mu        sync.Mutex
	fired     bool
	idle      time.Duration
}

// sessionProgress is what the session database says about the stage.
type sessionProgress struct {
	lastUpdate  time.Time // the latest part update, zero when none
	toolRunning bool      // a tool call is pending or running
}

// newModelStreamWatchdog returns nil when bound is not positive, and every
// method is a no-op on nil.
func newModelStreamWatchdog(endpoint string, bound time.Duration, sessionDB string) *modelStreamWatchdog {
	if bound <= 0 {
		return nil
	}
	poll := bound / 10
	if poll < 20*time.Millisecond {
		poll = 20 * time.Millisecond
	}
	if poll > 15*time.Second {
		poll = 15 * time.Second
	}
	w := &modelStreamWatchdog{
		endpoint:  endpoint,
		bound:     bound,
		poll:      poll,
		grace:     modelStreamStallGrace,
		sessionDB: sessionDB,
		session:   readSessionProgress,
	}
	w.lastEvent.Store(time.Now().UnixNano())
	return w
}

// observe records a stdout line; only a JSON object counts as a stream event.
func (w *modelStreamWatchdog) observe(line []byte) {
	if w == nil {
		return
	}
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return
	}
	w.lastEvent.Store(time.Now().UnixNano())
}

// arm watches the stage until exited closes, and stops it on a stall.
func (w *modelStreamWatchdog) arm(proc *os.Process, exited <-chan struct{}) {
	if w == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(w.poll)
		defer ticker.Stop()
		for {
			select {
			case <-exited:
				return
			case <-ticker.C:
				if w.check(time.Now()) {
					fmt.Fprintf(os.Stderr, "%s\n", w.notice())
					terminateProcessTree(proc, exited, w.grace)
					return
				}
			}
		}
	}()
}

// check reports whether the stage has stalled at now, and records it.
func (w *modelStreamWatchdog) check(now time.Time) bool {
	last := time.Unix(0, w.lastEvent.Load())
	if now.Sub(last) < w.bound {
		return false
	}
	if w.sessionDB != "" && w.session != nil {
		if p, err := w.session(w.sessionDB); err == nil {
			if p.toolRunning {
				// A tool is running: not the model stream. Look again a
				// whole bound later.
				w.lastEvent.Store(now.UnixNano())
				return false
			}
			if now.Sub(p.lastUpdate) < w.bound {
				if p.lastUpdate.After(last) {
					w.lastEvent.Store(p.lastUpdate.UnixNano())
				}
				return false
			}
		}
	}
	w.mu.Lock()
	w.fired = true
	w.idle = now.Sub(last)
	w.mu.Unlock()
	return true
}

// hasFired reports whether the watchdog stopped the stage.
func (w *modelStreamWatchdog) hasFired() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// notice is the failure line naming the endpoint and the idle time.
func (w *modelStreamWatchdog) notice() string {
	w.mu.Lock()
	idle := w.idle
	w.mu.Unlock()
	return fmt.Sprintf("%s endpoint %s: no stream event and no session progress for %s, past the endpoint's %s header/chunk timeout, while no tool ran; the model request was abandoned and the stage stopped",
		ModelStreamStalledMarker, w.endpoint, idle.Round(time.Second), w.bound)
}

// readSessionProgress reads the run's OpenCode session database read-only:
// the latest part update, and whether a tool call is pending or running.
func readSessionProgress(dir string) (sessionProgress, error) {
	path := filepath.Join(dir, "opencode.db")
	if _, err := os.Stat(path); err != nil {
		return sessionProgress{}, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return sessionProgress{}, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var p sessionProgress
	var maxUpdated sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(time_updated) FROM part`).Scan(&maxUpdated); err != nil {
		return sessionProgress{}, err
	}
	if maxUpdated.Valid {
		p.lastUpdate = time.UnixMilli(maxUpdated.Int64)
	}
	var running int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM part WHERE json_extract(data, '$.type') = 'tool' AND json_extract(data, '$.state.status') IN ('pending', 'running')`,
	).Scan(&running); err != nil {
		return sessionProgress{}, err
	}
	p.toolRunning = running > 0
	return p, nil
}
