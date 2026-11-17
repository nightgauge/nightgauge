//go:build opencode_integration

package opencodeplugin

// The cases here drive the shipped plugin through the real OpenCode 2.x
// binary's own plugin loader (#3302), not the fake host the rest of this
// package's tests use: the fake host proves what the plugin does with what
// it is handed; these prove opencode hands it that, and acts on the result.
// Each run is offline and isolated: the repository's stub provider
// (cmd/stub-provider) on loopback serves the scripted `@opencode2` turns, and
// opencode runs under `env -i`-style variables with a throwaway HOME, TMPDIR
// and XDG roots, OPENCODE_DISABLE_PROJECT_CONFIG=1 and --standalone. The run
// gets its own process group, which cleanup kills and verifies dead, as it
// does the stub.
//
//	go test -tags opencode_integration ./internal/execution/opencodeplugin/ -count=1 -v
//
// A 2.x plugin needs no package dependency, so no case seeds or installs
// @opencode-ai/plugin, and none makes a registry request.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// realOpenCodeForPluginTest returns the opencode on PATH, which must be the
// version the adapter manifest records as max_tested: these cases were
// observed on it. A missing binary skips outside CI and fails in CI.
func realOpenCodeForPluginTest(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("opencode")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("opencode is not on PATH, and CI must run this case: scripts/opencode-integration.sh installs it")
		}
		t.Skip("opencode is not on PATH")
	}
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	m, ok := adaptercompat.Get("opencode")
	if !ok {
		t.Fatal("the adapter manifest has no opencode entry")
	}
	if v := strings.TrimSpace(string(out)); v != "opencode v"+m.MaxTested {
		t.Fatalf("%s is installed; these cases were observed on opencode v%s (the manifest's max_tested)", v, m.MaxTested)
	}
	return path
}

// buildStubProviderBin builds cmd/stub-provider for one test.
func buildStubProviderBin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stub-provider")
	cmd := exec.Command("go", "build", "-o", out, moduleRootRelative("cmd/stub-provider"))
	cmd.Dir = moduleRoot()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building cmd/stub-provider: %v\n%s", err, b)
	}
	return out
}

// startStubProvider starts one loopback stub-provider serving script, with
// its PID captured at spawn and killed-and-verified-dead in t.Cleanup.
func startStubProvider(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command(buildStubProviderBin(t), "-script", script, "-listen", "127.0.0.1:0", "-idle-timeout", "120s", "-max-requests", "200")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting stub-provider -script %s: %v", script, err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err := syscall.Kill(pid, 0); err == nil {
			t.Errorf("stub-provider pid %d (-script %s) is still alive after Kill+Wait", pid, script)
		}
	})
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("stub-provider -script %s: reading its base_url line: %v\nstderr:\n%s", script, err, stderr.String())
	}
	var announced struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(line, &announced); err != nil {
		t.Fatalf("stub-provider -script %s printed a non-JSON base_url line %q: %v", script, line, err)
	}
	return announced.BaseURL
}

// realRunOptions shapes one real opencode run.
type realRunOptions struct {
	// script is the stub-provider script; its @opencode2 variant answers.
	script string
	// gateBin is exported as NIGHTGAUGE_BIN; "" leaves it unset.
	gateBin string
	// contextLimit is the stub model's declared context window (0: 200000).
	contextLimit int
	// permissions are appended after a rule allowing everything.
	permissions []map[string]any
	// files are written into the worktree and committed before the run.
	files map[string]string
}

// realRunResult is what a real run left behind.
type realRunResult struct {
	stream     []map[string]any
	stderr     string
	exitErr    error
	worktree   string
	sentinel   string
	nonce      string
	eventsFile string
}

// toolUses returns the stream's tool_use parts' tool and state.
func (r realRunResult) toolUses() []struct {
	Tool  string
	State map[string]any
} {
	var out []struct {
		Tool  string
		State map[string]any
	}
	for _, ev := range r.stream {
		if ev["type"] != "tool_use" {
			continue
		}
		part, _ := ev["part"].(map[string]any)
		tool, _ := part["tool"].(string)
		state, _ := part["state"].(map[string]any)
		out = append(out, struct {
			Tool  string
			State map[string]any
		}{tool, state})
	}
	return out
}

// runRealOpenCode runs `opencode run --standalone --format json` once
// against the stub provider, with the shipped plugin written to a per-run
// directory and named in the config's `plugins` array exactly as
// adapters.InstallNightgaugePlugin names it, and the handshake and events
// variables the adapter sets.
func runRealOpenCode(t *testing.T, real string, opts realRunOptions) realRunResult {
	t.Helper()
	baseURL := startStubProvider(t, opts.script)

	scratch := t.TempDir()
	home := filepath.Join(scratch, "home")
	worktree := filepath.Join(scratch, "work")
	outDir := filepath.Join(scratch, "out")
	for _, d := range []string{home, worktree, outDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		gittest.Run(t, worktree, append([]string{"-c", "user.email=t@example.invalid", "-c", "user.name=t"}, args...)...)
	}
	git("init", "-q", ".")
	for name, content := range opts.files {
		p := filepath.Join(worktree, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "init")

	pluginDir, err := Write(filepath.Join(home, ".config", "opencode", "nightgauge-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	limit := opts.contextLimit
	if limit == 0 {
		limit = 200000
	}
	model := map[string]any{
		"modelID":      "stub-model",
		"package":      "aisdk:@ai-sdk/openai-compatible",
		"limit":        map[string]any{"context": limit, "input": limit, "output": 1000},
		"capabilities": map[string]any{"tools": true},
	}
	permissions := append([]map[string]any{{"action": "*", "resource": "*", "effect": "allow"}}, opts.permissions...)
	cfg, err := json.Marshal(map[string]any{
		"model": map[string]any{"providerID": "stub", "model": "stub-model"},
		"providers": map[string]any{"stub": map[string]any{
			"package":  "aisdk:@ai-sdk/openai-compatible",
			"env":      []string{},
			"settings": map[string]any{"baseURL": baseURL, "apiKey": "placeholder"},
			"models":   map[string]any{"stub-model": model},
		}},
		"permissions": permissions,
		"plugins":     []string{pluginDir},
	})
	if err != nil {
		t.Fatal(err)
	}

	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-3302"
	outputFile := filepath.Join(outDir, "ctx.json")
	eventsFile, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken: EventsPath refused a valid output file")
	}
	sentinel := filepath.Join(outDir, "sentinel.json")
	env := []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + scratch,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".data"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".state"),
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_CONFIG_CONTENT=" + string(cfg),
		EnvNonce + "=" + nonce,
		EnvSentinel + "=" + sentinel,
		EnvPluginPath + "=" + pluginDir,
		"NIGHTGAUGE_OUTPUT_FILE=" + outputFile,
		"NIGHTGAUGE_RUN_ID=" + runID,
		notifyDisableEnv + "=1",
	}
	if opts.gateBin != "" {
		env = append(env, hookBinEnv(opts.gateBin)...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.Command(real, "run", "--standalone", "--format", "json", "-m", "stub/stub-model")
	cmd.Dir = worktree
	cmd.Env = env
	cmd.Stdin = strings.NewReader("go\n")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		if err := syscall.Kill(-pgid, 0); err == nil {
			t.Errorf("opencode process group %d is still alive after SIGKILL", pgid)
		}
	})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var exitErr error
	select {
	case exitErr = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		t.Fatalf("opencode run did not finish within the bound\nstderr:\n%s", stderr.String())
	}

	var stream []map[string]any
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stream line is not JSON: %q", line)
		}
		stream = append(stream, ev)
	}
	return realRunResult{
		stream:     stream,
		stderr:     stderr.String(),
		exitErr:    exitErr,
		worktree:   worktree,
		sentinel:   sentinel,
		nonce:      nonce,
		eventsFile: eventsFile,
	}
}

// writeGateBin writes a stand-in `nightgauge` that answers every gate verb
// with deny (a PreToolUse deny block naming reason) or, for reason "", with
// silence, the documented allow.
func writeGateBin(t *testing.T, reason string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gate.sh")
	script := ""
	if reason != "" {
		block, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason,
		}})
		script += "printf '%s\\n' " + shellQuote(string(block)) + "\n"
	}
	script += "exit 0\n"
	writeShFixture(t, p, script, 0o700)
	return p
}

const calcPy = "def add(a, b):\n    return a + b\n"

// TestPluginLoadsOnRealOpenCode: OpenCode 2.x's loader accepts the embedded
// plugin directory and runs setup before the first tool call: the handshake
// sentinel carries this run's nonce, PluginVersion and HookNames (what the
// manager's VerifyLoaded checks), and an edit the gates allow runs. Reverting
// nightgauge.js to the 1.x `async (ctx) => ({hooks})` export, or Write to
// return the entry file, leaves no sentinel.
func TestPluginLoadsOnRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	res := runRealOpenCode(t, real, realRunOptions{
		script:  "tool-edit-stop",
		gateBin: writeGateBin(t, ""),
		files:   map[string]string{"calc.py": calcPy},
	})
	if res.exitErr != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", res.exitErr, res.stderr)
	}
	data, err := os.ReadFile(res.sentinel)
	if err != nil {
		t.Fatalf("no handshake sentinel at %s (the real loader did not run setup): %v\nstderr:\n%s", res.sentinel, err, res.stderr)
	}
	var s Sentinel
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("the sentinel did not parse as JSON: %v (%s)", err, data)
	}
	if s.Nonce != res.nonce || s.PluginVersion != PluginVersion || strings.Join(s.Hooks, ",") != strings.Join(HookNames, ",") {
		t.Errorf("sentinel = %+v, want nonce %q, plugin_version %q, hooks %v", s, res.nonce, PluginVersion, HookNames)
	}
	uses := res.toolUses()
	if len(uses) != 1 || uses[0].Tool != "edit" || uses[0].State["status"] != "completed" {
		t.Fatalf("tool uses = %+v, want one completed edit", uses)
	}
	got, err := os.ReadFile(filepath.Join(res.worktree, "calc.py"))
	if err != nil || !strings.Contains(string(got), "return a - b") {
		t.Errorf("calc.py = %q (%v), want the allowed edit applied", got, err)
	}
}

// TestGateDeniedWriteIsRefusedOnRealOpenCode (#3302 AC 3, end to end): when a
// gate verb denies an edit, the plugin's execute.before throw blocks the tool
// on the real binary. The stream's tool_use ends status "error" with the
// gate's marker and reason as the error the model reads, and the file is
// unchanged. Registering the gates under a name 2.x does not fire (the 1.x
// "tool.execute.before") turns this red: the edit then completes.
func TestGateDeniedWriteIsRefusedOnRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	res := runRealOpenCode(t, real, realRunOptions{
		script:  "tool-edit-stop",
		gateBin: writeGateBin(t, "integration deny 3302"),
		files:   map[string]string{"calc.py": calcPy},
	})
	if res.exitErr != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", res.exitErr, res.stderr)
	}
	uses := res.toolUses()
	if len(uses) != 1 || uses[0].Tool != "edit" {
		t.Fatalf("tool uses = %+v, want one edit", uses)
	}
	if uses[0].State["status"] != "error" {
		t.Fatalf("edit status = %v, want error: the gate's deny did not block the tool", uses[0].State["status"])
	}
	msg, _ := uses[0].State["error"].(string)
	if !strings.HasPrefix(msg, "[nightgauge-gate:workflow]") || !strings.Contains(msg, "integration deny 3302") {
		t.Errorf("edit error = %q, want the [nightgauge-gate:workflow] marker and the gate's reason", msg)
	}
	got, err := os.ReadFile(filepath.Join(res.worktree, "calc.py"))
	if err != nil || string(got) != calcPy {
		t.Errorf("calc.py = %q (%v), want it unchanged", got, err)
	}
}

// TestSubagentDeniedOnRealOpenCode: a `subagent` call (1.x's `task`) is
// refused on the real binary with the subagent-denied marker, before any
// subagent session runs, so the scripted subagent's shell command never runs.
func TestSubagentDeniedOnRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	res := runRealOpenCode(t, real, realRunOptions{
		script:  "task-then-subagent-bash",
		gateBin: writeGateBin(t, ""),
	})
	if res.exitErr != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", res.exitErr, res.stderr)
	}
	uses := res.toolUses()
	if len(uses) != 1 || uses[0].Tool != "subagent" {
		t.Fatalf("tool uses = %+v, want one subagent call", uses)
	}
	msg, _ := uses[0].State["error"].(string)
	if uses[0].State["status"] != "error" || !strings.HasPrefix(msg, "[nightgauge-gate:subagent-denied]") {
		t.Errorf("subagent state = %v, want status error with the [nightgauge-gate:subagent-denied] marker", uses[0].State)
	}
}

// TestSessionEventsOnRealOpenCode: the event subscription receives 2.x's bus
// events on the real binary. A rejected `ask` records a permission_ask event
// naming only the action (never the command text), and the session's
// terminal event records "idle" and hands stop-verify its own line.
func TestSessionEventsOnRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	bin := buildNightgaugeBin(t)
	res := runRealOpenCode(t, real, realRunOptions{
		script:      "bash-then-stop",
		gateBin:     bin,
		permissions: []map[string]any{{"action": "shell", "resource": "*", "effect": "ask"}},
		files:       map[string]string{"calc.py": calcPy},
	})
	if res.exitErr != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", res.exitErr, res.stderr)
	}
	if _, err := WaitForRunEvent(res.eventsFile, "stop_verify", stopVerifyWaitBound); err != nil {
		t.Fatalf("no stop_verify event: %v", err)
	}
	events, err := ReadRunEvents(res.eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
		if ev.Kind == "permission_ask" && ev.Detail["permission_type"] != "shell" {
			t.Errorf("permission_ask detail = %v, want permission_type shell only", ev.Detail)
		}
	}
	if kinds["permission_ask"] != 1 || kinds["idle"] != 1 || kinds["stop_verify"] != 1 {
		t.Errorf("event kinds = %v, want one permission_ask, one idle and one stop_verify", kinds)
	}
	raw, err := os.ReadFile(res.eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "calc.py") {
		t.Errorf("the events file carries the asked command's text:\n%s", raw)
	}
}

// TestCompactionOnRealOpenCode: a run that crosses its context window records
// a "compaction" event from session.compaction.started on the real binary,
// and the compaction hook runs without failing the run's other handling (the
// terminal event still records "idle").
func TestCompactionOnRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	bin := buildNightgaugeBin(t)
	res := runRealOpenCode(t, real, realRunOptions{
		script:       "compaction-probe",
		gateBin:      bin,
		contextLimit: 12000,
	})
	// The stub's summary does not match 2.x's compaction template, so the
	// run ends on compaction.failed; only the plugin's records matter here.
	if _, err := WaitForRunEvent(res.eventsFile, "stop_verify", stopVerifyWaitBound); err != nil {
		t.Fatalf("no stop_verify event: %v\nstderr:\n%s", err, res.stderr)
	}
	n, err := CompactionCount(res.eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("CompactionCount = %d, want at least 1", n)
	}
}

// TestNightgaugeNeverWritesAnOperatorToolDirectory (#1635/A11 round 6,
// ADR-022 amendment 2026-09-15, narrowed AC1): an operator-owned OpenCode
// config directory holding a custom tool that imports @opencode-ai/plugin —
// OpenCode's own documented way to write one
// (`import { tool } from "@opencode-ai/plugin"`) — is never touched by
// Nightgauge at all: no dependency seed (removed, #3306),
// no MergeDependencies (removed this round), nothing. Round 5's merge tried
// to keep such a directory both "satisfied" and import-resolvable at once
// and a review found it fragile; round 6 does not attempt that trade at
// all — the directory is exactly as the operator left it before and after a
// dispatch touches a config naming it, proven here with no opencode spawn
// and so no registry request, real or stubbed.
func TestNightgaugeNeverWritesAnOperatorToolDirectory(t *testing.T) {
	home := t.TempDir()
	xdgConfigHome := filepath.Join(home, ".config")
	configDir := filepath.Join(xdgConfigHome, "opencode")
	toolDir := filepath.Join(configDir, "tool")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolSrc := "import { tool } from \"@opencode-ai/plugin\"\n" +
		"export default tool({\n" +
		"  description: \"hello\",\n" +
		"  args: {},\n" +
		"  async execute() { return \"hi\" },\n" +
		"})\n"
	toolPath := filepath.Join(toolDir, "hello.ts")
	if err := os.WriteFile(toolPath, []byte(toolSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}

	// The one thing InstallNightgaugePlugin's caller (adapters) does with
	// this kind of directory is read whether it already satisfies the pin —
	// never write it, whatever the answer.
	if OperatorInstallSatisfied(configDir) {
		t.Fatal("test premise broken: a directory with no node_modules at all must not read as satisfied")
	}

	after, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the operator's own tool file changed:\nbefore=%s\nafter=%s", before, after)
	}
	if entries, err := os.ReadDir(configDir); err != nil || len(entries) != 1 || entries[0].Name() != "tool" {
		t.Errorf("the operator directory gained or lost entries: entries=%v err=%v, want only the pre-existing tool/", entries, err)
	}
}
