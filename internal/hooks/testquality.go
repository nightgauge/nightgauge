package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// TestQualityToolInput is the parsed tool_input of a PostToolUse Edit/Write
// hook payload, exactly test-quality.sh's own stdin contract: Write carries
// the new file in Content, Edit carries it in NewString, and this verb reads
// both the same way the shell script does (concatenated, so either shape
// works without knowing which tool produced the payload).
type TestQualityToolInput struct {
	FilePath  string `json:"file_path"`
	Content   string `json:"content"`
	NewString string `json:"new_string"`
}

// TestQualityResult is test-quality's own verdict. FilePath and Warnings are
// both empty for a file this check skips entirely (wrong extension, no
// content, or NIGHTGAUGE_SKIP_TEST_QUALITY=1) — FormatWarnings reads that
// directly as "print nothing."
type TestQualityResult struct {
	FilePath string   `json:"file_path,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// The three zero-value-test patterns test-quality.sh's own three grep -E
// checks encode, ported byte-for-byte as Go regexps: a tautological
// assertion, an empty it() body, and a console.log with no expect()/assert()
// call anywhere in the same content. `.` matches within a line only in both
// grep (line-buffered) and Go's default (non-multiline, non-dotall) regexp
// semantics, so the empty-body pattern only matches an `it(...)  => {}` that
// is not split across lines in either implementation — EXCEPT for `\s`
// itself: grep's own `\s` is confined to whatever line grep is currently
// looking at (test-quality.sh feeds it $CONTENT one line at a time, since
// `echo "$CONTENT" | grep` still iterates line by line even though $CONTENT
// itself holds embedded newlines), but Go RE2's `\s` matches `\n` (and
// `\r`) same as any other whitespace, so evaluating the pattern against the
// WHOLE content string lets it span lines the shell script never would.
// evaluateEmptyTestBodyPerLine below is what actually keeps the two
// implementations in parity; emptyTestBodyPattern itself must never be run
// against multi-line content directly.
var (
	tautologicalAssertionPattern = regexp.MustCompile(`expect\(true\)\.toBe\(true\)|expect\(false\)\.toBe\(false\)`)
	emptyTestBodyPattern         = regexp.MustCompile(`it\(.*\(\)\s*=>\s*\{\s*\}\)`)
	consoleLogPattern            = regexp.MustCompile(`console\.log`)
	hasAssertionPattern          = regexp.MustCompile(`expect\(|assert[.(]`)
)

// evaluateEmptyTestBodyPerLine mirrors grep's own line-oriented matching:
// test-quality.sh's `echo "$CONTENT" | grep -qE '...'` only ever sees one
// line of $CONTENT at a time, so its `\s*` can never bridge a `\n` a real
// model's edit introduces between `() =>` and `{}`. Splitting on "\n" first
// (not "\r\n" — a lone "\r" stays attached to whichever line it trails,
// exactly like grep's own line splitting on a text stream) and matching
// each line independently reproduces that; the two-argument
// strings.Split(x,"\n") is a strict decomposition, so no line is ever
// dropped or merged.
func evaluateEmptyTestBodyPerLine(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if emptyTestBodyPattern.MatchString(line) {
			return true
		}
	}
	return false
}

// EvaluateTestQuality is the Go port of
// claude-plugins/nightgauge/hooks/test-quality.sh (#1642): the OpenCode
// plugin path (internal/execution/opencodeplugin/plugin/nightgauge/edit.js)
// has no shell script it can spawn, so this verb carries the same three
// checks, the same NIGHTGAUGE_SKIP_TEST_QUALITY=1 opt-out and the same
// *.test.ts/*.spec.ts filter as a compiled verb both the Claude Code
// PostToolUse hook (via test-quality.sh, unchanged) and the OpenCode plugin
// can reach. TestTestQualityMatchesShellScript (testquality_test.go) diffs
// FormatWarnings's own text against the real shell script's stderr for a
// fixture set covering every pattern plus a clean file; dropping any one of
// the three regexps above turns that comparison red.
func EvaluateTestQuality(inputJSON []byte) TestQualityResult {
	if os.Getenv("NIGHTGAUGE_SKIP_TEST_QUALITY") == "1" {
		return TestQualityResult{}
	}

	var input struct {
		ToolInput TestQualityToolInput `json:"tool_input"`
	}
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return TestQualityResult{}
	}

	filePath := input.ToolInput.FilePath
	if filePath == "" {
		return TestQualityResult{}
	}
	if !strings.HasSuffix(filePath, ".test.ts") && !strings.HasSuffix(filePath, ".spec.ts") {
		return TestQualityResult{}
	}

	// test-quality.sh builds its own $CONTENT as
	// `ti.get('content','') + '\n' + ti.get('new_string','')` piped through a
	// bash command substitution, which strips every trailing newline from the
	// captured value — replicated here (TrimRight, not TrimSpace, so leading
	// whitespace inside real content is never touched) so a payload with
	// neither field present skips identically to the shell script's own
	// `[ -z "$CONTENT" ]` check.
	content := strings.TrimRight(input.ToolInput.Content+"\n"+input.ToolInput.NewString, "\n")
	if content == "" {
		return TestQualityResult{}
	}

	var warnings []string
	if tautologicalAssertionPattern.MatchString(content) {
		warnings = append(warnings, "Tautological assertion detected (expect(true).toBe(true) or expect(false).toBe(false)). Replace with meaningful assertions.")
	}
	if evaluateEmptyTestBodyPerLine(content) {
		warnings = append(warnings, "Empty test body detected. Tests must contain assertions.")
	}
	if consoleLogPattern.MatchString(content) && !hasAssertionPattern.MatchString(content) {
		warnings = append(warnings, "Test file contains console.log but no expect()/assert() calls. Add real assertions.")
	}

	if len(warnings) == 0 {
		return TestQualityResult{}
	}
	return TestQualityResult{FilePath: filePath, Warnings: warnings}
}

// FormatWarnings renders r exactly as test-quality.sh's own stderr block: a
// leading blank line, "⚠  Test Quality Warning — <file>", one "   • "
// line per warning, and a trailing blank line — byte-for-byte, so a caller
// (the CLI verb, or edit.js appending it to an OpenCode tool result) can
// treat the two implementations' output as interchangeable. Empty for a
// skip (r.Warnings is empty), matching the shell script printing nothing at
// all on its own quiet exit-0 path.
func FormatWarnings(r TestQualityResult) string {
	if len(r.Warnings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n⚠  Test Quality Warning — %s\n", r.FilePath)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "   • %s\n", w)
	}
	b.WriteString("\n")
	return b.String()
}
