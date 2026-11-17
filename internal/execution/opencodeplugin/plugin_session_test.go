package opencodeplugin

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/skills"
)

// --- static scan: session.js must never itself continue the model turn ---

// TestSessionJSNeverPromptsTheModel is the AC's own static-scan bullet: this
// module observes and records session lifecycle events, but has no way to
// send a message into a session, and must never grow one. A regex scan of
// the embedded source for "session.prompt" or "prompt_async" — the two
// spellings opencode's own client uses to start a new model turn — fails if
// either appears anywhere in session.js. Adding either call (e.g. to "helpfully"
// nudge the model after an idle event) turns this red.
func TestSessionJSNeverPromptsTheModel(t *testing.T) {
	sub, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(sub, "nightgauge/session.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, marker := range []string{"session.prompt", "prompt_async"} {
		if strings.Contains(src, marker) {
			t.Errorf("session.js contains %q: this module must never start a new model turn", marker)
		}
	}
}

// --- Node harness: imports session.js directly (not through nightgauge.js's
// gates.js-gated execute.before) and drives one or more of its exports in
// sequence, in the same Node process, so a rate-limit test can observe state
// (lastNotifyAt, eventsTruncated) surviving across calls exactly as it does
// inside one real opencode process. Every export takes (ctx, arg), arg being
// the 2.x hook argument or bus event; the harness reports arg after the call,
// since a hook's effect is a mutation of its argument. ---

const sessionHarnessDriver = `
import { pathToFileURL } from "node:url";

const mod = await import(pathToFileURL(process.env.NG_SESSION_PATH).href);
const ctx = { location: { directory: process.env.NG_CWD } };
const calls = JSON.parse(process.env.NG_CALLS);

const results = [];
for (const call of calls) {
  const fn = mod[call.fn];
  let threw = false;
  let message = "";
  try {
    await fn(ctx, call.input);
  } catch (e) {
    threw = true;
    message = String(e && e.message ? e.message : e);
  }
  results.push({ threw, message, output: call.input });
}
process.stdout.write(JSON.stringify(results));
`

type sessionCall struct {
	Fn    string `json:"fn"`
	Input any    `json:"input"`
}

// busEvent is a 2.x bus event as ctx.event.subscribe yields it on 2.2.7:
// {id, created, type, location, data}.
func busEvent(eventType string, data map[string]any) map[string]any {
	return map[string]any{
		"id":       "evt_test",
		"created":  1,
		"type":     eventType,
		"location": map[string]any{"directory": "/w"},
		"data":     data,
	}
}

type sessionCallResult struct {
	Threw   bool            `json:"threw"`
	Message string          `json:"message"`
	Output  json.RawMessage `json:"output"`
}

// sessionModulePath writes the shipped plugin tree into a fresh temp dir
// and returns nightgauge/session.js's absolute path within it.
func sessionModulePath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(entry, "nightgauge", "session.js")
}

// runSessionHarness drives calls against session.js directly in one Node
// process, with env applied on top of the current process's own (so
// NIGHTGAUGE_BIN etc. can be set per call site via extraEnv).
func runSessionHarness(t *testing.T, node, cwd string, calls []sessionCall, extraEnv map[string]string) []sessionCallResult {
	t.Helper()
	sessionPath := sessionModulePath(t)
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(sessionHarnessDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	callsJSON, err := json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver)
	env := append(os.Environ(),
		"NG_SESSION_PATH="+sessionPath,
		"NG_CWD="+cwd,
		"NG_CALLS="+string(callsJSON),
	)
	env = removeEnv(env, "NIGHTGAUGE_BIN")
	env = removeEnv(env, "NIGHTGAUGE_OUTPUT_FILE")
	env = removeEnv(env, "NIGHTGAUGE_RUN_ID")
	for k, v := range extraEnv {
		env = upsertEnv(env, k, v)
	}
	// An extraEnv map may set NIGHTGAUGE_BIN under a dynamic key the guard
	// cannot see, so the harness pairs it with the opt-out itself (#3250).
	env = upsertEnv(env, notifyDisableEnv, "1")
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("session node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("session node harness failed: %v", err)
	}
	var results []sessionCallResult
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("session node harness printed non-JSON: %s (%v)", out, err)
	}
	return results
}

// --- session.hook("compaction") ---

// compactionSystemTexts returns the text of every part of a compaction
// argument's system array, as the harness reported it after the call.
func compactionSystemTexts(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var arg struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(raw, &arg); err != nil {
		t.Fatalf("arg did not parse: %v (%s)", err, raw)
	}
	var out []string
	for _, p := range arg.System {
		if p.Type == "text" {
			out = append(out, p.Text)
		}
	}
	return out
}

// compactionArg is the shape 2.2.7 hands ctx.session.hook("compaction"),
// trimmed to what session.js reads.
func compactionArg() map[string]any {
	return map[string]any{
		"sessionID": "ses_1",
		"agent":     "build",
		"system":    []any{map[string]any{"type": "text", "text": "You are an AI agent running in OpenCode."}},
		"messages":  []any{},
	}
}

// TestSessionCompactionInjectsContext: invoking the compaction hook in a
// temp git repo on branch feat/42-x appends one system text part whose JSON
// names "branch":"feat/42-x", driven against the real built nightgauge
// binary's `hook inject-context` verb. The existing system part is kept.
func TestSessionCompactionInjectsContext(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := initGitRepoOnBranch(t, "feat/42-x")

	results := runSessionHarness(t, node, root, []sessionCall{{Fn: "sessionCompaction", Input: compactionArg()}}, map[string]string{"NIGHTGAUGE_BIN": bin})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Threw {
		t.Fatalf("sessionCompaction threw: %s", results[0].Message)
	}
	texts := compactionSystemTexts(t, results[0].Output)
	if len(texts) != 2 || texts[0] != "You are an AI agent running in OpenCode." {
		t.Fatalf("system texts = %q, want the original part plus one injected part", texts)
	}
	var parsed struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(texts[1]), &parsed); err != nil || parsed.Branch != "feat/42-x" {
		t.Errorf("injected part = %q (err %v), want JSON whose \"branch\" is \"feat/42-x\"", texts[1], err)
	}
}

// TestSessionCompactionSkipsWithoutBin: with no NIGHTGAUGE_BIN, compaction
// context is skipped, never thrown, and the system array is untouched.
func TestSessionCompactionSkipsWithoutBin(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()

	results := runSessionHarness(t, node, root, []sessionCall{{Fn: "sessionCompaction", Input: compactionArg()}}, nil)
	if results[0].Threw {
		t.Fatalf("want no throw with NIGHTGAUGE_BIN unset, got %s", results[0].Message)
	}
	if texts := compactionSystemTexts(t, results[0].Output); len(texts) != 1 {
		t.Errorf("system texts = %q, want only the original part (no NIGHTGAUGE_BIN to inject from)", texts)
	}
}

// --- event: session.execution.succeeded -> idle, stop_verify ---

// stopVerifyWaitBound is how long a test waits for the detached `hook
// stop-verify --emit-event` child to append its line. A real stop-verify
// answers in ~70 ms warm; 30 s is wide headroom that still fails rather than
// hangs if the child never writes at all.
const stopVerifyWaitBound = 30 * time.Second

// TestEventIdleStopVerifyEventSurvivesItsHostProcess is #1810's own
// regression test, and the one that distinguishes every candidate fix.
//
// Measured against opencode 1.18.30, a one-shot `opencode run` exited within
// ~10 ms of publishing session.idle; on 2.2.7 the event stream closed ~200 ms
// after session.execution.succeeded (#3302). Either way the process can be
// gone before anything the plugin scheduled runs. This driver reproduces
// that: it calls event(session.execution.succeeded) and then kills its own
// process with process.exit(0) on the very next tick, allowing NOTHING the
// plugin scheduled to run afterwards.
//
// Against #1641's fix-forward shape — the verdict recorded from a .then()
// continuation on an in-process spawn — no stop_verify line is ever written
// here, which is precisely the "got 0 stop_verify events, want exactly 1"
// that TestCompactionAutocontinueSuppressionAgainstRealOpenCode reported on
// main. Awaiting runHook inline inside the event hook is red here too, and
// for the same reason: the await simply never resumes. Only a verdict
// written by a process that OUTLIVES the host passes.
func TestEventIdleStopVerifyEventSurvivesItsHostProcess(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := t.TempDir() // no PLAN.md: EvaluateStop's own OK path

	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-idle-survives-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken: EventsPath refused a valid absolute output file")
	}

	driver := `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.env.NG_SESSION_PATH).href);
const ctx = { location: { directory: process.env.NG_CWD } };

const t0 = Date.now();
await mod.event(ctx, { type: "session.execution.succeeded", data: { sessionID: "ses_survives_1" } });
process.stdout.write(JSON.stringify({ idleMs: Date.now() - t0 }));
// A one-shot run shuts down right after its last session ends (1.18.30
// exited ~10ms after session.idle; 2.2.7 closed the event stream ~200ms after
// session.execution.succeeded): tear the process down, running no queued
// continuation, no timer and no pending await.
process.exit(0);
`
	driverPath := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driverPath, []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driverPath)
	env := append(os.Environ(),
		"NG_SESSION_PATH="+sessionModulePath(t),
		"NG_CWD="+root,
	)
	for k, v := range map[string]string{
		"NIGHTGAUGE_BIN":         bin,
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	} {
		env = upsertEnv(env, k, v)
	}
	cmd.Env = env
	cmd.Dir = root
	started := time.Now()
	out, err := cmd.Output()
	hostElapsed := time.Since(started)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("driver failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("driver failed: %v", err)
	}
	var timing struct {
		IdleMs int64 `json:"idleMs"`
	}
	if err := json.Unmarshal(out, &timing); err != nil {
		t.Fatalf("driver printed non-JSON: %s (%v)", out, err)
	}
	// Invariant (i): the hook itself never waits on the verb. The spawn is
	// started and let go of, so this is a handful of milliseconds even though
	// the verb it starts has real work to do.
	if timing.IdleMs >= 1000 {
		t.Errorf("event(session.execution.succeeded) took %dms; it must never wait on the stop-verify verb at all", timing.IdleMs)
	}

	// Invariant (ii): the verdict is not lost, even though the process that
	// requested it is already gone.
	events, err := WaitForRunEvent(eventsFile, "stop_verify", stopVerifyWaitBound)
	if err != nil {
		t.Fatal(err)
	}
	var stopVerify []Event
	for _, e := range events {
		if e.Kind == "stop_verify" {
			stopVerify = append(stopVerify, e)
		}
	}
	if len(stopVerify) != 1 {
		t.Fatalf("got %d stop_verify events after the host process exited in %s, want exactly 1: %+v",
			len(stopVerify), hostElapsed, events)
	}
	if got := stopVerify[0].SessionID; got != "ses_survives_1" {
		t.Errorf("stop_verify.session_id = %q, want ses_survives_1", got)
	}
	if verdict, _ := stopVerify[0].Detail["verdict"].(string); verdict != "complete" {
		t.Errorf("stop_verify detail.verdict = %v, want \"complete\"", stopVerify[0].Detail["verdict"])
	}
}

// TestEventIdleRecordsNoBinWhenTheVerbCannotRun: the one stop_verify verdict
// the plugin still owns. With NIGHTGAUGE_BIN pointing at an absolute path
// that is not executable, no child can be started to write the line, so the
// plugin writes it itself — synchronously, before returning, because on a
// terminal event there is no later tick. Deferring this to spawn's own async
// 'error' event turns it red.
func TestEventIdleRecordsNoBinWhenTheVerbCannotRun(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()

	notExecutable := filepath.Join(t.TempDir(), "not-a-binary")
	writeShFixture(t, notExecutable, "exit 0\n", 0o600)

	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-idle-nobin-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	calls := []sessionCall{{
		Fn:    "event",
		Input: busEvent("session.execution.succeeded", map[string]any{"sessionID": "ses_nobin_1"}),
	}}
	results := runSessionHarness(t, node, root, calls, map[string]string{
		"NIGHTGAUGE_BIN":         notExecutable,
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	})
	if results[0].Threw {
		t.Fatalf("event(session.execution.succeeded) threw: %s", results[0].Message)
	}

	// No wait: this verdict must already be on disk when the hook returns.
	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	stopVerifies := 0
	for _, e := range events {
		if e.Kind != "stop_verify" {
			continue
		}
		stopVerifies++
		if verdict, _ := e.Detail["verdict"].(string); verdict != "no_bin" {
			t.Errorf("detail.verdict = %v, want \"no_bin\"", e.Detail["verdict"])
		}
	}
	if stopVerifies != 1 {
		t.Fatalf("got %d stop_verify events, want exactly 1 written synchronously: %+v", stopVerifies, events)
	}
}

// TestEventIdleWritesStopVerifyEvent: an idle event writes an "idle" line
// followed by a "stop_verify" line to the run's events file, with a verdict
// and no other free text. Removing the event handler's "idle" append (or its
// terminal-event branch entirely) turns this red.
//
// The two lines come from two processes now (#1810): "idle" from the plugin,
// "stop_verify" from the detached `hook stop-verify --emit-event` child,
// which by design outlives its host — so the read is WaitForRunEvent, not a
// bare ReadRunEvents, exactly as events.go tells #1653's reader to do it.
func TestEventIdleWritesStopVerifyEvent(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := t.TempDir() // no PLAN.md: stop-verify's own contract is "OK" (silent stdout)

	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-idle-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken: EventsPath refused a valid absolute output file")
	}

	calls := []sessionCall{{
		Fn:    "event",
		Input: busEvent("session.execution.succeeded", map[string]any{"sessionID": "ses_idle_1"}),
	}}
	results := runSessionHarness(t, node, root, calls, map[string]string{
		"NIGHTGAUGE_BIN":         bin,
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	})
	if results[0].Threw {
		t.Fatalf("event(session.execution.succeeded) threw: %s", results[0].Message)
	}

	events, err := WaitForRunEvent(eventsFile, "stop_verify", stopVerifyWaitBound)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want exactly 2 (idle, stop_verify): %+v", len(events), events)
	}
	idle := events[0]
	if idle.Kind != "idle" {
		t.Errorf("events[0].kind = %q, want idle", idle.Kind)
	}
	if idle.SessionID != "ses_idle_1" {
		t.Errorf("events[0].session_id = %q, want ses_idle_1", idle.SessionID)
	}
	ev := events[1]
	if ev.Kind != "stop_verify" {
		t.Errorf("events[1].kind = %q, want stop_verify", ev.Kind)
	}
	if ev.SessionID != "ses_idle_1" {
		t.Errorf("session_id = %q, want ses_idle_1", ev.SessionID)
	}
	if verdict, _ := ev.Detail["verdict"].(string); verdict != "complete" {
		t.Errorf("detail.verdict = %v, want \"complete\" (no PLAN.md means EvaluateStop's own OK path)", ev.Detail["verdict"])
	}
}

// --- execute.before: skill usage (via the real plugin on the fake 2.x host,
// gates.js AND session.js both wired, exactly as nightgauge.js composes them) ---

var skillToolDriver = hostDriver(`
const arg = toolArg("skill", { name: process.env.NG_SKILL || "" });
process.stdout.write(JSON.stringify(await runToolCall(host, arg)));
`)

func runSkillToolHarness(t *testing.T, node, cwd, skill, nightgaugeBin string) nodeHarnessResult {
	t.Helper()
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(skillToolDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver)
	env := append(os.Environ(),
		"NG_PLUGIN_PATH="+filepath.Join(entry, EntryFile),
		"NG_CWD="+cwd,
		"NG_SKILL="+skill,
	)
	if nightgaugeBin != "" {
		env = withHookBin(env, nightgaugeBin)
	} else {
		env = removeEnv(env, "NIGHTGAUGE_BIN")
	}
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("skill tool harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("skill tool harness failed: %v", err)
	}
	var res nodeHarnessResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("skill tool harness printed non-JSON: %s (%v)", out, err)
	}
	return res
}

// TestSkillToolCallFailsOpen: a skill call with NIGHTGAUGE_BIN pointing at a
// script exiting 1 still returns normally — the tool call is never blocked
// by a failing skill-usage verb.
func TestSkillToolCallFailsOpen(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	exit1 := filepath.Join(t.TempDir(), "exit1.sh")
	writeShFixture(t, exit1, "exit 1\n", 0o755)
	res := runSkillToolHarness(t, node, root, "nightgauge:some-skill", exit1)
	if res.Threw {
		t.Fatalf("want no throw when the skill-usage verb exits 1, got %q", res.Message)
	}
}

// TestSkillToolCallLogsUsage: with the real binary, the skills usage store
// gains one row naming the invoked skill.
func TestSkillToolCallLogsUsage(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := layouttest.Repo(t)

	before, err := skills.ReadUsage(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("test premise broken: usage store already has %d rows", len(before))
	}

	res := runSkillToolHarness(t, node, root, "nightgauge:some-skill", bin)
	if res.Threw {
		t.Fatalf("skill tool call threw: %s", res.Message)
	}

	after, err := skills.ReadUsage(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("got %d usage rows, want 1: %+v", len(after), after)
	}
	if after[0].Skill != "nightgauge:some-skill" {
		t.Errorf("usage row skill = %q, want nightgauge:some-skill", after[0].Skill)
	}
}

// --- event: permission.asked (no ctx.permission.hook name fired on 2.2.7, so
// the bus event is the only path a permission ask reaches the plugin) ---

// TestEventPermissionAskedEmitsEventAndThrottlesNotify: event() with a
// permission.asked bus event (2.2.7's shape: data {id, sessionID, action,
// resources, save, source}) records a permission_ask event carrying only
// permission_type (the action), never the model-authored `resources` text,
// and applies the 60s per-session notify throttle. Removing event()'s
// permission.asked branch turns this red.
func TestEventPermissionAskedEmitsEventAndThrottlesNotify(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	counter := filepath.Join(t.TempDir(), "notify-calls.txt")
	counterScript := filepath.Join(t.TempDir(), "count-notify.sh")
	writeShFixture(t, counterScript, "echo 1 >> "+shellQuote(counter)+"\nexit 0\n", 0o755)
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-perm-asked-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	// 2.2.7's permission.asked data, observed on a rejected `ask`.
	permAsked := func() map[string]any {
		return busEvent("permission.asked", map[string]any{
			"id":        "per_1",
			"sessionID": "ses_perm_asked_1",
			"action":    "shell",
			"resources": []any{"echo the deploy key is SECRET-PLACEHOLDER"},
			"save":      []any{"echo *"},
			"source":    map[string]any{"type": "tool", "messageID": "m1", "id": "c1"},
		})
	}
	calls := []sessionCall{
		{Fn: "event", Input: permAsked()},
		{Fn: "event", Input: permAsked()},
		{Fn: "event", Input: permAsked()},
	}
	results := runSessionHarness(t, node, root, calls, map[string]string{
		"NIGHTGAUGE_BIN":         counterScript,
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	})
	for i, r := range results {
		if r.Threw {
			t.Fatalf("call %d threw: %s", i, r.Message)
		}
	}

	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (one permission_ask per call): %+v", len(events), events)
	}
	for i, ev := range events {
		if ev.Kind != "permission_ask" {
			t.Errorf("events[%d].kind = %q, want permission_ask", i, ev.Kind)
		}
		if ev.SessionID != "ses_perm_asked_1" {
			t.Errorf("events[%d].session_id = %q, want ses_perm_asked_1", i, ev.SessionID)
		}
		if ev.Detail["permission_type"] != "shell" {
			t.Errorf("events[%d].detail.permission_type = %v, want shell", i, ev.Detail["permission_type"])
		}
		if _, hasPatterns := ev.Detail["patterns"]; hasPatterns {
			t.Errorf("events[%d].detail carries patterns (model-authored command text); must never be recorded", i)
		}
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("the notify script never ran even once: %v", err)
	}
	got := 0
	if lines := strings.TrimSpace(string(data)); lines != "" {
		got = len(strings.Split(lines, "\n"))
	}
	if got != 1 {
		t.Errorf("notify spawned %d times for 3 permission.asked events in the same 60s window, want exactly 1", got)
	}
}

// --- child session propagation ---

// TestChildSessionEventsAreTaggedChild: a session.updated event carrying a
// non-empty info.parentID marks that sessionID as a child for every
// subsequent event in this same process; a session with no parentID stays
// child:false. gates.js denies the `task` tool unconditionally today, so no
// real child session reaches this plugin yet (ADR-022) — this drives the
// tracking logic directly, the way a future lift of that denial would
// exercise it. Removing session.js's parentage tracking turns this red.
func TestChildSessionEventsAreTaggedChild(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-child-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	// 2.2.7's session.created data: a subagent's session carries parentID.
	sessionCreated := func(id, parentID string) map[string]any {
		data := map[string]any{"sessionID": id, "agent": "general"}
		if parentID != "" {
			data["parentID"] = parentID
		}
		return busEvent("session.created", data)
	}
	calls := []sessionCall{
		{Fn: "event", Input: sessionCreated("ses_parent", "")},
		{Fn: "event", Input: sessionCreated("ses_child", "ses_parent")},
		{Fn: "event", Input: busEvent("session.compaction.started", map[string]any{"sessionID": "ses_parent", "reason": "auto"})},
		{Fn: "event", Input: busEvent("session.compaction.started", map[string]any{"sessionID": "ses_child", "reason": "auto"})},
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

	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	bySession := map[string]bool{}
	for _, ev := range events {
		bySession[ev.SessionID] = ev.Child
	}
	if bySession["ses_parent"] {
		t.Errorf("ses_parent (no parentID) was tagged child:true")
	}
	if !bySession["ses_child"] {
		t.Errorf("ses_child (parentID=ses_parent) was tagged child:false, want true")
	}
}

// --- non-blocking spawn (#1641 fixed forward) ---

// TestEventIdleAndPermissionHandlersReturnQuicklyWithAHungHook is the direct
// regression test for the #1641 CI slowdown (one `hook stop-verify` call
// blocking a full SPAWN_TIMEOUT_MS=5000ms of opencode's single-threaded
// event loop). NIGHTGAUGE_BIN here points at a shim that sleeps 3s (well
// under SPAWN_TIMEOUT_MS, so it completes on its own rather than being
// killed by the bound) before appending to a marker file and exiting 0.
// Against session.js's own original synchronous spawnSync, both
// event(session.execution.succeeded) (-> `hook stop-verify`) and event(permission.asked)
// (-> `hook notify`) block for the full 3s the shim sleeps, because
// spawnSync freezes the whole process, including the very call that is
// waiting on it — this test is red there. Against the fix, both calls return
// in milliseconds while their children keep running.
//
// The two children have deliberately DIFFERENT lifetimes (#1810), and this
// test asserts both. The permission.asked child is an ordinary runHook
// spawn, fire-and-forget: Node keeps the driver alive until it exits, so its
// marker is written before the driver exits. The terminal-event child is runHookDetached and unref'd — it
// must outlive this process, because on a terminal event nothing here will
// be alive to record its result — so its marker is polled for AFTER the
// driver exits, and a shim that had been killed with its parent would never
// write it.
func TestEventIdleAndPermissionHandlersReturnQuicklyWithAHungHook(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook-ran.marker")
	shim := filepath.Join(t.TempDir(), "slow-hook.sh")
	// $2 is the verb ("stop-verify" or "notify"), so one shim serves both
	// paths and the marker says which child reached the finish line.
	writeShFixture(t, shim, "sleep 3\necho \"$2\" >> "+shellQuote(marker)+"\nexit 0\n", 0o755)

	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}

	driver := `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.env.NG_SESSION_PATH).href);
const ctx = { location: { directory: process.env.NG_CWD } };

async function timed(run) {
  const t0 = Date.now();
  await run();
  return Date.now() - t0;
}

const idleMs = await timed(() =>
  mod.event(ctx, { type: "session.execution.succeeded", data: { sessionID: "ses_slow_1" } })
);
const permMs = await timed(() =>
  mod.event(ctx, {
    type: "permission.asked",
    data: { id: "p1", sessionID: "ses_slow_2", action: "shell" },
  })
);

process.stdout.write(JSON.stringify({ idleMs, permMs }));
`
	sessionPath := sessionModulePath(t)
	driverPath := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driverPath, []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driverPath)
	env := append(os.Environ(),
		"NG_SESSION_PATH="+sessionPath,
		"NG_CWD="+root,
		"NIGHTGAUGE_OUTPUT_FILE="+outputFile,
		"NIGHTGAUGE_RUN_ID=run-nonblock-1",
	)
	env = append(env, hookBinEnv(shim)...)
	cmd.Env = env
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("driver failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("driver failed: %v", err)
	}
	var timing struct {
		IdleMs int64 `json:"idleMs"`
		PermMs int64 `json:"permMs"`
	}
	if err := json.Unmarshal(out, &timing); err != nil {
		t.Fatalf("driver printed non-JSON: %s (%v)", out, err)
	}
	if timing.IdleMs >= 1000 {
		t.Errorf("event(session.execution.succeeded) took %dms against a 3s-hung hook verb; want well under 1000ms — session.js must never block opencode's event loop on a slow spawn", timing.IdleMs)
	}
	if timing.PermMs >= 1000 {
		t.Errorf("event(permission.asked) took %dms against a 3s-hung hook verb; want well under 1000ms", timing.PermMs)
	}

	// The permission.asked child is an ordinary runHook spawn: this process
	// stayed alive for it, so its marker is already on disk.
	if raw, err := os.ReadFile(marker); err != nil || !strings.Contains(string(raw), "notify") {
		t.Errorf("the notify verb's marker line (written after its 3s sleep) is missing: %v / %q — the child was killed early rather than left to finish and reaped", err, raw)
	}
	// The terminal-event child is detached and unref'd: it MUST still be
	// running now, and must finish on its own. A child killed with its parent
	// never writes this line.
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw, _ := os.ReadFile(marker)
		if strings.Contains(string(raw), "stop-verify") {
			break
		}
		if !time.Now().Before(deadline) {
			t.Errorf("the stop-verify verb's marker line never appeared (%q); the detached child must outlive the process that spawned it", raw)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- tool.execute.before: skill argument validation ---

// TestSkillToolRejectsFreeTextAndNestedArgs: a "name" argument that is a
// nested object, or a free-text sentence, never lands verbatim in the events
// file — only a value matching the skill-id pattern does. Both bad shapes
// instead record {skill_invalid:true} and never spawn `hook skill-usage`.
// Reproduces the reviewer's own probe values
// ({"text":"SECRET-TRANSCRIPT-TEXT..."} and "The user wrote: deploy key is
// PLACEHOLDER-SECRET, ..."). Loosening SKILL_ID_RE or reading args.name
// unchecked turns this red.
func TestSkillToolRejectsFreeTextAndNestedArgs(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-skill-invalid-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	driver := `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.env.NG_SESSION_PATH).href);
const ctx = { location: { directory: process.env.NG_CWD } };
const cases = JSON.parse(process.env.NG_ARGS);
const results = [];
for (const args of cases) {
  const arg = { tool: "skill", sessionID: "s", agent: "build", messageID: "m", id: "c", input: args };
  try {
    await mod.toolExecuteBefore(ctx, arg);
    results.push({ threw: false });
  } catch (e) {
    results.push({ threw: true, message: String(e && e.message ? e.message : e) });
  }
}
process.stdout.write(JSON.stringify(results));
`
	sessionPath := sessionModulePath(t)
	driverPath := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driverPath, []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}
	argCases := []map[string]any{
		{"name": map[string]any{"text": "SECRET-TRANSCRIPT-TEXT from the conversation"}},
		{"name": "The user wrote: deploy key is PLACEHOLDER-SECRET, summarize the plan then continue"},
	}
	argsJSON, err := json.Marshal(argCases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driverPath)
	env := append(os.Environ(),
		"NG_SESSION_PATH="+sessionPath,
		"NG_CWD="+root,
		"NG_ARGS="+string(argsJSON),
		"NIGHTGAUGE_OUTPUT_FILE="+outputFile,
		"NIGHTGAUGE_RUN_ID="+runID,
	)
	env = removeEnv(env, "NIGHTGAUGE_BIN")
	cmd.Env = env
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("driver failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("driver failed: %v", err)
	}
	var results []sessionCallResult
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("driver printed non-JSON: %s (%v)", out, err)
	}
	for i, r := range results {
		if r.Threw {
			t.Fatalf("case %d threw: %s", i, r.Message)
		}
	}

	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	for i, ev := range events {
		if ev.Kind != "skill" {
			t.Errorf("events[%d].kind = %q, want skill", i, ev.Kind)
		}
		if ev.Detail["skill_invalid"] != true {
			t.Errorf("events[%d].detail = %v, want {skill_invalid:true}", i, ev.Detail)
		}
		if _, hasSkill := ev.Detail["skill"]; hasSkill {
			t.Errorf("events[%d].detail carries a raw skill value; the invalid input must never be recorded verbatim", i)
		}
	}
}

// --- test helpers ---

// initGitRepoOnBranch creates a temp git repo, one commit, on the given
// branch name, so hooks.EvaluateContext (via `hook inject-context`) has a
// real branch and commit to read.
func initGitRepoOnBranch(t *testing.T, branch string) string {
	t.Helper()
	root := gittest.InitRepo(t, t.TempDir(), "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, root, "add", "README.md")
	gittest.Run(t, root, "commit", "-q", "-m", "init")
	return root
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestEventStreamHeartbeat: a streaming part delta touches the heartbeat file
// in XDG_DATA_HOME/opencode that the stall watchdog reads (#2184); a burst
// of deltas writes it once per interval, and an unrelated event writes
// nothing.
func TestEventStreamHeartbeat(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	dataHome := t.TempDir()
	hb := filepath.Join(dataHome, "opencode", "nightgauge-stream.heartbeat")

	other := sessionCall{Fn: "event", Input: busEvent("session.renamed", map[string]any{"sessionID": "ses_1", "title": "t"})}
	runSessionHarness(t, node, root, []sessionCall{other}, map[string]string{"XDG_DATA_HOME": dataHome})
	if _, err := os.Stat(hb); err == nil {
		t.Fatal("an unrelated event wrote the heartbeat")
	}

	// 2.2.7's session.text.delta data, one of HEARTBEAT_EVENT_TYPES.
	delta := sessionCall{Fn: "event", Input: busEvent("session.text.delta", map[string]any{
		"sessionID": "ses_1", "assistantMessageID": "msg_1", "ordinal": 0, "delta": "x",
	})}
	results := runSessionHarness(t, node, root, []sessionCall{delta, delta, delta}, map[string]string{"XDG_DATA_HOME": dataHome})
	for i, r := range results {
		if r.Threw {
			t.Fatalf("call %d threw: %s", i, r.Message)
		}
	}
	info, err := os.Stat(hb)
	if err != nil {
		t.Fatalf("a part delta did not write the heartbeat: %v", err)
	}
	if age := time.Since(info.ModTime()); age > time.Minute {
		t.Errorf("heartbeat mtime is %v old, want it just written", age)
	}
}

// --- the entry's own registrations on the fake 2.x host ---

// runHostSession loads the entry on the fake 2.x host and runs body, which
// prints JSON; extraEnv is applied on top of a cleaned environment.
func runHostSession(t *testing.T, node, cwd, body string, extraEnv map[string]string) []byte {
	t.Helper()
	dir, err := Write(filepath.Join(t.TempDir(), "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driver, []byte(hostDriver(body)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "NG_PLUGIN_PATH="+filepath.Join(dir, EntryFile), "NG_CWD="+cwd)
	for _, k := range []string{"NIGHTGAUGE_BIN", "NIGHTGAUGE_OUTPUT_FILE", "NIGHTGAUGE_RUN_ID", EnvSentinel, EnvNonce} {
		env = removeEnv(env, k)
	}
	for k, v := range extraEnv {
		env = upsertEnv(env, k, v)
	}
	env = upsertEnv(env, notifyDisableEnv, "1")
	cmd := exec.Command(node, driver)
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("host driver failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("host driver failed: %v", err)
	}
	return out
}

// TestCompactionHookInjectsContextThroughTheEntry: the entry's registered
// session:compaction hook reaches session.js and appends the inject-context
// part to arg.system. Dropping or misnaming the delegate in nightgauge.js
// turns this red; the sessionCompaction tests above call the export directly.
func TestCompactionHookInjectsContextThroughTheEntry(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := initGitRepoOnBranch(t, "feat/42-x")
	arg, err := json.Marshal(compactionArg())
	if err != nil {
		t.Fatal(err)
	}
	out := runHostSession(t, node, root, `
const arg = JSON.parse(process.env.NG_ARG);
await host.compaction(arg);
process.stdout.write(JSON.stringify(arg));
`, map[string]string{"NIGHTGAUGE_BIN": bin, "NG_ARG": string(arg)})
	texts := compactionSystemTexts(t, out)
	if len(texts) != 2 || !strings.Contains(texts[1], `"feat/42-x"`) {
		t.Errorf("system texts = %q, want the original part plus an inject-context part naming feat/42-x", texts)
	}
}

// TestEventsReachSessionJSThroughTheSubscription: events emitted on the fake
// host's subscription reach session.js through the entry's types filter. A
// permission.asked records permission_ask, session.execution.failed records
// idle and (no NIGHTGAUGE_BIN) stop_verify no_bin, and a type outside the
// filter records nothing. Dropping a type from SUBSCRIBED_EVENT_TYPES, or the
// entry's subscription, turns this red.
func TestEventsReachSessionJSThroughTheSubscription(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-host-events-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}
	runHostSession(t, node, root, `
await host.emit({ type: "session.renamed", data: { sessionID: "ses_h", title: "x" } });
await host.emit({ type: "permission.asked", data: { id: "p", sessionID: "ses_h", action: "shell" } });
await host.emit({ type: "session.execution.failed", data: { sessionID: "ses_h", error: {} } });
process.stdout.write("{}");
process.exit(0);
`, map[string]string{"NIGHTGAUGE_OUTPUT_FILE": outputFile, "NIGHTGAUGE_RUN_ID": runID})
	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	if strings.Join(kinds, ",") != "permission_ask,idle,stop_verify" {
		t.Fatalf("event kinds = %v, want permission_ask, idle, stop_verify", kinds)
	}
	if v, _ := events[2].Detail["verdict"].(string); v != "no_bin" {
		t.Errorf("stop_verify verdict = %v, want no_bin with NIGHTGAUGE_BIN unset", events[2].Detail["verdict"])
	}
}

// TestEveryHeartbeatEventTouchesTheHeartbeat drives each streaming event type
// session.js counts as progress, one fresh process each.
func TestEveryHeartbeatEventTouchesTheHeartbeat(t *testing.T) {
	node := requireNode(t)
	for _, typ := range []string{"session.text.delta", "session.step.streamed", "session.tool.input.started", "session.tool.progress"} {
		t.Run(typ, func(t *testing.T) {
			dataHome := t.TempDir()
			call := sessionCall{Fn: "event", Input: busEvent(typ, map[string]any{"sessionID": "ses_1"})}
			runSessionHarness(t, node, t.TempDir(), []sessionCall{call}, map[string]string{"XDG_DATA_HOME": dataHome})
			if _, err := os.Stat(filepath.Join(dataHome, "opencode", "nightgauge-stream.heartbeat")); err != nil {
				t.Errorf("%s did not write the heartbeat: %v", typ, err)
			}
		})
	}
}
