package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hometest"
	"github.com/nightgauge/nightgauge/internal/models"
)

// openCodeIntegrationBuild is set when the package is built with the
// opencode_integration tag (opencode_preflight_integration_test.go), whose
// tests run the real binary.
var openCodeIntegrationBuild bool

// TestMain isolates HOME for the whole package, and, unless the package is
// built to run the real binary, puts a fake opencode first on PATH that
// answers `--version` with the compat manifest's max-tested version and
// nothing else. PreDispatch holds the binary to the version policy, so every
// test that dispatches with the gate open reads a version, and none may read
// it from whatever opencode, if any, the machine running the tests has.
//
// It also makes the forge an OpenCode dispatch reads its MCP servers from
// refuse every read for the whole test binary, so no test reaches GitHub; a
// test that reads servers sets req.McpForge itself (withMcpForge). And it
// makes discovery of a local model's limits find nothing, so no test asks
// whatever model server this machine runs; a test that discovers swaps in a
// discovery of its own.
func TestMain(m *testing.M) {
	cleanup := hometest.Isolate()
	restorePath := func() {}
	if !openCodeIntegrationBuild {
		restorePath = installPackageOpenCodeFake()
	}
	restoreForge := SwapOpenCodeMcpForgeForTest(mapForge{err: errors.New("the adapters test binary reads no forge: set OpenCodeRunRequest.McpForge")})
	restoreDiscovery := SwapOpenCodeLocalDiscoveryForTest(func(OpenCodeEndpoint, string) (models.LocalDescriptor, error) {
		return models.LocalDescriptor{}, errors.New("the adapters test binary asks no model server: swap in a discovery")
	})
	code := m.Run()
	restoreDiscovery()
	restoreForge()
	restorePath()
	cleanup()
	os.Exit(code)
}

func installPackageOpenCodeFake() func() {
	m, ok := adaptercompat.Get("opencode")
	if !ok {
		fmt.Fprintln(os.Stderr, "opencode_preflight_test: no opencode compat manifest")
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "nightgauge-opencode-fake-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "opencode_preflight_test:", err)
		os.Exit(1)
	}
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = --version ] && { echo %s; exit 0; }\necho \"the package's fake opencode was run with $*\" >&2\nexit 97\n", m.MaxTested)
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "opencode_preflight_test:", err)
		os.Exit(1)
	}
	prev := os.Getenv("PATH")
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+prev)
	return func() {
		_ = os.Setenv("PATH", prev)
		_ = os.RemoveAll(dir)
	}
}

// openCodeManifestForTest is the opencode compat manifest, the single source
// every version these tests use is derived from.
func openCodeManifestForTest(t *testing.T) adaptercompat.Manifest {
	t.Helper()
	m, ok := adaptercompat.Get("opencode")
	if !ok || m.MinVersion == "" || m.MaxTested == "" {
		t.Fatalf("the opencode compat manifest has no floor and max-tested: %+v", m)
	}
	return m
}

// patchStep returns version with its patch number moved by delta.
func patchStep(t *testing.T, version string, delta int) string {
	t.Helper()
	parts := strings.Split(version, ".")
	patch, err := strconv.Atoi(parts[2])
	if err != nil || patch+delta < 0 {
		t.Fatalf("cannot step %q by %d", version, delta)
	}
	parts[2] = strconv.Itoa(patch + delta)
	return strings.Join(parts, ".")
}

// fakeOpenCode is an opencode stand-in that records each invocation's
// arguments, one line each.
type fakeOpenCode struct {
	path string
	log  string
}

// fakeOpenCodeBehavior is the version a fake prints. Any other invocation,
// such as `debug config` or `run --help`, exits 97.
type fakeOpenCodeBehavior struct {
	version string
}

func installFakeOpenCode(t *testing.T, b fakeOpenCodeBehavior) fakeOpenCode {
	t.Helper()
	dir := t.TempDir()
	f := fakeOpenCode{path: filepath.Join(dir, "opencode"), log: filepath.Join(dir, "invocations.log")}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1 $2" in
"--version ") echo %s; exit 0 ;;
esac
exit 97
`, f.log, b.version)
	if err := os.WriteFile(f.path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// invocations returns the argument lines of every run of the fake.
func (f fakeOpenCode) invocations(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func (f fakeOpenCode) count(t *testing.T, args string) int {
	t.Helper()
	n := 0
	for _, line := range f.invocations(t) {
		if line == args {
			n++
		}
	}
	return n
}

// preflightEnv opens the gate and isolates HOME for one test, and returns
// the home directory.
func preflightEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "GH_CONFIG_DIR", "GOCACHE"} {
		t.Setenv(k, "")
	}
	return home
}

// pinnedAdapter is an adapter whose machine-tier block is settings with its
// binary pinned to bin.
func pinnedAdapter(settings config.OpenCodeConfig, bin string) *OpenCodeAdapter {
	settings.Binary = bin
	return &OpenCodeAdapter{managedConfig: []string{}, settings: fixedOpenCodeSettings(settings)}
}

// TestOpenCodeDispatchBelowMinTestedIsIncompatible: a binary below the compat
// manifest's floor is refused before spawn as adapter_incompatible, naming
// both versions and the managed install, and nothing but its `--version` ran.
func TestOpenCodeDispatchBelowMinTestedIsIncompatible(t *testing.T) {
	home := preflightEnv(t)
	m := openCodeManifestForTest(t)
	below := patchStep(t, m.MinVersion, -1)
	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: below})
	a := pinnedAdapter(lmStudioSettings(), fake.path)

	err := a.PreDispatch(context.Background(), RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b"})
	var incompatible *OpenCodeIncompatibleError
	if !errors.As(err, &incompatible) || incompatible.Kind() != "adapter_incompatible" {
		t.Fatalf("PreDispatch = %v, want an adapter_incompatible refusal", err)
	}
	command, _ := OpenCodeManagedInstall(m, home)
	for _, want := range []string{"adapter_incompatible", below, m.MinVersion, command, fake.path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if !strings.Contains(command, "npm i --prefix ~/.nightgauge/tools/opencode opencode-ai@"+m.MaxTested) {
		t.Errorf("the managed install is %q, want the npm install of max-tested %s", command, m.MaxTested)
	}
	if got := fake.invocations(t); len(got) != 1 || got[0] != "--version" {
		t.Errorf("the binary ran %q; a refused dispatch runs only `--version`", got)
	}
	if _, ok, _ := ReadOpenCodeDispatchRecord(home); ok {
		t.Error("a refused dispatch was recorded as the last dispatch")
	}
}

// TestOpenCodeUnreadableVersionIsIncompatible: a version that cannot be read
// cannot be held to the floor, so it fails closed.
func TestOpenCodeUnreadableVersionIsIncompatible(t *testing.T) {
	preflightEnv(t)
	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: "not-a-version"})
	err := pinnedAdapter(lmStudioSettings(), fake.path).PreDispatch(context.Background(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"})
	if err == nil || !strings.Contains(err.Error(), "adapter_incompatible") || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("PreDispatch = %v, want an adapter_incompatible refusal for an unreadable version", err)
	}
	if strings.Contains(err.Error(), "not-a-version") {
		t.Errorf("the refusal quotes what the binary printed: %v", err)
	}
}

// TestOpenCodeAboveMaxTestedDispatchesLikeATestedVersion: max_tested is only
// the version Nightgauge is tested up to (ADR-022 § 20, 2026-09-25
// amendment). A newer opencode dispatches a hosted model and a model server
// the operator runs exactly as a tested one does: no refusal, no warning, and
// no probe beyond the version read. The fake fails any `debug config` or
// `run --help` it is asked for, so a returning self-test gate turns this red.
func TestOpenCodeAboveMaxTestedDispatchesLikeATestedVersion(t *testing.T) {
	m := openCodeManifestForTest(t)
	for _, above := range []string{patchStep(t, m.MaxTested, 1), "99.0.0"} {
		for name, tc := range map[string]struct {
			settings config.OpenCodeConfig
			model    string
		}{
			"endpoint": {lmStudioSettings(), "lmstudio/qwen/qwen3.8-27b"},
			"hosted":   {config.OpenCodeConfig{}, "anthropic/claude-sonnet-5"},
		} {
			home := preflightEnv(t)
			t.Setenv("ANTHROPIC_API_KEY", "set-by-the-test")
			fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: above})
			run := RunOptions{Stage: "feature-dev", Model: tc.model, WorktreeDir: gitInitTestWorktree(t)}
			var err error
			stderr := captureAdapterStderr(t, func() { err = pinnedAdapter(tc.settings, fake.path).PreDispatch(context.Background(), run) })
			if err != nil {
				t.Fatalf("%s on opencode %s: PreDispatch = %v, want the dispatch allowed", name, above, err)
			}
			if strings.Contains(stderr, "max-tested") || strings.Contains(stderr, "newer than") || strings.Contains(stderr, "self-test") {
				t.Errorf("%s on opencode %s warned about the version:\n%s", name, above, stderr)
			}
			if got := fake.count(t, "debug config") + fake.count(t, "run --help"); got != 0 {
				t.Errorf("%s on opencode %s ran %d probe(s) beyond the version read", name, above, got)
			}
			if rec, ok, _ := ReadOpenCodeDispatchRecord(home); !ok || rec.Version != above {
				t.Errorf("%s on opencode %s: dispatch record = %+v, %v; want the version recorded", name, above, rec, ok)
			}
		}
	}
}

// TestOpenCodePinnedBinaryIsCheckedSpawnedAndRecorded: opencode.binary pins
// the binary the version policy reads, the one BuildCommand spawns, and the
// one the dispatch record names.
func TestOpenCodePinnedBinaryIsCheckedSpawnedAndRecorded(t *testing.T) {
	home := preflightEnv(t)
	m := openCodeManifestForTest(t)
	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: m.MaxTested})
	a := pinnedAdapter(lmStudioSettings(), fake.path)
	run := RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: gitInitTestWorktree(t)}
	if err := a.PreDispatch(context.Background(), run); err != nil {
		t.Fatalf("PreDispatch = %v", err)
	}
	if got := fake.invocations(t); len(got) != 1 || got[0] != "--version" {
		t.Errorf("the pinned binary ran %q, want its `--version` once", got)
	}
	rec, ok, err := ReadOpenCodeDispatchRecord(home)
	if err != nil || !ok || rec.Binary != fake.path || rec.Version != m.MaxTested {
		t.Errorf("dispatch record = %+v, %v, %v; want the pin at %s", rec, ok, err, m.MaxTested)
	}

	root, err := a.PrepareRunRoot(RunRootRequest{ID: testRunID, MachineConfigDir: filepath.Join(home, ".nightgauge"), Run: run})
	if err != nil {
		t.Fatalf("PrepareRunRoot = %v", err)
	}
	run.RunRoot = root
	if cmd, _, _ := a.BuildCommand(run); cmd != fake.path {
		t.Errorf("BuildCommand spawns %q, want the pinned %q", cmd, fake.path)
	}
	unpinned := &OpenCodeAdapter{managedConfig: []string{}, settings: fixedOpenCodeSettings(lmStudioSettings())}
	root, err = unpinned.PrepareRunRoot(RunRootRequest{ID: testRunID, MachineConfigDir: filepath.Join(home, ".nightgauge"), Run: run})
	if err != nil {
		t.Fatal(err)
	}
	run.RunRoot = root
	if cmd, _, _ := unpinned.BuildCommand(run); cmd != "opencode" {
		t.Errorf("an unpinned BuildCommand spawns %q, want opencode from PATH", cmd)
	}
}

// TestOpenCodeRejectsARelativeOrUnrunnablePin: a pin that is not the absolute
// path of an executable file is refused, and never looked up on PATH, by the
// version policy and by PrepareRunRoot alike.
func TestOpenCodeRejectsARelativeOrUnrunnablePin(t *testing.T) {
	home := preflightEnv(t)
	notExecutable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\necho 1.18.30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"bin/opencode":   "not an absolute path",
		"opencode":       "not an absolute path",
		notExecutable:    "not executable",
		t.TempDir():      "not a file",
		"/nonexistent/x": "cannot be run",
	}
	for pin, want := range cases {
		a := pinnedAdapter(lmStudioSettings(), pin)
		run := RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}
		if err := a.PreDispatch(context.Background(), run); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PreDispatch with opencode.binary %q = %v, want %q", pin, err, want)
		}
		if _, err := a.PrepareRunRoot(RunRootRequest{ID: testRunID, MachineConfigDir: filepath.Join(home, ".nightgauge"), Run: run}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PrepareRunRoot with opencode.binary %q = %v, want %q", pin, err, want)
		}
	}
}

// TestOpenCodeCatalogSnapshotIsTheMaxTestedVersion ties the catalog snapshots
// credential withholding and output redaction rest on
// (opencode_catalog_env.go, opencode_catalog_anthropic.go) to the version the
// adapter is tested against: raising max_tested without re-reading them from
// the new binary turns this red.
func TestOpenCodeCatalogSnapshotIsTheMaxTestedVersion(t *testing.T) {
	m := openCodeManifestForTest(t)
	if openCodeCatalogVersion != m.MaxTested {
		t.Fatalf("the catalog snapshots were read from opencode %s, and the compat manifest's max_tested is %s: re-read openCodeCatalogEnv and openCodeAnthropicModels from %s (build tag opencode_integration), then set openCodeCatalogVersion",
			openCodeCatalogVersion, m.MaxTested, m.MaxTested)
	}
}

// TestOpenCodeProbeRunsInItsOwnDirectory: a probe spawn gets HOME, TMPDIR and
// the four XDG directories inside its own directory, the switches every spawn
// sets, project config off, and nothing else of the environment but PATH, a
// credential least of all.
func TestOpenCodeProbeRunsInItsOwnDirectory(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "set-by-the-test")
	t.Setenv("OPENCODE_AUTH_CONTENT", "set-by-the-test")
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	res, err := probe.Run(context.Background(), bin, nil, `{"share":"disabled"}`, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	env := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, k := range []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if !strings.HasPrefix(env[k], probe.Root()+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a directory of the probe %s", k, env[k], probe.Root())
		}
	}
	for _, flag := range openCodeDisableFlags {
		if env[flag] != "1" {
			t.Errorf("%s = %q, want 1", flag, env[flag])
		}
	}
	// The probe's directory is in no git repository, so without the switch
	// OpenCode reads opencode.json and .opencode, and loads their plugins,
	// from every directory above it.
	if env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Errorf("OPENCODE_DISABLE_PROJECT_CONFIG = %q, want 1", env["OPENCODE_DISABLE_PROJECT_CONFIG"])
	}
	if env[openCodeConfigContentEnvVar] != `{"share":"disabled"}` {
		t.Errorf("OPENCODE_CONFIG_CONTENT = %q", env[openCodeConfigContentEnvVar])
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENCODE_AUTH_CONTENT"} {
		if _, ok := env[k]; ok {
			t.Errorf("the probe inherited %s", k)
		}
	}
	root := probe.Root()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("Close left the probe directory %s", root)
	}
}

// TestOpenCodeProbeSetsProviderVarsToAPlaceholder: a probe given a model
// provider's variables sets each to a placeholder, never to the value the
// environment holds, and refuses any other name: OpenCode's own variables, a
// provider base URL, a platform account's credentials. OpenCodeProviderVars
// names the dispatched provider's variables by whether the environment holds
// a value, and never one a dispatch withholds or a platform account's.
func TestOpenCodeProbeSetsProviderVarsToAPlaceholder(t *testing.T) {
	const value = "set-by-the-test"
	t.Setenv("OPENAI_API_KEY", value)
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	res, err := probe.Run(context.Background(), bin, nil, "", nil, "OPENAI_API_KEY")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if !strings.Contains(string(res.Stdout), "\nOPENAI_API_KEY="+openCodeProbePlaceholder+"\n") || strings.Contains(string(res.Stdout), value) {
		t.Errorf("the probe did not set OPENAI_API_KEY to the placeholder alone:\n%s", res.Stdout)
	}
	for _, name := range []string{"OPENCODE_CONFIG", "OPENCODE_API_KEY", "ANTHROPIC_BASE_URL", "GITHUB_TOKEN", "AWS_REGION", "PATH", "NOT_A_PROVIDER_VARIABLE"} {
		if _, err := probe.Run(context.Background(), bin, nil, "", nil, name); err == nil || !strings.Contains(err.Error(), "catalog variables") {
			t.Errorf("a probe setting %s = %v, want it refused", name, err)
		}
	}

	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	}
	for _, c := range []struct {
		model      string
		env        map[string]string
		set, unset string
	}{
		{"openai/gpt-4.1", map[string]string{"OPENAI_API_KEY": value}, "OPENAI_API_KEY", ""},
		{"openai/gpt-4.1", map[string]string{"OPENAI_API_KEY": ""}, "", "OPENAI_API_KEY"},
		{"google/gemini-3-pro", map[string]string{"GEMINI_API_KEY": value}, "GEMINI_API_KEY", "GOOGLE_API_KEY,GOOGLE_GENERATIVE_AI_API_KEY"},
		// OpenCode's own provider: every OPENCODE_* variable is withheld from
		// a dispatch, so a dispatch lists its free models only, and so does
		// the probe.
		{"opencode/some-model", map[string]string{"OPENCODE_API_KEY": value}, "", ""},
		// A platform provider's variables are the stage's tools', never a
		// probe's.
		{"github-copilot/some-model", map[string]string{"GITHUB_TOKEN": value}, "", ""},
	} {
		set, unset := OpenCodeProviderVars(c.model, lookup(c.env))
		if strings.Join(set, ",") != c.set || strings.Join(unset, ",") != c.unset {
			t.Errorf("OpenCodeProviderVars(%s) = set %q, unset %q; want %q, %q", c.model, set, unset, c.set, c.unset)
		}
	}
}

// TestOpenCodeProbeKillsItsProcessGroup: a probe kills everything it started
// once it exits, and the whole group when it runs past its timeout, so no
// process a probed binary starts outlives the probe.
func TestOpenCodeProbeKillsItsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	probe, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()

	// Exits at once, leaving a background child in its group.
	leaves := write("leaves", fmt.Sprintf("sleep 30 >/dev/null 2>&1 &\necho $! > %q\nexit 0\n", pidFile))
	if _, err := probe.Run(context.Background(), leaves, nil, "", nil); err != nil {
		t.Fatalf("Run = %v", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if !processGone(child, 3*time.Second) {
		t.Errorf("the probe's background child %d outlived it", child)
	}

	// Never exits: killed at the timeout.
	prev := openCodeProbeTimeout
	openCodeProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { openCodeProbeTimeout = prev })
	hangs := write("hangs", "sleep 30\n")
	start := time.Now()
	_, err = probe.Run(context.Background(), hangs, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "ran past") {
		t.Fatalf("Run of a process that does not exit = %v, want the timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the timeout took %s", elapsed)
	}
}

// TestOpenCodeProbeHonoursItsContext: a probe whose context is done starts
// nothing, and one whose context is cancelled while it runs is killed with
// its group at once, its error wrapping the context's instead of naming the
// timeout (#1627).
func TestOpenCodeProbeHonoursItsContext(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	bin := filepath.Join(dir, "hangs")
	if err := os.WriteFile(bin, []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\nsleep 30\n", started)), 0o755); err != nil {
		t.Fatal(err)
	}
	probe, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()

	done, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	if _, err := probe.Run(done, bin, nil, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run under a done context = %v, want its error", err)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("a probe whose context was done started its binary")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				break
			}
		}
		cancel()
	}()
	start := time.Now()
	_, err = probe.Run(ctx, bin, nil, "", nil)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "ran past") {
		t.Fatalf("Run cancelled while it ran = %v, want the context's error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the cancelled probe took %s to return", elapsed)
	}
}

// TestOpenCodeStoppedDispatchIsNotIncompatible: a dispatch whose context is
// done runs no probe and is refused with the context's error. It does not call
// the binary incompatible, and it does not record the dispatch (#1627).
func TestOpenCodeStoppedDispatchIsNotIncompatible(t *testing.T) {
	home := preflightEnv(t)
	var incompatible *OpenCodeIncompatibleError

	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: openCodeManifestForTest(t).MaxTested})
	done, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	var err error
	captureAdapterStderr(t, func() {
		err = pinnedAdapter(lmStudioSettings(), fake.path).PreDispatch(done, RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"})
	})
	if !errors.Is(err, context.Canceled) || errors.As(err, &incompatible) {
		t.Fatalf("PreDispatch under a done context = %v, want the context's error and no adapter_incompatible", err)
	}
	if got := fake.invocations(t); len(got) != 0 {
		t.Errorf("a dispatch whose context was done ran the binary: %q", got)
	}

	if _, ok, _ := ReadOpenCodeDispatchRecord(home); ok {
		t.Error("a stopped dispatch was recorded as the last dispatch")
	}
}

// TestOpenCodeMachineConfigRefusalsAreTheDispatchs: the refusals the doctor
// reports for the machine (OpenCodeMachineConfigRefusals) are the ones
// PrepareOpenCodeRun makes from the same request: none with
// opencode.inherit_user_config on, and otherwise ~/.opencode holding config
// and then the machine's managed OpenCode config, the first of which is the
// dispatch's own refusal, word for word. Each names what it found and never
// its content.
func TestOpenCodeMachineConfigRefusalsAreTheDispatchs(t *testing.T) {
	const sentinel = "machine-config-content-sentinel-1627"
	for _, c := range []struct {
		name          string
		operatorEntry bool // a populated ~/.opencode, which never refuses (#1787)
		managed       bool
		inherit       bool
		want          []string // what each refusal names, in order
	}{
		{"none", false, false, false, nil},
		{"operator ~/.opencode present, never refuses", true, false, false, nil},
		{"managed config", false, true, false, []string{"managed OpenCode config"}},
		{"both, only managed refuses", true, true, false, []string{"managed OpenCode config"}},
		{"both, inherited", true, true, true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if c.operatorEntry {
				path := filepath.Join(home, ".opencode", "opencode.jsonc")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			managed := []string{filepath.Join(t.TempDir(), "opencode.json")}
			if c.managed {
				if err := os.WriteFile(managed[0], []byte(sentinel), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			settings := lmStudioSettings()
			settings.InheritUserConfig = c.inherit
			req := OpenCodeRunRequest{
				Home:             home,
				ID:               testRunID,
				MachineConfigDir: filepath.Join(home, ".nightgauge"),
				// A worktree with nothing in it: this test is about machine
				// config refusals, not steering or MCP servers, and
				// PrepareOpenCodeRun now requires one to read those from.
				Run:                RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()},
				Settings:           settings,
				Lookup:             envLookup(nil),
				GOOS:               runtime.GOOS,
				ManagedConfigFiles: managed,
			}

			refusals := OpenCodeMachineConfigRefusals(req)
			if len(refusals) != len(c.want) {
				t.Fatalf("refusals = %v, want %d naming %q", refusals, len(c.want), c.want)
			}
			for i, want := range c.want {
				if !strings.Contains(refusals[i].Error(), want) {
					t.Errorf("refusal %d does not say %q: %v", i, want, refusals[i])
				}
				if strings.Contains(refusals[i].Error(), sentinel) {
					t.Errorf("refusal %d carries a file's content", i)
				}
			}

			var err error
			captureAdapterStderr(t, func() { _, err = PrepareOpenCodeRun(req) })
			switch {
			case len(refusals) == 0 && err != nil:
				t.Errorf("no machine refusal, and the dispatch was refused: %v", err)
			case len(refusals) > 0 && err == nil:
				t.Errorf("the dispatch went ahead past %v", refusals[0])
			case len(refusals) > 0 && err.Error() != refusals[0].Error():
				t.Errorf("the dispatch's refusal is\n%v\nwant the first machine refusal\n%v", err, refusals[0])
			}
		})
	}
}

// processGone polls until pid no longer runs (gone, or a zombie waiting to be
// reaped), or within gives up.
func processGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
