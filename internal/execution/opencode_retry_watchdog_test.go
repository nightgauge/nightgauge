package execution

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// replayRetryStorm feeds lines through the parser and the retry watchdog,
// with each event's own captured `timestamp` as the clock (no sleeps,
// #2635). It returns the index of the line that fired the watchdog, or -1,
// and how far into the stream that line was.
func replayRetryStorm(t *testing.T, w *openCodeRetryWatchdog, lines []string) (int, time.Duration) {
	t.Helper()
	acc := &TokenAccumulator{}
	var start time.Time
	for i, line := range lines {
		var ev struct {
			Timestamp int64 `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		now := time.UnixMilli(ev.Timestamp)
		if start.IsZero() {
			start = now
		}
		acc.ParseOpenCodeStreamLine(line)
		if w.observe(acc.OpenCode(), now) {
			return i, now.Sub(start)
		}
	}
	return -1, 0
}

// TestOpenCodeRetryWatchdogStopsCapturedStorms replays the two captured
// 2.2.7 retry storms (#3305). Each is stopped on its fifth retry event, about
// 24 s into the stage: before OpenCodeRetryMaxElapsed and far before the
// idle-stream watchdog's default 3-minute bound, which a hosted model never
// arms at all. The notice classifies as the provider failure kind its
// error.type names, and quotes none of the provider's message.
func TestOpenCodeRetryWatchdogStopsCapturedStorms(t *testing.T) {
	cases := []struct {
		fixture, kind string
		message       string
	}{
		{"stream-retry-transport.jsonl", adapters.OpenCodeProviderTransport, "ECONNREFUSED"},
		{"stream-retry-internal.jsonl", adapters.OpenCodeProviderError, "stub internal error"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			w := newOpenCodeRetryWatchdog()
			lines := openCode2xFixtureLines(t, c.fixture)
			at, elapsed := replayRetryStorm(t, w, lines)
			if at != len(lines)-1 || !w.fired || w.retries != adapters.OpenCodeRetryMaxAttempts {
				t.Fatalf("fired at line %d of %d, fired = %v, retries = %d; want the last line, after %d retries",
					at, len(lines), w.fired, w.retries, adapters.OpenCodeRetryMaxAttempts)
			}
			if elapsed >= adapters.OpenCodeRetryMaxElapsed {
				t.Errorf("stopped %s in, want before %s", elapsed, adapters.OpenCodeRetryMaxElapsed)
			}
			notice := w.notice()
			if got := terminalkind.Classify("exit -1: " + notice); got != c.kind {
				t.Errorf("notice classifies %q, want %q:\n%s", got, c.kind, notice)
			}
			if strings.Contains(notice, c.message) || strings.Contains(notice, "127.0.0.1") {
				t.Errorf("notice carries the provider's message: %q", notice)
			}
			if w.observe(&OpenCodeStream{RetriesSinceProgress: 99}, time.Now()) {
				t.Error("observe fired twice")
			}
		})
	}
}

// TestOpenCodeRetryWatchdogBounds pins both bounds and the reset: a run of
// retries shorter than either is left alone, a finished step starts the
// count again, and slow retries are stopped on elapsed time alone.
func TestOpenCodeRetryWatchdogBounds(t *testing.T) {
	retry := openCode2xFixtureLines(t, "stream-retry-transport.jsonl")[1]
	step := `{"type":"step_finish","sessionID":"ses_x","part":{"reason":"tool-calls","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}`
	t0 := time.Unix(1_800_000_000, 0)

	// Four retries, a step, four retries: never five in a row.
	w, acc := newOpenCodeRetryWatchdog(), &TokenAccumulator{}
	for i, line := range []string{retry, retry, retry, retry, step, retry, retry, retry, retry} {
		acc.ParseOpenCodeStreamLine(line)
		if w.observe(acc.OpenCode(), t0.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("fired on line %d: a finished step must reset the run of retries", i)
		}
	}

	// A recovered provider's later retry is timed from its own start, not
	// from the earlier run of retries.
	w, acc = newOpenCodeRetryWatchdog(), &TokenAccumulator{}
	for i, line := range []string{retry, step, retry} {
		acc.ParseOpenCodeStreamLine(line)
		if w.observe(acc.OpenCode(), t0.Add(time.Duration(i)*adapters.OpenCodeRetryMaxElapsed)) {
			t.Fatalf("fired on line %d: the step ended the first run of retries", i)
		}
	}

	// Two retries, the second past the elapsed bound.
	w, acc = newOpenCodeRetryWatchdog(), &TokenAccumulator{}
	acc.ParseOpenCodeStreamLine(retry)
	if w.observe(acc.OpenCode(), t0) {
		t.Fatal("fired on the first retry")
	}
	acc.ParseOpenCodeStreamLine(retry)
	if !w.observe(acc.OpenCode(), t0.Add(adapters.OpenCodeRetryMaxElapsed)) {
		t.Fatal("did not fire once the run of retries reached OpenCodeRetryMaxElapsed")
	}

	// A retry whose error.type is not a provider's still stops as one.
	w = newOpenCodeRetryWatchdog()
	w.observe(&OpenCodeStream{RetriesSinceProgress: adapters.OpenCodeRetryMaxAttempts, ProviderErrorType: "Bogus type\nwith a line"}, t0)
	notice := w.notice()
	if got := terminalkind.Classify(notice); got != adapters.OpenCodeProviderError || strings.Contains(notice, "Bogus") {
		t.Errorf("notice %q classifies %q, want %q and no quoted type", notice, got, adapters.OpenCodeProviderError)
	}

	var nilWatchdog *openCodeRetryWatchdog
	if nilWatchdog.observe(&OpenCodeStream{RetriesSinceProgress: 99}, t0) {
		t.Error("a nil watchdog fired")
	}
}

// TestOpenCodeRetryStormStopsStage drives the watchdog through
// Manager.RunStage (#3305): a fake opencode replays the captured 2.2.7
// refused-connection storm and then blocks, as the real binary does. The
// stage is stopped at once rather than at its 20 s timeout, failed but not
// cancelled. Its stderr ends with the adapter_provider_transport marker, so
// it classifies as that kind with no timeout notice beside it. The manager
// logged the notice once, and no process of the stage's group survived.
func TestOpenCodeRetryStormStopsStage(t *testing.T) {
	storm, err := filepath.Abs(filepath.Join("adapters", "testdata", "opencode-cli", "stream-retry-transport.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := runCostStage(t, costStage{
		model: "anthropic/claude-sonnet-5",
		run:   fmt.Sprintf("cat %q; sleep 30", storm),
	})
	res := out.result
	if res.Cancelled {
		t.Error("RunResult.Cancelled = true: a retry stop is not an operator's stop")
	}
	if res.ExitCode == 0 {
		t.Error("the stopped stage exited 0; a stage stopped on a retry storm failed")
	}
	lines := strings.Split(strings.TrimRight(res.Stderr, "\n"), "\n")
	want := OpenCodeProviderFailureMarker + " " + adapters.OpenCodeProviderTransport + ":"
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, want) {
		t.Errorf("the stage's stderr does not end with %s:\n%s", want, res.Stderr)
	}
	if kind := terminalkind.Classify(fmt.Sprintf("exit %d: %s", res.ExitCode, res.Stderr)); kind != adapters.OpenCodeProviderTransport {
		t.Errorf("the stage classifies as %q, want %q:\n%s", kind, adapters.OpenCodeProviderTransport, res.Stderr)
	}
	if strings.Contains(res.Stderr, "ECONNREFUSED") {
		t.Errorf("the stage's stderr carries the provider's message:\n%s", res.Stderr)
	}
	if n := strings.Count(out.logged, OpenCodeProviderFailureMarker); n != 1 {
		t.Errorf("the manager logged %s %d times, want once:\n%s", OpenCodeProviderFailureMarker, n, out.logged)
	}
	if out.pid == 0 {
		t.Fatal("the fake never recorded its pid")
	}
	if !processGone(out.pid) {
		_ = syscall.Kill(-out.pid, syscall.SIGKILL)
		t.Errorf("process %d or its group survived the retry stop", out.pid)
	}
	if out.elapsed > 8*time.Second {
		t.Errorf("RunStage took %s, want the stage stopped at once", out.elapsed)
	}
}
