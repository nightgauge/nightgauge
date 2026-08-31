package skillrender

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The feature-validate AC-completion gate (Phase 0.6) is a HARD gate: a
// `type:docs` issue with unchecked acceptance criteria must not validate. #1145
// found it fail-OPEN on every error path — stderr was discarded, the exit status
// was never checked, `$BINARY` could be empty, and all three collapsed to
// AC_STATUS="" which the if/elif/else sent to the PASSING branch.
//
// These tests execute the skill's own shell, lifted verbatim out of the
// markdown, against a stubbed `nightgauge` binary — the same
// extract-the-fence-and-run-it pattern as
// TestPRMergeBatchProbeSignalsContextPresence above. A gate nothing exercises
// degrades into an unconditional pass; that is exactly what happened here.

const acContextLoadRel = "skills/nightgauge-feature-validate/_includes/context-load.md"

// acGateScript returns Step 0.6.2 (run the check) and Step 0.6.3 (gate on the
// result) concatenated, as the stage runs them.
func acGateScript(t *testing.T) string { return acGateScriptWith(t, "") }

// acGateScriptWith assembles the stage's shell with `judgement` spliced in at
// the point the skill expects the model's per-criterion verdict — between the
// block that gathers the criteria and the block that acts on them. Seeding those
// variables from the environment instead would be a test that cannot fail: the
// first block assigns AC_SUBSTANTIATED="" and would clobber them.
func acGateScriptWith(t *testing.T, judgement string) string {
	t.Helper()
	path := filepath.Join("..", "..", filepath.FromSlash(acContextLoadRel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", acContextLoadRel, err)
	}
	content := string(data)

	run := fencedBashAfter(t, content, "### Step 0.6.2: Run AC Completion Check")
	// Step 0.6.2b (#1233) sits between the check and the gate and is what makes
	// the gate satisfiable at all. It has TWO fenced blocks — inputs, then the
	// mark-and-recheck — with the model's per-criterion judgement in prose
	// between them. Both must run here, or the step that closes the deadlock is
	// never exercised and this harness certifies a gate nothing can pass.
	subHead := "### Step 0.6.2b: Substantiate Unchecked Criteria Against the Change"
	subA := fencedBashAfter(t, content, subHead)
	subB := nthFencedBashAfter(t, content, subHead, 2)
	gate := fencedBashAfter(t, content, "### Step 0.6.3: Gate on Result")
	return run + "\n" + subA + "\n" + judgement + "\n" + subB + "\n" + gate + "\necho \"AC_COMPLETION_STATUS=${AC_COMPLETION_STATUS:-<unset>}\"\n"
}

// fencedBashAfter returns the first ```bash block following heading.
func fencedBashAfter(t *testing.T, content, heading string) string {
	t.Helper()
	h := strings.Index(content, heading)
	if h < 0 {
		t.Fatalf("%s no longer contains heading %q", acContextLoadRel, heading)
	}
	rest := content[h:]
	open := strings.Index(rest, "```bash\n")
	if open < 0 {
		t.Fatalf("no bash block under %q", heading)
	}
	body := rest[open+len("```bash\n"):]
	end := strings.Index(body, "\n```")
	if end < 0 {
		t.Fatalf("unterminated bash block under %q", heading)
	}
	return body[:end]
}

// nthFencedBashAfter returns the nth (1-based) ```bash block following heading.
// A step whose shell is split around a prose instruction has more than one, and
// running only the first would execute the inputs without the action.
func nthFencedBashAfter(t *testing.T, content, heading string, n int) string {
	t.Helper()
	h := strings.Index(content, heading)
	if h < 0 {
		t.Fatalf("%s no longer contains heading %q", acContextLoadRel, heading)
	}
	rest := content[h:]
	for i := 0; i < n; i++ {
		open := strings.Index(rest, "```bash\n")
		if open < 0 {
			t.Fatalf("fewer than %d bash blocks under %q", n, heading)
		}
		body := rest[open+len("```bash\n"):]
		end := strings.Index(body, "\n```")
		if end < 0 {
			t.Fatalf("unterminated bash block %d under %q", i+1, heading)
		}
		if i == n-1 {
			return body[:end]
		}
		rest = body[end:]
	}
	t.Fatalf("unreachable")
	return ""
}

// sandboxPath builds a PATH containing only the utilities the skill's shell
// uses, so a `nightgauge` that happens to be installed on the developer's
// machine cannot satisfy the resolution cascade and mask the unresolved-binary
// case.
func sandboxPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"bash", "sh", "git", "jq", "mktemp", "cat", "rm", "sed", "head", "dirname", "mkdir", "env", "chmod"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH; the skill's shell needs it", tool)
		}
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "nightgauge")); err == nil {
		t.Fatalf("sandbox PATH leaked a nightgauge binary")
	}
	return dir
}

// stubBinary writes a fake `nightgauge` whose ac-check emits stdout/stderr and
// exits with the given code.
func stubBinary(t *testing.T, stdout, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nightgauge")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\nprintf '%%s' %s >&2\nexit %d\n",
		shellQuote(stdout), shellQuote(stderr), exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type acGateResult struct {
	exitCode int
	output   string
}

// runACGate executes the extracted gate with NIGHTGAUGE_BIN pointed at binary
// ("" to exercise the unresolved-binary path).
func runACGate(t *testing.T, binary string) acGateResult {
	t.Helper()
	script := acGateScript(t)
	work := t.TempDir()
	home := t.TempDir()
	file := filepath.Join(work, "ac-gate.sh")
	if err := os.WriteFile(file, []byte("set -u\n"+script), 0o644); err != nil {
		t.Fatalf("write gate script: %v", err)
	}

	cmd := exec.Command("bash", file)
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=" + sandboxPath(t),
		"HOME=" + home,
		"NIGHTGAUGE_BIN=" + binary,
		"ISSUE_NUMBER=1145",
		"AC_CHECK_REQUIRED=true",
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run gate: %v\n%s", err, out)
	}
	return acGateResult{exitCode: code, output: string(out)}
}

func (r acGateResult) mustContain(t *testing.T, label string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(r.output, w) {
			t.Errorf("%s: output missing %q\n--- output ---\n%s", label, w, r.output)
		}
	}
}

func (r acGateResult) mustExit(t *testing.T, label string, want int) {
	t.Helper()
	if r.exitCode != want {
		t.Errorf("%s: exit = %d, want %d\n--- output ---\n%s", label, r.exitCode, want, r.output)
	}
}

// ─── The fail-open cases #1145 was filed about ───────────────────────────────

func TestACGate_EmptyOutputDoesNotPass(t *testing.T) {
	// The verb "succeeded" but wrote nothing — the shape a forge fetch failure
	// took before #1145. Pre-fix this printed "✓ AC completion check passed —
	// all  box(es) checked" and exited 0.
	got := runACGate(t, stubBinary(t, "", "", 0))
	got.mustExit(t, "empty ac-check output", 1)
	got.mustContain(t, "empty ac-check output", "COULD NOT RUN", "no output")
	if strings.Contains(got.output, "check passed") {
		t.Errorf("empty output reported the gate as passed:\n%s", got.output)
	}
}

func TestACGate_NonZeroExitDoesNotPass(t *testing.T) {
	got := runACGate(t, stubBinary(t, "", "get issue #1145: 502 Bad Gateway", 3))
	got.mustExit(t, "non-zero ac-check exit", 1)
	got.mustContain(t, "non-zero ac-check exit",
		"COULD NOT RUN",
		"exited 3",
		// stderr must be surfaced, not blackholed
		"502 Bad Gateway",
	)
}

func TestACGate_UnresolvedBinaryDoesNotPass(t *testing.T) {
	// Every rung of the resolution cascade misses: NIGHTGAUGE_BIN empty, no
	// nightgauge on PATH, cwd is not a repo with bin/nightgauge, $HOME/go/bin
	// empty. Pre-fix this ran `"" issue ac-check`, which the shell reports as
	// "command not found" on stderr — discarded — and the gate passed.
	got := runACGate(t, "")
	got.mustExit(t, "unresolved binary", 1)
	got.mustContain(t, "unresolved binary", "COULD NOT RUN", "binary could not be resolved")
}

func TestACGate_UnparseableOutputDoesNotPass(t *testing.T) {
	got := runACGate(t, stubBinary(t, "not json at all", "", 0))
	got.mustExit(t, "unparseable ac-check output", 1)
	got.mustContain(t, "unparseable ac-check output", "COULD NOT RUN")
}

func TestACGate_UnrecognizedStatusDoesNotPass(t *testing.T) {
	// Only an explicit "passed" may pass. If the verb's enum ever grows a value
	// this gate has not been taught, that is "did not run", not "passed".
	got := runACGate(t, stubBinary(t, `{"status":"indeterminate","checked_count":0,"unchecked_count":0,"total":0}`, "", 0))
	got.mustExit(t, "unrecognized status", 1)
	got.mustContain(t, "unrecognized status", "UNRECOGNIZED STATUS", "indeterminate")
}

// ─── The genuine verdicts still behave ───────────────────────────────────────

func TestACGate_PassedStillPasses(t *testing.T) {
	got := runACGate(t, stubBinary(t, `{"status":"passed","checked_count":4,"unchecked_count":0,"total":4}`, "", 0))
	got.mustExit(t, "passed", 0)
	got.mustContain(t, "passed",
		"status=passed checked=4 unchecked=0",
		"✓ AC completion check passed — all 4 box(es) checked",
		"AC_COMPLETION_STATUS=passed",
	)
}

func TestACGate_FailedStillFails(t *testing.T) {
	got := runACGate(t, stubBinary(t, `{"status":"failed","checked_count":1,"unchecked_count":2,"total":3}`, "", 0))
	got.mustExit(t, "failed", 1)
	// #1233 reworded this: "2 unchecked boxes" told a human nothing about WHICH
	// sentence the change failed to satisfy.
	got.mustContain(t, "failed", "AC COMPLETION CHECK FAILED — 2 criterion(a) could not be substantiated")
}

func TestACGate_NotApplicableStillPassesThrough(t *testing.T) {
	got := runACGate(t, stubBinary(t, `{"status":"not_applicable","checked_count":0,"unchecked_count":0,"total":0}`, "", 0))
	got.mustExit(t, "not_applicable", 0)
	got.mustContain(t, "not_applicable", "not_applicable", "AC_COMPLETION_STATUS=not_applicable")
}

// ─── #1233: the gate must be satisfiable, and only by evidence ───────────────

// A stub that answers ac-check with `before` until an `ac-mark` is issued, then
// with `after`. It records every argv it was called with, so a test can assert
// WHICH criteria were marked rather than only the resulting verdict.
func stubMarkAwareBinary(t *testing.T, before, after string) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "nightgauge")
	logPath = filepath.Join(dir, "calls.log")
	marker := filepath.Join(dir, "marked")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %s
case "$2" in
  ac-mark)
    for a in "$@"; do
      if [ "$a" = "--list" ]; then
        printf '%%s' '{"items":[{"index":1,"text":"one","checked":false},{"index":2,"text":"two","checked":false}]}'
        exit 0
      fi
    done
    : > %s
    printf '%%s' '{"changed":[1]}'
    exit 0
    ;;
  ac-check)
    if [ -f %s ]; then printf '%%s' %s; else printf '%%s' %s; fi
    exit 0
    ;;
esac
exit 0
`, shellQuote(logPath), shellQuote(marker), shellQuote(marker), shellQuote(after), shellQuote(before))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path, logPath
}

// runACGateWithEnv is runACGate with extra environment, so a test can supply the
// AC_SUBSTANTIATED / AC_EVIDENCE_JSON the model's judgement would have set.
func runACGateWithEnv(t *testing.T, binary string, judgement string) acGateResult {
	t.Helper()
	script := acGateScriptWith(t, judgement)
	work := t.TempDir()
	home := t.TempDir()
	file := filepath.Join(work, "ac-gate.sh")
	// `set -u` as in runACGate, but the substantiation vars are assigned by the
	// model between the two fenced blocks; seed them the way the stage would.
	if err := os.WriteFile(file, []byte("set -u\n"+script), 0o644); err != nil {
		t.Fatalf("write gate script: %v", err)
	}
	cmd := exec.Command("bash", file)
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=" + sandboxPath(t),
		"HOME=" + home,
		"NIGHTGAUGE_BIN=" + binary,
		"ISSUE_NUMBER=1233",
		"AC_CHECK_REQUIRED=true",
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run gate: %v\n%s", err, out)
	}
	return acGateResult{exitCode: code, output: string(out)}
}

// THE DEADLOCK. Before #1233 this was unreachable: ac-check said failed, nothing
// wrote `- [x]`, and the stage exited 1 forever. Substantiating the criteria has
// to be able to clear the gate, or a type:docs issue can never validate.
func TestACGate_SubstantiatedCriteriaClearTheGate(t *testing.T) {
	bin, calls := stubMarkAwareBinary(t,
		`{"status":"failed","checked_count":0,"unchecked_count":2,"total":2}`,
		`{"status":"passed","checked_count":2,"unchecked_count":0,"total":2}`)

	got := runACGateWithEnv(t, bin, `AC_SUBSTANTIATED="1 2"
AC_EVIDENCE_JSON='[{"index":1,"verdict":"substantiated","evidence":"doc.md section 3 rewritten"},{"index":2,"verdict":"substantiated","evidence":"mermaid graph updated"}]'`)

	got.mustExit(t, "substantiated", 0)
	got.mustContain(t, "substantiated", "Marked substantiated criteria: 1 2", "AC_COMPLETION_STATUS=passed")

	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "--check 1") || !strings.Contains(string(log), "--check 2") {
		t.Errorf("ac-mark was not called with the substantiated indices:\n%s", log)
	}
	// The verdict must be RE-READ from the verb, never assumed from the write.
	if strings.Count(string(log), "ac-check") < 2 {
		t.Errorf("the gate did not re-check after marking:\n%s", log)
	}
}

// The other half, and the one that keeps this from being a rubber stamp: a
// criterion the stage could not substantiate still fails, and the operator is
// told WHICH and WHY.
func TestACGate_UnsubstantiatedCriteriaStillFail(t *testing.T) {
	bin, _ := stubMarkAwareBinary(t,
		`{"status":"failed","checked_count":0,"unchecked_count":2,"total":2}`,
		`{"status":"failed","checked_count":1,"unchecked_count":1,"total":2}`)

	got := runACGateWithEnv(t, bin, `AC_SUBSTANTIATED="1"
AC_EVIDENCE_JSON='[{"index":1,"verdict":"substantiated","evidence":"doc.md updated"},{"index":2,"verdict":"unsubstantiated","evidence":"asserts a visual result the diff cannot show"}]'`)

	got.mustExit(t, "partial", 1)
	got.mustContain(t, "partial",
		"AC COMPLETION CHECK FAILED",
		"asserts a visual result the diff cannot show",
	)
}

// Substantiating NOTHING must leave the gate exactly as it was — the deadlock
// path is still a failure, not an accidental pass.
func TestACGate_NoSubstantiationChangesNothing(t *testing.T) {
	bin, calls := stubMarkAwareBinary(t,
		`{"status":"failed","checked_count":0,"unchecked_count":3,"total":3}`,
		`{"status":"passed","checked_count":3,"unchecked_count":0,"total":3}`)

	got := runACGateWithEnv(t, bin, `AC_SUBSTANTIATED=""
AC_EVIDENCE_JSON='[]'`)

	got.mustExit(t, "none substantiated", 1)
	log, _ := os.ReadFile(calls)
	if strings.Contains(string(log), "--check") {
		t.Errorf("ac-mark was called with nothing substantiated — that is the rubber stamp:\n%s", log)
	}
}

// A failed write must not be laundered into a pass.
func TestACGate_MarkFailureDoesNotPass(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "nightgauge")
	script := `#!/bin/sh
case "$2" in
  ac-mark)
    for a in "$@"; do
      if [ "$a" = "--list" ]; then printf '%s' '{"items":[{"index":1,"text":"one","checked":false}]}'; exit 0; fi
    done
    echo "forge rejected the edit" >&2
    exit 1
    ;;
  ac-check) printf '%s' '{"status":"failed","checked_count":0,"unchecked_count":1,"total":1}'; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	got := runACGateWithEnv(t, bin, `AC_SUBSTANTIATED="1"
AC_EVIDENCE_JSON='[{"index":1,"verdict":"substantiated","evidence":"x"}]'`)

	got.mustExit(t, "mark failed", 1)
	got.mustContain(t, "mark failed", "ac-mark failed")
}

// ─── `applicable` must mean what docs/CONTEXT_ARCHITECTURE.md says ───────────

const acBoardRel = "skills/nightgauge-feature-validate/_includes/context-and-board.md"

// acApplicableExpr lifts the `$( … )` derivation of ac_applicable out of the
// deliverable's jq invocation.
func acApplicableExpr(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", filepath.FromSlash(acBoardRel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", acBoardRel, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "--argjson ac_applicable") {
			continue
		}
		open := strings.Index(line, "$(")
		close := strings.LastIndex(line, ")")
		if open < 0 || close <= open {
			t.Fatalf("ac_applicable line has no command substitution: %q", line)
		}
		return line[open+2 : close]
	}
	t.Fatalf("%s no longer derives ac_applicable", acBoardRel)
	return ""
}

func TestACApplicableIsTrueOnlyForDocsIssues(t *testing.T) {
	// docs/CONTEXT_ARCHITECTURE.md: applicable is "true only for type:docs
	// issues". #1145: it was derived from AC_COMPLETION_STATUS != "not_applicable",
	// so the non-docs path (which sets AC_COMPLETION_STATUS="skipped") reported
	// applicable:true on EVERY run, and a docs issue with no checkboxes —
	// where the gate genuinely did apply — reported false.
	expr := acApplicableExpr(t)
	cases := []struct {
		label    string
		required string
		status   string
		want     string
	}{
		{"type:docs issue, gate ran and passed", "true", "passed", "true"},
		{"type:docs issue, gate ran and failed", "true", "failed", "true"},
		{"type:docs issue, no checkboxes found", "true", "not_applicable", "true"},
		{"type:docs issue, gate could not run", "true", "error", "true"},
		{"non-docs issue, gate skipped", "false", "skipped", "false"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", expr)
			cmd.Env = []string{
				"PATH=" + sandboxPath(t),
				"AC_CHECK_REQUIRED=" + tc.required,
				"AC_COMPLETION_STATUS=" + tc.status,
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("evaluate ac_applicable: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Errorf("applicable = %q, want %q (AC_CHECK_REQUIRED=%s AC_COMPLETION_STATUS=%s)",
					got, tc.want, tc.required, tc.status)
			}
		})
	}
}
