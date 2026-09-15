package opencodeplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
