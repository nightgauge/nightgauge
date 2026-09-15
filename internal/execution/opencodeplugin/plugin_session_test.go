package opencodeplugin

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
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
// gates.js-gated tool.execute.before) and drives one or more of its exports
// in sequence, in the same Node process, so a rate-limit test can observe
// state (lastNotifyAt, eventsTruncated) surviving across calls exactly as it
// does inside one real opencode process. ---

const sessionHarnessDriver = `
import { pathToFileURL } from "node:url";

const mod = await import(pathToFileURL(process.env.NG_SESSION_PATH).href);
const ctx = { directory: process.env.NG_CWD, worktree: process.env.NG_CWD };
const calls = JSON.parse(process.env.NG_CALLS);

const results = [];
for (const call of calls) {
  const fn = mod[call.fn];
  let threw = false;
  let message = "";
  const output = call.output === undefined ? undefined : call.output;
  try {
    if (call.fn === "event") {
      await fn(ctx, call.input);
    } else {
      await fn(ctx, call.input, output);
    }
  } catch (e) {
    threw = true;
    message = String(e && e.message ? e.message : e);
  }
  results.push({ threw, message, output });
}
process.stdout.write(JSON.stringify(results));
`

type sessionCall struct {
	Fn     string `json:"fn"`
	Input  any    `json:"input"`
	Output any    `json:"output,omitempty"`
}

type sessionCallResult struct {
	Threw   bool            `json:"threw"`
	Message string          `json:"message"`
	Output  json.RawMessage `json:"output"`
}

// sessionModulePath writes the embedded plugin tree into a fresh temp dir
// and returns nightgauge/session.js's absolute path within it.
func sessionModulePath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "plugin")
	entry, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(filepath.Dir(entry), "nightgauge", "session.js")
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

// --- experimental.session.compacting ---

// TestSessionCompactingInjectsContext: invoking the compacting hook in a
// temp git repo on branch feat/42-x yields output.context containing
// "branch":"feat/42-x" — the AC's own verification bullet, driven against
// the real built nightgauge binary's `hook inject-context` verb.
func TestSessionCompactingInjectsContext(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := initGitRepoOnBranch(t, "feat/42-x")

	calls := []sessionCall{{
		Fn:     "sessionCompacting",
		Input:  map[string]any{"sessionID": "ses_1"},
		Output: map[string]any{"context": []any{}},
	}}
	results := runSessionHarness(t, node, root, calls, map[string]string{"NIGHTGAUGE_BIN": bin})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Threw {
		t.Fatalf("sessionCompacting threw: %s", results[0].Message)
	}
	var output struct {
		Context []string `json:"context"`
	}
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatalf("output did not parse: %v (%s)", err, results[0].Output)
	}
	// `nightgauge hook inject-context` prints via cmd/nightgauge's printJSON,
	// which pretty-prints (json.MarshalIndent: a space after every colon), so
	// the injected entry reads `"branch": "feat/42-x"`, not the compact
	// `"branch":"feat/42-x"` the issue text describes — verified empirically
	// here rather than assumed; parsing the entry as JSON is robust to that
	// either way.
	found := false
	for _, c := range output.Context {
		var parsed struct {
			Branch string `json:"branch"`
		}
		if err := json.Unmarshal([]byte(c), &parsed); err == nil && parsed.Branch == "feat/42-x" {
			found = true
		}
	}
	if !found {
		t.Errorf("output.context = %v, want an entry whose parsed \"branch\" field is \"feat/42-x\"", output.Context)
	}
}

// TestSessionCompactingSkipsWithoutBin: with no NIGHTGAUGE_BIN, compaction
// context is skipped, never thrown.
func TestSessionCompactingSkipsWithoutBin(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()

	calls := []sessionCall{{
		Fn:     "sessionCompacting",
		Input:  map[string]any{"sessionID": "ses_1"},
		Output: map[string]any{"context": []any{}},
	}}
	results := runSessionHarness(t, node, root, calls, nil)
	if results[0].Threw {
		t.Fatalf("want no throw with NIGHTGAUGE_BIN unset, got %s", results[0].Message)
	}
	var output struct {
		Context []string `json:"context"`
	}
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Context) != 0 {
		t.Errorf("output.context = %v, want empty (no NIGHTGAUGE_BIN to inject from)", output.Context)
	}
}

// --- experimental.compaction.autocontinue ---

// TestCompactionAutocontinueAlwaysDisables: whatever output.enabled starts
// as, this hook sets it to false, unconditionally and without spawning
// anything.
func TestCompactionAutocontinueAlwaysDisables(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()

	calls := []sessionCall{{
		Fn:     "compactionAutocontinue",
		Input:  map[string]any{"sessionID": "ses_1", "overflow": false},
		Output: map[string]any{"enabled": true},
	}}
	results := runSessionHarness(t, node, root, calls, nil)
	if results[0].Threw {
		t.Fatalf("compactionAutocontinue threw: %s", results[0].Message)
	}
	var output struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.Enabled {
		t.Error("output.enabled is still true; want the synthetic continue turn disabled")
	}
}

// --- event: session.idle -> idle, stop_verify ---

// TestEventIdleWritesStopVerifyEvent: an idle event writes an "idle" line
// followed by a "stop_verify" line to the run's events file, with a verdict
// and no other free text. Removing the event handler's "idle" append (or its
// session.idle branch entirely) turns this red.
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
		Input: map[string]any{"event": map[string]any{"type": "session.idle", "properties": map[string]any{"sessionID": "ses_idle_1"}}},
	}}
	results := runSessionHarness(t, node, root, calls, map[string]string{
		"NIGHTGAUGE_BIN":         bin,
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	})
	if results[0].Threw {
		t.Fatalf("event(session.idle) threw: %s", results[0].Message)
	}

	events, err := ReadRunEvents(eventsFile)
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

// --- tool.execute.before: skill usage (via the real plugin, gates.js AND
// session.js both wired, exactly as nightgauge.js composes them) ---

const skillToolDriver = `
import { pathToFileURL } from "node:url";

const mod = await import(pathToFileURL(process.env.NG_PLUGIN_PATH).href);
const plugin = mod.NightgaugePlugin || mod.default;
const ctx = { directory: process.env.NG_CWD, worktree: process.env.NG_CWD };
const hooks = await plugin(ctx);

const input = { tool: "skill", sessionID: "s", callID: "c" };
const output = { args: { skill: process.env.NG_SKILL || "" } };

let result;
try {
  await hooks["tool.execute.before"](input, output);
  result = { threw: false };
} catch (e) {
  result = { threw: true, message: String(e && e.message ? e.message : e) };
}
process.stdout.write(JSON.stringify(result));
`

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
		"NG_PLUGIN_PATH="+entry,
		"NG_CWD="+cwd,
		"NG_SKILL="+skill,
	)
	if nightgaugeBin != "" {
		env = upsertEnv(env, "NIGHTGAUGE_BIN", nightgaugeBin)
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
	if err := os.WriteFile(exit1, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	root := t.TempDir()

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

// --- permission.ask ---

// TestPermissionAskThrottlesNotify: three permission.ask calls in quick
// succession (the same Node process, so the same in-memory throttle state a
// real opencode process would keep) produce exactly one notify spawn, and
// output.status is unchanged after every call.
func TestPermissionAskThrottlesNotify(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	counter := filepath.Join(t.TempDir(), "notify-calls.txt")
	counterScript := filepath.Join(t.TempDir(), "count-notify.sh")
	// POSIX sh only: `<<<` is a bash/zsh herestring dash (Ubuntu's /bin/sh)
	// rejects, which silently never wrote the counter file in CI (this
	// workspace's own required Go build & test job runs on ubuntu-latest).
	script := "#!/bin/sh\necho 1 >> " + shellQuote(counter) + "\nexit 0\n"
	if err := os.WriteFile(counterScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	perm := map[string]any{"id": "p1", "type": "bash", "sessionID": "ses_perm_1", "messageID": "m1", "title": "run a command", "metadata": map[string]any{}, "time": map[string]any{"created": 1}}
	calls := []sessionCall{
		{Fn: "permissionAsk", Input: perm, Output: map[string]any{"status": "ask"}},
		{Fn: "permissionAsk", Input: perm, Output: map[string]any{"status": "ask"}},
		{Fn: "permissionAsk", Input: perm, Output: map[string]any{"status": "ask"}},
	}
	results := runSessionHarness(t, node, root, calls, map[string]string{"NIGHTGAUGE_BIN": counterScript})
	for i, r := range results {
		if r.Threw {
			t.Fatalf("call %d threw: %s", i, r.Message)
		}
		var output struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(r.Output, &output); err != nil {
			t.Fatal(err)
		}
		if output.Status != "ask" {
			t.Errorf("call %d: output.status = %q, want unchanged \"ask\"", i, output.Status)
		}
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("the notify script never ran even once: %v", err)
	}
	lines := strings.TrimSpace(string(data))
	got := 0
	if lines != "" {
		got = len(strings.Split(lines, "\n"))
	}
	if got != 1 {
		t.Errorf("notify spawned %d times for 3 calls in the same 60s window, want exactly 1", got)
	}
}

// TestPermissionAskEmitsEvent: every permission.ask call, throttled or not,
// records a permission_ask event.
func TestPermissionAskEmitsEvent(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-perm-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	perm := map[string]any{"id": "p1", "type": "bash", "sessionID": "ses_perm_2", "messageID": "m1", "title": "run a command", "metadata": map[string]any{}, "time": map[string]any{"created": 1}}
	calls := []sessionCall{{Fn: "permissionAsk", Input: perm, Output: map[string]any{"status": "ask"}}}
	results := runSessionHarness(t, node, root, calls, map[string]string{
		"NIGHTGAUGE_OUTPUT_FILE": outputFile,
		"NIGHTGAUGE_RUN_ID":      runID,
	})
	if results[0].Threw {
		t.Fatalf("permissionAsk threw: %s", results[0].Message)
	}

	events, err := ReadRunEvents(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "permission_ask" {
		t.Fatalf("events = %+v, want exactly one permission_ask event", events)
	}
	if events[0].SessionID != "ses_perm_2" {
		t.Errorf("session_id = %q, want ses_perm_2", events[0].SessionID)
	}
	if events[0].Detail["permission_type"] != "bash" {
		t.Errorf("detail.permission_type = %v, want \"bash\"", events[0].Detail["permission_type"])
	}
}

// --- event: permission.asked (the path opencode 1.18.30 actually triggers;
// the permission.ask hook above never fires on that binary — see session.js's
// own comment on permissionAsk) ---

// TestEventPermissionAskedEmitsEventAndThrottlesNotify: event() with a
// permission.asked bus event (1.18.30's real shape: {id, sessionID,
// permission, patterns, metadata, always, tool}) records a permission_ask
// event carrying only permission_type, never the model-authored `patterns`
// text, and applies the same 60s per-session notify throttle permissionAsk
// does. Removing event()'s permission.asked branch turns this red — the
// production defect this covers: the plugin's permission.ask hook export is
// never called by the pinned binary at all.
func TestEventPermissionAskedEmitsEventAndThrottlesNotify(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	counter := filepath.Join(t.TempDir(), "notify-calls.txt")
	counterScript := filepath.Join(t.TempDir(), "count-notify.sh")
	script := "#!/bin/sh\necho 1 >> " + shellQuote(counter) + "\nexit 0\n"
	if err := os.WriteFile(counterScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	outputFile := filepath.Join(t.TempDir(), "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "run-perm-asked-1"
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken")
	}

	permAsked := map[string]any{
		"type": "permission.asked",
		"properties": map[string]any{
			"id":         "per_1",
			"sessionID":  "ses_perm_asked_1",
			"permission": "bash",
			"patterns":   []any{"echo the deploy key is SECRET-PLACEHOLDER"},
			"metadata":   map[string]any{"command": "echo the deploy key is SECRET-PLACEHOLDER"},
			"always":     []any{"echo *"},
			"tool":       map[string]any{"messageID": "m1", "callID": "c1"},
		},
	}
	calls := []sessionCall{
		{Fn: "event", Input: map[string]any{"event": permAsked}},
		{Fn: "event", Input: map[string]any{"event": permAsked}},
		{Fn: "event", Input: map[string]any{"event": permAsked}},
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
		if ev.Detail["permission_type"] != "bash" {
			t.Errorf("events[%d].detail.permission_type = %v, want bash", i, ev.Detail["permission_type"])
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

	sessionUpdated := func(id, parentID string) map[string]any {
		info := map[string]any{"id": id}
		if parentID != "" {
			info["parentID"] = parentID
		}
		return map[string]any{"type": "session.updated", "properties": map[string]any{"info": info}}
	}
	calls := []sessionCall{
		{Fn: "event", Input: map[string]any{"event": sessionUpdated("ses_parent", "")}},
		{Fn: "event", Input: map[string]any{"event": sessionUpdated("ses_child", "ses_parent")}},
		{Fn: "event", Input: map[string]any{"event": map[string]any{"type": "session.compacted", "properties": map[string]any{"sessionID": "ses_parent"}}}},
		{Fn: "event", Input: map[string]any{"event": map[string]any{"type": "session.compacted", "properties": map[string]any{"sessionID": "ses_child"}}}},
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
const ctx = { directory: process.env.NG_CWD, worktree: process.env.NG_CWD };
const cases = JSON.parse(process.env.NG_ARGS);
const results = [];
for (const args of cases) {
  const input = { tool: "skill", sessionID: "s", callID: "c" };
  const output = { args };
  try {
    await mod.toolExecuteBefore(ctx, input, output);
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
