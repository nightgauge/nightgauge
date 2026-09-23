package skillrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// issue-create's Phase 2.85 capacity gate (#1655), run as the skill runs it:
// the detection and decision shell lifted verbatim out of scope-gates.md,
// against a stubbed `nightgauge` whose `size-gate capacity --json` answers
// with a chosen cap — the same extract-the-fence-and-run-it pattern as the
// feature-validate AC gate tests in this package.

const scopeGatesRel = "skills/nightgauge-issue-create/_includes/scope-gates.md"

const scopeGateHeading = "## Phase 2.85: Oversized-Scope Hard-Gate"

type scopeGateCase struct {
	size, typeLabel, body, capacityJSON string
	subIssues                           string
}

type scopeGateRun struct {
	exitCode      int
	output        string
	capacityCalls int
}

func runScopeGate(t *testing.T, c scopeGateCase) scopeGateRun {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(scopeGatesRel)))
	if err != nil {
		t.Fatalf("read %s: %v", scopeGatesRel, err)
	}
	content := string(data)
	script := nthFencedBashAfter(t, content, scopeGateHeading, 1) + "\n" +
		nthFencedBashAfter(t, content, scopeGateHeading, 2) + "\n"

	work := t.TempDir()
	bin := t.TempDir()
	calls := filepath.Join(work, "capacity-calls")
	stub := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  'issue extract-targets') printf '%s' '{\"count\":1}' ;;\n" +
		"  'size-gate capacity') echo x >> " + shellQuote(calls) + "; printf '%s' \"$CAPACITY_JSON\" ;;\n" +
		"  *) echo \"unexpected nightgauge $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "nightgauge"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"bash", "sh", "jq", "grep", "tr", "cat"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH; the skill's shell needs it", tool)
		}
		if err := os.Symlink(src, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(work, "scope-gate.sh")
	// pipefail, as the stage's shell may run it: a marker check that pipes
	// into `grep -q` can lose a found marker to SIGPIPE under it.
	if err := os.WriteFile(file, []byte("set -o pipefail\n"+script), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", file)
	cmd.Dir = work
	cmd.Env = []string{
		"PATH=" + bin,
		"HOME=" + work,
		"PREDICTED_SIZE=" + c.size,
		"TYPE_LABEL=" + c.typeLabel,
		"ISSUE_BODY=" + c.body,
		"SUB_ISSUE_COUNT=" + c.subIssues,
		"CAPACITY_JSON=" + c.capacityJSON,
	}
	out, err := cmd.CombinedOutput()
	run := scopeGateRun{output: string(out)}
	if exitErr, ok := err.(*exec.ExitError); ok {
		run.exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run scope gate: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(calls); err == nil {
		run.capacityCalls = strings.Count(string(b), "x")
	}
	return run
}

func TestScopeGateCapacity(t *testing.T) {
	const capS = `{"max_size":"S"}`
	tests := []struct {
		name     string
		c        scopeGateCase
		wantExit int
		want     []string
		notWant  []string
	}{
		{
			name:     "M above an S cap is forced into decomposition",
			c:        scopeGateCase{size: "M", typeLabel: "feature", body: "## Summary\nwork", capacityJSON: capS},
			wantExit: 1,
			want:     []string{"capacity-gate — decomposition required", "exceeds the target model's capacity", "nightgauge:capacity-decomposed"},
		},
		{
			name:     "S within an S cap passes",
			c:        scopeGateCase{size: "S", typeLabel: "feature", body: "## Summary\nwork", capacityJSON: capS},
			wantExit: 0,
			want:     []string{"Phase 2.85: PASS", "capacity=S"},
		},
		{
			name:     "the oversized-scope marker does not override capacity",
			c:        scopeGateCase{size: "M", typeLabel: "feature", body: "<!-- nightgauge:oversized-scope-accepted -->", capacityJSON: capS},
			wantExit: 1,
			want:     []string{"decomposition required"},
		},
		{
			name:     "an epic decomposed now passes",
			c:        scopeGateCase{size: "M", typeLabel: "epic", body: "## Summary\nepic", capacityJSON: capS, subIssues: "3"},
			wantExit: 0,
			want:     []string{"Phase 2.85: PASS"},
		},
		{
			name:     "a capacity-forced child still over the cap is reported, not decomposed again",
			c:        scopeGateCase{size: "M", typeLabel: "feature", body: "<!-- nightgauge:capacity-decomposed -->\n## Summary", capacityJSON: capS},
			wantExit: 1,
			want:     []string{"requires human decomposition", "Decomposition stops at one level"},
			notWant:  []string{"decomposition required"},
		},
		{
			name:     "an unsized issue on a 32k target is not forced to decompose",
			c:        scopeGateCase{size: "", typeLabel: "feature", body: "## Summary", capacityJSON: capS},
			wantExit: 0,
			want:     []string{"capacity: size unknown", "Phase 2.85: PASS"},
			notWant:  []string{"decomposition required"},
		},
		{
			name:     "a marker ahead of a long body is still found under pipefail",
			c:        scopeGateCase{size: "M", typeLabel: "feature", body: "<!-- nightgauge:capacity-decomposed -->\n" + strings.Repeat("filler line\n", 8000), capacityJSON: capS},
			wantExit: 1,
			want:     []string{"requires human decomposition"},
		},
		{
			name:     "an unknown window applies no cap and says so",
			c:        scopeGateCase{size: "L", typeLabel: "feature", body: "## Summary", capacityJSON: `{"max_size":""}`},
			wantExit: 0,
			want:     []string{"capacity: window unknown", "Phase 2.85: PASS"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.c.subIssues == "" {
				tt.c.subIssues = "0"
			}
			run := runScopeGate(t, tt.c)
			if run.exitCode != tt.wantExit {
				t.Fatalf("exit = %d, want %d\n%s", run.exitCode, tt.wantExit, run.output)
			}
			for _, w := range tt.want {
				if !strings.Contains(run.output, w) {
					t.Errorf("output missing %q\n%s", w, run.output)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(run.output, w) {
					t.Errorf("output contains %q\n%s", w, run.output)
				}
			}
			if run.capacityCalls != 1 {
				t.Errorf("size-gate capacity was called %d times, want exactly 1", run.capacityCalls)
			}
		})
	}
}
