package adapters

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// TestPreserveOpenCodeRunEvidence is #2171 item 3: a failed run keeps its
// session database and logs, owner-only, and nothing else from the root.
func TestPreserveOpenCodeRunEvidence(t *testing.T) {
	home := t.TempDir()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenCodeRunRoot(testStateHome(home), id)
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("data/opencode/opencode.db", "db")
	write("data/opencode/opencode.db-wal", "wal")
	write("data/opencode/log/2026-09-26.log", "log")
	write("data/opencode/auth.json", "secret")
	write("home/.netrc", "secret")
	write("config/opencode/opencode.json", "{}")
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "data/opencode/log/link.log")); err != nil {
		t.Fatal(err)
	}

	dst, err := PreserveOpenCodeRunEvidence(testStateHome(home), id, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(OpenCodeEvidenceDir(testStateHome(home)), id); dst != want {
		t.Fatalf("dst = %q, want %q", dst, want)
	}
	var got []string
	_ = filepath.WalkDir(dst, func(p string, d os.DirEntry, _ error) error {
		fi, _ := os.Lstat(p)
		if d.IsDir() {
			if fi.Mode().Perm() != 0o700 {
				t.Errorf("%s mode %v, want 0700", p, fi.Mode().Perm())
			}
			return nil
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, fi.Mode().Perm())
		}
		rel, _ := filepath.Rel(dst, p)
		got = append(got, rel)
		return nil
	})
	want := []string{"log/2026-09-26.log", "opencode.db", "opencode.db-wal"}
	if len(got) != len(want) {
		t.Fatalf("preserved %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("preserved %v, want %v", got, want)
		}
	}
	for _, d := range []string{OpenCodeEvidenceDir(testStateHome(home)), filepath.Dir(OpenCodeEvidenceDir(testStateHome(home)))} {
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", d, fi.Mode().Perm())
		}
	}

	// The root can now go; the evidence stays.
	if err := RemoveOpenCodeRunRoot(testStateHome(home), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "opencode.db")); err != nil {
		t.Fatalf("evidence gone with the root: %v", err)
	}

	// Swept by age like the run roots.
	old := time.Now().Add(-OpenCodeOrphanMaxAge - time.Hour)
	if err := os.Chtimes(dst, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := SweepOpenCodeRunEvidence(testStateHome(home), OpenCodeOrphanMaxAge, time.Now())
	if err != nil || len(removed) != 1 || removed[0] != id {
		t.Fatalf("sweep removed %v, %v; want [%s]", removed, err, id)
	}
}

func TestPreserveOpenCodeRunEvidence_NothingToKeep(t *testing.T) {
	home := t.TempDir()
	id, _ := runstate.NewRunID()
	dst, err := PreserveOpenCodeRunEvidence(testStateHome(home), id, time.Now())
	if err != nil || dst != "" {
		t.Fatalf("missing root: dst=%q err=%v, want none", dst, err)
	}
	if _, err := os.Stat(OpenCodeEvidenceDir(testStateHome(home))); !os.IsNotExist(err) {
		t.Fatalf("evidence dir created for nothing: %v", err)
	}
}

// readToolEvidenceFixture reads every line of a captured 2.2.7 stream and
// returns the tool call evidence ReadOpenCodeToolEvidence records from it.
func readToolEvidenceFixture(t *testing.T, name string) []OpenCodeToolEvidence {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "opencode-cli", name))
	if err != nil {
		t.Fatal(err)
	}
	var recs []OpenCodeToolEvidence
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if rec, ok := ReadOpenCodeToolEvidence(sc.Bytes()); ok {
			recs = append(recs, rec)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

// TestReadOpenCodeToolEvidence_2x is #3305: the 2.x tool_use part.state and
// part.state.metadata.providerCall/providerResult, read from the captured
// 2.2.7 stream, and none of the transcript.
func TestReadOpenCodeToolEvidence_2x(t *testing.T) {
	const session = "ses_fixture0000000000000000001"
	recs := readToolEvidenceFixture(t, "stream-tool-state.jsonl")
	f := false
	ms := func(n int64) *int64 { return &n }
	want := []OpenCodeToolEvidence{
		{Tool: "read", CallID: "call_1", SessionID: session, Status: "completed", Errored: false,
			ProviderCallExecuted: &f, ProviderResultExecuted: &f, DurationMS: ms(7)},
		{Tool: "shell", CallID: "call_2", SessionID: session, Status: "error", Errored: true,
			ProviderCallExecuted: &f, ProviderResultExecuted: &f, DurationMS: ms(3)},
		{Tool: "read", CallID: "call_3", SessionID: session, Status: "error", Errored: true,
			ProviderCallExecuted: &f, ProviderResultExecuted: &f, DurationMS: ms(3)},
	}
	gotJSON, _ := json.Marshal(recs)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("evidence =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	for _, leak := range []string{"hello.txt", "echo hi", "File not found", "non-interactive", "missing.txt", "rawInput"} {
		if bytes.Contains(gotJSON, []byte(leak)) {
			t.Errorf("evidence records transcript text %q: %s", leak, gotJSON)
		}
	}

	// A provider-side tool reports executed: true.
	body, _ := os.ReadFile(filepath.Join("testdata", "opencode-cli", "stream-tool-state.jsonl"))
	line := bytes.SplitN(body, []byte("\n"), 3)[1]
	if !bytes.Contains(line, []byte(`"id":"call_1"`)) || !bytes.Contains(line, []byte(`"providerCall":{"executed":false}`)) {
		t.Fatalf("fixture line 1 is not the read/call_1 tool_use: %s", line)
	}
	executed := bytes.Replace(line, []byte(`"providerCall":{"executed":false}`), []byte(`"providerCall":{"executed":true}`), 1)
	rec, ok := ReadOpenCodeToolEvidence(executed)
	if !ok || rec.ProviderCallExecuted == nil || !*rec.ProviderCallExecuted ||
		rec.ProviderResultExecuted == nil || *rec.ProviderResultExecuted {
		t.Errorf("provider-executed call = %+v, %v; want providerCall true, providerResult false", rec, ok)
	}

	// The subagent's child session.
	sub := readToolEvidenceFixture(t, "stream-subagent.jsonl")
	if len(sub) != 1 || sub[0].Tool != "subagent" || sub[0].SubagentSessionID != "ses_fixture0000000000000000002" {
		t.Errorf("subagent evidence = %+v, want one subagent call in ses_fixture0000000000000000002", sub)
	}

	// 1.x: no providerCall/providerResult, so the flags are absent, not false.
	v1 := `{"type":"tool_use","sessionID":"ses_a","part":{"id":"c","tool":"bash","state":{"status":"completed","metadata":{"output":"x"},"time":{"start":5,"end":2}}}}`
	rec, ok = ReadOpenCodeToolEvidence([]byte(v1))
	if !ok || rec.ProviderCallExecuted != nil || rec.ProviderResultExecuted != nil || rec.DurationMS != nil || rec.SessionID != "ses_a" {
		t.Errorf("1.x call = %+v, %v; want nil provider flags and duration, session ses_a", rec, ok)
	}
	// Metadata of another shape loses only the fields it mangles.
	for _, md := range []string{
		`"metadata":"text"`,
		`"metadata":{"providerCall":true,"providerResult":{"executed":"yes"},"metadata":"x"}`,
		`"metadata":[1]`,
	} {
		odd := `{"type":"tool_use","part":{"id":"c","sessionID":"ses_b","tool":"edit","state":{"status":"completed",` + md + `}}}`
		rec, ok := ReadOpenCodeToolEvidence([]byte(odd))
		if !ok || rec.Tool != "edit" || rec.CallID != "c" || rec.Status != "completed" ||
			rec.ProviderCallExecuted != nil || rec.ProviderResultExecuted != nil || rec.SubagentSessionID != "" {
			t.Errorf("%s: %+v, %v; want tool, call id and status, no flags", md, rec, ok)
		}
	}
	// A part without a state still names its call.
	rec, ok = ReadOpenCodeToolEvidence([]byte(`{"type":"tool_use","part":{"id":"c","tool":"read"}}`))
	if !ok || rec.Tool != "read" || rec.CallID != "c" || rec.Status != "" || rec.Errored {
		t.Errorf("stateless call = %+v, %v; want read/c, no status", rec, ok)
	}
	for _, other := range []string{
		`{"type":"step_finish","part":{"tool":"read"}}`,
		`{"type":"tool_use"}`,
		`not json`,
	} {
		if _, ok := ReadOpenCodeToolEvidence([]byte(other)); ok {
			t.Errorf("%s: recorded evidence, want none", other)
		}
	}
}

// TestPreserveOpenCodeRunEvidence_ToolCalls: the recorded tool call evidence
// is kept with a failed run's evidence, owner-only, even from a root that
// holds nothing else, and a symbolic link in its place is not followed.
func TestPreserveOpenCodeRunEvidence_ToolCalls(t *testing.T) {
	home := t.TempDir()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenCodeRunRoot(testStateHome(home), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	recs := readToolEvidenceFixture(t, "stream-tool-state.jsonl")
	if err := AppendOpenCodeToolEvidence(root, recs[:2], 0); err != nil {
		t.Fatal(err)
	}
	if err := AppendOpenCodeToolEvidence(root, recs[2:], 4); err != nil {
		t.Fatal(err)
	}
	if err := AppendOpenCodeToolEvidence(root, nil, 0); err != nil {
		t.Fatal(err)
	}

	if fi, err := os.Lstat(filepath.Join(root, OpenCodeToolEvidenceFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("run root %s: %v, %v; want mode 0600", OpenCodeToolEvidenceFile, fi, err)
	}

	dst, err := PreserveOpenCodeRunEvidence(testStateHome(home), id, time.Now())
	if err != nil || dst == "" {
		t.Fatalf("preserve: dst=%q err=%v", dst, err)
	}
	path := filepath.Join(dst, OpenCodeToolEvidenceFile)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode %v, want 0600", path, fi.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(body), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("%d lines, want 4 (three calls and the dropped count):\n%s", len(lines), body)
	}
	for i, rec := range recs {
		var got OpenCodeToolEvidence
		if err := json.Unmarshal(lines[i], &got); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(rec)
		if !bytes.Equal(a, b) {
			t.Errorf("line %d = %s, want %s", i, a, b)
		}
	}
	if string(lines[3]) != `{"dropped":4}` {
		t.Errorf("last line = %s, want {\"dropped\":4}", lines[3])
	}

	// A link in its place is neither followed by the preserve nor appended to.
	id2, _ := runstate.NewRunID()
	root2, _ := OpenCodeRunRoot(testStateHome(home), id2)
	if err := os.MkdirAll(root2, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "elsewhere.jsonl")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root2, OpenCodeToolEvidenceFile)); err != nil {
		t.Fatal(err)
	}
	if err := AppendOpenCodeToolEvidence(root2, recs, 0); err == nil {
		t.Error("appended through a symbolic link")
	}
	if b, _ := os.ReadFile(target); string(b) != "outside\n" {
		t.Errorf("link target changed: %q", b)
	}
	dst2, err := PreserveOpenCodeRunEvidence(testStateHome(home), id2, time.Now())
	if err != nil || dst2 != "" {
		t.Fatalf("symlinked evidence: dst=%q err=%v, want nothing kept", dst2, err)
	}
}

// TestAppendOpenCodeToolEvidence_RootGuard: nothing is written outside a run
// root that is a realDir directory.
func TestAppendOpenCodeToolEvidence_RootGuard(t *testing.T) {
	dir := t.TempDir()
	rec := []OpenCodeToolEvidence{{Tool: "read", Status: "completed"}}
	if err := AppendOpenCodeToolEvidence(filepath.Join(dir, "missing"), rec, 0); err == nil {
		t.Error("appended to a missing root")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendOpenCodeToolEvidence(file, rec, 0); err == nil {
		t.Error("appended under a root that is a file")
	}
	link := filepath.Join(dir, "link")
	realDir := filepath.Join(dir, "realDir")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if err := AppendOpenCodeToolEvidence(link, rec, 0); err == nil {
		t.Error("appended under a root that is a symbolic link")
	}
	if _, err := os.Stat(filepath.Join(realDir, OpenCodeToolEvidenceFile)); !os.IsNotExist(err) {
		t.Errorf("evidence written through the linked root: %v", err)
	}
}
