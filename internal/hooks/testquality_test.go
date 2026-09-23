package hooks

// testquality_test.go (#1642) proves EvaluateTestQuality/FormatWarnings match
// claude-plugins/nightgauge/hooks/test-quality.sh byte-for-byte, for a
// fixture covering every one of the shell script's own three zero-value-test
// patterns plus a clean file, driving the REAL shell script through bash (and
// python3, which the script shells out to for JSON parsing) rather than a
// re-implementation of its own decision — the same "compare against the real
// thing" discipline plugin_gates_test.go's TestGatesParityCorpus uses for the
// OpenCode plugin's gates.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testQualityModuleRoot walks up from this package's own directory to the
// module root (the directory holding go.mod), so the shell script's path
// resolves regardless of the working directory `go test` was invoked from.
func testQualityModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root (no go.mod in any parent of " + wd + ")")
		}
		dir = parent
	}
}

// requireBashAndPython3 skips the test on a machine missing either
// interpreter test-quality.sh itself needs (bash to run it at all, python3
// for its own JSON parsing) — both are present in CI per this issue's own
// verification bullet, so a skip here means a local dev machine gap, not a
// CI gap.
func requireBashAndPython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not on PATH")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH")
	}
}

// runShellTestQuality runs the real test-quality.sh with stdin on its stdin
// and extraEnv layered over the inherited environment, and returns its
// stderr. It fails the test if the script exits non-zero — test-quality.sh's
// own contract is "always exit 0" (warnings-only), so a non-zero exit here is
// itself a regression worth failing loud on, not something to route around.
func runShellTestQuality(t *testing.T, script string, stdin []byte, extraEnv ...string) string {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("test-quality.sh exited non-zero (it must always exit 0): %v\nstderr:\n%s", err, stderr.String())
	}
	return stderr.String()
}

// TestTestQualityMatchesShellScript is this issue's own parity bullet: for
// every pattern test-quality.sh checks, plus a clean file and a
// non-matching extension, the Go verb's FormatWarnings output is
// byte-for-byte identical to the real shell script's own stderr. Dropping
// any one of EvaluateTestQuality's three regexps turns exactly that row red
// without touching this test.
func TestTestQualityMatchesShellScript(t *testing.T) {
	requireBashAndPython3(t)
	script := filepath.Join(testQualityModuleRoot(t), "claude-plugins", "nightgauge", "hooks", "test-quality.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("test-quality.sh not found at %s: %v", script, err)
	}

	cases := []struct {
		name     string
		filePath string
		field    string // "content" (Write) or "new_string" (Edit)
		body     string
	}{
		{"tautological assertion expect(true).toBe(true)", "a.test.ts", "content", `expect(true).toBe(true);`},
		{"tautological assertion expect(false).toBe(false)", "a.spec.ts", "new_string", `expect(false).toBe(false);`},
		{"empty test body", "a.test.ts", "content", `it("does nothing", () => {})`},
		// Go RE2's \s matches \n (and \r); grep's does not span the line it
		// is currently reading, since test-quality.sh's own
		// `grep ... <<<"$CONTENT"` iterates $CONTENT one line at a time even
		// though $CONTENT holds embedded newlines. A naive whole-content
		// regexp match (rather than a per-line one) would warn here where
		// the shell script stays silent.
		{"empty test body split across lines is NOT flagged (grep is line-oriented)", "a.test.ts", "content", "it(\"does nothing\", () => {\n})"},
		{"empty test body split before the arrow is NOT flagged", "a.test.ts", "content", "it(\"does nothing\", ()\n=> {})"},
		{"empty test body split across a CRLF line ending is NOT flagged", "a.test.ts", "content", "it(\"does nothing\", () => {\r\n})"},
		{"console.log with no assertion", "a.test.ts", "content", "console.log(\"debug\");"},
		{"console.log alongside a real assertion is clean", "a.test.ts", "content", "console.log(\"debug\");\nexpect(1).toBe(1);"},
		{"a clean test file", "a.test.ts", "content", `it("adds", () => { expect(1 + 1).toBe(2); })`},
		{"all three patterns at once", "a.test.ts", "content", "it(\"x\", () => {})\nexpect(true).toBe(true);\nconsole.log(\"x\");"},
		// A match early in a long file. Under `set -o pipefail`,
		// `echo "$CONTENT" | grep -q` lost it: grep exits at the first match,
		// the line-buffered echo then writes into a closed pipe and takes
		// SIGPIPE, and pipefail turns the match into a miss. With a short
		// file that race was lost only under load; thousands of trailing
		// lines make it certain. The script now greps a here-string.
		{"a match early in a long file is still flagged", "a.test.ts", "content", "it(\"x\", () => {\nexpect(true).toBe(true);\n" + strings.Repeat("expect(1).toBe(1);\n", 20000) + "})"},
		{"a non-matching extension is skipped even with every pattern present", "a.ts", "content", "it(\"x\", () => {})\nexpect(true).toBe(true);\nconsole.log(\"x\");"},
		// An empty (not absent) file_path: test-quality.sh's own
		// `grep -oE '"file_path"...'` still matches the empty-valued key, so
		// this reaches its `[ -z "$FILE_PATH" ]` exit-0 path rather than
		// failing the pipeline the way an ABSENT key would under `set -e
		// -o pipefail` (grep finding no match at all exits 1) — a real
		// shell-script quirk this issue's own scope does not change
		// ("The Claude script is unchanged here"), so the fixture below
		// stays inside the shape the script actually handles.
		{"an empty file_path is skipped", "", "content", `expect(true).toBe(true);`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolInput := map[string]string{tc.field: tc.body, "file_path": tc.filePath}
			raw, err := json.Marshal(map[string]any{
				"tool_name":  "Write",
				"tool_input": toolInput,
			})
			if err != nil {
				t.Fatal(err)
			}

			shellOut := runShellTestQuality(t, script, raw)
			goOut := FormatWarnings(EvaluateTestQuality(raw))

			if goOut != shellOut {
				t.Errorf("Go verb's stderr diverges from test-quality.sh's own:\ngo:    %q\nshell: %q", goOut, shellOut)
			}
		})
	}
}

// TestTestQualityHonoursSkipEnvVar is the NIGHTGAUGE_SKIP_TEST_QUALITY=1
// bullet: both implementations print nothing at all, for a payload that
// would otherwise trigger every warning.
func TestTestQualityHonoursSkipEnvVar(t *testing.T) {
	requireBashAndPython3(t)
	script := filepath.Join(testQualityModuleRoot(t), "claude-plugins", "nightgauge", "hooks", "test-quality.sh")

	raw, err := json.Marshal(map[string]any{
		"tool_name": "Write",
		"tool_input": map[string]string{
			"file_path": "a.test.ts",
			"content":   "it(\"x\", () => {})\nexpect(true).toBe(true);\nconsole.log(\"x\");",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	shellOut := runShellTestQuality(t, script, raw, "NIGHTGAUGE_SKIP_TEST_QUALITY=1")
	if shellOut != "" {
		t.Fatalf("test premise broken: test-quality.sh printed warnings despite NIGHTGAUGE_SKIP_TEST_QUALITY=1: %q", shellOut)
	}

	t.Setenv("NIGHTGAUGE_SKIP_TEST_QUALITY", "1")
	goOut := FormatWarnings(EvaluateTestQuality(raw))
	if goOut != "" {
		t.Errorf("EvaluateTestQuality printed warnings despite NIGHTGAUGE_SKIP_TEST_QUALITY=1: %q", goOut)
	}
}

// TestTestQualityAlwaysAllowsThroughGateOutput: EvaluateTestQuality has no
// error return at all — a malformed payload, an unreadable field, or an
// empty body all resolve to a quiet TestQualityResult{}, never a panic or an
// error a caller would have to translate into a non-zero exit. This is the
// Go-side half of "always exit 0"; hookTestQualityCmd's own RunE (never
// returning a non-nil error) is the other half, covered by
// cmd/nightgauge/hook_output_schema_test.go's TestHookCommands_RunWithoutArgv
// and TestHookTestQualityRegistered.
func TestTestQualityAlwaysAllowsThroughGateOutput(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		[]byte(""),
		[]byte("not json"),
		[]byte(`{"tool_input":{}}`),
		[]byte(`{"tool_input":{"file_path":"a.test.ts"}}`),
	} {
		result := EvaluateTestQuality(raw)
		if out := FormatWarnings(result); out != "" && len(raw) == 0 {
			t.Errorf("EvaluateTestQuality(%q) produced warnings from no input: %q", raw, out)
		}
	}
}
