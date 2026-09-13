package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// writeFakeOpenCode writes an executable `opencode` into a fresh directory
// and returns its path. body is the shell script after the shebang.
func writeFakeOpenCode(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// testFold is a fold of bin with a short helper timeout, run in dir.
func testFold(bin, dir string, env ...string) openCodeFold {
	return openCodeFold{
		bin:            bin,
		env:            append([]string{"PATH=/usr/bin:/bin"}, env...),
		dir:            dir,
		timeout:        5 * time.Second,
		budget:         30 * time.Second,
		maxDescendants: openCodeMaxDescendants,
	}
}

// sessionExport is a sanitized export with the given session usage and one
// assistant message served by provider/model.
func sessionExport(input, output, reasoning, read, write int, cost float64, provider, model string) string {
	return fmt.Sprintf(`{"info":{"id":"ses_x","title":"[redacted:session-title:ses_x]","cost":%v,`+
		`"tokens":{"input":%d,"output":%d,"reasoning":%d,"cache":{"read":%d,"write":%d}}},`+
		`"messages":[{"info":{"role":"user","id":"msg_1"},"parts":[]},`+
		`{"info":{"role":"assistant","providerID":%q,"modelID":%q},"parts":[{"type":"text","text":"[redacted:text:prt_1]"}]}]}`,
		cost, input, output, reasoning, read, write, provider, model)
}

// treeListing lists every path under dir, so a test can tell that nothing
// was created there.
func treeListing(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

// TestOpenCodeServedModel: the stream names no model, so the served model
// comes from the session export's assistant message, recorded as ADR-022 § 1
// and § 2 decide, and from the dispatched -m when there is no export.
func TestOpenCodeServedModel(t *testing.T) {
	for _, tc := range []struct {
		providerID, modelID, dispatched string
		want                            OpenCodeServedModel
	}{
		{"lmstudio", "qwen/qwen3.8-27b", "", OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "lmstudio/qwen/qwen3.8-27b"}},
		{"", "", "lmstudio/qwen/qwen3.8-27b", OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "lmstudio/qwen/qwen3.8-27b"}},
		{"ollama", "qwen3-coder:30b", "lmstudio/qwen/qwen3.8-27b", OpenCodeServedModel{"ollama", "ollama/qwen3-coder:30b", "ollama/qwen3-coder:30b"}},
		{"anthropic", "claude-sonnet-5", "", OpenCodeServedModel{"anthropic", "claude-sonnet-5", "anthropic/claude-sonnet-5"}},
		{"openai", "gpt-9-preview", "", OpenCodeServedModel{"openai", "openai/gpt-9-preview", "openai/gpt-9-preview"}},
		{"openrouter", "meta-llama/llama-4", "", OpenCodeServedModel{"other", "openrouter/meta-llama/llama-4", "openrouter/meta-llama/llama-4"}},
		{"", "", "", OpenCodeServedModel{}},
	} {
		if got := ResolveOpenCodeServedModel(tc.providerID, tc.modelID, tc.dispatched); got != tc.want {
			t.Errorf("ResolveOpenCodeServedModel(%q, %q, %q) = %+v, want %+v", tc.providerID, tc.modelID, tc.dispatched, got, tc.want)
		}
	}

	// Through the fold: the export's message wins over what was dispatched.
	dir := t.TempDir()
	exportFile := filepath.Join(dir, "export.json")
	if err := os.WriteFile(exportFile, []byte(sessionExport(0, 0, 0, 0, 0, 0, "lmstudio", "qwen/qwen3.8-27b")), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := writeFakeOpenCode(t, fmt.Sprintf(`case "$1" in
--version) echo 1.18.30 ;;
db) echo '[]' ;;
export) cat %q ;;
esac
`, exportFile))
	stream := &OpenCodeStream{SessionID: "ses_fixture0000000000000000001"}
	res := testFold(bin, dir).run(context.Background(), stream, "ollama/qwen3-coder:30b")
	want := OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "lmstudio/qwen/qwen3.8-27b"}
	if res.served != want {
		t.Errorf("served = %+v, want the export's %+v", res.served, want)
	}
	if markers := stream.DriftMarkers(); len(markers) != 0 {
		t.Errorf("a clean fold left drift markers: %q", markers)
	}

	// No export to read: the dispatched -m, and a marker saying why.
	broken := writeFakeOpenCode(t, `case "$1" in --version) echo 1.18.30 ;; db) echo '[]' ;; *) exit 1 ;; esac
`)
	stream = &OpenCodeStream{SessionID: "ses_fixture0000000000000000001"}
	res = testFold(broken, dir).run(context.Background(), stream, "ollama/qwen3-coder:30b")
	if want := (OpenCodeServedModel{"ollama", "ollama/qwen3-coder:30b", "ollama/qwen3-coder:30b"}); res.served != want {
		t.Errorf("served with no export = %+v, want the dispatched %+v", res.served, want)
	}
	if markers := stream.DriftMarkers(); len(markers) != 1 || !strings.Contains(markers[0], "the served model is the dispatched model") {
		t.Errorf("markers = %q, want one saying the export failed", markers)
	}
}

// TestOpenCodeChildUsageFold: a subagent's usage never appears in the stream,
// so after exit every descendant session of the stage's own session is
// exported and its info.tokens and info.cost are added: here two children
// and a grandchild, while a session of another stage in the same run root is
// not. The exports stay in memory: nothing is written under the run's root or
// the stage's directory.
func TestOpenCodeChildUsageFold(t *testing.T) {
	const parent = "ses_fixture0000000000000000001"
	fakeDir := t.TempDir()
	rows := `[{"id":"ses_childA","parent_id":"` + parent + `"},` +
		`{"id":"ses_childB","parent_id":"` + parent + `"},` +
		`{"id":"ses_grandchildC","parent_id":"ses_childA"},` +
		`{"id":"ses_otherStageD","parent_id":"ses_otherStageRoot"}]`
	exports := map[string]string{
		parent:            sessionExport(3089, 14, 0, 0, 0, 0, "lmstudio", "qwen/qwen3.8-27b"),
		"ses_childA":      sessionExport(1000, 100, 10, 50, 5, 0.5, "lmstudio", "qwen/qwen3.8-27b"),
		"ses_childB":      sessionExport(2000, 200, 20, 60, 6, 0.25, "lmstudio", "qwen/qwen3.8-27b"),
		"ses_grandchildC": sessionExport(300, 30, 3, 0, 0, 0, "lmstudio", "qwen/qwen3.8-27b"),
		"ses_otherStageD": sessionExport(99999, 99999, 0, 0, 0, 9, "lmstudio", "qwen/qwen3.8-27b"),
	}
	for id, body := range exports {
		if err := os.WriteFile(filepath.Join(fakeDir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(fakeDir, "calls.log")
	bin := writeFakeOpenCode(t, fmt.Sprintf(`echo "$*" >> %[1]q
case "$1" in
--version) echo 1.18.30 ;;
db) printf '%%s' %[2]q ;;
export) cat %[3]q/"$2".json ;;
esac
`, log, rows, fakeDir))

	runDir := t.TempDir()
	for _, d := range []string{"data", "config", "cache", "state", "tmp", "worktree"} {
		if err := os.MkdirAll(filepath.Join(runDir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	before := treeListing(t, runDir)

	acc := parseOpenCode(openCodeFixtureLines(t, "opencode_stream_research_sample.jsonl"))
	run := newOpenCodeRun(acc.OpenCode(), nil)
	run.fold = testFold(bin, filepath.Join(runDir, "worktree"),
		"XDG_DATA_HOME="+filepath.Join(runDir, "data"), "XDG_CONFIG_HOME="+filepath.Join(runDir, "config"),
		"XDG_CACHE_HOME="+filepath.Join(runDir, "cache"), "XDG_STATE_HOME="+filepath.Join(runDir, "state"),
		"TMPDIR="+filepath.Join(runDir, "tmp"))
	outcome := run.finish(context.Background(), bin, run.fold.env, run.fold.dir, 0, acc, "lmstudio/qwen/qwen3.8-27b")

	if acc.InputTokens != 3089+1000+2000+300 {
		t.Errorf("input = %d, want the stream's 3089 plus the children's 3300", acc.InputTokens)
	}
	if acc.OutputTokens != 14+110+220+33 {
		t.Errorf("output = %d, want the stream's 14 plus the children's output and reasoning, 363", acc.OutputTokens)
	}
	if acc.CacheRead != 110 || acc.CacheCreated != 11 {
		t.Errorf("cache read/write = %d/%d, want 110/11", acc.CacheRead, acc.CacheCreated)
	}
	if outcome.cost != 0.75 {
		t.Errorf("reported cost = %v, want the children's 0.75", outcome.cost)
	}
	if acc.PeakStepInputTokens != 1550 {
		t.Errorf("peak = %d; a child's session total must not become the stage's per-step peak", acc.PeakStepInputTokens)
	}
	if outcome.partial || len(outcome.drift) != 0 {
		t.Errorf("partial = %v, drift = %q; want a complete fold", outcome.partial, outcome.drift)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "ses_otherStageD") {
		t.Errorf("the fold exported another stage's session:\n%s", calls)
	}
	for _, id := range []string{"ses_childA", "ses_childB", "ses_grandchildC"} {
		if !strings.Contains(string(calls), "export "+id+" --sanitize") {
			t.Errorf("%s was not exported with --sanitize:\n%s", id, calls)
		}
	}
	if after := treeListing(t, runDir); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("the fold wrote under the run's root:\nbefore %q\nafter  %q", before, after)
	}
}

// TestOpenCodeFoldBoundedProcesses: every opencode process the fold starts
// runs in its own process group under a timeout, so one that hangs is killed
// with everything it started, and the stage still completes with its usage
// marked partial; and a stage with more subagent sessions than the cap folds
// exactly the cap.
func TestOpenCodeFoldBoundedProcesses(t *testing.T) {
	const parent = "ses_fixture0000000000000000001"

	t.Run("a helper that sleeps 60s is killed at the timeout", func(t *testing.T) {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "sleeper.pid")
		bin := writeFakeOpenCode(t, fmt.Sprintf(`case "$1" in
--version) echo 1.18.30 ;;
db) echo '[{"id":"ses_slowChild","parent_id":"%[1]s"}]' ;;
export)
  [ "$2" = %[1]s ] && { echo '{"messages":[]}'; exit 0; }
  sleep 60 &
  echo $! > %[2]q
  wait ;;
esac
`, parent, pidFile))
		fold := testFold(bin, dir)
		fold.timeout = 300 * time.Millisecond
		stream := &OpenCodeStream{SessionID: parent}
		start := time.Now()
		res := fold.run(context.Background(), stream, "lmstudio/qwen/qwen3.8-27b")
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("the fold took %s; the helper was not killed at its timeout", elapsed)
		}
		if !res.partial {
			t.Error("usage is not marked partial after a helper timed out")
		}
		markers := stream.DriftMarkers()
		if len(markers) != 1 || !strings.Contains(markers[0], "usage partial") || !strings.Contains(markers[0], "timed out") {
			t.Errorf("markers = %q, want one usage-partial marker naming the timeout", markers)
		}
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("the slow export never started its sleeper: %v", err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if !waitGone(pid, 5*time.Second) {
			_ = killPID(pid)
			t.Errorf("the sleeper %d the timed-out helper started is still running: the helper's group was not killed", pid)
		}
	})

	t.Run("100 children fold exactly 64", func(t *testing.T) {
		dir := t.TempDir()
		var rows []string
		for i := 0; i < 100; i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"ses_child%03d","parent_id":%q}`, i, parent))
		}
		listing := filepath.Join(dir, "rows.json")
		if err := os.WriteFile(listing, []byte("["+strings.Join(rows, ",")+"]"), 0o600); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "exports.log")
		bin := writeFakeOpenCode(t, fmt.Sprintf(`case "$1" in
--version) echo 1.18.30 ;;
db) cat %[1]q ;;
export)
  [ "$2" = %[3]s ] && { echo '{"messages":[]}'; exit 0; }
  echo "$2" >> %[2]q
  echo '{"info":{"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}' ;;
esac
`, listing, log, parent))
		stream := &OpenCodeStream{SessionID: parent}
		res := testFold(bin, dir).run(context.Background(), stream, "lmstudio/qwen/qwen3.8-27b")
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(strings.Fields(string(raw))); n != 64 {
			t.Errorf("exported %d child sessions, want exactly 64", n)
		}
		if res.children.Input != 64 || res.children.Output != 64 {
			t.Errorf("children input/output = %d/%d, want 64/64", res.children.Input, res.children.Output)
		}
		if !res.partial {
			t.Error("usage is not marked partial with 36 sessions left unfolded")
		}
		if markers := stream.DriftMarkers(); len(markers) != 1 || !strings.Contains(markers[0], "more than 64 subagent sessions") {
			t.Errorf("markers = %q, want one naming the cap", markers)
		}
	})
}

// killPID is the test's own cleanup for a process the code under test
// failed to kill.
func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// TestOpenCodeVersionStampCached: `opencode --version` is read once per
// resolved binary and modification time. A second read of an unchanged
// binary does not spawn it again; a binary whose modification time changed
// is read again.
func TestOpenCodeVersionStampCached(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "version-calls")
	bin := writeFakeOpenCode(t, fmt.Sprintf(`echo x >> %q
echo 1.18.30
`, counter))
	calls := func() int {
		raw, err := os.ReadFile(counter)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "x\n")
	}
	fold := testFold(bin, t.TempDir())
	for i := 0; i < 2; i++ {
		v, err := fold.version(context.Background())
		if err != nil || v != "1.18.30" {
			t.Fatalf("version = %q, %v; want 1.18.30", v, err)
		}
	}
	if n := calls(); n != 1 {
		t.Errorf("--version ran %d times for an unchanged binary, want 1", n)
	}

	// Reached through a symlink, the binary resolves to the same entry.
	link := filepath.Join(t.TempDir(), "opencode")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	linked := fold
	linked.bin = link
	if _, err := linked.version(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := calls(); n != 1 {
		t.Errorf("--version ran %d times; a symlink to the same binary must hit the cache", n)
	}

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := fold.version(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := calls(); n != 2 {
		t.Errorf("--version ran %d times after the binary changed, want 2", n)
	}
}

// openCodeStageRun dispatches one stage through Manager.RunStage to a fake
// opencode that prints stdout on stdout and stderr on stderr and exits with
// exitCode, and answers the parser's --version, db and export.
//
// RunStage runs under a watchdog. A reader that stops before the child's
// output ends leaves the child blocked on a full pipe, and RunStage then never
// returns, which is the failure a long line used to cause; the watchdog kills
// the fake's process group so the test fails instead of hanging.
func openCodeStageRun(t *testing.T, stdout, stderr string, exitCode int, allowedTools []string, streamer adapters.OutputStreamer) (*adapters.RunResult, string) {
	t.Helper()
	isolateOpenCodeHome(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "stage.pid")
	outFile, errFile := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	if err := os.WriteFile(outFile, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errFile, []byte(stderr), 0o600); err != nil {
		t.Fatal(err)
	}
	exportFile := filepath.Join(dir, "export.json")
	if err := os.WriteFile(exportFile, []byte(sessionExport(0, 0, 0, 0, 0, 0, "lmstudio", "qwen/qwen3.8-27b")), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
--version) echo 1.18.30; exit 0 ;;
db) echo '[]'; exit 0 ;;
export) cat %[1]q; exit 0 ;;
esac
echo $$ > %[5]q
cat > /dev/null
cat %[2]q
cat %[3]q >&2
exit %[4]d
`, exportFile, outFile, errFile, exitCode, pidFile)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	opts := openCodeStageOptions("lmstudio/qwen/qwen3.8-27b", nil)
	opts.AllowedTools = allowedTools
	opts.Streamer = streamer
	opts.Timeout = 20 * time.Second
	workspace := openCodeWorkspace(t)
	var result *adapters.RunResult
	var err error
	hung := false
	logged := captureStderr(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
		}()
		select {
		case <-done:
		case <-time.After(45 * time.Second):
			hung = true
			if raw, readErr := os.ReadFile(pidFile); readErr == nil {
				if pgid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pgid > 0 {
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				}
			}
			<-done
		}
	})
	if hung {
		t.Fatal("RunStage did not return within 45s: the stage's output was not read to its end, so the child blocked on a full pipe")
	}
	if err != nil {
		t.Fatalf("RunStage: %v", err)
	}
	return result, logged
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestOpenCodeAutoRejectMarker: a permission OpenCode rejects on its own is
// named on stderr, and the run then exits 0. The captured line yields
// [adapter-permission-rejected] when the stage's allowed tools grant the tool,
// so the adapter's own posture refused it, and [permission-denied] when they
// do not. Through a stage, the exit-0 run is reported failed with the marker
// ending the stderr classification reads, and its usage is kept.
func TestOpenCodeAutoRejectMarker(t *testing.T) {
	line := strings.TrimSpace(readTestdata(t, "opencode_auto_reject_stderr.txt"))
	if !strings.Contains(line, "\x1b[") {
		t.Fatalf("the captured line lost its terminal escapes, so it no longer proves they are handled: %q", line)
	}
	for _, tc := range []struct {
		allowed []string
		want    string
	}{
		{[]string{"Read", "Bash"}, "[adapter-permission-rejected] tool=bash"},
		{[]string{"Bash(git:*)"}, "[adapter-permission-rejected] tool=bash"},
		{[]string{"Read", "Edit", "Write"}, "[permission-denied] tool=bash"},
		{nil, "[permission-denied] tool=bash"},
	} {
		got, ok := OpenCodeAutoRejectMarker(line, tc.allowed)
		if !ok || got != tc.want {
			t.Errorf("OpenCodeAutoRejectMarker(allowed %q) = %q, %v; want %q", tc.allowed, got, ok, tc.want)
		}
	}
	for _, notReject := range []string{
		"permission requested: bash (x); auto-rejecting, said the model",
		`{"type":"text","part":{"text":"! permission requested: bash (x); auto-rejecting"}}`,
		"timestamp=2026-09-13 level=ERROR message=\"stream error\"",
	} {
		if got, ok := OpenCodeAutoRejectMarker(notReject, []string{"Bash"}); ok {
			t.Errorf("%q yielded marker %q", notReject, got)
		}
	}
	for claude, want := range map[string]string{
		"Bash": "bash", "Read": "read", "Write": "edit", "Edit": "edit", "MultiEdit": "edit",
		"Glob": "glob", "Grep": "grep", "Task": "task", "WebFetch": "webfetch",
	} {
		if got, ok := adapters.OpenCodeToolForClaudeTool(claude); !ok || got != want {
			t.Errorf("OpenCodeToolForClaudeTool(%q) = %q, %v; want %q", claude, got, ok, want)
		}
	}

	stream := readTestdata(t, "opencode_auto_reject_stream.jsonl")
	stderr := readTestdata(t, "opencode_auto_reject_stderr.txt")
	for _, tc := range []struct {
		allowed []string
		marker  string
	}{
		{[]string{"Read", "Bash"}, "[adapter-permission-rejected] tool=bash"},
		{[]string{"Read"}, "[permission-denied] tool=bash"},
	} {
		result, _ := openCodeStageRun(t, stream, stderr, 0, tc.allowed, nil)
		if result.ExitCode != 1 {
			t.Errorf("allowed %q: exit code = %d; an exit-0 run whose tool call was rejected must not read as success", tc.allowed, result.ExitCode)
		}
		if !strings.HasSuffix(result.Stderr, tc.marker+"\n") {
			t.Errorf("allowed %q: stderr does not end with %q:\n%s", tc.allowed, tc.marker, result.Stderr)
		}
		if result.InputTokens != 1533 || result.OutputTokens != 3 {
			t.Errorf("allowed %q: input/output = %d/%d, want the capture's 1533/3", tc.allowed, result.InputTokens, result.OutputTokens)
		}
		if len(result.DriftMarkers) != 0 {
			t.Errorf("allowed %q: drift markers on a real capture: %q", tc.allowed, result.DriftMarkers)
		}
	}

	// The stream shows the rejection but stderr does not: the wording drifted.
	// That is a drift marker, not a guess at a classification marker.
	result, _ := openCodeStageRun(t, stream, "", 0, []string{"Bash"}, nil)
	if result.ExitCode != 0 || strings.Contains(result.Stderr, "[permission-denied]") || strings.Contains(result.Stderr, "[adapter-permission-rejected]") {
		t.Errorf("a marker came from the stream rather than stderr: exit %d, stderr %q", result.ExitCode, result.Stderr)
	}
	if len(result.DriftMarkers) != 1 || !strings.Contains(result.DriftMarkers[0], "stderr carried no auto-reject line") {
		t.Errorf("drift markers = %q, want the stderr cross-check", result.DriftMarkers)
	}
}

// credentialSamples are one credential of each shape the redaction covers,
// built at run time so no credential-shaped literal is committed.
func credentialSamples() map[string]string {
	return map[string]string{
		"api-key":          "sk-" + strings.Repeat("Ab1", 8),
		"forge-token":      "gh" + "p_" + strings.Repeat("a1B2", 9),
		"bearer-token":     "Bearer " + strings.Repeat("xY9", 7),
		"authorization":    "Authorization: Basic " + strings.Repeat("dXNl", 4),
		"userinfo":         "https://u:p@h/",
		"query-credential": "https://h/v1?api_key=" + strings.Repeat("q7", 6),
	}
}

// credentialValue is the part of a sample that must not survive redaction.
func credentialValue(shape, sample string) string {
	switch shape {
	case "bearer-token":
		return strings.TrimPrefix(sample, "Bearer ")
	case "authorization":
		return strings.TrimPrefix(sample, "Authorization: Basic ")
	case "userinfo":
		return "u:p@"
	case "query-credential":
		_, v, _ := strings.Cut(sample, "api_key=")
		return v
	}
	return sample
}

// TestOpenCodeStderrRedaction: credentials a child prints are removed before
// its output is streamed, kept, or classified: API keys, GitHub tokens,
// bearer and authorization credentials, a URL's user:password and a
// credential query parameter, on stderr and stdout alike, and a JSON event
// stays valid. Markers come from stderr and the parser only: a text part
// that says "context length exceeded", or that quotes an auto-reject line,
// yields no marker.
func TestOpenCodeStderrRedaction(t *testing.T) {
	samples := credentialSamples()
	for shape, sample := range samples {
		got := RedactCredentials("before " + sample + " after")
		if strings.Contains(got, credentialValue(shape, sample)) || !strings.Contains(got, "[REDACTED:") {
			t.Errorf("%s: RedactCredentials left %q", shape, got)
		}
	}
	for _, keep := range []string{
		"task-abcdefghijklmnopqrstuvwxyz0123456789", // sk- inside a word is not a key
		"the bearer of bad news",
		"https://github.com/nightgauge/nightgauge/pull/1624",
		"ssh://git@github.com/nightgauge/nightgauge.git",
		"https://h/v1?page=2&per_page=100",
		"[REDACTED:OPENAI_API_KEY]",
	} {
		if got := RedactCredentials(keep); got != keep {
			t.Errorf("RedactCredentials(%q) = %q, want it unchanged", keep, got)
		}
	}

	var all []string
	for _, shape := range []string{"api-key", "forge-token", "bearer-token", "authorization", "userinfo", "query-credential"} {
		all = append(all, samples[shape])
	}
	text, err := json.Marshal(strings.Join(all, " ") + " context length exceeded; ! permission requested: bash (x); auto-rejecting")
	if err != nil {
		t.Fatal(err)
	}
	stdout := readTestdata(t, "opencode_stream_research_sample.jsonl") +
		`{"type":"text","timestamp":1,"sessionID":"ses_fixture0000000000000000001","part":{"type":"text","text":` + string(text) + `}}` + "\n"
	stderr := "ERROR provider request failed " + strings.Join(all, " ") + "\n"
	streamer := &redactionStreamer{}
	result, _ := openCodeStageRun(t, stdout, stderr, 0, []string{"Bash"}, streamer)

	outputs := map[string]string{"streamed": streamer.out.String(), "result.Stdout": result.Stdout, "result.Stderr": result.Stderr}
	for where, out := range outputs {
		for shape, sample := range samples {
			if strings.Contains(out, credentialValue(shape, sample)) {
				t.Errorf("%s still holds the %s credential", where, shape)
			}
		}
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if last := lines[len(lines)-1]; !json.Valid([]byte(last)) {
		t.Errorf("the redacted text event is not valid JSON: %s", last)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d; text in the transcript must not fail a stage", result.ExitCode)
	}
	for _, marker := range []string{PermissionDeniedMarker, PermissionRejectedMarker} {
		if strings.Contains(result.Stderr, marker) {
			t.Errorf("a text part produced the %s marker:\n%s", marker, result.Stderr)
		}
	}
	if len(result.DriftMarkers) != 0 {
		t.Errorf("drift markers = %q, want none", result.DriftMarkers)
	}
	if result.InputTokens != 3089 {
		t.Errorf("input = %d; redaction must not change usage", result.InputTokens)
	}
}

// TestOpenCodeLongLine: a line longer than the scanner's 1 MiB limit is
// dropped with one drift marker, and the lines after it are still read. The
// scanner used to stop at it, so every step_finish after a large tool output
// was lost.
func TestOpenCodeLongLine(t *testing.T) {
	huge := strings.Repeat("x", 20<<20)
	var got []string
	oversize := 0
	err := forEachLine(strings.NewReader("a\n"+huge+"\nb\r\nc"), streamLineLimit,
		func(line []byte) { got = append(got, string(line)) }, func() { oversize++ })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "a|b|c" || oversize != 1 {
		t.Errorf("lines = %q, oversize = %d; want a, b, c and one oversized line", got, oversize)
	}

	sample := openCodeFixtureLines(t, "opencode_stream_research_sample.jsonl")
	stdout := strings.Join(sample[:2], "\n") + "\n" + `{"type":"tool_use","part":{"text":"` + huge + `"}}` + "\n" +
		strings.Join(sample[2:], "\n") + "\n"
	result, logged := openCodeStageRun(t, stdout, "", 0, nil, nil)
	if result.InputTokens != 3089 || result.OutputTokens != 14 {
		t.Errorf("input/output = %d/%d, want 3089/14: the steps after the long line were lost", result.InputTokens, result.OutputTokens)
	}
	if len(result.DriftMarkers) != 1 || !strings.Contains(result.DriftMarkers[0], "dropped a stdout line longer than the 1048576-byte line limit") {
		t.Errorf("drift markers = %q, want one for the long line", result.DriftMarkers)
	}
	if strings.Contains(result.Stdout, "xxxxxxxxxxxxxxxx") {
		t.Error("the dropped line reached the kept stdout")
	}
	if !strings.Contains(logged, OpenCodeDriftMarker+" nightgauge/nightgauge#1612 feature-dev: dropped a stdout line") {
		t.Errorf("the drift marker was not logged with the stage's identity:\n%s", logged)
	}
}

// captureScript is the fixture capture script, from the repository root.
func captureScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "capture-opencode-fixture.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func runCaptureScript(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{captureScript(t)}, args...)...)
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// TestCaptureOpenCodeFixtureRefusesCredentials: the capture script refuses to
// write a fixture that still holds a credential shape. --self-test plants one
// in a staged copy of the fixtures and runs the write path, which must refuse
// it and exit non-zero, having written nothing. --check passes the committed
// fixtures, and refuses a file holding any one shape the redaction covers, or
// an IPv4 address other than 127.0.0.1. The same samples are removed by
// RedactCredentials and by redact-opencode.jq, so the three agree.
func TestCaptureOpenCodeFixtureRefusesCredentials(t *testing.T) {
	fixtures := []string{"opencode_stream_research_sample.jsonl", "opencode_auto_reject_stream.jsonl", "opencode_auto_reject_stderr.txt"}
	hashes := map[string]string{}
	for _, name := range fixtures {
		hashes[name] = readTestdata(t, name)
	}

	out, code := runCaptureScript(t, "--self-test")
	if code == 0 || !strings.Contains(out, "refused: the staged opencode_stream_research_sample.jsonl: it still matches the credential shape 'forge-token'") {
		t.Errorf("--self-test exited %d; want non-zero with the refusal:\n%s", code, out)
	}
	for _, name := range fixtures {
		if readTestdata(t, name) != hashes[name] {
			t.Errorf("--self-test changed the committed %s", name)
		}
	}

	var committed []string
	for _, name := range fixtures {
		committed = append(committed, filepath.Join("testdata", name))
	}
	if out, code := runCaptureScript(t, append([]string{"--check"}, committed...)...); code != 0 {
		t.Errorf("--check refused the committed fixtures (exit %d):\n%s", code, out)
	}

	dir := t.TempDir()
	planted := credentialSamples()
	planted["ipv4"] = "http://192.0.2.10:1234/v1"
	for shape, sample := range planted {
		raw := filepath.Join(dir, shape+".raw")
		if err := os.WriteFile(raw, []byte(`{"type":"text","part":{"text":"`+sample+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, code := runCaptureScript(t, "--check", raw); code == 0 {
			t.Errorf("--check passed a file holding the %s sample:\n%s", shape, out)
		}
		if shape == "ipv4" {
			continue
		}
		redacted := filepath.Join(dir, shape+".redacted")
		if err := os.WriteFile(redacted, []byte(RedactCredentials(sample)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, code := runCaptureScript(t, "--check", redacted); code != 0 {
			t.Errorf("--check refused the redacted %s sample (exit %d):\n%s", shape, code, out)
		}
	}

	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH — redact-opencode.jq is a jq filter")
	}
	for shape, sample := range credentialSamples() {
		cmd := exec.Command(jq, "-j", "-R", "-s", "--arg", "mode", "stderr", "--argjson", "roots", "[]",
			"-f", filepath.Join("testdata", "redact-opencode.jq"))
		cmd.Stdin = strings.NewReader("x " + sample + " y\n")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("redact-opencode.jq: %v", err)
		}
		if got := stdout.String(); strings.Contains(got, credentialValue(shape, sample)) || got != RedactCredentials("x "+sample+" y\n") {
			t.Errorf("%s: redact-opencode.jq gave %q; RedactCredentials gives %q", shape, got, RedactCredentials("x "+sample+" y\n"))
		}
	}
}
