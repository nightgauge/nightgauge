package opencodeplugin

// plugin_edit_test.go (#1642) drives the REAL shipped plugin tree through
// Node, spawning either a small recording fake or the REAL built
// `nightgauge` binary for each row — never a re-implementation of edit.js's
// own decisions in Go — exactly like plugin_gates_test.go's own harness
// (writePluginTreeForGatesTest, requireNode, buildNightgaugeBin).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// editJSPath writes a fresh copy of the shipped plugin tree and returns
// both the plugin directory and edit.js's own path.
func editJSPath(t *testing.T) (entry, editPath string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "plugin")
	e, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	return e, filepath.Join(dir, "nightgauge", "edit.js")
}

// toolExecuteAfterResult is what nodeToolExecuteAfterDriver prints: whether
// the hook threw (it never should — edit.js's own contract), and the tool's
// result as the model and the stream see it after the hook: Output["output"]
// is the result's text parts joined with "\n" (2.x hands the model each
// part), Output["metadata"] its metadata.
type toolExecuteAfterResult struct {
	Threw   bool           `json:"threw"`
	Message string         `json:"message"`
	Output  map[string]any `json:"output"`
}

// nodeToolExecuteAfterDriver drives the entry's own registered OpenCode 2.x
// execute.after hook on the fake host — the wiring itself (edit.js's optional
// import), not edit.js's export called directly — with the argument 2.2.7
// hands it: {tool, sessionID, agent, messageID, id, input, status, result}.
// NG_OUTPUT's {output, metadata} becomes a completed tool's result, whose
// content holds the original output as one text part.
var nodeToolExecuteAfterDriver = hostDriver(`
const given = JSON.parse(process.env.NG_OUTPUT || '{"output":"original"}');
const original = typeof given.output === "string" ? given.output : "";
const result = {
  output: {},
  content: original === "" ? [] : [{ type: "text", text: original }],
  metadata: given.metadata || {},
};
const arg = {
  ...toolArg(process.env.NG_TOOL || "edit", JSON.parse(process.env.NG_ARGS || "{}")),
  status: process.env.NG_STATUS || "completed",
  result,
};
const view = () => ({
  output: result.content.filter((p) => p && p.type === "text").map((p) => p.text).join("\n"),
  metadata: result.metadata,
});

let out;
try {
  await host.after(arg);
  out = { threw: false, output: view() };
} catch (e) {
  out = { threw: true, message: String(e && e.message ? e.message : e), output: view() };
}
process.stdout.write(JSON.stringify(out));
`)

// nodeEditTimeoutsDriver reads edit.js's own exported timeout constants
// directly, so a test can pin the exact numbers (30000/10000/5000) without
// waiting out a real spawn timeout to observe them.
const nodeEditTimeoutsDriver = `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.env.NG_EDIT_PATH).href);
process.stdout.write(JSON.stringify({
  format: mod.FORMAT_TIMEOUT_MS,
  checkVersion: mod.CHECK_VERSION_TIMEOUT_MS,
  testQuality: mod.TEST_QUALITY_TIMEOUT_MS,
}));
`

// nodeEditEPIPEStatusDriver supplies the exact result shape observed in CI,
// without depending on a pipe-close race to make spawnSync produce it.
const nodeEditEPIPEStatusDriver = `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.env.NG_EDIT_PATH).href);
const error = new Error("write EPIPE");
error.code = "EPIPE";
const result = mod.classifyQualityVerbResult("test-quality", {
  error,
  status: 0,
  signal: null,
  stderr: "quality warning\n",
});
process.stdout.write(JSON.stringify(result));
`

// runEditHarness drives one execute.after call through the entry's own
// registered hook for a 2.x-shaped edit/write `input` object.
func runEditHarness(t *testing.T, node, cwd, tool, argsJSON, outputJSON, nightgaugeBin string, extraEnv map[string]string) toolExecuteAfterResult {
	t.Helper()
	entry, _ := editJSPath(t)
	driver := writeGatesDriver(t, nodeToolExecuteAfterDriver)
	env := append(editHarnessEnv(nightgaugeBin, extraEnv),
		"NG_PLUGIN_PATH="+filepath.Join(entry, EntryFile),
		"NG_CWD="+cwd,
		"NG_TOOL="+tool,
		"NG_ARGS="+argsJSON,
		"NG_OUTPUT="+outputJSON,
	)
	cmd := exec.Command(node, driver)
	cmd.Env = env
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("node harness failed: %v", err)
	}
	var res toolExecuteAfterResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node harness printed non-JSON: %s (%v)", out, err)
	}
	return res
}

// editHarnessEnv is the child env every edit-hook harness run gets: the
// parent's env with an inherited NIGHTGAUGE_SKIP_TEST_QUALITY removed (an
// operator or parent process that exports it would otherwise silence the real
// verb, #3343), then the shared harness scrubbing. extraEnv may still set the
// opt-out deliberately.
func editHarnessEnv(nightgaugeBin string, extraEnv map[string]string) []string {
	env := removeEnv(os.Environ(), skipTestQualityEnv)
	return applyHarnessEnv(env, nightgaugeBin, extraEnv)
}

// readEditTimeoutConstants reads edit.js's own exported timeout constants
// via the real embedded module.
func readEditTimeoutConstants(t *testing.T, node string) map[string]int {
	t.Helper()
	_, editPath := editJSPath(t)
	driver := writeGatesDriver(t, nodeEditTimeoutsDriver)
	cmd := exec.Command(node, driver)
	cmd.Env = append(os.Environ(), "NG_EDIT_PATH="+editPath)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("node harness failed: %v", err)
	}
	var res map[string]int
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("node harness printed non-JSON: %s (%v)", out, err)
	}
	return res
}

func readEditEPIPEStatusResult(t *testing.T, node string) map[string]any {
	t.Helper()
	_, editPath := editJSPath(t)
	driver := writeGatesDriver(t, nodeEditEPIPEStatusDriver)
	cmd := exec.Command(node, driver)
	cmd.Env = append(os.Environ(), "NG_EDIT_PATH="+editPath)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("node harness failed: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("node harness printed non-JSON: %s (%v)", out, err)
	}
	return result
}

// writeRecordingBin writes a tiny POSIX shell script that appends its own
// verb argument ($2 — spawnSync(bin, ["hook", "<verb>"], ...) puts the verb
// there) to logPath, one per line, then exits 0. When sleepOnVerb is
// non-empty, invoking it with that verb sleeps sleepSeconds first, so a
// caller can force exactly one of the three spawns to hang without waiting
// out the other two verbs' own (much longer) timeouts.
func writeRecordingBin(t *testing.T, logPath, sleepOnVerb string, sleepSeconds int) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-nightgauge")
	var b strings.Builder
	fmt.Fprintf(&b, "printf '%%s\\n' \"$2\" >> %s\n", shellQuote(logPath))
	if sleepOnVerb != "" {
		fmt.Fprintf(&b, "if [ \"$2\" = %s ]; then sleep %d; fi\n", shellQuote(sleepOnVerb), sleepSeconds)
	}
	b.WriteString("exit 0\n")
	writeShFixture(t, binPath, b.String(), 0o700)
	return binPath
}

func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// --- AC1/AC2: format/check-version/test-quality run in hooks.json order,
// with hooks.json timeouts, and format's own spawn is conditional on
// OpenCode's own formatter setting ---

func TestEditHooksOrderAndFormatterToggle(t *testing.T) {
	node := requireNode(t)

	writeArgs := func(t *testing.T) string {
		t.Helper()
		b, err := json.Marshal(map[string]any{"path": "a.test.ts", "content": "console.log(1)"})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("formatter disabled: format, check-version, test-quality all spawn, in order", func(t *testing.T) {
		root := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "log.txt")
		bin := writeRecordingBin(t, logPath, "", 0)
		env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}

		res := runEditHarness(t, node, root, "write", writeArgs(t), `{"output":"orig"}`, bin, env)
		if res.Threw {
			t.Fatalf("want no throw, got %q", res.Message)
		}
		got := readLogLines(t, logPath)
		want := []string{"format", "check-version", "test-quality"}
		if !equalStrings(got, want) {
			t.Errorf("spawned verbs = %v, want %v in that order", got, want)
		}
	})

	t.Run("formatter enabled (explicit true): format never spawns", func(t *testing.T) {
		root := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "log.txt")
		bin := writeRecordingBin(t, logPath, "", 0)
		env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":true}`}

		res := runEditHarness(t, node, root, "write", writeArgs(t), `{"output":"orig"}`, bin, env)
		if res.Threw {
			t.Fatalf("want no throw, got %q", res.Message)
		}
		got := readLogLines(t, logPath)
		want := []string{"check-version", "test-quality"}
		if !equalStrings(got, want) {
			t.Errorf("spawned verbs = %v, want %v (no format)", got, want)
		}
	})

	t.Run("formatter unset defaults to enabled: format never spawns", func(t *testing.T) {
		root := t.TempDir()
		logPath := filepath.Join(t.TempDir(), "log.txt")
		bin := writeRecordingBin(t, logPath, "", 0)
		env := map[string]string{} // no OPENCODE_CONFIG_CONTENT at all

		res := runEditHarness(t, node, root, "edit", `{"path":"a.test.ts","oldString":"x","newString":"console.log(1)"}`, `{"output":"orig"}`, bin, env)
		if res.Threw {
			t.Fatalf("want no throw, got %q", res.Message)
		}
		got := readLogLines(t, logPath)
		want := []string{"check-version", "test-quality"}
		if !equalStrings(got, want) {
			t.Errorf("spawned verbs = %v, want %v (no format)", got, want)
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEditHookTimeoutConstants locks in hooks.json's own PostToolUse:Edit|Write
// timeouts (30000/10000/5000 ms), read directly off edit.js's own exported
// constants rather than waited out in real time.
func TestEditHookTimeoutConstants(t *testing.T) {
	node := requireNode(t)
	got := readEditTimeoutConstants(t, node)
	want := map[string]int{"format": 30000, "checkVersion": 10000, "testQuality": 5000}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s timeout = %d, want %d", k, got[k], v)
		}
	}
}

// --- AC3: a path outside the worktree spawns nothing and adds no warning ---

func TestEditHookOutsideWorktreeSpawnsNothing(t *testing.T) {
	node := requireNode(t)

	for _, tc := range []struct {
		name     string
		filePath string
		// filePathFn overrides filePath when set, for a case whose path
		// depends on root (created fresh per subtest below).
		filePathFn func(root string) string
	}{
		{name: "absolute path elsewhere", filePath: "/etc/hosts"},
		{name: "parent-directory traversal", filePath: "../../x.go"},
		// A sibling directory that merely shares root as a PREFIX
		// (root-evil, not root itself) must still be refused — the
		// guardrail against a naive `startsWith(cwd)` containment check,
		// which path.relative-based isPathContained avoids: path.relative
		// resolves this to a path starting with "..", not a prefix match.
		{
			name: "sibling directory sharing only a name prefix with the worktree",
			filePathFn: func(root string) string {
				return filepath.Join(root+"-evil", "x.go")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			filePath := tc.filePath
			if tc.filePathFn != nil {
				filePath = tc.filePathFn(root)
			}
			logPath := filepath.Join(t.TempDir(), "log.txt")
			bin := writeRecordingBin(t, logPath, "", 0)
			env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}

			argsJSON, err := json.Marshal(map[string]any{"path": filePath, "content": "x"})
			if err != nil {
				t.Fatal(err)
			}
			res := runEditHarness(t, node, root, "write", string(argsJSON), `{"output":"orig"}`, bin, env)
			if res.Threw {
				t.Fatalf("want no throw, got %q", res.Message)
			}
			if got := readLogLines(t, logPath); len(got) != 0 {
				t.Errorf("spawned verbs for %q = %v, want zero spawns", filePath, got)
			}
			if res.Output["output"] != "orig" {
				t.Errorf("output.output = %v, want the original tool output unchanged", res.Output["output"])
			}
		})
	}
}

// --- an ABSOLUTE filePath inside the worktree runs the hooks: opencode
// 1.18.30's own tool schemas require an absolute filePath, so refusing
// every absolute path (the pre-fix behaviour) made every one of these hooks
// dead against a real model. AC3 only excludes an absolute path OUTSIDE the
// worktree — one inside it is exactly as contained as a relative one. ---

func TestEditHookAbsolutePathInsideWorktreeRunsHooks(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "log.txt")
	bin := writeRecordingBin(t, logPath, "", 0)
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}

	absPath := filepath.Join(root, "src", "a.test.ts")
	argsJSON, err := json.Marshal(map[string]any{"path": absPath, "content": "expect(true).toBe(true);"})
	if err != nil {
		t.Fatal(err)
	}
	res := runEditHarness(t, node, root, "write", string(argsJSON), `{"output":"orig"}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw, got %q", res.Message)
	}
	got := readLogLines(t, logPath)
	want := []string{"format", "check-version", "test-quality"}
	if !equalStrings(got, want) {
		t.Errorf("spawned verbs for an absolute path inside the worktree = %v, want %v", got, want)
	}
}

// --- warnings are appended to output.output, capped at 2 KB, never thrown,
// and a timed-out verb leaves the tool result intact ---

func TestEditHookAppendsWarningsCappedAt2KB(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "log.txt")

	// A fake verb that prints 5 KB to stderr for test-quality alone (format
	// and check-version print nothing, matching their real, quiet contract),
	// so this test isolates the cap itself from edit.js's real ordering.
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-nightgauge")
	script := "printf '%s\\n' \"$2\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$2\" = 'test-quality' ]; then head -c 5120 /dev/zero | tr '\\0' 'w' 1>&2; fi\n" +
		"exit 0\n"
	writeShFixture(t, binPath, script, 0o700)

	env := map[string]string{
		"NIGHTGAUGE_EDIT_HOOK_DIAGNOSTICS": "1",
		"OPENCODE_CONFIG_CONTENT":          `{"formatter":true}`,
	} // skip format
	argsJSON := `{"path":"a.test.ts","content":"x"}`
	res := runEditHarness(t, node, root, "write", argsJSON, `{"output":"orig"}`, binPath, env)
	if res.Threw {
		t.Fatalf("want no throw, got %q", res.Message)
	}
	out, _ := res.Output["output"].(string)
	if !strings.HasPrefix(out, "orig") {
		t.Errorf("output.output = %q, want it to still start with the original tool output", out)
	}
	// The warning is its own text part, which the view joins after "orig\n".
	appended := len(out) - len("orig\n")
	if appended > 2048 {
		t.Errorf("appended %d bytes, want <= 2048 (issue's own 2 KB cap)", appended)
	}
	if appended == 0 {
		t.Errorf("nothing was appended at all; want the (capped) 5 KB fake warning; outcomes=%#v", res.Output["metadata"])
	}
}

func TestEditHookEPIPEWithSuccessfulStatusKeepsWarning(t *testing.T) {
	node := requireNode(t)
	result := readEditEPIPEStatusResult(t, node)
	if result["warning"] != "quality warning" {
		t.Fatalf("warning = %#v, want child stderr when EPIPE accompanies status 0", result["warning"])
	}
	outcome, ok := result["outcome"].(map[string]any)
	if !ok {
		t.Fatalf("outcome = %#v, want object", result["outcome"])
	}
	if outcome["verb"] != "test-quality" || outcome["status"] != float64(0) ||
		outcome["signal"] != nil || outcome["error_kind"] != nil || outcome["error_code"] != nil ||
		outcome["stderr_nonempty"] != true || outcome["stderr_bytes"] != float64(16) {
		t.Errorf("outcome = %#v, want bounded successful EPIPE classification", outcome)
	}
	if _, exposed := outcome["stderr"]; exposed {
		t.Errorf("diagnostic outcome exposes stderr content: %#v", outcome)
	}
}

func TestEditHookQualityVerbDiagnostics(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	argsJSON := `{"path":"a.test.ts","content":"x"}`
	env := map[string]string{
		"NIGHTGAUGE_EDIT_HOOK_DIAGNOSTICS": "1",
		"OPENCODE_CONFIG_CONTENT":          `{"formatter":true}`,
	}

	readOutcomes := func(t *testing.T, res toolExecuteAfterResult) []map[string]any {
		t.Helper()
		metadata, ok := res.Output["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("diagnostic metadata = %#v, want object", res.Output["metadata"])
		}
		raw, ok := metadata["nightgauge_edit_hook_outcomes"].([]any)
		if !ok {
			t.Fatalf("diagnostic outcomes = %#v, want array", metadata["nightgauge_edit_hook_outcomes"])
		}
		outcomes := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			outcome, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("diagnostic outcome = %#v, want object", item)
			}
			outcomes = append(outcomes, outcome)
		}
		return outcomes
	}

	t.Run("known-good warning producer records successful test-quality", func(t *testing.T) {
		dir := t.TempDir()
		binPath := filepath.Join(dir, "fake-nightgauge")
		script := "if [ \"$2\" = 'test-quality' ]; then printf 'quality warning\\n' 1>&2; fi\n" +
			"exit 0\n"
		writeShFixture(t, binPath, script, 0o700)
		res := runEditHarness(t, node, root, "write", argsJSON, `{"output":"orig"}`, binPath, env)
		if res.Threw {
			t.Fatalf("want no throw, got %q", res.Message)
		}
		outcomes := readOutcomes(t, res)
		if len(outcomes) != 2 {
			t.Fatalf("outcome count = %d, want check-version and test-quality", len(outcomes))
		}
		quality := outcomes[1]
		if quality["verb"] != "test-quality" || quality["status"] != float64(0) ||
			quality["stderr_nonempty"] != true || quality["error_kind"] != nil {
			t.Errorf("test-quality outcome = %#v, want successful non-empty warning", quality)
		}
	})

	t.Run("known-bad producer records non-zero exit and leaves output intact", func(t *testing.T) {
		dir := t.TempDir()
		binPath := filepath.Join(dir, "fake-nightgauge")
		script := "if [ \"$2\" = 'test-quality' ]; then exit 23; fi\n" +
			"exit 0\n"
		writeShFixture(t, binPath, script, 0o700)
		res := runEditHarness(t, node, root, "write", argsJSON, `{"output":"orig"}`, binPath, env)
		if res.Threw {
			t.Fatalf("want no throw, got %q", res.Message)
		}
		if res.Output["output"] != "orig" {
			t.Errorf("output.output = %#v, want original output after failed child", res.Output["output"])
		}
		outcomes := readOutcomes(t, res)
		quality := outcomes[1]
		if quality["verb"] != "test-quality" || quality["status"] != float64(23) ||
			quality["error_kind"] != nil || quality["signal"] != nil {
			t.Errorf("test-quality outcome = %#v, want non-zero exit classification", quality)
		}
	})
}

// TestEditHookTimeoutLeavesToolResultIntact: a fake test-quality verb that
// sleeps past TEST_QUALITY_TIMEOUT_MS (5000 ms) never gets its output
// appended, and the tool result's output.output comes back byte-for-byte
// the original — never partially appended, never thrown. format and
// check-version, invoked against the SAME slow fake, both complete well
// inside their own (much longer) timeouts, so only the test-quality spawn is
// actually killed; this keeps the test's own wall time to ~test-quality's
// own timeout rather than the sum of all three.
func TestEditHookTimeoutLeavesToolResultIntact(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "log.txt")
	bin := writeRecordingBin(t, logPath, "test-quality", 6)

	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}
	argsJSON := `{"path":"a.test.ts","content":"x"}`
	res := runEditHarness(t, node, root, "write", argsJSON, `{"output":"orig"}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw even on a timed-out verb, got %q", res.Message)
	}
	if res.Output["output"] != "orig" {
		t.Errorf("output.output = %v, want the original tool output intact after a timeout", res.Output["output"])
	}
}

// --- AC5 (second half): real test-quality warnings actually reach
// output.output, driven against the REAL built nightgauge binary rather
// than a fake ---

const (
	// skipTestQualityEnv is the verb's opt-out (internal/hooks/testquality.go).
	skipTestQualityEnv = "NIGHTGAUGE_SKIP_TEST_QUALITY"
	// editTestQualityTimeoutMS mirrors edit.js's TEST_QUALITY_TIMEOUT_MS,
	// which TestEditHookTimeoutConstants pins.
	editTestQualityTimeoutMS = 5000
	// testQualityWarmupTimeout bounds the warm-up exec: a cold first exec
	// under gate load, not the verb's own work.
	testQualityWarmupTimeout = 60 * time.Second
)

var realTestQualityWarnings = []string{"Tautological assertion detected", "Empty test body detected"}

// warmTestQualityVerb runs the freshly linked binary's `hook test-quality`
// once directly, with the payload edit.js would send and the harness env,
// under a generous bound. A fresh binary's first exec (cold page cache, the
// macOS codesign check) under gate load can exceed edit.js's 5000 ms
// test-quality bound; paying it here keeps the measured call about the verb
// (#3343). A broken verb fails here, with its own stderr, not as a missing
// append. Returns the verb's stderr.
func warmTestQualityVerb(t *testing.T, bin, cwd string, payload []byte, env []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testQualityWarmupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "hook", "test-quality")
	cmd.Env = env
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("warm-up `hook test-quality` failed: %v\nstderr:\n%s", err, stderr.String())
	}
	for _, want := range realTestQualityWarnings {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("warm-up `hook test-quality` stderr lacks %q (verb itself is broken):\n%s", want, stderr.String())
		}
	}
	return stderr.String()
}

// describeTestQualityOutcome names why a test-quality warning did not reach
// output.output, from edit.js's own diagnostics outcome (#1863) and whether
// the child env carried the skip opt-out.
func describeTestQualityOutcome(outcome map[string]any, skipInEnv bool) string {
	if skipInEnv {
		return "skipped: NIGHTGAUGE_SKIP_TEST_QUALITY=1 reached the child env"
	}
	if outcome == nil {
		return "no test-quality outcome recorded (verb never spawned)"
	}
	sig, _ := outcome["signal"].(string)
	if sig == "SIGTERM" || outcome["error_code"] == "ETIMEDOUT" {
		return fmt.Sprintf("timed out: past edit.js's %d ms test-quality bound (signal=%v error_code=%v)",
			editTestQualityTimeoutMS, outcome["signal"], outcome["error_code"])
	}
	if sig != "" {
		return "killed by " + sig
	}
	if kind, _ := outcome["error_kind"].(string); kind != "" {
		return fmt.Sprintf("could not run: error_kind=%s error_code=%v", kind, outcome["error_code"])
	}
	if status, ok := outcome["status"].(float64); ok && status != 0 {
		return fmt.Sprintf("exited %d", int(status))
	}
	if outcome["stderr_nonempty"] != true {
		return "ran and printed nothing"
	}
	return fmt.Sprintf("printed %v bytes but the warning is not in output.output", outcome["stderr_bytes"])
}

// testQualityOutcome returns the test-quality entry from the diagnostics
// metadata, or nil when absent.
func testQualityOutcome(res toolExecuteAfterResult) map[string]any {
	metadata, _ := res.Output["metadata"].(map[string]any)
	raw, _ := metadata["nightgauge_edit_hook_outcomes"].([]any)
	for _, item := range raw {
		if o, ok := item.(map[string]any); ok && o["verb"] == "test-quality" {
			return o
		}
	}
	return nil
}

// assertRealTestQualityWarnings drives the measured edit hook against the
// real binary, after a warm-up, and explains any missing warning.
func assertRealTestQualityWarnings(t *testing.T) {
	t.Helper()
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := t.TempDir()

	extra := map[string]string{
		"OPENCODE_CONFIG_CONTENT":          `{"formatter":true}`, // skip format; no formatter binaries required
		"NIGHTGAUGE_EDIT_HOOK_DIAGNOSTICS": "1",
	}
	content := "it(\"does nothing\", () => {})\nexpect(true).toBe(true);"
	argsJSON, err := json.Marshal(map[string]any{"path": "a.test.ts", "content": content})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"tool_name":  "Write",
		"cwd":        root,
		"tool_input": map[string]any{"file_path": "a.test.ts", "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := editHarnessEnv(bin, extra)
	skipInEnv := slices.Contains(env, skipTestQualityEnv+"=1")
	if skipInEnv {
		t.Fatalf("%s", describeTestQualityOutcome(nil, true))
	}
	warm := warmTestQualityVerb(t, bin, root, payload, env)

	res := runEditHarness(t, node, root, "write", string(argsJSON), `{"output":"original tool output"}`, bin, extra)
	if res.Threw {
		t.Fatalf("want no throw, got %q", res.Message)
	}
	out, _ := res.Output["output"].(string)
	if !strings.HasPrefix(out, "original tool output") {
		t.Fatalf("output.output = %q, want it to still start with the original tool output", out)
	}
	for _, want := range realTestQualityWarnings {
		if !strings.Contains(out, want) {
			reason := describeTestQualityOutcome(testQualityOutcome(res), skipInEnv)
			t.Errorf("output.output lacks real test-quality warning %q: %s (warm-up printed %d bytes)\n%s",
				want, reason, len(warm), out)
		}
	}
}

func TestEditHookAppendsRealTestQualityWarnings(t *testing.T) {
	assertRealTestQualityWarnings(t)
}

// TestEditHookRealTestQualityIgnoresInheritedSkip: a parent process
// exporting NIGHTGAUGE_SKIP_TEST_QUALITY=1 must not silence the real verb in
// the harness (#3343). Not parallel: t.Setenv.
func TestEditHookRealTestQualityIgnoresInheritedSkip(t *testing.T) {
	t.Setenv(skipTestQualityEnv, "1")
	assertRealTestQualityWarnings(t)
}

func TestDescribeTestQualityOutcome(t *testing.T) {
	cases := []struct {
		name    string
		outcome map[string]any
		skip    bool
		want    string
	}{
		{"skip", map[string]any{"status": float64(0)}, true, "skipped"},
		{"missing", nil, false, "no test-quality outcome"},
		{"signal", map[string]any{"signal": "SIGTERM"}, false, "timed out"},
		{"etimedout", map[string]any{"signal": "SIGTERM", "error_code": "ETIMEDOUT"}, false, "timed out"},
		{"other-signal", map[string]any{"signal": "SIGKILL"}, false, "killed by SIGKILL"},
		{"spawn", map[string]any{"error_kind": "spawn-error", "error_code": "ENOENT"}, false,
			"could not run: error_kind=spawn-error error_code=ENOENT"},
		{"exit", map[string]any{"status": float64(23)}, false, "exited 23"},
		{"silent", map[string]any{"status": float64(0), "stderr_nonempty": false}, false, "ran and printed nothing"},
		{"absent", map[string]any{"status": float64(0), "stderr_nonempty": true, "stderr_bytes": float64(40)}, false, "printed 40 bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describeTestQualityOutcome(c.outcome, c.skip); !strings.HasPrefix(got, c.want) {
				t.Errorf("got %q, want prefix %q", got, c.want)
			}
		})
	}
}

// TestEditHookNoFileEditedRegistration is the AC's own "a formatter write
// does not re-run the edit hooks" bullet: setup registers no hook and
// subscribes to no event that a file edit publishes, so a formatter write's
// own edit event reaches nothing. Tested at the wiring level (what setup
// registers on the fake 2.x host), which is what would change if a future
// edit ever added one.
func TestEditHookNoFileEditedRegistration(t *testing.T) {
	node := requireNode(t)
	entry, _ := editJSPath(t)
	driver := writeGatesDriver(t, hostDriver(`
process.stdout.write(JSON.stringify({
  registered: host.registered,
  types: host.subscriptions.flatMap((s) => s.types || ["*"]),
}));
`))
	cmd := exec.Command(node, driver)
	cmd.Env = append(os.Environ(), "NG_PLUGIN_PATH="+filepath.Join(entry, EntryFile), "NG_CWD="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("node harness failed: %v", err)
	}
	var got struct {
		Registered []string `json:"registered"`
		Types      []string `json:"types"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node harness printed non-JSON: %s (%v)", out, err)
	}
	for _, name := range append(got.Registered, got.Types...) {
		if name == "*" || strings.Contains(name, "file") || strings.Contains(name, "edited") {
			t.Fatalf("setup registers or subscribes to %q (registered %v, types %v); a formatter write must never re-trigger the edit hooks", name, got.Registered, got.Types)
		}
	}
}

// TestEditHookSkipsAToolThatDidNotComplete: an edit whose execute.after
// status is not "completed" changed no file, so no verb is spawned and the
// result is untouched.
func TestEditHookSkipsAToolThatDidNotComplete(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "log.txt")
	bin := writeRecordingBin(t, logPath, "", 0)
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`, "NG_STATUS": "error"}
	res := runEditHarness(t, node, root, "edit", `{"path":"calc.py","oldString":"a","newString":"b"}`, `{"output":"orig"}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw, got %q", res.Message)
	}
	if got := readLogLines(t, logPath); len(got) != 0 {
		t.Errorf("spawned verbs = %v for a tool that did not complete, want none", got)
	}
	if res.Output["output"] != "orig" {
		t.Errorf("output = %v, want the original tool output unchanged", res.Output["output"])
	}
}

// --- guardrail: replaying 2.2.7's real execute.after argument shape
// (captured by #3302's offline probe against opencode v2.2.7 — see edit.js's
// header comment). The stub's edit names the worktree-relative path
// "calc.py"; TestEditHookMatchesRealModelCaptureArgumentShape below replays
// an absolute path inside the worktree. ---

func TestEditHookMatches2xArgumentShape(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "log.txt")
	bin := writeRecordingBin(t, logPath, "", 0)
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}

	// Captured from the probe: an `edit` call's input and its result's text
	// against opencode v2.2.7 (tool-edit-stop@opencode2).
	const capturedEditInput = `{"newString":"return a - b","oldString":"return a + b","path":"calc.py"}`
	res := runEditHarness(t, node, root, "edit", capturedEditInput, `{"metadata":{},"output":"Edited calc.py (1 replacement)"}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw against the captured 2.2.7 edit shape, got %q", res.Message)
	}
	got := readLogLines(t, logPath)
	want := []string{"format", "check-version", "test-quality"}
	if !equalStrings(got, want) {
		t.Errorf("spawned verbs = %v, want %v against the captured 2.2.7 edit shape", got, want)
	}

	logPath2 := filepath.Join(t.TempDir(), "log2.txt")
	bin2 := writeRecordingBin(t, logPath2, "", 0)
	const writeInput = `{"content":"print(1)\n","path":"new.py"}`
	res2 := runEditHarness(t, node, root, "write", writeInput, `{"metadata":{},"output":"Wrote new.py"}`, bin2, env)
	if res2.Threw {
		t.Fatalf("want no throw against the 2.x write shape, got %q", res2.Message)
	}
	if got2 := readLogLines(t, logPath2); !equalStrings(got2, want) {
		t.Errorf("spawned verbs = %v, want %v against the 2.x write shape", got2, want)
	}
}

// TestEditHookMatchesRealModelCaptureArgumentShape replays the ABSOLUTE
// filePath shape this repository's own real-model captures show for a real
// `edit` call (internal/execution/testdata/opencode_stream_local_capture.jsonl:
// `"tool":"edit"... "input":{"path":"/tmp/nightgauge-fixture/repo/calc.py",
// "oldString":"    return a - b","newString":"    return a + b"}`), rooted at
// this test's own temp dir with the same relative structure
// (root/repo/calc.py) so the path is genuinely INSIDE the run's cwd, exactly
// as it was for the real capture's own worktree. That capture predates 2.x
// (its key was filePath); the absolute-path form still reaches 2.x as
// input.path when a model names its file that way.
func TestEditHookMatchesRealModelCaptureArgumentShape(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "log.txt")
	bin := writeRecordingBin(t, logPath, "", 0)
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}

	absPath := filepath.Join(root, "repo", "calc.py")
	argsJSON, err := json.Marshal(map[string]any{
		"path":      absPath,
		"oldString": "    return a - b",
		"newString": "    return a + b",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := runEditHarness(t, node, root, "edit", string(argsJSON), `{"metadata":{},"title":"calc.py","output":"Edit applied successfully."}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw against the real-model capture's absolute-path shape, got %q", res.Message)
	}
	got := readLogLines(t, logPath)
	want := []string{"format", "check-version", "test-quality"}
	if !equalStrings(got, want) {
		t.Errorf("spawned verbs = %v, want %v against the real-model capture's absolute filePath", got, want)
	}
}
