package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAutonomousVerbsReachTheDaemon is a source-level guard for #1555.
//
// The daemon has always exposed a full autonomous control surface over IPC —
// start, stop, status, pause, dispatch and more. The CLI wired two of them.
// The consequence was not cosmetic: `autonomous status` printed state.json,
// which the running scheduler writes and never re-reads, so it reported a
// snapshot of unknown age as the live answer; `autonomous stop` wrote a file no
// running scheduler consults and printed "Stop signal written" while the fleet
// kept dispatching (#1536); and `autonomous start` did not exist at all, so
// nothing without a UI could operate the pipeline it could observe.
//
// A source assertion rather than a behavioural one because the alternative is
// standing up a daemon in a unit test; what regresses here is someone adding a
// verb that reads the file and forgetting the socket, and that is visible in
// the source.
func TestAutonomousVerbsReachTheDaemon(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(src)

	for _, tc := range []struct {
		fn     string
		method string
		why    string
	}{
		{"autonomousStatusCmd", `"autonomous.status"`,
			"status must ask the running scheduler; state.json is never re-read by it, so with a daemon up the file is a snapshot of unknown age"},
		{"autonomousStopCmd", `"autonomous.stop"`,
			"a stop that only writes a file stops nothing while a daemon is running (#1536)"},
		{"autonomousStartCmd", `"autonomous.start"`,
			"without this verb the fleet can only be started from the extension UI, so no headless caller can operate what it can observe"},
		{"autonomousResumeCmd", `"autonomous.resume"`,
			"already correct before #1555 — pinned so it stays that way"},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			body := funcBody(t, text, tc.fn)
			if !strings.Contains(body, tc.method) {
				t.Errorf("%s does not call %s — %s", tc.fn, tc.method, tc.why)
			}
			if !strings.Contains(body, "ipc.DialClient") {
				t.Errorf("%s never dials the daemon socket — %s", tc.fn, tc.why)
			}
		})
	}
}

// TestAutonomousStartIsDaemonOnly pins the deliberate asymmetry: start has no
// state-file fallback, because there is nothing a file write could start. It
// must say so rather than appear to succeed.
func TestAutonomousStartIsDaemonOnly(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := funcBody(t, string(src), "autonomousStartCmd")

	if strings.Contains(body, "state.json") {
		t.Error("autonomousStartCmd must not fall back to the state file — nothing reads it to start a scheduler")
	}
	if !strings.Contains(body, "autonomous run") {
		t.Error("with no daemon, start must point at `nightgauge autonomous run`, the way to get a scheduler in this process")
	}
}

// funcBody returns the source of the named top-level func, from its signature
// to the next top-level `func` (or EOF).
func funcBody(t *testing.T, text, name string) string {
	t.Helper()
	start := strings.Index(text, "func "+name+"(")
	if start < 0 {
		t.Fatalf("func %s not found in main.go", name)
	}
	rest := text[start+1:]
	if next := regexp.MustCompile(`(?m)^func `).FindStringIndex(rest); next != nil {
		return rest[:next[0]]
	}
	return rest
}
