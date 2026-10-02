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

const epicGateHeading = "## Phase 2.9: Epic Decomposition Hard-Gate"

// TestEpicGateMarkersInALongBodyUnderPipefail runs Phase 2.9's detection
// fence, lifted verbatim out of scope-gates.md, under pipefail with each body
// marker on the first line of a body several pipe buffers long (#2360). The
// marker checks used to pipe the body into `grep -q`, which exits at its first
// match, so printf died of SIGPIPE writing the rest and pipefail turned the
// found marker into a miss. That is no race at this size: the body cannot fit
// in the pipe, so the writer is always still writing when grep exits.
func TestEpicGateMarkersInALongBodyUnderPipefail(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(scopeGatesRel)))
	if err != nil {
		t.Fatalf("read %s: %v", scopeGatesRel, err)
	}
	detect := nthFencedBashAfter(t, string(data), epicGateHeading, 1)
	filler := strings.Repeat("filler line\n", 40000) // 480 KB

	for _, tc := range []struct{ name, marker, flag string }{
		{"a decompose-later placeholder", "<!-- nightgauge:decompose-later -->", "HAS_PLACEHOLDER_MARKER"},
		{"a standalone epic", "<!-- nightgauge:standalone-epic -->", "HAS_STANDALONE_MARKER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			bin := t.TempDir()
			for _, tool := range []string{"bash", "grep", "cat"} {
				src, err := exec.LookPath(tool)
				if err != nil {
					t.Skipf("%s is not on PATH; the skill's shell needs it", tool)
				}
				if err := os.Symlink(src, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			// The body is read from a file, not the environment, which could
			// not carry half a megabyte everywhere.
			body := filepath.Join(work, "body.md")
			if err := os.WriteFile(body, []byte(tc.marker+"\n"+filler), 0o644); err != nil {
				t.Fatal(err)
			}
			script := "set -o pipefail\n" +
				"ISSUE_BODY=\"$(cat " + shellQuote(body) + ")\"\n" +
				detect + "\n" +
				"echo \"" + tc.flag + "=${" + tc.flag + "}\"\n"
			file := filepath.Join(work, "epic-gate.sh")
			if err := os.WriteFile(file, []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", file)
			cmd.Dir = work
			cmd.Env = []string{"PATH=" + bin, "HOME=" + work, "TYPE_LABEL=epic", "SUB_ISSUE_COUNT=0"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Phase 2.9 detection exited %v\n%s", err, out)
			}
			if want := tc.flag + "=true"; !strings.Contains(string(out), want) {
				t.Errorf("Phase 2.9 missed %s on the first line of a long body under pipefail: want %s\n%s", tc.marker, want, out)
			}
		})
	}
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
