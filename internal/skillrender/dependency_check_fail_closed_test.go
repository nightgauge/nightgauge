package skillrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A dependency check that could not run is an unknown, never "no open
// dependencies" (#1700). The skills used to write
// `check-deps ... || echo '{"has_open_dependencies":false}'`, so a failed read
// made a candidate look unblocked, and treated `deps-gate check` exit 2 as a
// pass. These tests run the shell exactly as the skills carry it, lifted out
// of the markdown, against a stubbed `nightgauge` whose answer per issue is
// chosen:
//
//	101  check-deps fails: exit 1, nothing on stdout (a rate limit, say)
//	102  check-deps reports an open blocker (exit 1 under --check-only)
//	103  check-deps reports no open blocker
//
// and whose `deps-gate check` exit comes from DEPS_GATE_EXIT.

const (
	autoSelectionRel      = "skills/_shared/AUTO_SELECTION.md"
	dependencyCheckingRel = "skills/_shared/DEPENDENCY_CHECKING.md"
	pickupGatesRel        = "skills/nightgauge-issue-pickup/_includes/issue-selection-and-gates.md"
	pickupCompactRel      = "skills/nightgauge-issue-pickup/_profiles/compact.md"
)

const depsStub = `#!/bin/sh
echo "$*" >> "$STUB_CALLS"
case "$1 $2" in
  'hook check-deps')
    case "$3" in
      101) exit 1 ;;
      102) printf '%s\n' '{"issue_number":102,"has_open_dependencies":true,"open_dependencies":[{"number":9,"title":"blocker"}],"open_count":1}'
           case "$*" in *--check-only*) exit 1 ;; esac
           exit 0 ;;
      103) printf '%s\n' '{"issue_number":103,"has_open_dependencies":false,"open_dependencies":[],"open_count":0}'
           exit 0 ;;
    esac
    exit 3 ;;
  'deps-gate check')
    case "$DEPS_GATE_EXIT" in
      1) printf '%s\n' '{"issue_number":7,"decision":"deferred","open_dependencies":[{"number":9,"title":"blocker"}],"open_count":1,"reason":"blocked"}' ;;
      2) echo "could not run: evaluate deps gate for #7: rate limited" >&2 ;;
      *) printf '%s\n' '{"issue_number":7,"decision":"allow","open_count":0}' ;;
    esac
    exit "$DEPS_GATE_EXIT" ;;
esac
# forge issue view / forge issue comment / outcome record / git repo-slug
exit 0
`

func readSkillFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// fencedBashContaining returns the body of the bash fence in rel's content
// that contains marker.
func fencedBashContaining(t *testing.T, rel, content, marker string) string {
	t.Helper()
	rest := content
	for {
		open := strings.Index(rest, "```bash\n")
		if open < 0 {
			t.Fatalf("%s has no bash block containing %q", rel, marker)
		}
		body := rest[open+len("```bash\n"):]
		end := strings.Index(body, "\n```")
		if end < 0 {
			t.Fatalf("%s: unterminated bash block", rel)
		}
		if strings.Contains(body[:end], marker) {
			return body[:end]
		}
		rest = body[end:]
	}
}

type snippetRun struct {
	exitCode int
	output   string
	calls    string
}

// runDepsSnippet runs script under bash with the stub as `nightgauge` (on
// PATH and as NIGHTGAUGE_BIN/BINARY) and env added.
func runDepsSnippet(t *testing.T, script string, env ...string) snippetRun {
	t.Helper()
	work := t.TempDir()
	bin := t.TempDir()
	stub := filepath.Join(bin, "nightgauge")
	if err := os.WriteFile(stub, []byte(depsStub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"bash", "sh", "jq", "head", "sed", "cat", "rm", "dirname", "git", "mktemp"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH; the skill's shell needs it", tool)
		}
		if err := os.Symlink(src, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(work, "calls")
	file := filepath.Join(work, "snippet.sh")
	if err := os.WriteFile(file, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", file)
	cmd.Dir = work
	cmd.Env = append([]string{
		"PATH=" + bin,
		"HOME=" + work,
		"BINARY=" + stub,
		"NIGHTGAUGE_BIN=" + stub,
		"STUB_CALLS=" + calls,
	}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run snippet: %v", err)
		}
		code = exitErr.ExitCode()
	}
	callLog, _ := os.ReadFile(calls)
	return snippetRun{exitCode: code, output: string(out), calls: string(callLog)}
}

func TestAutoSelection_FailedCheckSkipsTheCandidate(t *testing.T) {
	content := readSkillFile(t, autoSelectionRel)
	step2 := fencedBashContaining(t, autoSelectionRel, content, "# Step 2: blocker check")
	script := step2 + "\nprintf 'SELECTED=%s\\n' \"$(printf '%s\\n' \"$ISSUE\" | jq -r '.number // empty')\"\n"
	candidates := func(nums ...string) string {
		parts := make([]string, len(nums))
		for i, n := range nums {
			parts[i] = `{"number":` + n + `}`
		}
		return "TIER_CANDIDATES=[" + strings.Join(parts, ",") + "]"
	}

	// The failed check comes first: the old fallback selected it.
	r := runDepsSnippet(t, script, candidates("101", "102", "103"), "BOARD=/nonexistent")
	if !strings.Contains(r.output, "SELECTED=103\n") {
		t.Fatalf("want #103 selected (101's check failed, 102 is blocked); output:\n%s", r.output)
	}
	if !strings.Contains(r.output, "#101 skipped: its dependency check could not run") {
		t.Errorf("the skipped candidate is not named; output:\n%s", r.output)
	}

	r = runDepsSnippet(t, script, candidates("101"), "BOARD=/nonexistent")
	if !strings.Contains(r.output, "SELECTED=\n") {
		t.Fatalf("a lone candidate whose check failed must not be selected; output:\n%s", r.output)
	}
}

func TestAutoSelection_AllBlockedLeavesOutAFailedCheck(t *testing.T) {
	content := readSkillFile(t, autoSelectionRel)
	block := fencedBashContaining(t, autoSelectionRel, content, "BLOCKED_ISSUES=\"[]\"")
	script := block + "\nprintf 'BLOCKED=%s\\n' \"$(printf '%s\\n' \"$BLOCKED_ISSUES\" | jq -c '[.[].number]')\"\n"

	r := runDepsSnippet(t, script, `CANDIDATES=[{"number":101},{"number":102}]`)
	if !strings.Contains(r.output, "BLOCKED=[102]\n") {
		t.Fatalf("want only #102 listed as blocked; output:\n%s", r.output)
	}
	if !strings.Contains(r.output, "#101 left out") {
		t.Errorf("the left-out candidate is not named; output:\n%s", r.output)
	}
}

func TestDependencyChecking_FetchStopsWhenTheCheckFails(t *testing.T) {
	content := readSkillFile(t, dependencyCheckingRel)
	fetch := fencedBashContaining(t, dependencyCheckingRel, content, "hook check-deps \"$ISSUE_NUMBER\"")
	script := fetch + "\nprintf 'HAS_OPEN_DEPS=%s\\n' \"$HAS_OPEN_DEPS\"\n"
	base := []string{"DEP_ENABLED=true", "DEP_MODE=warn"}

	r := runDepsSnippet(t, script, append(base, "NIGHTGAUGE_ISSUE_NUMBER=101")...)
	if r.exitCode == 0 || strings.Contains(r.output, "HAS_OPEN_DEPS=") {
		t.Fatalf("a failed check must stop the skill (exit %d); output:\n%s", r.exitCode, r.output)
	}
	if !strings.Contains(r.output, "could not check #101's dependencies") {
		t.Errorf("the stop does not say why; output:\n%s", r.output)
	}

	r = runDepsSnippet(t, script, append(base, "NIGHTGAUGE_ISSUE_NUMBER=103")...)
	if r.exitCode != 0 || !strings.Contains(r.output, "HAS_OPEN_DEPS=false\n") {
		t.Fatalf("a check that answered must proceed (exit %d); output:\n%s", r.exitCode, r.output)
	}
	r = runDepsSnippet(t, script, append(base, "NIGHTGAUGE_ISSUE_NUMBER=102")...)
	if r.exitCode != 0 || !strings.Contains(r.output, "HAS_OPEN_DEPS=true\n") {
		t.Fatalf("an open blocker is a verdict, not a failure (exit %d); output:\n%s", r.exitCode, r.output)
	}
}

// Every copy of the deps-gate block: exit 2 stops the pickup with no deferral
// comment and no deferral record; exit 1 still defers; exit 0 still passes.
func TestDepsGateBlocks_Exit2IsNotADeferral(t *testing.T) {
	for _, rel := range []string{dependencyCheckingRel, pickupGatesRel, pickupCompactRel} {
		t.Run(filepath.Base(filepath.Dir(rel))+"/"+filepath.Base(rel), func(t *testing.T) {
			content := readSkillFile(t, rel)
			gate := fencedBashContaining(t, rel, content, "deps-gate check --issue")
			script := "set -u\n" + gate + "\necho REACHED_END\n"

			r := runDepsSnippet(t, script, "NIGHTGAUGE_ISSUE_NUMBER=7", "DEPS_GATE_EXIT=2")
			if r.exitCode == 0 || strings.Contains(r.output, "REACHED_END") {
				t.Fatalf("exit 2 must stop the pickup (exit %d); output:\n%s", r.exitCode, r.output)
			}
			if strings.Contains(r.output, "signal=deferred") ||
				strings.Contains(r.calls, "forge issue comment") || strings.Contains(r.calls, "outcome record") {
				t.Fatalf("exit 2 must not defer, comment or record; output:\n%s\ncalls:\n%s", r.output, r.calls)
			}

			r = runDepsSnippet(t, script, "NIGHTGAUGE_ISSUE_NUMBER=7", "DEPS_GATE_EXIT=1")
			if r.exitCode != 0 || !strings.Contains(r.output, "signal=deferred") ||
				!strings.Contains(r.calls, "forge issue comment") {
				t.Fatalf("exit 1 must still defer (exit %d); output:\n%s\ncalls:\n%s", r.exitCode, r.output, r.calls)
			}

			r = runDepsSnippet(t, script, "NIGHTGAUGE_ISSUE_NUMBER=7", "DEPS_GATE_EXIT=0")
			if r.exitCode != 0 || !strings.Contains(r.output, "REACHED_END") ||
				strings.Contains(r.output, "signal=deferred") {
				t.Fatalf("exit 0 must still pass (exit %d); output:\n%s", r.exitCode, r.output)
			}
		})
	}
}
