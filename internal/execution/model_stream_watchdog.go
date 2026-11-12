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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	// toolUnknown: no sqlite3 CLI, so whether a tool runs cannot be read.
	// A long tool call writes no heartbeat, so check grants the heartbeat
	// unknownToolGraceBounds bounds instead of one before it stops the stage.
	toolUnknown bool
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

// unknownToolGraceBounds is how many bounds a quiet stream heartbeat is
// allowed when no sqlite3 CLI can say whether a tool is running.
const unknownToolGraceBounds = 10

// check reports whether the stage has stalled at now, and records it.
func (w *modelStreamWatchdog) check(now time.Time) bool {
	last := time.Unix(0, w.lastEvent.Load())
	if now.Sub(last) < w.bound {
		return false
	}
	if w.sessionDB != "" && w.session != nil {
		if p, err := w.session(w.sessionDB); err == nil {
			if p.toolUnknown && !p.lastUpdate.IsZero() {
				// The file-based fallback: the heartbeat alone judges
				// progress, with a longer allowance because a running tool
				// may be what keeps it quiet.
				if p.lastUpdate.After(last) {
					w.lastEvent.Store(p.lastUpdate.UnixNano())
					last = p.lastUpdate
				}
				if now.Sub(last) < unknownToolGraceBounds*w.bound {
					return false
				}
			} else if p.toolRunning {
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
	p, err := readSessionDBProgress(dir)
	if errors.Is(err, errSQLiteCLIUnavailable) {
		p.toolUnknown = true
	}
	// The plugin's stream heartbeat (#2184) is progress too: a streaming
	// delta is never written to the database.
	if info, statErr := os.Stat(filepath.Join(dir, streamHeartbeatFile)); statErr == nil {
		if info.ModTime().After(p.lastUpdate) {
			p.lastUpdate = info.ModTime()
		}
		return p, nil
	}
	return p, err
}

// streamHeartbeatFile is the file the OpenCode plugin touches beside
// opencode.db whenever a part streams or updates (#2184). OpenCode 1.18.32
// publishes a streaming delta only on its bus and never writes it to the
// session database, and `opencode run --format json` prints a part only when
// it completes, so without it a model slowly writing a long reasoning block
// (#1659 leg 1 run 13: about 1 token/s at 86k context) read as a dead request
// and was stopped. Mirrors session.js STREAM_HEARTBEAT_FILE byte for byte.
const streamHeartbeatFile = "nightgauge-stream.heartbeat"

// errSQLiteCLIUnavailable is readSessionDBProgress's answer when no sqlite3
// CLI is on PATH: the database exists but cannot be read.
var errSQLiteCLIUnavailable = errors.New("sqlite3 not on PATH")

// lookSQLite3 resolves the sqlite3 CLI. A variable so tests can take it away.
var lookSQLite3 = func() (string, error) { return exec.LookPath("sqlite3") }

// sqliteFallbackOnce guards the one log line that says the watchdog is
// running on the stream heartbeat alone; sqliteFallbackLog writes it. Both are
// variables so tests can count the line.
var (
	sqliteFallbackOnce = &sync.Once{}
	sqliteFallbackLog  = log.Printf
)

// sessionProgressQuery answers both questions in one row, "max|running": the
// latest part update in Unix milliseconds (empty when there are no parts) and
// the number of tool calls pending or running.
const sessionProgressQuery = `SELECT MAX(time_updated), ` +
	`(SELECT COUNT(*) FROM part WHERE json_extract(data, '$.type') = 'tool' ` +
	`AND json_extract(data, '$.state.status') IN ('pending', 'running')) FROM part;`

// readSessionDBProgress reads opencode.db read-only through the sqlite3 CLI.
//
// The release CLI does not link a SQLite engine (#3192): the embedded one was
// a large share of what antivirus heuristics flagged on the Linux binary, and
// this watchdog was its only reader that needed SQL. When sqlite3 is not on
// PATH this returns errSQLiteCLIUnavailable, logged once per process, and
// readSessionProgress falls back to the plugin's stream heartbeat file.
func readSessionDBProgress(dir string) (sessionProgress, error) {
	path, err := filepath.Abs(filepath.Join(dir, "opencode.db"))
	if err != nil {
		return sessionProgress{}, err
	}
	if _, err := os.Stat(path); err != nil {
		return sessionProgress{}, err
	}
	cli, err := lookSQLite3()
	if err != nil {
		sqliteFallbackOnce.Do(func() {
			sqliteFallbackLog("model-stream watchdog: sqlite3 not on PATH; " +
				"the watchdog uses the stream-heartbeat file only")
		})
		return sessionProgress{}, errSQLiteCLIUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "-readonly", "-batch", "-noheader", "-list",
		"-separator", "|", "-cmd", ".timeout 2000", path, sessionProgressQuery)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return sessionProgress{}, fmt.Errorf("sqlite3 %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return parseSessionProgressRow(strings.TrimSpace(string(out)))
}

// parseSessionProgressRow parses sessionProgressQuery's "max|running" row.
func parseSessionProgressRow(row string) (sessionProgress, error) {
	maxField, runningField, ok := strings.Cut(row, "|")
	if !ok {
		return sessionProgress{}, fmt.Errorf("sqlite3: unexpected session progress row %q", row)
	}
	var p sessionProgress
	if maxField != "" {
		ms, err := strconv.ParseInt(maxField, 10, 64)
		if err != nil {
			return sessionProgress{}, fmt.Errorf("sqlite3: part.time_updated %q: %w", maxField, err)
		}
		p.lastUpdate = time.UnixMilli(ms)
	}
	running, err := strconv.Atoi(runningField)
	if err != nil {
		return sessionProgress{}, fmt.Errorf("sqlite3: running tool count %q: %w", runningField, err)
	}
	p.toolRunning = running > 0
	return p, nil
}
