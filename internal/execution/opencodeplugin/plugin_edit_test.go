package opencodeplugin

// plugin_edit_test.go (#1642) drives the REAL embedded plugin tree through
// Node, spawning either a small recording fake or the REAL built
// `nightgauge` binary for each row — never a re-implementation of edit.js's
// own decisions in Go — exactly like plugin_gates_test.go's own harness
// (writePluginTreeForGatesTest, requireNode, buildNightgaugeBin).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// editJSPath writes a fresh copy of the embedded plugin tree and returns
// both the entry file (nightgauge.js) and edit.js's own path, mirroring
// writePluginTreeForGatesTest's (entry, gatesPath) shape.
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
// the hook threw (it never should — edit.js's own contract), and the
// (possibly mutated) output object the hook was handed.
type toolExecuteAfterResult struct {
	Threw   bool           `json:"threw"`
	Message string         `json:"message"`
	Output  map[string]any `json:"output"`
}

// nodeToolExecuteAfterDriver drives nightgauge.js's own registered
// `tool.execute.after` hook — the wiring itself (edit.js's optional import,
// exactly as nightgauge.js's optionalHooks loads it), not edit.js's export
// called directly — so a wiring gap (nightgauge.js forgetting to delegate)
// would turn this red the same way plugin_gates_test.go's
// TestCommandExecuteBeforeWiredThroughEntry catches the equivalent gap for
// command.execute.before.
const nodeToolExecuteAfterDriver = `
import { pathToFileURL } from "node:url";

const mod = await import(pathToFileURL(process.env.NG_PLUGIN_PATH).href);
const plugin = mod.NightgaugePlugin || mod.default;
const ctx = { directory: process.env.NG_CWD, worktree: process.env.NG_CWD };
const hooks = await plugin(ctx);

const input = {
  tool: process.env.NG_TOOL || "edit",
  sessionID: "s",
  callID: "c",
  args: JSON.parse(process.env.NG_ARGS || "{}"),
};
const output = JSON.parse(process.env.NG_OUTPUT || '{"output":"original"}');

let result;
try {
  await hooks["tool.execute.after"](input, output);
  result = { threw: false, output };
} catch (e) {
  result = { threw: true, message: String(e && e.message ? e.message : e), output };
}
process.stdout.write(JSON.stringify(result));
`

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

// runEditHarness drives one tool.execute.after call through nightgauge.js's
// own registered hook for an opencode-shaped edit/write args object.
func runEditHarness(t *testing.T, node, cwd, tool, argsJSON, outputJSON, nightgaugeBin string, extraEnv map[string]string) toolExecuteAfterResult {
	t.Helper()
	entry, _ := editJSPath(t)
	driver := writeGatesDriver(t, nodeToolExecuteAfterDriver)
	env := append(os.Environ(),
		"NG_PLUGIN_PATH="+entry,
		"NG_CWD="+cwd,
		"NG_TOOL="+tool,
		"NG_ARGS="+argsJSON,
		"NG_OUTPUT="+outputJSON,
	)
	env = applyHarnessEnv(env, nightgaugeBin, extraEnv)
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
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "printf '%%s\\n' \"$2\" >> %s\n", shellQuote(logPath))
	if sleepOnVerb != "" {
		fmt.Fprintf(&b, "if [ \"$2\" = %s ]; then sleep %d; fi\n", shellQuote(sleepOnVerb), sleepSeconds)
	}
	b.WriteString("exit 0\n")
	if err := os.WriteFile(binPath, []byte(b.String()), 0o700); err != nil {
		t.Fatal(err)
	}
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
		b, err := json.Marshal(map[string]any{"filePath": "a.test.ts", "content": "console.log(1)"})
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

		res := runEditHarness(t, node, root, "edit", `{"filePath":"a.test.ts","oldString":"x","newString":"console.log(1)"}`, `{"output":"orig"}`, bin, env)
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

			argsJSON, err := json.Marshal(map[string]any{"filePath": filePath, "content": "x"})
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
	argsJSON, err := json.Marshal(map[string]any{"filePath": absPath, "content": "expect(true).toBe(true);"})
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
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$2\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$2\" = 'test-quality' ]; then head -c 5120 /dev/zero | tr '\\0' 'w' 1>&2; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(binPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{
		"NIGHTGAUGE_EDIT_HOOK_DIAGNOSTICS": "1",
		"OPENCODE_CONFIG_CONTENT":          `{"formatter":true}`,
	} // skip format
	argsJSON := `{"filePath":"a.test.ts","content":"x"}`
	res := runEditHarness(t, node, root, "write", argsJSON, `{"output":"orig"}`, binPath, env)
	if res.Threw {
		t.Fatalf("want no throw, got %q", res.Message)
	}
	out, _ := res.Output["output"].(string)
	if !strings.HasPrefix(out, "orig") {
		t.Errorf("output.output = %q, want it to still start with the original tool output", out)
	}
	appended := len(out) - len("orig")
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
	argsJSON := `{"filePath":"a.test.ts","content":"x"}`
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
		script := "#!/bin/sh\n" +
			"if [ \"$2\" = 'test-quality' ]; then printf 'quality warning\\n' 1>&2; fi\n" +
			"exit 0\n"
		if err := os.WriteFile(binPath, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
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
		script := "#!/bin/sh\n" +
			"if [ \"$2\" = 'test-quality' ]; then exit 23; fi\n" +
			"exit 0\n"
		if err := os.WriteFile(binPath, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
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
	argsJSON := `{"filePath":"a.test.ts","content":"x"}`
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

func TestEditHookAppendsRealTestQualityWarnings(t *testing.T) {
	node := requireNode(t)
	bin := buildNightgaugeBin(t)
	root := t.TempDir()

	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":true}`} // skip format; no formatter binaries required
	argsJSON, err := json.Marshal(map[string]any{
		"filePath": "a.test.ts",
		"content":  "it(\"does nothing\", () => {})\nexpect(true).toBe(true);",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := runEditHarness(t, node, root, "write", string(argsJSON), `{"output":"original tool output"}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw, got %q", res.Message)
	}
	out, _ := res.Output["output"].(string)
	if !strings.HasPrefix(out, "original tool output") {
		t.Fatalf("output.output = %q, want it to still start with the original tool output", out)
	}
	for _, want := range []string{"Tautological assertion detected", "Empty test body detected"} {
		if !strings.Contains(out, want) {
			t.Errorf("output.output does not contain the real test-quality.sh-equivalent warning %q:\n%s", want, out)
		}
	}
}

// TestEditHookNoFileEditedRegistration is the AC's own "a formatter write
// does not re-run the edit hooks" bullet: nightgauge.js registers no
// `file.edited` hook at all — there is nothing for a formatter write's own
// `file.edited` event to reach — so emitting one spawns nothing. This is
// tested at the wiring level (the registered hooks object itself carries no
// such key), which is what would actually change if a future edit ever added
// one.
func TestEditHookNoFileEditedRegistration(t *testing.T) {
	node := requireNode(t)
	entry, _ := editJSPath(t)
	driver := writeGatesDriver(t, `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.env.NG_PLUGIN_PATH).href);
const plugin = mod.NightgaugePlugin || mod.default;
const hooks = await plugin({ directory: process.env.NG_CWD, worktree: process.env.NG_CWD });
process.stdout.write(JSON.stringify(Object.keys(hooks)));
`)
	cmd := exec.Command(node, driver)
	cmd.Env = append(os.Environ(), "NG_PLUGIN_PATH="+entry, "NG_CWD="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("node harness failed: %v\nstderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("node harness failed: %v", err)
	}
	var keys []string
	if err := json.Unmarshal(out, &keys); err != nil {
		t.Fatalf("node harness printed non-JSON: %s (%v)", out, err)
	}
	for _, k := range keys {
		if k == "file.edited" {
			t.Fatalf("nightgauge.js registers a file.edited hook (%v); a formatter write must never re-trigger the edit hooks", keys)
		}
	}
}

// --- guardrail: replaying 1.18.30's real tool.execute.after argument shape
// (captured by this issue's own offline probe against the pinned binary —
// see edit.js's header comment). The stub fixture below (tool-edit-stop)
// supplies its OWN relative filePath ("calc.py") — that is a property of the
// stub, not of 1.18.30's tool schemas, which require an absolute filePath;
// TestEditHookMatchesRealModelCaptureArgumentShape below replays the
// absolute-path shape this repository's own real-model captures show
// (internal/execution/testdata/opencode_stream_local_capture.jsonl and
// siblings), which is what a schema-conformant model actually sends. ---

func TestEditHookMatches1_18_30ArgumentShape(t *testing.T) {
	node := requireNode(t)
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "log.txt")
	bin := writeRecordingBin(t, logPath, "", 0)
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"formatter":false}`}

	// Captured verbatim from the probe: an `edit` call's real input/output
	// shape against opencode 1.18.30 (tool-edit-stop fixture).
	const capturedEditInput = `{"filePath":"calc.py","newString":"return a - b","oldString":"return a + b"}`
	res := runEditHarness(t, node, root, "edit", capturedEditInput, `{"metadata":{},"title":"calc.py","output":"Edit applied successfully."}`, bin, env)
	if res.Threw {
		t.Fatalf("want no throw against the captured 1.18.30 edit shape, got %q", res.Message)
	}
	got := readLogLines(t, logPath)
	want := []string{"format", "check-version", "test-quality"}
	if !equalStrings(got, want) {
		t.Errorf("spawned verbs = %v, want %v against the captured 1.18.30 edit shape", got, want)
	}

	// Captured verbatim from the probe: a `write` call's real input/output
	// shape against opencode 1.18.30 (write-then-stop fixture).
	logPath2 := filepath.Join(t.TempDir(), "log2.txt")
	bin2 := writeRecordingBin(t, logPath2, "", 0)
	const capturedWriteInput = `{"content":"print(1)\n","filePath":"new.py"}`
	res2 := runEditHarness(t, node, root, "write", capturedWriteInput, `{"title":"new.py","metadata":{},"output":"Wrote file successfully."}`, bin2, env)
	if res2.Threw {
		t.Fatalf("want no throw against the captured 1.18.30 write shape, got %q", res2.Message)
	}
	if got2 := readLogLines(t, logPath2); !equalStrings(got2, want) {
		t.Errorf("spawned verbs = %v, want %v against the captured 1.18.30 write shape", got2, want)
	}
}

// TestEditHookMatchesRealModelCaptureArgumentShape replays the ABSOLUTE
// filePath shape this repository's own real-model captures show for a real
// `edit` call (internal/execution/testdata/opencode_stream_local_capture.jsonl:
// `"tool":"edit"... "input":{"filePath":"/tmp/nightgauge-fixture/repo/calc.py",
// "oldString":"    return a - b","newString":"    return a + b"}`), rooted at
// this test's own temp dir with the same relative structure
// (root/repo/calc.py) so the path is genuinely INSIDE the run's cwd, exactly
// as it was for the real capture's own worktree. Unlike
// TestEditHookMatches1_18_30ArgumentShape's stub fixture (a relative
// "calc.py", an artifact of the stub's own tool implementation), this is
// what a schema-conformant model — which 1.18.30's own tool schemas require
// to send an absolute filePath — actually produces.
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
		"filePath":  absPath,
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
