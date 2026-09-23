package opencodeplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEventsFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReadRunEventsRoundTripsEveryKind: every one of the five documented
// kinds, plus the writer's own "truncated" sentinel, round-trips through
// ReadRunEvents with its session_id, child and detail fields intact.
func TestReadRunEventsRoundTripsEveryKind(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i, kind := range append(append([]string{}, EventKinds...), EventsTruncatedKind) {
		lines = append(lines, `{"v":1,"ts":"2026-09-15T00:00:0`+string(rune('0'+i))+`Z","kind":"`+kind+`","session_id":"ses_`+kind+`","child":`+boolStr(i%2 == 0)+`,"detail":{"n":`+string(rune('0'+i))+`}}`)
	}
	p := writeEventsFile(t, dir, "events.jsonl", strings.Join(lines, "\n")+"\n")

	events, err := ReadRunEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	want := len(EventKinds) + 1
	if len(events) != want {
		t.Fatalf("got %d events, want %d: %+v", len(events), want, events)
	}
	for i, kind := range append(append([]string{}, EventKinds...), EventsTruncatedKind) {
		ev := events[i]
		if ev.Kind != kind {
			t.Errorf("event %d kind = %q, want %q", i, ev.Kind, kind)
		}
		if ev.SessionID != "ses_"+kind {
			t.Errorf("event %d session_id = %q, want %q", i, ev.SessionID, "ses_"+kind)
		}
		wantChild := i%2 == 0
		if ev.Child != wantChild {
			t.Errorf("event %d child = %v, want %v", i, ev.Child, wantChild)
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestCompactionCount counts only "compaction" kind lines, ignoring every
// other kind mixed in around them.
func TestCompactionCount(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		`{"v":1,"ts":"t","kind":"compaction","session_id":"a"}`,
		`{"v":1,"ts":"t","kind":"stop_verify","session_id":"a","detail":{"verdict":"complete"}}`,
		`{"v":1,"ts":"t","kind":"compaction","session_id":"b"}`,
		`{"v":1,"ts":"t","kind":"skill","session_id":"a","detail":{"skill":"foo"}}`,
		`{"v":1,"ts":"t","kind":"compaction","session_id":"c","child":true}`,
	}, "\n") + "\n"
	p := writeEventsFile(t, dir, "events.jsonl", content)

	n, err := CompactionCount(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("CompactionCount = %d, want 3", n)
	}
}

// TestReadRunEventsRejectsTextField: a line whose top level or whose detail
// object carries a "text" key is dropped — never returned to a caller — the
// file's own retention contract (no transcript, summary, tool output or
// prompt text) enforced defensively on the read side too. Removing either
// check in ReadRunEvents turns this red.
func TestReadRunEventsRejectsTextField(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		`{"v":1,"ts":"t","kind":"stop_verify","session_id":"a","detail":{"verdict":"complete"}}`,
		`{"v":1,"ts":"t","kind":"compaction","session_id":"b","text":"the model said hello"}`,
		`{"v":1,"ts":"t","kind":"skill","session_id":"c","detail":{"skill":"foo","text":"leaked transcript"}}`,
		`{"v":1,"ts":"t","kind":"permission_ask","session_id":"d","detail":{"permission_type":"bash"}}`,
	}, "\n") + "\n"
	p := writeEventsFile(t, dir, "events.jsonl", content)

	events, err := ReadRunEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (the two text-bearing lines dropped): %+v", len(events), events)
	}
	for _, ev := range events {
		if ev.SessionID == "b" || ev.SessionID == "c" {
			t.Errorf("a text-bearing line (session_id=%s) was not dropped", ev.SessionID)
		}
	}
}

// TestReadRunEventsRejectsNonScalarOrOversizedDetail: a defence-in-depth
// backstop independent of session.js's own SKILL_ID_RE — a "detail" object
// with a nested value (an object or array, under any key, not only "text")
// or an over-length string is dropped, exactly as a line carrying a literal
// "text" key already is. This is what still catches a buggy or tampered
// writer even if session.js's own validation regresses. Removing
// detailHoldsOnlyScalars's check turns this red.
func TestReadRunEventsRejectsNonScalarOrOversizedDetail(t *testing.T) {
	dir := t.TempDir()
	oversized := strings.Repeat("a", detailValueMaxLen+1)
	content := strings.Join([]string{
		`{"v":1,"ts":"t","kind":"skill","session_id":"a","detail":{"skill":"nightgauge:ok"}}`,
		`{"v":1,"ts":"t","kind":"skill","session_id":"b","detail":{"skill":{"nested":"object"}}}`,
		`{"v":1,"ts":"t","kind":"skill","session_id":"c","detail":{"skill":["array","value"]}}`,
		`{"v":1,"ts":"t","kind":"skill","session_id":"d","detail":{"skill":"` + oversized + `"}}`,
		`{"v":1,"ts":"t","kind":"stop_verify","session_id":"e","detail":{"verdict":"complete"}}`,
	}, "\n") + "\n"
	p := writeEventsFile(t, dir, "events.jsonl", content)

	events, err := ReadRunEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (a, e survive; b, c, d dropped): %+v", len(events), events)
	}
	for _, ev := range events {
		if ev.SessionID == "b" || ev.SessionID == "c" || ev.SessionID == "d" {
			t.Errorf("a non-scalar or oversized detail line (session_id=%s) was not dropped", ev.SessionID)
		}
	}
}

// TestReadRunEventsSkipsUnparseableLines: a line that is not valid JSON is
// dropped rather than failing the whole read.
func TestReadRunEventsSkipsUnparseableLines(t *testing.T) {
	dir := t.TempDir()
	content := `{"v":1,"ts":"t","kind":"skill","session_id":"a","detail":{"skill":"foo"}}
not json at all

{"v":1,"ts":"t","kind":"compaction","session_id":"b"}
`
	p := writeEventsFile(t, dir, "events.jsonl", content)

	events, err := ReadRunEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (the garbage line and blank line skipped): %+v", len(events), events)
	}
}

// TestReadRunEventsMissingFile: a missing events file is not an error — a
// run that never wrote one (e.g. NIGHTGAUGE_OUTPUT_FILE unset) reads as an
// empty, valid event list.
func TestReadRunEventsMissingFile(t *testing.T) {
	events, err := ReadRunEvents(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("a missing file must not error, got %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from a missing file, want 0", len(events))
	}
}

// TestEventsFileCapsAtOneMiBWithOneTruncatedLine drives session.js's real
// appendEvent (via the Node harness, not a Go-side re-implementation of the
// writer): once the events file is already at or over the 1 MiB cap, a new
// event is never appended — instead exactly one "truncated" line is, and a
// second call in the same process does not add a second one. Removing
// session.js's size check before its fs.appendFileSync call turns this red.
func TestEventsFileCapsAtOneMiBWithOneTruncatedLine(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-cap-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	// Seed the file at just over the 1 MiB cap with valid, parseable lines,
	// so this test exercises the writer's own size check (fs.statSync
	// against the real file), not a hand-rolled Go approximation of it.
	line := `{"v":1,"ts":"t","kind":"skill","session_id":"seed","detail":{"skill":"x"}}` + "\n"
	var seed strings.Builder
	for seed.Len() < eventsMaxBytes+1024 {
		seed.WriteString(line)
	}
	if err := os.MkdirAll(filepath.Dir(eventsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventsFile, []byte(seed.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() < eventsMaxBytes {
		t.Fatalf("test premise broken: seed file is %d bytes, want at least %d", before.Size(), eventsMaxBytes)
	}

	perm := map[string]any{"id": "p1", "type": "bash", "sessionID": "ses_cap_1", "messageID": "m1", "title": "t", "metadata": map[string]any{}, "time": map[string]any{"created": 1}}
	calls := []sessionCall{
		{Fn: "permissionAsk", Input: perm, Output: map[string]any{"status": "ask"}},
		{Fn: "permissionAsk", Input: perm, Output: map[string]any{"status": "ask"}},
	}
	results := runSessionHarness(t, node, root, calls, map[string]string{
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	})
	for i, r := range results {
		if r.Threw {
			t.Fatalf("call %d threw: %s", i, r.Message)
		}
	}

	after, err := os.Stat(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	grew := after.Size() - before.Size()
	// One "truncated" line's own bytes are the only growth allowed; two
	// permission_ask calls that each tried to append their own line would
	// have grown it far more than one short sentinel line's worth.
	if grew <= 0 || grew > 200 {
		t.Errorf("events file grew by %d bytes after 2 calls past the cap, want only one short truncated line's worth (<200)", grew)
	}

	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	truncated := 0
	permissionAsks := 0
	for _, e := range events {
		switch e.Kind {
		case EventsTruncatedKind:
			truncated++
		case "permission_ask":
			permissionAsks++
		}
	}
	if truncated != 1 {
		t.Errorf("got %d truncated lines, want exactly 1", truncated)
	}
	if permissionAsks != 0 {
		t.Errorf("got %d permission_ask events past the cap, want 0 (writing must have stopped)", permissionAsks)
	}
}

// --- AppendRunEvent: the Go-side writer (#1810) ---

// TestAppendRunEventWritesALineTheReaderAccepts: the second writer's output
// is indistinguishable from session.js's to ReadRunEvents — same version,
// same ISO-8601 millisecond timestamp shape, same fields.
func TestAppendRunEventWritesALineTheReaderAccepts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	if err := AppendRunEvent(p, Event{Kind: "stop_verify", SessionID: "ses_go_1", Child: true, Detail: map[string]any{"verdict": "blocked"}}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadRunEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	e := events[0]
	if e.V != 1 || e.Kind != "stop_verify" || e.SessionID != "ses_go_1" || !e.Child {
		t.Errorf("event = %+v, want v=1 kind=stop_verify session_id=ses_go_1 child=true", e)
	}
	if verdict, _ := e.Detail["verdict"].(string); verdict != "blocked" {
		t.Errorf("detail.verdict = %v, want blocked", e.Detail["verdict"])
	}
	// Date.toISOString()'s own shape, so a reader cannot tell the two writers
	// apart by their timestamps.
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", e.TS); err != nil {
		t.Errorf("ts = %q, want a JavaScript Date.toISOString()-shaped timestamp: %v", e.TS, err)
	}
	// 0600, like every line session.js writes.
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("events file mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestAppendRunEventRefusesToBreakTheRetentionContract: the Go writer
// enforces the SAME contract the reader does, so a caller cannot smuggle
// transcript text into the file through the process that computes a verdict.
func TestAppendRunEventRefusesToBreakTheRetentionContract(t *testing.T) {
	for name, detail := range map[string]map[string]any{
		"text key":  {"text": "SECRET-TRANSCRIPT-TEXT"},
		"nested":    {"verdict": map[string]any{"reason": "3 tasks incomplete in PLAN.md"}},
		"oversized": {"verdict": strings.Repeat("x", detailValueMaxLen+1)},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "events.jsonl")
			if err := AppendRunEvent(p, Event{Kind: "stop_verify", Detail: detail}); err == nil {
				t.Fatal("AppendRunEvent accepted a detail the reader would drop; the writer must refuse it")
			}
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("the events file was created anyway: %v", err)
			}
		})
	}
}

// TestAppendRunEventCapsAtOneMiBWithOneTruncatedLine: the Go writer honours
// the same 1 MiB cap and the same single "truncated" sentinel session.js
// does — and, unlike the JS side's in-process flag, gets it right across
// process boundaries, since every `hook stop-verify` child is a fresh
// process. Three separate appends past the cap must leave exactly one
// truncated line.
func TestAppendRunEventCapsAtOneMiBWithOneTruncatedLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	line := `{"v":1,"ts":"t","kind":"skill","session_id":"seed","detail":{"skill":"x"}}` + "\n"
	var seed strings.Builder
	for seed.Len() < eventsMaxBytes+1024 {
		seed.WriteString(line)
	}
	if err := os.WriteFile(p, []byte(seed.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := AppendRunEvent(p, Event{Kind: "stop_verify", Detail: map[string]any{"verdict": "complete"}}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	events, err := ReadRunEvents(p)
	if err != nil {
		t.Fatal(err)
	}
	truncated, stopVerifies := 0, 0
	for _, e := range events {
		switch e.Kind {
		case EventsTruncatedKind:
			truncated++
		case "stop_verify":
			stopVerifies++
		}
	}
	if truncated != 1 {
		t.Errorf("got %d truncated lines after 3 appends past the cap, want exactly 1", truncated)
	}
	if stopVerifies != 0 {
		t.Errorf("got %d stop_verify events past the cap, want 0 (writing must have stopped)", stopVerifies)
	}
}

// TestAppendRunEventDisabledPathIsANoOp: an empty path is the events file
// being disabled for this run, not a failure — the same fail-quiet rule
// session.js's own eventsPath() applies.
func TestAppendRunEventDisabledPathIsANoOp(t *testing.T) {
	if err := AppendRunEvent("", Event{Kind: "stop_verify"}); err != nil {
		t.Errorf("AppendRunEvent(\"\") = %v, want nil", err)
	}
}

// TestSanitizeEventIDKeepsOnlyOpaqueIdentifiers: a session id reaches the
// writer through a child process's argv, so its shape is re-checked there.
func TestSanitizeEventIDKeepsOnlyOpaqueIdentifiers(t *testing.T) {
	for _, keep := range []string{"ses_f587a64abffeWo2wDy3QSY38pV", "a", "A1.b-c:d"} {
		if got := SanitizeEventID(keep); got != keep {
			t.Errorf("SanitizeEventID(%q) = %q, want it kept", keep, got)
		}
	}
	for _, drop := range []string{"", " ses_1", "the user wrote: deploy key is PLACEHOLDER", "ses/1", strings.Repeat("s", 129)} {
		if got := SanitizeEventID(drop); got != "" {
			t.Errorf("SanitizeEventID(%q) = %q, want \"\"", drop, got)
		}
	}
}

// TestWaitForRunEventReturnsEarlyAndBounds: the reader contract for a file
// whose terminal line is written by a process that outlives the CLI.
func TestWaitForRunEventReturnsEarlyAndBounds(t *testing.T) {
	t.Run("returns as soon as the kind appears", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "events.jsonl")
		if err := AppendRunEvent(p, Event{Kind: "idle"}); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(150 * time.Millisecond)
			_ = AppendRunEvent(p, Event{Kind: "stop_verify", Detail: map[string]any{"verdict": "complete"}})
		}()
		started := time.Now()
		events, err := WaitForRunEvent(p, "stop_verify", 10*time.Second)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 {
			t.Fatalf("got %d events, want 2: %+v", len(events), events)
		}
		if elapsed > 5*time.Second {
			t.Errorf("WaitForRunEvent took %s; it must return as soon as the event lands, not wait out its bound", elapsed)
		}
	})

	t.Run("returns what it has when the bound expires", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "events.jsonl")
		if err := AppendRunEvent(p, Event{Kind: "idle"}); err != nil {
			t.Fatal(err)
		}
		events, err := WaitForRunEvent(p, "stop_verify", 100*time.Millisecond)
		if err != nil {
			t.Fatalf("an expired bound must not be an error: %v", err)
		}
		if len(events) != 1 || events[0].Kind != "idle" {
			t.Errorf("events = %+v, want the one idle event it did have", events)
		}
	})
}

// TestRunEventsPathFromEnv: the child resolves the same file the plugin
// does, from the two variables it inherits, applying EventsPath's rules.
func TestRunEventsPathFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(RunOutputFileEnvVar, filepath.Join(dir, "output", "run.json"))
	t.Setenv(RunIDEnvVar, "run-env-1")
	got, ok := RunEventsPathFromEnv()
	if !ok {
		t.Fatal("RunEventsPathFromEnv refused an absolute output file")
	}
	if want := filepath.Join(dir, "output", EventsFileName("run-env-1")); got != want {
		t.Errorf("RunEventsPathFromEnv() = %q, want %q", got, want)
	}

	t.Setenv(RunIDEnvVar, "")
	if _, ok := RunEventsPathFromEnv(); ok {
		t.Error("RunEventsPathFromEnv accepted an empty run id; outside a run the file is disabled")
	}
}

// --- EventsPath ---

func TestEventsPath(t *testing.T) {
	cases := []struct {
		name       string
		outputFile string
		runID      string
		wantOK     bool
		want       string
	}{
		{"absolute output file", "/run/root/output/ctx.json", "run-1", true, filepath.Join("/run/root/output", "opencode-events-run-1.jsonl")},
		{"relative output file disables it", "relative/output.json", "run-1", false, ""},
		{"empty output file disables it", "", "run-1", false, ""},
		{"empty run id disables it", "/run/root/output/ctx.json", "", false, ""},
		{"a literal .. segment disables it", "/run/root/../escape/output.json", "run-1", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := EventsPath(tc.outputFile, tc.runID)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (path=%q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- ReadRunEvents: containment and read bound (#1653) ---

// TestReadRunEventsRefusesASymlinkOutOfItsRunDir: an events path that is a
// symlink to a file outside its run dir is refused, not read; a symlink to
// a file inside the run dir is read.
func TestReadRunEventsRefusesASymlinkOutOfItsRunDir(t *testing.T) {
	line := `{"v":1,"ts":"t","kind":"compaction","session_id":"s","child":false,"detail":{}}` + "\n"
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	if err := os.WriteFile(outside, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	path := filepath.Join(runDir, EventsFileName("run-1"))
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	events, err := ReadRunEvents(path)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("ReadRunEvents(symlink out of run dir) = %d events, err %v; want a refusal", len(events), err)
	}

	inside := filepath.Join(runDir, "real.jsonl")
	if err := os.WriteFile(inside, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	inDir := filepath.Join(runDir, EventsFileName("run-2"))
	if err := os.Symlink(inside, inDir); err != nil {
		t.Fatal(err)
	}
	if events, err := ReadRunEvents(inDir); err != nil || len(events) != 1 {
		t.Errorf("ReadRunEvents(symlink inside run dir) = %d events, err %v; want 1, nil", len(events), err)
	}
}

// TestReadRunEventsReadsNoFurtherThanItsBound: a file far larger than the
// writers' 1 MiB cap is read only up to eventsReadMaxBytes, so the reader
// returns the complete lines within the bound and nothing past it.
func TestReadRunEventsReadsNoFurtherThanItsBound(t *testing.T) {
	line := `{"v":1,"ts":"t","kind":"compaction","session_id":"s","child":false,"detail":{}}` + "\n"
	path := filepath.Join(t.TempDir(), EventsFileName("run-1"))
	total := (5 << 20) / len(line)
	if err := os.WriteFile(path, []byte(strings.Repeat(line, total)), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := ReadRunEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := eventsReadMaxBytes / len(line); len(events) != want {
		t.Errorf("ReadRunEvents returned %d of %d lines, want the %d complete lines within %d bytes", len(events), total, want, eventsReadMaxBytes)
	}
}
