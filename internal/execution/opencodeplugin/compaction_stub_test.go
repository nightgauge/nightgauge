//go:build opencode_integration

package opencodeplugin

// TestCompactionAutocontinueSuppressionAgainstRealOpenCode drives the pinned
// opencode 1.18.30 binary through a real compaction cycle against two
// instances of #1618's deterministic, offline, loopback stub provider (never
// a live model, never network egress): one ("compaction-probe") scripted
// with enough padded bash turns to push the conversation past a small
// declared limit.context (12000), the other ("compaction-summary") scripted
// to answer the compaction agent's own summarization call with plain content
// so the compaction actually SUCCEEDS, rather than merely being attempted
// (see the file's own package-level comment on plugin_integration_test.go
// for why no test here may reach a real registry: WriteDependencies seeds
// the run's XDG config directory before opencode ever reads it).
//
//	go test -tags opencode_integration ./internal/execution/opencodeplugin/ -run TestCompactionAutocontinueSuppressionAgainstRealOpenCode -count=1 -v
//
// Compaction routes to a SEPARATE stub instance (a distinct provider id,
// lmstudio-remote) rather than the growth stub's own next scripted turn,
// because this suite's own probe (recorded in this comment, not re-derived
// at every run) found the compaction agent's summarization request always
// carries zero prior assistant messages of its own — so if it shared the
// growth stub's turn-counting script, it would always land on that script's
// turn 0, which must stay a bash tool call for the growth turns to work at
// all. opencode 1.18.30 refuses a tool call while generating a compaction
// summary ("Tool call not allowed while generating summary: <tool>"), so a
// shared script cannot serve both roles: this was observed directly against
// the pinned binary (not assumed), and is why the config below gives
// compaction and summary their own model id, on their own baseURL.
//
// Two assumptions from ADR-022 (quoted from #1641's own issue text) are
// confirmed here, empirically, every time this test runs: (1)
// experimental.session.compacting lets a plugin add compaction context —
// this suite's own probe read output-before={"context":[]} directly off the
// real binary, matching @opencode-ai/plugin's own bundled type declarations
// for opencode 1.18.30; (2) experimental.compaction.autocontinue can disable
// the continue turn — this test's own green case is that proof, and its red
// case (below) is the negative control. Two further ADR-022 assumptions —
// that session.compacted and session.idle reach the plugin event hook, and
// that they reach it for child sessions too — are each confirmed for a
// parent session by this same run (the "event session.compacted" line and
// the "compaction" event this test asserts on); child-session propagation is
// NOT verified here or anywhere else in this codebase yet, because AC9
// (gates.js, AC9's own doc comment) denies every "task" tool call
// unconditionally, so no child session can currently reach any hook this
// plugin registers. That gap is inherited from AC9, not introduced here.
import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// compactionStubBudget bounds the whole opencode run: the probe this test's
// config was derived from completed in ~1-2s once compaction routed to its
// own stub, so 45s leaves wide headroom without masking a real hang as a
// slow pass.
const compactionStubBudget = 45 * time.Second

// buildStubProviderBin builds cmd/stub-provider once per test process, the
// same pattern buildNightgaugeBin (plugin_test.go) uses for cmd/nightgauge.
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

// stubInstance is one running stub-provider process, loopback-only, with its
// PID captured at spawn and killed-and-verified-dead in t.Cleanup, per this
// workspace's own rule (AGENTS.md: "Capture every background process PID at
// spawn, kill that PID, and verify it is dead").
type stubInstance struct {
	baseURL string
	cmd     *exec.Cmd
	pid     int
}

func startStubInstance(t *testing.T, bin, script string) stubInstance {
	t.Helper()
	cmd := exec.Command(bin, "-script", script, "-listen", "127.0.0.1:0", "-idle-timeout", "120s", "-max-requests", "500")
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

	line := make([]byte, 0, 128)
	buf := make([]byte, 1)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			t.Fatalf("stub-provider -script %s: reading its base_url line: %v\nstderr:\n%s", script, err, stderr.String())
		}
	}
	var announced struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(line, &announced); err != nil {
		t.Fatalf("stub-provider -script %s printed a non-JSON base_url line %q: %v", script, line, err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		// Signal(0) is the portable "is this pid still alive" probe: it sends
		// no actual signal, only reports whether the target could be
		// signalled at all. Verifying the kill, not merely issuing it, is
		// this workspace's own rule (AGENTS.md).
		if err := cmd.Process.Signal(syscall.Signal(0)); err == nil {
			t.Errorf("stub-provider pid %d (-script %s) is still alive after Kill+Wait", pid, script)
		}
	})

	return stubInstance{baseURL: announced.BaseURL, cmd: cmd, pid: pid}
}

// compactionStubConfig is the exact per-run OPENCODE_CONFIG_CONTENT this
// suite's own offline probe validated against the real 1.18.30 binary:
// two provider ids (growth and summary/compaction get separate ones, see
// the file header), a small declared limit.context (12000) so a handful of
// padded bash turns cross the compaction threshold, and title generation
// disabled — mirroring adapters.BuildOpenCodeConfig's own agent.title.disable
// (internal/execution/adapters/opencode_config.go), which this test cannot
// import (opencodeplugin is imported BY adapters; importing it back would
// cycle), so the shape is reproduced by hand instead.
func compactionStubConfig(pluginEntry, growthBaseURL, summaryBaseURL string) []byte {
	agent := func(model string, steps int, disable bool) map[string]any {
		m := map[string]any{"model": model}
		if steps > 0 {
			m["steps"] = steps
		}
		if disable {
			m["disable"] = true
		}
		return m
	}
	cfg := map[string]any{
		"$schema":           "https://opencode.ai/config.json",
		"plugin":            []string{pluginEntry},
		"share":             "disabled",
		"autoupdate":        false,
		"enabled_providers": []string{"lmstudio", "lmstudio-remote"},
		"provider": map[string]any{
			"lmstudio": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"options": map[string]any{"baseURL": growthBaseURL},
				"models": map[string]any{
					"stub-model": map[string]any{
						"name":  "stub-model",
						"limit": map[string]any{"context": 12000, "input": 12000, "output": 2000},
					},
				},
			},
			"lmstudio-remote": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"options": map[string]any{"baseURL": summaryBaseURL},
				"models": map[string]any{
					"stub-model": map[string]any{
						"name":  "stub-model",
						"limit": map[string]any{"context": 12000, "input": 12000, "output": 2000},
					},
				},
			},
		},
		"agent": map[string]any{
			"build":      agent("lmstudio/stub-model", 8, false),
			"plan":       agent("lmstudio/stub-model", 8, false),
			"general":    agent("lmstudio/stub-model", 8, false),
			"explore":    agent("lmstudio/stub-model", 8, false),
			"title":      agent("lmstudio/stub-model", 0, true),
			"summary":    agent("lmstudio-remote/stub-model", 0, false),
			"compaction": agent("lmstudio-remote/stub-model", 0, false),
		},
		"mode": map[string]any{
			"build":      agent("lmstudio/stub-model", 8, false),
			"plan":       agent("lmstudio/stub-model", 8, false),
			"title":      agent("lmstudio/stub-model", 0, true),
			"summary":    agent("lmstudio-remote/stub-model", 0, false),
			"compaction": agent("lmstudio-remote/stub-model", 0, false),
		},
		"compaction": map[string]any{
			"auto": true, "prune": true, "reserved": 2000, "tail_turns": 2, "preserve_recent_tokens": 2500,
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err) // unreachable: cfg is built from static, marshalable values
	}
	return raw
}

// compactionStubHome isolates HOME/XDG exactly like
// plugin_integration_test.go's TestPluginLoadsOnRealOpenCode: a throwaway
// HOME, the embedded plugin tree and its dependency seed written before
// opencode ever reads them, and npm pointed at a closed loopback port as a
// belt-and-suspenders check on top of that seed.
type compactionStubHome struct {
	home, xdgConfigHome, pluginEntry string
}

func newCompactionStubHome(t *testing.T) compactionStubHome {
	t.Helper()
	home := t.TempDir()
	xdgConfigHome := filepath.Join(home, ".config")
	pluginDir := filepath.Join(xdgConfigHome, "opencode", "nightgauge-plugin")
	entry, err := Write(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDependencies(filepath.Join(xdgConfigHome, "opencode")); err != nil {
		t.Fatal(err)
	}
	return compactionStubHome{home: home, xdgConfigHome: xdgConfigHome, pluginEntry: entry}
}

// runCompactionStub runs one `opencode run` against the compaction fixture
// and returns its exit status, stderr, the events file path, and the
// session id opencode assigned (parsed off stderr's own "created id=..."
// startup line, the same field TestOpenCodeIntegration* cases rely on
// opencode's own --print-logs to expose).
type compactionStubResult struct {
	exitErr    error
	stderr     string
	eventsPath string
	sessionID  string
	runDir     string
	real       string
	home       compactionStubHome
}

func runCompactionStub(t *testing.T, real string, growth, summary stubInstance) compactionStubResult {
	t.Helper()
	sh := newCompactionStubHome(t)
	projectDir := t.TempDir()
	runDir := t.TempDir()
	outputFile := filepath.Join(runDir, "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "compaction-stub-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	sentinelPath := filepath.Join(runDir, "sentinel.json")
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}

	configContent := compactionStubConfig(sh.pluginEntry, growth.baseURL, summary.baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), compactionStubBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, "run", "please do the task, using bash as needed",
		"-m", "lmstudio/stub-model", "--agent", "build", "--print-logs", "--log-level", "DEBUG")
	cmd.Dir = projectDir
	cmd.Env = []string{
		"HOME=" + sh.home,
		"PATH=/usr/bin:/bin",
		"XDG_CONFIG_HOME=" + sh.xdgConfigHome,
		"XDG_DATA_HOME=" + filepath.Join(sh.home, ".data"),
		"XDG_CACHE_HOME=" + filepath.Join(sh.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(sh.home, ".state"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_CONFIG_CONTENT=" + string(configContent),
		pluginIntegrationNoRegistry,
		EnvNonce + "=" + nonce,
		EnvSentinel + "=" + sentinelPath,
		EnvPluginPath + "=" + sh.pluginEntry,
		"NIGHTGAUGE_BIN=" + buildNightgaugeBin(t),
		"NIGHTGAUGE_OUTPUT_FILE=" + outputFile,
		"NIGHTGAUGE_RUN_ID=" + runID,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	exitErr := cmd.Run()

	eventsPath, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken: EventsPath refused this test's own absolute output file")
	}

	sessionID := ""
	for _, line := range strings.Split(stderr.String(), "\n") {
		if idx := strings.Index(line, "message=created id="); idx >= 0 {
			rest := line[idx+len("message=created id="):]
			sessionID = strings.Fields(rest)[0]
			break
		}
	}

	return compactionStubResult{
		exitErr:    exitErr,
		stderr:     stderr.String(),
		eventsPath: eventsPath,
		sessionID:  sessionID,
		runDir:     runDir,
		real:       real,
		home:       sh,
	}
}

// exportSanitized runs `opencode export --sanitize <sessionID>` in the same
// isolated HOME/XDG environment the run itself used, so it reads that run's
// own session store rather than any other. It is a read-only follow-up call,
// never the run under test.
func exportSanitized(t *testing.T, result compactionStubResult) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, result.real, "export", "--sanitize", result.sessionID)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"HOME=" + result.home.home,
		"PATH=/usr/bin:/bin",
		"XDG_CONFIG_HOME=" + result.home.xdgConfigHome,
		"XDG_DATA_HOME=" + filepath.Join(result.home.home, ".data"),
		"XDG_CACHE_HOME=" + filepath.Join(result.home.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(result.home.home, ".state"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		pluginIntegrationNoRegistry,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opencode export --sanitize %s: %v\n%s", result.sessionID, err, out)
	}
	return string(out)
}

const continueMarker = "Continue if you have next steps"

// TestCompactionAutocontinueSuppressionAgainstRealOpenCode is #1641's own
// compaction_stub_test.go verification bullet: opencode 1.18.30 against the
// #1618 stub, limit.context 12000, steps cap 8; the run exits within budget,
// `opencode export --sanitize` carries no synthetic continue turn, and the
// events file holds exactly one compaction event. Backing the fix out (see
// TestCompactionAutocontinueSuppressionRedGreen) turns this red: the
// synthetic continue turn reappears and a second compaction cycle follows
// it.
func TestCompactionAutocontinueSuppressionAgainstRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	stubBin := buildStubProviderBin(t)
	growth := startStubInstance(t, stubBin, "compaction-probe")
	summary := startStubInstance(t, stubBin, "compaction-summary")

	result := runCompactionStub(t, real, growth, summary)
	assertCompactionStubGreen(t, result)
}

// TestPermissionAskEventAgainstRealOpenCode drives the pinned opencode
// 1.18.30 binary with `permission: {bash: "ask"}` — the config production
// dispatch takes whenever careful mode's own gate is not what is asking —
// against the #1618 stub's single-bash-call fixture, and asserts the run's
// events file gains exactly one permission_ask event carrying only
// detail.permission_type ("bash"), never the command text opencode's own
// permission.asked bus event otherwise carries in `patterns`/`metadata`.
// This is the AC5/AC6 assertion the review found missing: the plugin's own
// `permission.ask` hook is never called by this binary at all (0
// occurrences of the literal "permission.ask" in its own trigger sites, see
// session.js's permissionAsk comment); only event()'s permission.asked
// branch reaches production. Reverting that branch (or reverting it back to
// reading input.type from a permission.ask call that never happens) turns
// this red: opencode's own stderr still shows "message=asking ... permission=bash"
// and "permission requested: bash ...; auto-rejecting", but the events file
// gains no permission_ask line.
func TestPermissionAskEventAgainstRealOpenCode(t *testing.T) {
	real := realOpenCodeForPluginTest(t)
	stubBin := buildStubProviderBin(t)
	growth := startStubInstance(t, stubBin, "bash-then-stop")

	sh := newCompactionStubHome(t)
	projectDir := t.TempDir()
	runDir := t.TempDir()
	outputFile := filepath.Join(runDir, "output", "run.json")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "permask-stub-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(compactionStubConfig(sh.pluginEntry, growth.baseURL, growth.baseURL), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["permission"] = map[string]any{"bash": "ask"}
	configContent, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), compactionStubBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, real, "run", "please do the task, using bash as needed",
		"-m", "lmstudio/stub-model", "--agent", "build", "--print-logs", "--log-level", "DEBUG")
	cmd.Dir = projectDir
	cmd.Env = []string{
		"HOME=" + sh.home,
		"PATH=/usr/bin:/bin",
		"XDG_CONFIG_HOME=" + sh.xdgConfigHome,
		"XDG_DATA_HOME=" + filepath.Join(sh.home, ".data"),
		"XDG_CACHE_HOME=" + filepath.Join(sh.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(sh.home, ".state"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_CONFIG_CONTENT=" + string(configContent),
		pluginIntegrationNoRegistry,
		EnvNonce + "=" + nonce,
		EnvSentinel + "=" + filepath.Join(runDir, "sentinel.json"),
		EnvPluginPath + "=" + sh.pluginEntry,
		"NIGHTGAUGE_BIN=" + buildNightgaugeBin(t),
		"NIGHTGAUGE_OUTPUT_FILE=" + outputFile,
		"NIGHTGAUGE_RUN_ID=" + runID,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// The stub's one bash call is auto-rejected (no interactive UI attached),
	// so the run itself is expected to exit non-zero — only the events file
	// and stderr's own permission trace are asserted on.
	_ = cmd.Run()

	if !strings.Contains(stderr.String(), "permission=bash") {
		t.Fatalf("test premise broken: stderr never shows opencode asking for bash permission:\n%s", stderr.String())
	}

	eventsPath, ok := EventsPath(outputFile, runID)
	if !ok {
		t.Fatal("test premise broken: EventsPath refused this test's own absolute output file")
	}
	events, err := ReadRunEvents(eventsPath)
	if err != nil {
		t.Fatalf("reading events file %s: %v", eventsPath, err)
	}
	permAsks := 0
	for _, e := range events {
		if e.Kind != "permission_ask" {
			continue
		}
		permAsks++
		if e.Detail["permission_type"] != "bash" {
			t.Errorf("permission_ask detail.permission_type = %v, want bash", e.Detail["permission_type"])
		}
		if _, hasPatterns := e.Detail["patterns"]; hasPatterns {
			t.Errorf("permission_ask detail carries patterns (model-authored command text); must never be recorded")
		}
	}
	if permAsks != 1 {
		t.Fatalf("got %d permission_ask events, want exactly 1: %+v", permAsks, events)
	}
}

// loopStepsWantMax bounds this fixture's own loop-step count. #1625's steps
// cap (8, set on every agent in compactionStubConfig) is NOT the hard stop
// AC2 and ADR-022 assumed: on a step at or past the cap, 1.18.30 only appends
// an assistant nudge message and still passes every tool, so the run keeps
// making bash tool calls past the cap rather than stopping at it (measured
// directly off the pinned binary's own decompiled loop condition). Against
// this fixture's own deterministic, offline, scripted turns, a green run
// consistently logs 13 loop steps (steps 8-11 each still call bash, one past
// the declared cap of 8) — never the wide, cap-agnostic slack of 20 an
// earlier version of this bound allowed, which is loose enough to hide that
// contradiction entirely. This divergence is recorded in ADR-022 ("Nightgauge
// OpenCode plugin" amendment) and on #1625; AC2's "within #1625's steps cap"
// wording is inaccurate for 1.18.30 and should be read as "within the
// autocontinue-suppression's own bound", not #1625's literal cap. The bound
// below still meaningfully guards the autocontinue-suppression regression
// this test exists for: TestCompactionAutocontinueSuppressionRedGreen's
// negative control (autocontinue suppression removed) logged 311+ loop steps
// in the same fixture, over 20x this bound.
const loopStepsWantMax = 16

// assertCompactionStubGreen is the green-path assertion set, factored out so
// the red/green procedure (below) can run it against both the fixed and the
// backed-out session.js from the same call sites.
func assertCompactionStubGreen(t *testing.T, result compactionStubResult) {
	t.Helper()
	if result.exitErr != nil {
		t.Fatalf("opencode run: %v\nstderr:\n%s", result.exitErr, result.stderr)
	}
	if result.sessionID == "" {
		t.Fatalf("could not find the session id in stderr:\n%s", result.stderr)
	}
	if n := strings.Count(result.stderr, "message=loop"); n > loopStepsWantMax {
		t.Errorf("the run logged %d loop steps, want at most %d (see loopStepsWantMax's own comment on why #1625's steps cap of 8 is not itself the bound); a runaway continuation is exactly what compaction.autocontinue suppression exists to prevent", n, loopStepsWantMax)
	}

	events, err := ReadRunEvents(result.eventsPath)
	if err != nil {
		t.Fatalf("reading events file %s: %v", result.eventsPath, err)
	}
	compactions, idles, stopVerifies := 0, 0, 0
	for _, e := range events {
		switch e.Kind {
		case "compaction":
			compactions++
		case "idle":
			idles++
		case "stop_verify":
			stopVerifies++
		}
	}
	if compactions != 1 {
		t.Errorf("got %d compaction events, want exactly 1: %+v", compactions, events)
	}
	if idles != 1 {
		t.Errorf("got %d idle events, want exactly 1: %+v", idles, events)
	}
	if stopVerifies != 1 {
		t.Errorf("got %d stop_verify events, want exactly 1: %+v", stopVerifies, events)
	}

	if strings.Contains(result.stderr, continueMarker) {
		t.Errorf("stderr contains the synthetic continue marker %q; autocontinue was not suppressed", continueMarker)
	}

	exported := exportSanitized(t, result)
	if strings.Contains(exported, continueMarker) {
		t.Errorf("`opencode export --sanitize` contains the synthetic continue marker %q; autocontinue was not suppressed", continueMarker)
	}
}
