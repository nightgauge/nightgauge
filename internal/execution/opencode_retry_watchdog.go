// opencode_retry_watchdog.go stops an opencode stage whose provider requests
// only retry (#3305). OpenCode 2.x retries a provider 500 or a refused
// connection without end and writes nothing but `retry` events meanwhile
// (spike #3197 § 3), so without this a hosted model's stage waits for its
// stage timeout, and a declared endpoint's for the idle-stream watchdog
// (#2176). The bounds are adapters.OpenCodeRetryMaxAttempts and
// adapters.OpenCodeRetryMaxElapsed.
package execution

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// OpenCodeProviderFailureMarker starts the stderr line that names an
// opencode stage's provider failure, followed by its kind
// (adapters.OpenCodeProviderNoRoute, ...Transport or ...Error), which the
// terminal-kind table classifies.
const OpenCodeProviderFailureMarker = "[opencode-provider-failure]"

// openCodeRetryKillGrace is how long a stage stopped on a retry storm has to
// exit on SIGTERM before its process group is sent SIGKILL.
const openCodeRetryKillGrace = 10 * time.Second

// openCodeErrorTypeRE is the shape of an error.type a marker may quote. The
// type comes from the stream, so anything else is quoted as "unknown".
var openCodeErrorTypeRE = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,40}$`)

// openCodeSafeErrorType returns t when it has the shape of an error.type,
// and "unknown" otherwise.
func openCodeSafeErrorType(t string) string {
	if openCodeErrorTypeRE.MatchString(t) {
		return t
	}
	return "unknown"
}

// openCodeProviderFailureMarker renders the marker for an OpenCode
// error.type, and reports false for a type that is not a provider's. detail
// says what ended the stage. It carries no provider message.
func openCodeProviderFailureMarker(errorType, detail string) (string, bool) {
	kind, ok := adapters.OpenCodeProviderFailureKind(errorType)
	if !ok {
		return "", false
	}
	return renderOpenCodeProviderFailure(kind, errorType, detail), true
}

func renderOpenCodeProviderFailure(kind, errorType, detail string) string {
	return fmt.Sprintf("%s %s: opencode reported error type %s; %s", OpenCodeProviderFailureMarker, kind,
		openCodeSafeErrorType(errorType), detail)
}

// openCodeRetryWatchdog watches one opencode stage's run of retries. Only the
// stdout reader touches it until wg.Wait(), after which it is only read.
type openCodeRetryWatchdog struct {
	maxAttempts int
	maxElapsed  time.Duration
	grace       time.Duration
	// since is when the current run of retries was first seen, zero while
	// there is none.
	since time.Time
	// fired is set once the run of retries passed a bound; retries,
	// elapsed and errorType record what it saw then.
	fired     bool
	retries   int
	elapsed   time.Duration
	errorType string
}

// newOpenCodeRetryWatchdog returns the watchdog with the adapter's bounds.
func newOpenCodeRetryWatchdog() *openCodeRetryWatchdog {
	return &openCodeRetryWatchdog{
		maxAttempts: adapters.OpenCodeRetryMaxAttempts,
		maxElapsed:  adapters.OpenCodeRetryMaxElapsed,
		grace:       openCodeRetryKillGrace,
	}
}

// observe reads the stream after one stdout line, at now, and reports
// whether the run of retries just passed a bound. It reports that once. A
// finished step or tool call ends the run of retries (the stream resets
// RetriesSinceProgress), so a provider that recovers is never stopped.
func (w *openCodeRetryWatchdog) observe(s *OpenCodeStream, now time.Time) bool {
	if w == nil || w.fired || s == nil {
		return false
	}
	if s.RetriesSinceProgress == 0 {
		w.since = time.Time{}
		return false
	}
	if w.since.IsZero() {
		w.since = now
	}
	elapsed := now.Sub(w.since)
	if s.RetriesSinceProgress < w.maxAttempts && elapsed < w.maxElapsed {
		return false
	}
	w.fired, w.retries, w.elapsed, w.errorType = true, s.RetriesSinceProgress, elapsed, s.ProviderErrorType
	return true
}

// notice is the line that ends the stderr of a stage the watchdog stopped.
// A retry whose error.type is not a provider's is still a provider request
// that kept failing, so it is reported as adapters.OpenCodeProviderError.
func (w *openCodeRetryWatchdog) notice() string {
	detail := fmt.Sprintf("the provider request was retried %d times over %s with no step finished, past the bound of %d retries or %s, so the stage was stopped",
		w.retries, w.elapsed.Round(time.Second), w.maxAttempts, w.maxElapsed)
	kind, ok := adapters.OpenCodeProviderFailureKind(w.errorType)
	if !ok {
		kind = adapters.OpenCodeProviderError
	}
	return renderOpenCodeProviderFailure(kind, w.errorType, detail)
}

// stop sends SIGTERM to the stage's process group and, unless exited closes
// first, SIGKILL once the grace period has passed. It returns at once. It is
// not an operator's stop, so RunResult.Cancelled stays false.
func (w *openCodeRetryWatchdog) stop(proc *os.Process, exited <-chan struct{}) {
	terminateProcessTree(proc, exited, w.grace)
}
