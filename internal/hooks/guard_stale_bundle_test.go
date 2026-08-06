package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
)

type capturedBundle struct {
	BundleVersion    string `json:"bundle_version"`
	BinaryExists     bool   `json:"binary_exists"`
	BinaryExecutable bool   `json:"binary_executable"`
}

type capturedBundleLayout struct {
	Bundles []capturedBundle `json:"bundles"`
}

// loadCapturedBundleVersions reads the redacted capture of a real machine's
// extension install (internal/doctor/testdata/vscode-bundles/, see the README
// there) and returns the bundle versions, oldest first. The fixture is shared
// with the resolver tests so both layers exercise the same real layout.
func loadCapturedBundleVersions(t *testing.T, repoRoot string) []string {
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
	var versions []string
	for _, b := range layout.Bundles {
		if b.BinaryExists && b.BundleVersion != "" {
			versions = append(versions, b.BundleVersion)
		}
	}
	if len(versions) < 2 {
		t.Fatalf("captured layout has %d usable bundle(s); the staleness tests need at least 2", len(versions))
	}
	sort.Slice(versions, func(i, j int) bool { return bundleVersionLess(versions[i], versions[j]) })
	return versions
}

// bundleVersionLess is the test's own ordering oracle, deliberately
// independent of guard.sh's comparator.
func bundleVersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

// bundleStub writes a fake bundled binary that (a) records every invocation
// with its own bundle version so a test can prove WHICH bundle ran, (b)
// prints nothing for guard.sh's `forge auth token` probe, and (c) prints
// exactly payload (plus a trailing newline) for a `hook …` invocation.
func bundleStub(t *testing.T, home, version, payload string, executable bool) string {
	t.Helper()
	binary := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-"+version, "dist", "bin", "nightgauge")
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

type wrapperRun struct {
	stdout   string
	stderr   string
	exitCode int
	log      string
	marker   string
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
		"NIGHTGAUGE_BIN":      "",
		"NIGHTGAUGE_HOOK_LOG": logFile,
		"NG_STUB_MARKER":      marker,
	}
	for k, v := range extraEnv {
		env[k] = v
	}

	cmd := exec.Command("bash", filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", wrapper))
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

// TestGuardShellStaleBundleSignal covers #356 AC1: when the hooks resolve a
// bundle that is NOT the newest one installed, that must produce a visible
// signal naming both bundle versions and the resolved path — routed to the
// side-channel log by default, because hook stderr surfaces to the parent
// agent as a stop-hook-error notification (#3262).
func TestGuardShellStaleBundleSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	versions := loadCapturedBundleVersions(t, repoRoot)
	oldest, newest := versions[0], versions[len(versions)-1]

	t.Run("silent default routes the staleness notice to the side-channel log", func(t *testing.T) {
		home := t.TempDir()
		// Newest bundle installed but its binary is not runnable → the hooks
		// fall back to an older bundle. That resolved binary is stale.
		resolved := bundleStub(t, home, oldest, preToolUsePayload, true)
		bundleStub(t, home, newest, preToolUsePayload, false)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`, nil)

		if run.log == "" {
			t.Fatal("side-channel log is empty — the staleness signal was never emitted")
		}
		for _, want := range []string{oldest, newest, resolved} {
			if !strings.Contains(run.log, want) {
				t.Errorf("side-channel log must name %q (both bundle versions and the resolved path), got %q", want, run.log)
			}
		}
		if strings.TrimSpace(run.stderr) != "" {
			t.Errorf("silent default must keep stderr empty (a stop-hook-error notification aborts turns per #3262), got %q", run.stderr)
		}
	})

	t.Run("verbose mode moves the notice to stderr", func(t *testing.T) {
		home := t.TempDir()
		resolved := bundleStub(t, home, oldest, preToolUsePayload, true)
		bundleStub(t, home, newest, preToolUsePayload, false)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`, map[string]string{
			"NIGHTGAUGE_HOOK_SILENT": "false",
		})
		for _, want := range []string{oldest, newest, resolved} {
			if !strings.Contains(run.stderr, want) {
				t.Errorf("verbose stderr must name %q, got %q", want, run.stderr)
			}
		}
	})

	t.Run("healthy multi-bundle install stays quiet and runs the newest bundle", func(t *testing.T) {
		home := t.TempDir()
		bundleStub(t, home, oldest, preToolUsePayload, true)
		newestPath := bundleStub(t, home, newest, preToolUsePayload, true)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`, nil)

		// #356 core defect: the first glob match is the OLDER bundle.
		if !strings.Contains(run.marker, newest) {
			t.Errorf("expected the newest bundle %q (%s) to have run, invocations were %q", newest, newestPath, run.marker)
		}
		if strings.Contains(run.marker, oldest) {
			t.Errorf("the superseded bundle %q must not run when a newer one is installed, invocations were %q", oldest, run.marker)
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("a correctly-resolved newest bundle must not log a staleness notice on every tool call, got %q", run.log)
		}
		if strings.TrimSpace(run.stderr) != "" {
			t.Errorf("expected empty stderr, got %q", run.stderr)
		}
	})
}

// TestGuardShellReleaseTargetSuffixedBundles runs guard.sh against the
// bundle-directory shape that every RELEASED / marketplace install actually
// has, which no other test here exercises.
//
// VS Code appends the target platform to platform-specific extension folder
// names, and .github/workflows/release.yml packages every released VSIX with
// `vsce package --target <t>` (darwin-arm64, darwin-x64, linux-x64). So real
// users have `nightgauge.nightgauge-vscode-0.2.1-darwin-arm64`, and the version
// guard.sh extracts is `0.2.1-darwin-arm64`.
//
// The captured fixture and every version above come from the maintainer-only
// dev scheme (dev-install.sh runs `vsce package` WITHOUT --target), which is
// pure dotted digits. Before the trim in _ng_version_gt, the trailing component
// `1-darwin-arm64` was not all-digits and collapsed to 0, so any two releases
// differing only in patch compared EQUAL — leaving selection at first glob
// match (the OLDER bundle) and suppressing the staleness signal entirely. The
// suite was green while the fix was inert everywhere it mattered.
func TestGuardShellReleaseTargetSuffixedBundles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	const payload = `{"tool_name":"Write","tool_input":{"file_path":"x.go"}}`

	t.Run("runs the newest release bundle, not the first glob match", func(t *testing.T) {
		home := t.TempDir()
		// Collation order is 0.2.0, 0.2.1, 0.2.10, 0.2.9 — so the buggy answer
		// is the OLDEST (0.2.0) and "last match" would be 0.2.9. Only numeric
		// ordering of the suffix-trimmed version yields 0.2.10.
		for _, v := range []string{"0.2.0", "0.2.1", "0.2.9"} {
			bundleStub(t, home, v+"-darwin-arm64", preToolUsePayload, true)
		}
		newest := "0.2.10-darwin-arm64"
		bundleStub(t, home, newest, preToolUsePayload, true)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, payload, nil)

		if !strings.Contains(run.marker, newest) {
			t.Errorf("expected the newest release bundle %q to have run, invocations were %q", newest, run.marker)
		}
		for _, stale := range []string{"0.2.0-darwin-arm64", "0.2.1-darwin-arm64", "0.2.9-darwin-arm64"} {
			if strings.Contains(run.marker, stale) {
				t.Errorf("superseded bundle %q must not run, invocations were %q", stale, run.marker)
			}
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("a correctly-resolved newest bundle must stay silent, got %q", run.log)
		}
	})

	t.Run("no inverted stale alarm when two targets of one release tie", func(t *testing.T) {
		home := t.TempDir()
		// Same version, two target platforms; the glob-first one is the broken
		// (non-executable) copy. Comparing the two version STRINGS for
		// staleness fires the warning backwards here — reporting the machine as
		// stale, and naming the non-runnable bundle as the "newer" one — while
		// it is in fact running the newest binary available.
		bundleStub(t, home, "0.2.1-darwin-arm64", preToolUsePayload, false)
		runnable := "0.2.1-linux-x64"
		bundleStub(t, home, runnable, preToolUsePayload, true)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, payload, nil)

		if !strings.Contains(run.marker, runnable) {
			t.Errorf("expected the runnable bundle %q to have run, invocations were %q", runnable, run.marker)
		}
		if strings.Contains(run.log, "[stale-binary]") {
			t.Errorf("false stale-binary alarm on a healthy install (nothing newer is installed), log was %q", run.log)
		}
		if strings.TrimSpace(run.stderr) != "" {
			t.Errorf("expected empty stderr, got %q", run.stderr)
		}
	})

	t.Run("still signals genuine staleness with release-shaped dirs", func(t *testing.T) {
		home := t.TempDir()
		newest := "0.2.2-darwin-arm64"
		bundleStub(t, home, newest, preToolUsePayload, false)
		selected := "0.2.1-darwin-arm64"
		resolved := bundleStub(t, home, selected, preToolUsePayload, true)

		run := runWrapper(t, repoRoot, "workflow-gate.sh", home, payload, nil)

		for _, want := range []string{selected, newest, resolved} {
			if !strings.Contains(run.log, want) {
				t.Errorf("side-channel log must name %q, got %q", want, run.log)
			}
		}
		if strings.TrimSpace(run.stderr) != "" {
			t.Errorf("silent default must keep stderr empty (#3262), got %q", run.stderr)
		}
	})
}

// TestGuardShellStaleBundleStdoutContract covers #356 AC2: the staleness
// signal must not put a single byte on hook stdout, for any hook event shape.
// Each case asserts stdout is EXACTLY what the binary produced — not "starts
// with", not "contains".
func TestGuardShellStaleBundleStdoutContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	versions := loadCapturedBundleVersions(t, repoRoot)
	oldest, newest := versions[0], versions[len(versions)-1]

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
				bundleStub(t, home, oldest, tc.payload, true)
				bundleStub(t, home, newest, tc.payload, false)

				run := runWrapper(t, repoRoot, tc.wrapper, home, tc.stdin, map[string]string{
					"NIGHTGAUGE_HOOK_SILENT": silent,
				})

				// The staleness condition must actually be active, or this
				// case proves nothing about the signal's stdout discipline.
				signalled := strings.Contains(run.log, "[stale-binary]") || strings.Contains(run.stderr, "[stale-binary]")
				if !signalled {
					t.Fatalf("staleness signal never fired; log=%q stderr=%q", run.log, run.stderr)
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
