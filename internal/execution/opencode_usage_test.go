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
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
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
// and § 2 decide, and from the dispatched -m when there is no export. The
// upstream model is the -m value exactly as dispatched (§ 2), also when the
// export shows that another model served the stage, such as an agent's model
// from a config the run read: then it is the only record of what was
// dispatched.
func TestOpenCodeServedModel(t *testing.T) {
	for _, tc := range []struct {
		providerID, modelID, dispatched string
		want                            OpenCodeServedModel
	}{
		{"lmstudio", "qwen/qwen3.8-27b", "lmstudio/qwen/qwen3.8-27b", OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "lmstudio/qwen/qwen3.8-27b"}},
		{"", "", "lmstudio/qwen/qwen3.8-27b", OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "lmstudio/qwen/qwen3.8-27b"}},
		{"ollama", "qwen3-coder:30b", "lmstudio/qwen/qwen3.8-27b", OpenCodeServedModel{"ollama", "ollama/qwen3-coder:30b", "lmstudio/qwen/qwen3.8-27b"}},
		{"lmstudio", "qwen/qwen3.8-27b", "ollama/qwen3-coder:30b", OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "ollama/qwen3-coder:30b"}},
		{"anthropic", "claude-sonnet-5", "anthropic/claude-sonnet-5", OpenCodeServedModel{"anthropic", "claude-sonnet-5", "anthropic/claude-sonnet-5"}},
		{"openai", "gpt-9-preview", "openai/gpt-9-preview", OpenCodeServedModel{"openai", "openai/gpt-9-preview", "openai/gpt-9-preview"}},
		{"openrouter", "meta-llama/llama-4", "openrouter/meta-llama/llama-4", OpenCodeServedModel{"other", "openrouter/meta-llama/llama-4", "openrouter/meta-llama/llama-4"}},
		{"anthropic", "claude-sonnet-5", "", OpenCodeServedModel{"anthropic", "claude-sonnet-5", ""}},
		{"", "", "", OpenCodeServedModel{}},
	} {
		if got := ResolveOpenCodeServedModel(tc.providerID, tc.modelID, tc.dispatched); got != tc.want {
			t.Errorf("ResolveOpenCodeServedModel(%q, %q, %q) = %+v, want %+v", tc.providerID, tc.modelID, tc.dispatched, got, tc.want)
		}
	}

	// Through the fold: the export's message wins over what was dispatched,
	// and the dispatched -m stays the upstream model.
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
	want := OpenCodeServedModel{"lm-studio", "lm-studio/qwen/qwen3.8-27b", "ollama/qwen3-coder:30b"}
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
	run.fold = testFold(bin, runDir)
	stageEnv := append(append([]string{}, run.fold.env...),
		"XDG_DATA_HOME="+filepath.Join(runDir, "data"), "XDG_CONFIG_HOME="+filepath.Join(runDir, "config"),
		"XDG_CACHE_HOME="+filepath.Join(runDir, "cache"), "XDG_STATE_HOME="+filepath.Join(runDir, "state"),
		"TMPDIR="+filepath.Join(runDir, "tmp"))
	outcome := run.finish(context.Background(), openCodeExit{bin: bin, env: stageEnv, runRoot: runDir,
		exitCode: 0, dispatched: "lmstudio/qwen/qwen3.8-27b"}, acc)

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
// exitCode, and answers the parser's --version, db and export
// (openCodeStageRunWith).
func openCodeStageRun(t *testing.T, stdout, stderr string, exitCode int, allowedTools []string, streamer adapters.OutputStreamer) (*adapters.RunResult, string) {
	t.Helper()
	out := openCodeStageRunWith(t, openCodeStage{stdout: stdout, stderr: stderr, exitCode: exitCode, allowedTools: allowedTools, streamer: streamer})
	return out.result, out.logged
}

// openCodeStage is one stage openCodeStageRunWith dispatches.
type openCodeStage struct {
	// model is the dispatched model; lmstudio/qwen/qwen3.8-27b when empty.
	model          string
	stdout, stderr string
	exitCode       int
	allowedTools   []string
	streamer       adapters.OutputStreamer
	// worktree, when set, prepares the stage's worktree before dispatch.
	worktree func(dir string)
	// hold keeps the stage running after its output until it is signalled,
	// and during then runs with the manager running it.
	hold   bool
	during func(m *Manager)
}

// openCodeStageOutcome is what one stage left behind.
type openCodeStageOutcome struct {
	result *adapters.RunResult
	// logged is what the manager wrote to its own stderr.
	logged string
	// helpers are the fake's invocations other than the stage's own run:
	// the processes the parser started after the stage.
	helpers []openCodeHelperCall
	// worktree is the stage's worktree.
	worktree string
}

// openCodeHelperCall is one process the parser started, as the fake saw it.
type openCodeHelperCall struct {
	args []string
	cwd  string
	env  map[string]string
}

// openCodeStageRunWith dispatches stage through Manager.RunStage to a fake
// opencode first on PATH. The fake answers --version, db and export, and
// records each of those calls' argv, working directory and environment. Like
// opencode 1.18.30, an `export` without --pure loads the plugins of the
// .opencode/ directory it runs in: here, it sources every
// .opencode/plugin/*.sh.
//
// RunStage runs under a watchdog. A reader that stops before the child's
// output ends leaves the child blocked on a full pipe, and RunStage then never
// returns, which is the failure a long line used to cause; the watchdog kills
// the fake's process group so the test fails instead of hanging.
func openCodeStageRunWith(t *testing.T, stage openCodeStage) openCodeStageOutcome {
	t.Helper()
	isolateOpenCodeHome(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "stage.pid")
	helperLog := filepath.Join(dir, "helpers.log")
	outFile, errFile := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	if err := os.WriteFile(outFile, []byte(stage.stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errFile, []byte(stage.stderr), 0o600); err != nil {
		t.Fatal(err)
	}
	exportFile := filepath.Join(dir, "export.json")
	if err := os.WriteFile(exportFile, []byte(sessionExport(0, 0, 0, 0, 0, 0, "lmstudio", "qwen/qwen3.8-27b")), 0o600); err != nil {
		t.Fatal(err)
	}
	// A held stage has printed everything, and traps the stop, once ready
	// exists; during waits for it, or for the stage's pid otherwise.
	readyFile, hold := pidFile, ""
	if stage.hold {
		readyFile = filepath.Join(dir, "ready")
		hold = fmt.Sprintf("trap 'exit 0' TERM\ntouch %q\nsleep 30 &\nwait", readyFile)
	}
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
--version|db|export)
  { printf 'ARGS'; for a in "$@"; do printf '\t%%s' "$a"; done; printf '\nCWD\t%%s\n' "$(pwd -P)"
    env | sed 's/^/ENV	/'; echo END; } >> %[6]q ;;
esac
case "$1" in
--version) echo 1.18.30; exit 0 ;;
db) echo '[]'; exit 0 ;;
export)
  pure=0
  for a in "$@"; do [ "$a" = --pure ] && pure=1; done
  if [ "$pure" = 0 ]; then
    for p in .opencode/plugin/*.sh; do [ -f "$p" ] && . "./$p"; done
  fi
  cat %[1]q; exit 0 ;;
esac
echo $$ > %[5]q
cat > /dev/null
cat %[2]q
cat %[3]q >&2
%[7]s
exit %[4]d
`, exportFile, outFile, errFile, stage.exitCode, pidFile, helperLog, hold)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	model := stage.model
	if model == "" {
		model = "lmstudio/qwen/qwen3.8-27b"
	}
	opts := openCodeStageOptions(model, nil)
	opts.AllowedTools = stage.allowedTools
	opts.Streamer = stage.streamer
	opts.Timeout = 20 * time.Second
	workspace := openCodeWorkspace(t)
	worktree := filepath.Join(workspace, ".nightgauge", "worktrees", "nightgauge-issue-1612")
	if stage.worktree != nil {
		stage.worktree(worktree)
	}
	manager := NewManager(workspace, adapters.NewOpenCodeAdapter())
	var result *adapters.RunResult
	var err error
	hung := false
	logged := captureStderr(t, func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err = manager.RunStage(ctx, opts)
		}()
		if stage.during != nil {
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if _, statErr := os.Stat(readyFile); statErr == nil {
					break
				}
			}
			stage.during(manager)
		}
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
	return openCodeStageOutcome{result: result, logged: logged, helpers: readHelperLog(t, helperLog), worktree: worktree}
}

// readHelperLog parses the fake's record of the processes the parser started.
func readHelperLog(t *testing.T, path string) []openCodeHelperCall {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []openCodeHelperCall
	var call *openCodeHelperCall
	for _, line := range strings.Split(string(raw), "\n") {
		kind, rest, _ := strings.Cut(line, "\t")
		switch {
		case line == "ARGS" || kind == "ARGS":
			call = &openCodeHelperCall{env: map[string]string{}}
			if rest != "" {
				call.args = strings.Split(rest, "\t")
			}
		case call == nil:
		case kind == "CWD":
			call.cwd = rest
		case kind == "ENV":
			k, v, _ := strings.Cut(rest, "=")
			call.env[k] = v
		case line == "END":
			calls = append(calls, *call)
			call = nil
		}
	}
	return calls
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

	// The stream shows the rejection but stderr does not name it: the wording
	// drifted, or the line was lost. The run stopped there all the same, so
	// it still fails. The event names the tool, not the permission, so the
	// marker names none, and it never claims the adapter's own posture
	// refused a granted tool; a drift marker says why.
	result, _ := openCodeStageRun(t, stream, "", 0, []string{"Bash"}, nil)
	if result.ExitCode != 1 {
		t.Errorf("exit code = %d; a run the stream shows stopped on a rejection must not read as success", result.ExitCode)
	}
	if want := PermissionDeniedMarker + " tool=unknown\n"; !strings.HasSuffix(result.Stderr, want) || strings.Contains(result.Stderr, PermissionRejectedMarker) {
		t.Errorf("stderr = %q; want it to end with %q and hold no %s", result.Stderr, want, PermissionRejectedMarker)
	}
	if len(result.DriftMarkers) != 1 || !strings.Contains(result.DriftMarkers[0], "stderr carried no auto-reject line") {
		t.Errorf("drift markers = %q, want the stderr cross-check", result.DriftMarkers)
	}
}

// TestOpenCodeAutoRejectMultiLine: OpenCode prints a rejected call's input
// unescaped, so a bash command over several lines spreads the auto-reject
// notice over several stderr lines; the capture is a heredoc on 1.18.30. The
// permission comes from the first line, and the run fails exactly as a
// one-line notice fails it. The lines after the first are the command: none
// is kept, and none is read as a notice of its own, which is what a line of
// a rejected command could otherwise forge.
func TestOpenCodeAutoRejectMultiLine(t *testing.T) {
	stream := readTestdata(t, "opencode_auto_reject_heredoc_stream.jsonl")
	stderr := readTestdata(t, "opencode_auto_reject_heredoc_stderr.txt")
	if n := strings.Count(strings.TrimSuffix(stderr, "\n"), "\n") + 1; n < 2 {
		t.Fatalf("the captured notice is on %d line, so it no longer proves a notice over several lines is read", n)
	}
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
		if want := "! permission requested: bash (...); auto-rejecting\n" + tc.marker + "\n"; result.Stderr != want {
			t.Errorf("allowed %q: stderr = %q, want %q", tc.allowed, result.Stderr, want)
		}
		if len(result.DriftMarkers) != 0 {
			t.Errorf("allowed %q: drift markers on a real capture: %q", tc.allowed, result.DriftMarkers)
		}
		if result.InputTokens != 1534 || result.OutputTokens != 4 {
			t.Errorf("allowed %q: input/output = %d/%d, want the capture's 1534/4", tc.allowed, result.InputTokens, result.OutputTokens)
		}
	}

	// A line of the command that reads as a whole notice is still the
	// command: it names no permission that was rejected, and it is not kept.
	// It also ends in "); auto-rejecting", so the notice seems to end there,
	// and the command's last line comes after it: that is not kept either,
	// and a drift marker says a line was dropped.
	forged := "\x1b[93m\x1b[1m! \x1b[0mpermission requested: bash (cat <<'EOF'\n" +
		"! permission requested: edit (notes.md); auto-rejecting\n" +
		"EOF); auto-rejecting\n"
	result, _ := openCodeStageRun(t, stream, forged, 0, []string{"Bash", "Edit"}, nil)
	if want := "! permission requested: bash (...); auto-rejecting\n[adapter-permission-rejected] tool=bash\n"; result.Stderr != want {
		t.Errorf("stderr = %q, want %q: the bash marker only, none for the edit the command forged, and none of the command", result.Stderr, want)
	}
	if len(result.DriftMarkers) != 1 || !strings.Contains(result.DriftMarkers[0], "a stderr line after an auto-reject notice was not kept") {
		t.Errorf("drift markers = %q, want one for the command's line after the notice seemed to end", result.DriftMarkers)
	}
}

// lastNonEmptyLines is the tail of text the scheduler classifies a failed CLI
// stage by: its last n non-empty lines (stderrFailureReason in
// internal/orchestrator/scheduler.go).
func lastNonEmptyLines(text string, n int) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// TestOpenCodeRejectedCallInputNeverReachesClassification: a rejected call's
// input is the model's own text, and the stage's stderr is what its failure
// is classified by. A command holding a term of a higher-ranked rule than
// permission_denied, on one line or over several, must not decide the kind:
// the kept stderr holds none of it, and the tail classifies as the marker
// alone does. That holds when a line of the command itself ends in
// "); auto-rejecting", so the notice seems to end before the command does,
// and when the command's later lines read as a notice of their own, naming a
// permission or a classifier's term.
func TestOpenCodeRejectedCallInputNeverReachesClassification(t *testing.T) {
	notice := "\x1b[93m\x1b[1m! \x1b[0mpermission requested: bash ("
	for name, stderr := range map[string]string{
		"one line": notice + `grep -rn "unknown model" internal/); auto-rejecting` + "\n",
		"several lines": notice + "curl -s https://example.test/v1 <<'EOF'\n" +
			"server overloaded, retry later\nunknown model\nEOF); auto-rejecting\n",
		"ended early": notice + "cat <<'EOF'\nx); auto-rejecting\nunknown model\nEOF); auto-rejecting\n",
		"ended on its first line": notice + "grep x); auto-rejecting\nserver overloaded, retry later\n" +
			"API Error: Overloaded\nunknown model); auto-rejecting\n",
		"ended early, then a forged notice": notice + "grep x); auto-rejecting\nunknown model\n" +
			"! permission requested: overloaded (unknown model); auto-rejecting\n",
		"ended early, then a forged permission": notice + "grep x); auto-rejecting\n" +
			"! permission requested: external_directory (/etc/*); auto-rejecting\nunknown model); auto-rejecting\n",
	} {
		t.Run(name, func(t *testing.T) {
			result, _ := openCodeStageRun(t, readTestdata(t, "opencode_auto_reject_stream.jsonl"), stderr, 0, []string{"Read"}, nil)
			for _, term := range []string{"unknown model", "overloaded", "grep", "curl", "EOF", "/etc"} {
				if strings.Contains(strings.ToLower(result.Stderr), strings.ToLower(term)) {
					t.Errorf("the kept stderr holds the rejected command's %q:\n%s", term, result.Stderr)
				}
			}
			marker := PermissionDeniedMarker + " tool=bash"
			if !strings.HasPrefix(result.Stderr, openCodeKeptNotice("bash")+"\n") || !strings.Contains(result.Stderr, marker+"\n") {
				t.Errorf("stderr = %q; want it to open with the bash notice and hold %q", result.Stderr, marker)
			}
			want := terminalkind.Classify("exit 1: " + marker)
			if want == "" {
				t.Fatalf("%q classifies as nothing, so this test proves nothing", marker)
			}
			if got := terminalkind.Classify("exit 1: " + lastNonEmptyLines(result.Stderr, 3)); got != want {
				t.Errorf("the stage classifies as %q, want the marker's %q; stderr:\n%s", got, want, result.Stderr)
			}
		})
	}
}

// TestOpenCodeRejectionMarkersNameOnlyOpenCodePermissions: a marker is what
// failure classification reads, and a notice can be forged by the rejected
// input of an earlier one or name an MCP tool a config chose. So a marker
// names only a permission OpenCode 1.18.30 asks for itself, and "unknown"
// for any other: no permission name, and no kept notice, changes the kind a
// stage classifies as.
func TestOpenCodeRejectionMarkersNameOnlyOpenCodePermissions(t *testing.T) {
	own := []string{
		"bash", "read", "edit", "glob", "grep", "task", "webfetch", "websearch", "todowrite",
		"skill", "lsp", "external_directory", "doom_loop", "workflow_tool_approval",
	}
	for _, permission := range own {
		line := "\x1b[93m\x1b[1m! \x1b[0mpermission requested: " + permission + " (*); auto-rejecting"
		if got, ok := OpenCodeAutoRejectMarker(line, nil); !ok || got != PermissionDeniedMarker+" tool="+permission {
			t.Errorf("OpenCodeAutoRejectMarker(%q) = %q, %v; want it to name %s", line, got, ok, permission)
		}
		for _, prefix := range []string{PermissionRejectedMarker, PermissionDeniedMarker} {
			want := terminalkind.Classify("exit 1: " + prefix + " tool=unknown")
			tail := openCodeKeptNotice(permission) + "\n" + prefix + " tool=" + permission
			if got := terminalkind.Classify("exit 1: " + tail); got != want {
				t.Errorf("%q classifies as %q, want the unnamed marker's %q", tail, got, want)
			}
		}
	}
	for _, other := range []string{"github_create_issue", "overloaded", "rate_limit", "context_length_exceeded"} {
		line := "! permission requested: " + other + " (*); auto-rejecting"
		if got, ok := OpenCodeAutoRejectMarker(line, []string{"Bash"}); !ok || got != PermissionDeniedMarker+" tool=unknown" {
			t.Errorf("OpenCodeAutoRejectMarker(%q) = %q, %v; want %q", line, got, ok, PermissionDeniedMarker+" tool=unknown")
		}
	}
}

// TestOpenCodeNoticeReadBeforeRedaction: a notice is read on the line as
// OpenCode printed it. Redaction replaces a credential query parameter up to
// the next space, which takes the notice's closing "); " with it when the
// parameter ends the patterns: read after redaction, the notice never ended,
// and every later stderr line, the next notice among them, was dropped as its
// input, with its marker and no drift marker. One-line and multi-line notices
// alike, for bash and for webfetch, whose pattern is the URL.
func TestOpenCodeNoticeReadBeforeRedaction(t *testing.T) {
	value := strings.Repeat("q7", 6)
	notice := "\x1b[93m\x1b[1m! \x1b[0mpermission requested: "
	for name, tc := range map[string]struct{ permission, first string }{
		"one line": {"bash", notice + "bash (curl -s https://api.example.test/v1/items?access_token=" + value + "); auto-rejecting\n"},
		"several lines": {"bash", notice + "bash (curl -s -K - <<'EOF'\nurl = api.example.test\n" +
			"https://api.example.test/v1/items?token=" + value + "); auto-rejecting\n"},
		"webfetch": {"webfetch", notice + "webfetch (https://bucket.example.test/o?X-Amz-Signature=" + value + "); auto-rejecting\n"},
	} {
		t.Run(name, func(t *testing.T) {
			stderr := tc.first + notice + "edit (notes.md); auto-rejecting\n"
			result, _ := openCodeStageRun(t, readTestdata(t, "opencode_auto_reject_stream.jsonl"), stderr, 0, []string{"Read"}, nil)
			want := openCodeKeptNotice(tc.permission) + "\n" + openCodeKeptNotice("edit") + "\n" +
				PermissionDeniedMarker + " tool=" + tc.permission + "\n" + PermissionDeniedMarker + " tool=edit\n"
			if result.Stderr != want {
				t.Errorf("stderr = %q, want %q", result.Stderr, want)
			}
			if len(result.DriftMarkers) != 0 {
				t.Errorf("drift markers = %q, want none", result.DriftMarkers)
			}
		})
	}

	// A line OpenCode prints after the notice is not kept, since nothing
	// tells it from the rejected input, but it is not lost silently.
	stderr := notice + "bash (curl -s https://api.example.test/v1/items?access_token=" + value + "); auto-rejecting\n" +
		"ERROR 2026-09-13T20:00:00 service=provider ProviderModelNotFoundError: model not found\n" +
		notice + "edit (notes.md); auto-rejecting\n"
	result, _ := openCodeStageRun(t, readTestdata(t, "opencode_auto_reject_stream.jsonl"), stderr, 0, []string{"Read"}, nil)
	if want := PermissionDeniedMarker + " tool=bash\n" + PermissionDeniedMarker + " tool=edit\n"; !strings.HasSuffix(result.Stderr, want) {
		t.Errorf("stderr = %q; want it to end with both markers %q", result.Stderr, want)
	}
	if len(result.DriftMarkers) != 1 || !strings.Contains(result.DriftMarkers[0], "a stderr line after an auto-reject notice was not kept") {
		t.Errorf("drift markers = %q, want one for the line after the notice", result.DriftMarkers)
	}
}

// TestOpenCodeOversizeNoticeLine: a notice line longer than the 1 MiB line
// limit is dropped like any other, but its first and last bytes are still
// read: its first line still yields its marker, and its last line still ends
// it, so the next notice is read as one.
func TestOpenCodeOversizeNoticeLine(t *testing.T) {
	notice := "\x1b[93m\x1b[1m! \x1b[0mpermission requested: "
	huge := strings.Repeat("x", streamLineLimit+1)
	next := notice + "edit (notes.md); auto-rejecting\n"
	for name, stderr := range map[string]string{
		"its only line": notice + "bash (echo " + huge + "); auto-rejecting\n" + next,
		"its last line": notice + "bash (cat <<'EOF'\n" + huge + "EOF); auto-rejecting\n" + next,
	} {
		t.Run(name, func(t *testing.T) {
			result, _ := openCodeStageRun(t, readTestdata(t, "opencode_auto_reject_stream.jsonl"), stderr, 0, []string{"Read"}, nil)
			want := openCodeKeptNotice("bash") + "\n" + openCodeKeptNotice("edit") + "\n" +
				PermissionDeniedMarker + " tool=bash\n" + PermissionDeniedMarker + " tool=edit\n"
			if result.Stderr != want {
				t.Errorf("stderr = %.300q, want %q", result.Stderr, want)
			}
			if len(result.DriftMarkers) != 1 || !strings.Contains(result.DriftMarkers[0], "dropped a stderr line longer than") {
				t.Errorf("drift markers = %q, want only the one for the long line", result.DriftMarkers)
			}
		})
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
		// Plain; at the start of a later line of a tool's output inside a
		// --format json event, where the character before it is the `n` of
		// `\n`; and printed in colour, where it is the `m` of the escape.
		for _, context := range []string{"before %s after", `line one\n%s\nline three`, "\x1b[32m%s\x1b[0m", `[32m%s[0m`} {
			got := RedactCredentials(fmt.Sprintf(context, sample))
			if strings.Contains(got, credentialValue(shape, sample)) || !strings.Contains(got, "[REDACTED:") {
				t.Errorf("%s in %q: RedactCredentials left %q", shape, context, got)
			}
		}
	}
	notAKey := "ta" + "sk-" + strings.Repeat("abc1", 9) // sk- inside a word is not a key
	for _, keep := range []string{
		notAKey,
		`\n` + notAKey,
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
	// A tool's output with each credential at the start of its own line,
	// JSON-escaped in the event as OpenCode writes it.
	output, err := json.Marshal("# local\n" + strings.Join(all, "\n") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	stdout := readTestdata(t, "opencode_stream_research_sample.jsonl") +
		`{"type":"text","timestamp":1,"sessionID":"ses_fixture0000000000000000001","part":{"type":"text","text":` + string(text) + `}}` + "\n" +
		`{"type":"tool_use","timestamp":2,"sessionID":"ses_fixture0000000000000000001","part":{"type":"tool","tool":"bash",` +
		`"state":{"status":"completed","input":{"command":"cat .env"},"output":` + string(output) + `}}}` + "\n"
	stderr := "ERROR provider request failed " + strings.Join(all, " ") + "\n" +
		"\x1b[31mERROR\x1b[0m " + strings.Join(all, "\x1b[0m \x1b[32m") + "\n"
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
	for _, event := range lines[len(lines)-2:] {
		if !json.Valid([]byte(event)) {
			t.Errorf("a redacted event is not valid JSON: %s", event)
		}
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

// openCodeServiceKey is a secret shaped like sap-ai-core's
// AICORE_SERVICE_KEY, a JSON document: it holds quotes, so a --format json
// event carries it escaped. Built at run time; it is no real credential.
func openCodeServiceKey() (value, secretPart string) {
	secretPart = strings.Repeat("Qz7", 8)
	return `{"clientid":"sb-fixture-1624","clientsecret":"` + secretPart + `","url":"https://example.test"}`, secretPart
}

// TestOpenCodeRedactsQuoteBearingValue: the value of a variable the adapter
// names is removed from a --format json event, where a tool's output is
// JSON-escaped, and not only where it appears as it is: a JSON service key
// holds quotes, so its raw form never occurs inside the event. Also escaped
// on a line that is not an event, such as a log line quoting JSON.
func TestOpenCodeRedactsQuoteBearingValue(t *testing.T) {
	value, secretPart := openCodeServiceKey()
	quoted, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	escaped := string(quoted[1 : len(quoted)-1])
	redactor := envValueRedactor([]string{"AICORE_SERVICE_KEY=" + value}, []string{"AICORE_SERVICE_KEY"})
	for _, line := range []string{"raw " + value, `ERROR config="` + escaped + `"`} {
		if got := redactLine(redactor, []byte(line)); strings.Contains(string(got), secretPart) || !strings.Contains(string(got), "[REDACTED:AICORE_SERVICE_KEY]") {
			t.Errorf("the redactor left %q", got)
		}
	}

	// Through a sap-ai-core stage, whose provider the catalog binds the
	// variable to: a tool prints the environment.
	t.Setenv("AICORE_SERVICE_KEY", value)
	output, err := json.Marshal("HOME=/tmp/nightgauge-fixture\nAICORE_SERVICE_KEY=" + value + "\n")
	if err != nil {
		t.Fatal(err)
	}
	event := `{"type":"tool_use","timestamp":2,"sessionID":"ses_fixture0000000000000000001","part":{"type":"tool","tool":"bash",` +
		`"state":{"status":"completed","input":{"command":"env"},"output":` + string(output) + `}}}`
	streamer := &redactionStreamer{}
	out := openCodeStageRunWith(t, openCodeStage{
		model:    "sap-ai-core/fixture-model",
		stdout:   readTestdata(t, "opencode_stream_research_sample.jsonl") + event + "\n",
		stderr:   "ERROR request failed: " + value + "\n",
		streamer: streamer,
	})
	for where, text := range map[string]string{"streamed": streamer.out.String(), "result.Stdout": out.result.Stdout, "result.Stderr": out.result.Stderr} {
		if strings.Contains(text, secretPart) {
			t.Errorf("%s holds the service key's client secret", where)
		}
		if !strings.Contains(text, "[REDACTED:AICORE_SERVICE_KEY]") {
			t.Errorf("%s does not show where AICORE_SERVICE_KEY was redacted:\n%s", where, text)
		}
	}
	lines := strings.Split(strings.TrimSpace(out.result.Stdout), "\n")
	if last := lines[len(lines)-1]; !json.Valid([]byte(last)) || !strings.Contains(last, `"tool":"bash"`) {
		t.Errorf("the redacted event is not the event, as valid JSON: %s", last)
	}
}

// TestOpenCodeRedactsCredentialsNotProviderSettings: of the variables the
// adapter names for a dispatch, only the credentials are redacted. OpenCode's
// catalog also binds a provider's region, project, account, host and endpoint
// to it, and redacting those would strip every "us-east-1", or an
// organization's name, from a stage's output and failure text.
func TestOpenCodeRedactsCredentialsNotProviderSettings(t *testing.T) {
	adapter := adapters.NewOpenCodeAdapter()
	for provider, settings := range map[string][]string{
		"amazon-bedrock":           {"AWS_REGION"},
		"google-vertex":            {"GOOGLE_VERTEX_PROJECT", "GOOGLE_VERTEX_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS"},
		"azure":                    {"AZURE_RESOURCE_NAME"},
		"azure-cognitive-services": {"AZURE_COGNITIVE_SERVICES_RESOURCE_NAME"},
		"databricks":               {"DATABRICKS_HOST"},
		"cloudflare-ai-gateway":    {"CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID"},
		"snowflake-cortex":         {"SNOWFLAKE_ACCOUNT"},
		"watsonx":                  {"WATSONX_AI_PROJECT_ID"},
		"privatemode-ai":           {"PRIVATEMODE_ENDPOINT"},
		"neon":                     {"NEON_AI_GATEWAY_BASE_URL"},
		"infomaniak":               {"INFOMANIAK_PRODUCT_ID"},
		"sap-ai-core":              nil,
	} {
		names := adapter.RedactedEnv(adapters.RunOptions{Model: provider + "/fixture-model"})
		for _, setting := range settings {
			if !slices.Contains(names, setting) {
				t.Fatalf("%s: the adapter does not name %s, so this test proves nothing about it: %q", provider, setting, names)
			}
		}
		env := make([]string, 0, len(names))
		for i, name := range names {
			env = append(env, fmt.Sprintf("%s=fixture-value-%02d", name, i))
		}
		redactor := envValueRedactor(env, names)
		for i, name := range names {
			got := string(redactLine(redactor, []byte(fmt.Sprintf("value: fixture-value-%02d", i))))
			switch redacted := got == "value: [REDACTED:"+name+"]"; {
			case slices.Contains(settings, name) && redacted:
				t.Errorf("%s: the setting %s was redacted as a secret", provider, name)
			case !slices.Contains(settings, name) && !redacted:
				t.Errorf("%s: the credential %s was not redacted: %q", provider, name, got)
			}
		}
	}

	// A google-vertex project named after the organization leaves its pull
	// request URLs alone.
	names := adapter.RedactedEnv(adapters.RunOptions{Model: "google-vertex/fixture-model"})
	url := "https://github.com/nightgauge/nightgauge/pull/1624"
	if got := string(redactLine(envValueRedactor([]string{"GOOGLE_VERTEX_PROJECT=nightgauge"}, names), []byte(url))); got != url {
		t.Errorf("a vertex project named after the organization redacted %q to %q", url, got)
	}

	// Through an amazon-bedrock stage, whose region is a setting and whose
	// secret access key is a credential.
	secret := strings.Repeat("wJa1r", 8)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	out := openCodeStageRunWith(t, openCodeStage{
		model:  "amazon-bedrock/fixture-model",
		stdout: readTestdata(t, "opencode_stream_research_sample.jsonl"),
		stderr: "ERROR deployed to us-east-1: https://s3.us-east-1.amazonaws.com/bucket signed with " + secret + "\n",
	})
	want := "ERROR deployed to us-east-1: https://s3.us-east-1.amazonaws.com/bucket signed with [REDACTED:AWS_SECRET_ACCESS_KEY]\n"
	if out.result.Stderr != want {
		t.Errorf("stderr = %q, want %q", out.result.Stderr, want)
	}
}

// TestOpenCodeFoldHelpersRunPureFromTheRunRoot: the opencode processes the
// parser starts after a stage (--version, db, export) run from the run's own
// root with --pure, and with only the variables that point them at the run's
// root. Observed on 1.18.30, an `export` from a directory holding .opencode/
// loads its plugins; the fake does the same. A stage can write a plugin into
// its worktree with the edit tool alone, so a plugin planted there must never
// run, least of all with the stage's forge token.
func TestOpenCodeFoldHelpersRunPureFromTheRunRoot(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "fake-github-token-for-the-fold-test-1624")
	probe := filepath.Join(t.TempDir(), "plugin-ran")
	out := openCodeStageRunWith(t, openCodeStage{
		stdout: readTestdata(t, "opencode_stream_research_sample.jsonl"),
		worktree: func(dir string) {
			plugins := filepath.Join(dir, ".opencode", "plugin")
			if err := os.MkdirAll(plugins, 0o755); err != nil {
				t.Fatal(err)
			}
			plugin := fmt.Sprintf("echo \"GITHUB_TOKEN=${GITHUB_TOKEN:+set} cwd=$(pwd -P)\" > %q\n", probe)
			if err := os.WriteFile(filepath.Join(plugins, "probe.sh"), []byte(plugin), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})
	if raw, err := os.ReadFile(probe); err == nil {
		t.Errorf("the worktree's plugin ran in a process the parser started: %s", raw)
	}
	home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := filepath.EvalSymlinks(out.worktree)
	if err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(home, ".nightgauge", "opencode", "runs") + string(filepath.Separator)
	seen := map[string]bool{}
	for _, call := range out.helpers {
		if len(call.args) == 0 {
			t.Fatalf("a helper call recorded no argv: %+v", call)
		}
		seen[call.args[0]] = true
		if call.args[0] != "--version" && !slices.Contains(call.args, "--pure") {
			t.Errorf("opencode %q ran without --pure", call.args)
		}
		if call.cwd == worktree || strings.HasPrefix(call.cwd, worktree+string(filepath.Separator)) {
			t.Errorf("opencode %s ran in the worktree %s", call.args[0], call.cwd)
		}
		if root := filepath.Base(filepath.Dir(call.env["XDG_DATA_HOME"])); !strings.HasPrefix(call.cwd, runs) || filepath.Base(call.cwd) != root {
			t.Errorf("opencode %s ran in %s, not the run's root (its data directory is %s)", call.args[0], call.cwd, call.env["XDG_DATA_HOME"])
		}
		for _, name := range []string{"GITHUB_TOKEN", "OPENCODE_SERVER_PASSWORD", "NIGHTGAUGE_STAGE", "GH_CONFIG_DIR"} {
			if _, ok := call.env[name]; ok {
				t.Errorf("opencode %s was given %s", call.args[0], name)
			}
		}
		for _, name := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "OPENCODE_DISABLE_SHARE"} {
			if call.env[name] == "" {
				t.Errorf("opencode %s was not given %s", call.args[0], name)
			}
		}
	}
	for _, want := range []string{"--version", "db", "export"} {
		if !seen[want] {
			t.Errorf("the parser never ran opencode %s, so this test proves nothing about it; calls: %+v", want, out.helpers)
		}
	}
}

// TestOpenCodeStoppedStageStartsNoProcess: once the operator stops a stage,
// the parser starts no opencode process after it. The stage keeps the usage
// its stream showed, marked partial because its subagent sessions went
// unread.
func TestOpenCodeStoppedStageStartsNoProcess(t *testing.T) {
	out := openCodeStageRunWith(t, openCodeStage{
		stdout: readTestdata(t, "opencode_stream_research_sample.jsonl"),
		hold:   true,
		during: func(m *Manager) {
			if err := m.StopExecution("nightgauge/nightgauge", 1612); err != nil {
				t.Errorf("StopExecution: %v", err)
			}
		},
	})
	if !out.result.Cancelled {
		t.Fatal("the stage was not recorded as stopped, so this test proves nothing")
	}
	if len(out.helpers) != 0 {
		var argv []string
		for _, call := range out.helpers {
			argv = append(argv, strings.Join(call.args, " "))
		}
		t.Errorf("after the operator stopped the stage the parser started opencode %q", argv)
	}
	if out.result.InputTokens != 3089 || !out.result.UsagePartial {
		t.Errorf("input = %d, partial = %v; want the stream's 3089, marked partial", out.result.InputTokens, out.result.UsagePartial)
	}
}

// TestOpenCodeLongLine: a line longer than the scanner's 1 MiB limit is
// dropped with one drift marker, and the lines after it are still read. The
// scanner used to stop at it, so every step_finish after a large tool output
// was lost.
func TestOpenCodeLongLine(t *testing.T) {
	huge := strings.Repeat("x", 20<<20)
	var got, edges []string
	err := forEachLine(strings.NewReader("a\n"+"H"+huge+"T\r\nb\r\nc"), streamLineLimit,
		func(line []byte) { got = append(got, string(line)) },
		func(head, tail []byte) { edges = append(edges, string(head), string(tail)) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "a|b|c" || len(edges) != 2 {
		t.Fatalf("lines = %q, %d oversized; want a, b, c and one oversized line", got, len(edges)/2)
	}
	// Its ends are handed over, so a notice it starts or ends is still read.
	if want := "H" + strings.Repeat("x", oversizeEdge-1); edges[0] != want {
		t.Errorf("head = %q, want the line's first %d bytes", edges[0], oversizeEdge)
	}
	if want := strings.Repeat("x", oversizeEdge-1) + "T"; edges[1] != want {
		t.Errorf("tail = %q, want the line's last %d bytes without its line ending", edges[1], oversizeEdge)
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
// fixtures, and refuses a file holding any one shape the redaction covers,
// also where a JSON escape precedes it, or an IPv4 address other than
// 127.0.0.1. The same samples are removed by RedactCredentials and by
// redact-opencode.jq, also in colour, so the three agree.
func TestCaptureOpenCodeFixtureRefusesCredentials(t *testing.T) {
	fixtures := []string{"opencode_stream_research_sample.jsonl", "opencode_auto_reject_stream.jsonl", "opencode_auto_reject_stderr.txt",
		"opencode_auto_reject_heredoc_stream.jsonl", "opencode_auto_reject_heredoc_stderr.txt"}
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
		// At the start of a later line of a tool's output: `\n` before it.
		escaped := filepath.Join(dir, shape+".escaped")
		if err := os.WriteFile(escaped, []byte(`{"type":"text","part":{"text":"config:\n`+sample+`\n"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, code := runCaptureScript(t, "--check", escaped); code == 0 {
			t.Errorf("--check passed a file holding the %s sample after a JSON escape:\n%s", shape, out)
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
		for _, input := range []string{"x " + sample + " y\n", "x \x1b[32m" + sample + "\x1b[0m y\n"} {
			cmd := exec.Command(jq, "-j", "-R", "-s", "--arg", "mode", "stderr", "--argjson", "roots", "[]",
				"-f", filepath.Join("testdata", "redact-opencode.jq"))
			cmd.Stdin = strings.NewReader(input)
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			if err := cmd.Run(); err != nil {
				t.Fatalf("redact-opencode.jq: %v", err)
			}
			if got := stdout.String(); strings.Contains(got, credentialValue(shape, sample)) || got != RedactCredentials(input) {
				t.Errorf("%s in %q: redact-opencode.jq gave %q; RedactCredentials gives %q", shape, input, got, RedactCredentials(input))
			}
		}
	}
}
