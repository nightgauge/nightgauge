package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file covers #356 AC1 and AC2 at the layer that actually runs on a
// user's machine: the real hook wrapper scripts, executed through bash.
//
// cmd/nightgauge/hook_output_schema_test.go pins the same stdout contracts,
// but it invokes the cobra command IN-PROCESS — it never runs the shell
// wrapper, so it cannot see a byte written by guard.sh (or anything guard.sh
// sources) ahead of the `exec`. That class of corruption is silent: Claude
// Code reports hook schema failures as NON-blocking, so the gate dies
// invisibly. Only an end-to-end shell run catches it.

const (
	// preToolUsePayload is a realistic PreToolUse response (#354/#355) — the
	// shape whose first byte must be `{`.
	preToolUsePayload = `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"stub"}}`
	// stopPayload is a realistic Stop response.
	stopPayload = `{"decision":"block","reason":"stub"}`
	// bundleDirPrefix mirrors internal/doctor's constant; the two packages do
	// not import each other.
	bundleDirPrefix = "nightgauge.nightgauge-vscode-"
)

type capturedBundle struct {
	BundleVersion     string `json:"bundle_version"`
	RecordedInstalled bool   `json:"recorded_installed"`
	BinaryExists      bool   `json:"binary_exists"`
}

type capturedBundleLayout struct {
	RecordedRelativeLocation string           `json:"recorded_relative_location"`
	Bundles                  []capturedBundle `json:"bundles"`
}

// loadCapturedLayout reads the redacted capture of a real machine's extension
// install (internal/doctor/testdata/vscode-bundles/, see the README there).
// The fixture is shared with the resolver tests so both layers exercise the
// same real layout: two bundles on disk, the SECOND in glob order recorded as
// installed.
func loadCapturedLayout(t *testing.T, repoRoot string) capturedBundleLayout {
	t.Helper()
	path := filepath.Join(repoRoot, "internal", "doctor", "testdata", "vscode-bundles", "bundle-layout.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured bundle layout %s: %v", path, err)
	}
	var layout capturedBundleLayout
	if err := json.Unmarshal(raw, &layout); err != nil {
		t.Fatalf("parse captured bundle layout %s: %v", path, err)
	}
	usable := 0
	for _, b := range layout.Bundles {
		if b.BinaryExists && b.BundleVersion != "" {
			usable++
		}
	}
	if usable < 2 {
		t.Fatalf("captured layout has %d usable bundle(s); these tests need at least 2", usable)
	}
	if layout.RecordedRelativeLocation == "" {
		t.Fatal("captured layout records no installed bundle — re-capture with scripts/capture-vscode-bundle-layout.sh")
	}
	return layout
}

// capturedVersions returns (unrecordedVersion, recordedVersion) from the
// capture: the bundle VS Code does NOT record (the leftover that pre-#356
// resolution silently ran) and the one it does.
func capturedVersions(t *testing.T, layout capturedBundleLayout) (string, string) {
	t.Helper()
	recorded := strings.TrimPrefix(layout.RecordedRelativeLocation, bundleDirPrefix)
	unrecorded := ""
	for _, b := range layout.Bundles {
		if b.BinaryExists && b.BundleVersion != "" && b.BundleVersion != recorded {
			unrecorded = b.BundleVersion
			break
		}
	}
	if unrecorded == "" {
		t.Fatal("captured layout has no unrecorded bundle — it cannot express the #356 defect")
	}
	return unrecorded, recorded
}

// bundleStub writes a fake bundled binary that (a) records every invocation
// with its own bundle version so a test can prove WHICH bundle ran, (b)
// prints nothing for guard.sh's `forge auth token` probe, and (c) prints
// exactly payload (plus a trailing newline) for a `hook …` invocation.
func bundleStub(t *testing.T, home, version, payload string, executable bool) string {
	t.Helper()
	binary := filepath.Join(home, ".vscode", "extensions", bundleDirPrefix+version, "dist", "bin", "nightgauge")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}
	body := "#!/bin/sh\n" +
		"if [ -n \"${NG_STUB_MARKER:-}\" ]; then printf '%s %s\\n' \"" + version + "\" \"$1\" >> \"$NG_STUB_MARKER\"; fi\n" +
		"if [ \"$1\" = \"forge\" ]; then exit 0; fi\n"
	if payload != "" {
		body += "printf '%s\\n' '" + payload + "'\n"
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	if err := os.WriteFile(binary, []byte(body), mode); err != nil {
		t.Fatalf("write bundle stub: %v", err)
	}
	return binary
}

// recordInstall writes a VS Code-shaped extensions.json naming the given
// bundle versions as installed. This is the SELECTION AUTHORITY guard.sh
// consults (#356) — the whole point being that it, and not a version
// comparison, decides which bundle the hooks run.
func recordInstall(t *testing.T, home string, versions ...string) {
	t.Helper()
	entries := []string{`{"identifier":{"id":"publisher1.extension1"},"version":"1.0.0","relativeLocation":"publisher1.extension1-1.0.0"}`}
	for _, v := range versions {
		dir := bundleDirPrefix + v
		entries = append(entries, `{"identifier":{"id":"nightgauge.nightgauge-vscode"},"version":"`+v+
			`","location":{"$mid":1,"path":"`+filepath.Join(home, ".vscode", "extensions", dir)+
			`","scheme":"file"},"relativeLocation":"`+dir+`"}`)
	}
	path := filepath.Join(home, ".vscode", "extensions", "extensions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create extensions dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("["+strings.Join(entries, ",")+"]"), 0o644); err != nil {
		t.Fatalf("write extensions index: %v", err)
	}
}

// largeIndexRecordedVersion is the bundle version recorded by the committed
// 120-entry index (internal/doctor/testdata/vscode-bundles/).
const largeIndexRecordedVersion = "0.2.0-rc.23-darwin-arm64"

// installLargeExtensionsIndex copies the committed 120-entry extensions.json
// into home. It is the same fixture the resolver parity tests use, so both
// layers are pinned to one artifact.
func installLargeExtensionsIndex(t *testing.T, repoRoot, home string) {
	t.Helper()
	src := filepath.Join(repoRoot, "internal", "doctor", "testdata", "vscode-bundles", "extensions-index-large.json")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read large extensions index %s: %v", src, err)
	}
	if !strings.Contains(string(raw), bundleDirPrefix+largeIndexRecordedVersion) {
		t.Fatalf("large extensions index no longer records %s%s", bundleDirPrefix, largeIndexRecordedVersion)
	}
	dst := filepath.Join(home, ".vscode", "extensions", "extensions.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create extensions dir: %v", err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatalf("write extensions index: %v", err)
	}
}

type wrapperRun struct {
	stdout   string
	stderr   string
	exitCode int
	log      string
	marker   string
}

// hookBash is the interpreter the wrappers are run under.
//
// It is pinned to /bin/bash rather than resolved from PATH because that is
// what Claude Code hooks actually execute under — bash 3.2.57 on macOS, where
// PATH would otherwise hand us Homebrew's bash 5.x. A bash-4ism in guard.sh
// (arrays, `mapfile`, `${var,,}`) would pass CI against 5.x and break every
// macOS hook silently.
func hookBash(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	return path
}

// runWrapper executes one of the real hook wrapper scripts through bash from a
// cwd with no git repo and no binary on PATH, so the VSCode-bundle fallback
// (step 4) is the step under test.
func runWrapper(t *testing.T, repoRoot, wrapper, home, stdin string, extraEnv map[string]string) wrapperRun {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "hook-warnings.log")
	marker := filepath.Join(t.TempDir(), "invocations.log")

	env := map[string]string{
		"HOME":                filepath.Clean(home),
		"PATH":                lookupGitDir(t) + ":/bin:/usr/bin",
		"LC_ALL":              "C",
		"NIGHTGAUGE_BIN":      "",
		"NIGHTGAUGE_HOOK_LOG": logFile,
		"NG_STUB_MARKER":      marker,
	}
	for k, v := range extraEnv {
		env[k] = v
	}

	cmd := exec.Command(hookBash(t), filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", wrapper))
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = []string{}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	code := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("wrapper %s failed to run: %v", wrapper, err)
		}
		code = exitErr.ExitCode()
	}
	logContent, _ := readFileIfExists(t, logFile)
	markerContent, _ := readFileIfExists(t, marker)
	return wrapperRun{stdout: out.String(), stderr: errBuf.String(), exitCode: code, log: logContent, marker: markerContent}
}

// TestGuardShellRecordedBundleWins covers the #356 core defect end to end: the
// hooks must run the bundle VS Code RECORDS as installed, not whichever
// directory the glob lists first — and must do it silently, because this path
// executes on every single tool call.
func TestGuardShellRecordedBundleWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	const stdin = `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`

	t.Run("captured real layout runs the recorded bundle", func(t *testing.T) {
		layout := loadCapturedLayout(t, repoRoot)
		unrecorded, recorded := capturedVersions(t, layout)

		home := t.TempDir()
		bundleStub(t, home, unrecorded, preToolUsePayload, true)
		bundleStub(t, home, recorded, preToolUsePayload, true)
		recordInstall(t, home, recorded)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

		if !strings.Contains(run.marker, recorded) {
			t.Errorf("expected the recorded bundle %q to have run, invocations were %q", recorded, run.marker)
		}
		if strings.Contains(run.marker, unrecorded) {
			t.Errorf("the unrecorded leftover %q must not run, invocations were %q", unrecorded, run.marker)
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("a confirmed resolution must not log on every tool call, got %q", run.log)
		}
		if strings.TrimSpace(run.stderr) != "" {
			t.Errorf("expected empty stderr, got %q", run.stderr)
		}
	})

	t.Run("recorded RC bundle wins over the tie-breaking glob order", func(t *testing.T) {
		// The channel this repo actually ships: staging.yml runs
		// `npm version 0.2.0-rc.23` then `vsce package --target <t>`. rc.22
		// sorts first, and every dotted-numeric comparator ties the two.
		home := t.TempDir()
		bundleStub(t, home, "0.2.0-rc.22-darwin-arm64", preToolUsePayload, true)
		bundleStub(t, home, "0.2.0-rc.23-darwin-arm64", preToolUsePayload, true)
		recordInstall(t, home, "0.2.0-rc.23-darwin-arm64")

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

		if !strings.Contains(run.marker, "0.2.0-rc.23-darwin-arm64") {
			t.Errorf("expected rc.23 to have run, invocations were %q", run.marker)
		}
		if strings.Contains(run.marker, "0.2.0-rc.22-darwin-arm64") {
			t.Errorf("rc.22 must not run when rc.23 is the recorded install, invocations were %q", run.marker)
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("expected silence, got %q", run.log)
		}
	})

	t.Run("recorded downgrade wins, silently", func(t *testing.T) {
		// A maintainer dev-install is permanently `0.1.<epoch>`; a leftover
		// release directory carries a bigger number. Ranking versions throws
		// away the build the maintainer just installed. The recorded bundle
		// here is neither the first glob match nor the highest version.
		home := t.TempDir()
		bundleStub(t, home, "0.1.0", preToolUsePayload, true)
		bundleStub(t, home, "0.2.0", preToolUsePayload, true)
		bundleStub(t, home, "0.3.0", preToolUsePayload, true)
		recordInstall(t, home, "0.2.0")

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

		if !strings.Contains(run.marker, "0.2.0") {
			t.Errorf("expected the recorded bundle 0.2.0 to have run, invocations were %q", run.marker)
		}
		for _, other := range []string{"0.1.0", "0.3.0"} {
			if strings.Contains(run.marker, other) {
				t.Errorf("bundle %q must not run; VS Code records 0.2.0, invocations were %q", other, run.marker)
			}
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("a recorded downgrade is a healthy install and must stay silent, got %q", run.log)
		}
	})

	t.Run("vsctmp partial-install orphan never wins", func(t *testing.T) {
		home := t.TempDir()
		bundleStub(t, home, "0.2.0-darwin-arm64.vsctmp", preToolUsePayload, true)
		bundleStub(t, home, "0.2.1-darwin-arm64", preToolUsePayload, true)
		recordInstall(t, home, "0.2.1-darwin-arm64")

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

		if !strings.Contains(run.marker, "0.2.1-darwin-arm64") {
			t.Errorf("expected the recorded install to have run, invocations were %q", run.marker)
		}
		if strings.Contains(run.marker, "vsctmp") {
			t.Errorf("a .vsctmp partial-install orphan ran, invocations were %q", run.marker)
		}
	})

	t.Run("large real-shaped index selects the recorded bundle", func(t *testing.T) {
		// Every other fixture here is a 1-3 entry extensions.json, which is
		// small enough to hide both a superlinear parser and an extraction
		// that only works near the head of the file. This one is the committed
		// 120-entry index with the nightgauge entry 100th, minified onto a
		// single line exactly as VS Code writes it.
		home := t.TempDir()
		bundleStub(t, home, "0.1.0-darwin-arm64", preToolUsePayload, true) // sorts first
		bundleStub(t, home, largeIndexRecordedVersion, preToolUsePayload, true)
		installLargeExtensionsIndex(t, repoRoot, home)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

		if !strings.Contains(run.marker, largeIndexRecordedVersion) {
			t.Errorf("expected the recorded bundle %q to have run, invocations were %q", largeIndexRecordedVersion, run.marker)
		}
		if strings.Contains(run.marker, "0.1.0-darwin-arm64") {
			t.Errorf("the first glob match must not run when the 120-entry index records another bundle, invocations were %q", run.marker)
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("a confirmed resolution must stay silent, got %q", run.log)
		}
		if strings.TrimSpace(run.stderr) != "" {
			t.Errorf("expected empty stderr, got %q", run.stderr)
		}
	})

	t.Run("single unrecorded bundle resolves silently", func(t *testing.T) {
		home := t.TempDir()
		bundleStub(t, home, "0.2.1-darwin-arm64", preToolUsePayload, true)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

		if !strings.Contains(run.marker, "0.2.1-darwin-arm64") {
			t.Errorf("expected the only bundle to have run, invocations were %q", run.marker)
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("one bundle and no record is not ambiguous; expected silence, got %q", run.log)
		}
	})
}

// TestGuardShellDivergenceSignal covers #356 AC1: when the hooks resolve a
// bundle VS Code's record does NOT confirm, that must produce a visible signal
// naming the recorded version, the resolved version and the resolved path —
// routed to the side-channel log by default, because hook stderr surfaces to
// the parent agent as a stop-hook-error notification (#3262).
func TestGuardShellDivergenceSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	const stdin = `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`

	// setup builds a $HOME exhibiting the divergence and returns the strings
	// the signal must name.
	unusableRecord := func(t *testing.T, home string) (resolved string, mustName []string) {
		path := bundleStub(t, home, "0.2.0-darwin-arm64", preToolUsePayload, true)
		bundleStub(t, home, "0.2.1-darwin-arm64", preToolUsePayload, false)
		recordInstall(t, home, "0.2.1-darwin-arm64")
		return path, []string{"0.2.1-darwin-arm64", "0.2.0-darwin-arm64", path}
	}
	noRecord := func(t *testing.T, home string) (resolved string, mustName []string) {
		path := bundleStub(t, home, "0.2.0-darwin-arm64", preToolUsePayload, true)
		bundleStub(t, home, "0.2.1-darwin-arm64", preToolUsePayload, true)
		return path, []string{"0.2.0-darwin-arm64", path}
	}

	cases := []struct {
		name  string
		setup func(t *testing.T, home string) (string, []string)
	}{
		{"recorded bundle is not executable", unusableRecord},
		{"no install record with several bundles", noRecord},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/silent default routes to the side-channel log", func(t *testing.T) {
			home := t.TempDir()
			_, mustName := tc.setup(t, home)

			run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, nil)

			if !strings.Contains(run.log, "[stale-binary]") {
				t.Fatalf("the divergence signal was never emitted; log=%q", run.log)
			}
			for _, want := range mustName {
				if !strings.Contains(run.log, want) {
					t.Errorf("side-channel log must name %q, got %q", want, run.log)
				}
			}
			if strings.TrimSpace(run.stderr) != "" {
				t.Errorf("silent default must keep stderr empty (a stop-hook-error notification aborts turns per #3262), got %q", run.stderr)
			}
		})

		t.Run(tc.name+"/verbose mode moves the notice to stderr", func(t *testing.T) {
			home := t.TempDir()
			_, mustName := tc.setup(t, home)

			run := runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, map[string]string{
				"NIGHTGAUGE_HOOK_SILENT": "false",
			})
			for _, want := range mustName {
				if !strings.Contains(run.stderr, want) {
					t.Errorf("verbose stderr must name %q, got %q", want, run.stderr)
				}
			}
			if strings.Contains(run.log, "[stale-binary]") {
				t.Errorf("verbose mode must not ALSO write the log, got %q", run.log)
			}
		})
	}
}

// TestGuardShellDivergenceIsNotRepeatedPerToolCall covers the other half of
// "a signal, not noise": both divergences are STANDING conditions — a leftover
// bundle directory survives days, a lost exec bit never heals — and hooks.json
// fires 2-3 guard.sh-sourcing wrappers per tool call. Without a dedupe the same
// ~250-byte line lands in an unrotated log thousands of times, which is exactly
// the noise the healthy path stays silent to avoid.
//
// The rule is deliberately cheap and stateless: skip the append when the log's
// LAST line already carries this exact message. A different condition, or the
// same condition after something else was logged, still writes.
func TestGuardShellDivergenceIsNotRepeatedPerToolCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	const stdin = `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`

	home := t.TempDir()
	bundleStub(t, home, "0.2.0-darwin-arm64", preToolUsePayload, true)
	bundleStub(t, home, "0.2.1-darwin-arm64", preToolUsePayload, false)
	recordInstall(t, home, "0.2.1-darwin-arm64")

	sharedLog := filepath.Join(t.TempDir(), "hook-warnings.log")
	env := map[string]string{"NIGHTGAUGE_HOOK_LOG": sharedLog}
	for i := 0; i < 3; i++ {
		runWrapper(t, repoRoot, "workflow-gate.sh", home, stdin, env)
	}

	raw, err := os.ReadFile(sharedLog)
	if err != nil {
		t.Fatalf("the divergence signal must still be emitted at least once: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(line, "[stale-binary]") {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("3 tool calls against one standing condition wrote %d [stale-binary] lines, want exactly 1; log=%q", lines, raw)
	}

	// A different condition must still get through: the record becomes usable,
	// so the next signal is the unrecorded-ambiguous one from a fresh $HOME.
	other := t.TempDir()
	bundleStub(t, other, "0.3.0-darwin-arm64", preToolUsePayload, true)
	bundleStub(t, other, "0.3.1-darwin-arm64", preToolUsePayload, true)
	runWrapper(t, repoRoot, "workflow-gate.sh", other, stdin, env)

	raw, err = os.ReadFile(sharedLog)
	if err != nil {
		t.Fatalf("read shared log: %v", err)
	}
	if !strings.Contains(string(raw), "no usable VSCode install record") {
		t.Errorf("a DIFFERENT standing condition must still be recorded, got %q", raw)
	}
}

// TestGuardShellDivergenceStdoutContract covers #356 AC2: the divergence
// signal must not put a single byte on hook stdout, for any hook event shape.
// Each case asserts stdout is EXACTLY what the binary produced — not "starts
// with", not "contains".
func TestGuardShellDivergenceStdoutContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	cases := []struct {
		name    string
		wrapper string
		stdin   string
		payload string // what the resolved binary prints; "" = prints nothing
	}{
		{
			name:    "PreToolUse hookSpecificOutput",
			wrapper: "workflow-gate.sh",
			stdin:   `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`,
			payload: preToolUsePayload,
		},
		{
			name:    "PostToolUse silent allow",
			wrapper: "format-on-save.sh",
			stdin:   `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`,
			payload: "",
		},
		{
			name:    "Stop decision",
			wrapper: "stop-verification.sh",
			stdin:   `{"stop_hook_active":false}`,
			payload: stopPayload,
		},
	}

	for _, tc := range cases {
		for _, silent := range []string{"true", "false"} {
			t.Run(tc.name+"/silent="+silent, func(t *testing.T) {
				home := t.TempDir()
				bundleStub(t, home, "0.2.0-darwin-arm64", tc.payload, true)
				bundleStub(t, home, "0.2.1-darwin-arm64", tc.payload, false)
				recordInstall(t, home, "0.2.1-darwin-arm64")

				run := runWrapper(t, repoRoot, tc.wrapper, home, tc.stdin, map[string]string{
					"NIGHTGAUGE_HOOK_SILENT": silent,
				})

				// The divergence condition must actually be active, or this
				// case proves nothing about the signal's stdout discipline.
				signalled := strings.Contains(run.log, "[stale-binary]") || strings.Contains(run.stderr, "[stale-binary]")
				if !signalled {
					t.Fatalf("divergence signal never fired; log=%q stderr=%q", run.log, run.stderr)
				}

				want := ""
				if tc.payload != "" {
					want = tc.payload + "\n"
				}
				if run.stdout != want {
					t.Errorf("stdout must be byte-identical to the binary's output.\n  got  %q\n  want %q", run.stdout, want)
				}
				if want != "" {
					var doc map[string]any
					if err := json.Unmarshal([]byte(run.stdout), &doc); err != nil {
						t.Fatalf("stdout is not a single valid JSON document: %v (stdout=%q)", err, run.stdout)
					}
					if len(doc) == 0 {
						t.Errorf("decoded hook output is empty, got %q", run.stdout)
					}
				}
			})
		}
	}
}
