package skillrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── #1663: feature-validate's compact render profile ──────────────────────
//
// Validation is the honesty gate of the pipeline: a compact profile that
// dropped a gate or the anti-laundering rule would let a local run pass work
// that is not done. These tests pin skills/nightgauge-feature-validate/
// _profiles/compact.md to the issue's Verification list — budget fit at a
// 32768-token window, marker parity with the full render, the honesty and
// no-flaky-dismissal rules verbatim, the AC-gate and step-wiring guarantees
// re-pointed at the compact render, and absolute, existing Read paths.
// Shared helpers live in compact_issue_pickup_test.go.

// The two rules #1663 names, as they read in the base SKILL.md's Gotchas.
const (
	fvHonestyRule     = "**Honesty rule — record every gate result as observed.** Never turn a catch into a pass by weakening the check"
	fvNoFlakyRule     = "**Never dismiss a failing test as flaky without root-causing it.**"
	fvVerifyUIInclude = "_includes/verify-ui-gate.md"
)

func TestCompactFeatureValidate_FitsBudget(t *testing.T) {
	_, compact := renderStagePair(t, "feature-validate")
	got := fitCompact(t, "feature-validate", compact.Content)
	if !got.Fits {
		t.Errorf("Fit(feature-validate compact, %d) = %+v, want Fits=true", compactTestWindow, got)
	}
}

// TestCompactFeatureValidate_VerifyUIGateIsReadNotInlined pins the shape the
// budget depends on: the verify-ui gate procedure (the stage's largest
// single-phase include) is loaded by a Read directive at Phase 2.45, while
// its blocking rules stay in the skeleton. Inlining the include puts its
// procedure text into the render and fails here.
func TestCompactFeatureValidate_VerifyUIGateIsReadNotInlined(t *testing.T) {
	_, compact := renderStagePair(t, "feature-validate")
	skillDir := filepath.Dir(compact.SkillPath)
	include, err := os.ReadFile(filepath.Join(skillDir, filepath.FromSlash(fvVerifyUIInclude)))
	if err != nil {
		t.Fatalf("read verify-ui include: %v", err)
	}
	// The include's first non-empty line after its title is a stable,
	// unique fingerprint of its body.
	var fingerprint string
	for _, line := range strings.Split(string(include), "\n")[1:] {
		if s := strings.TrimSpace(line); len(s) > 40 {
			fingerprint = s
			break
		}
	}
	if fingerprint == "" {
		t.Fatal("could not fingerprint the verify-ui include")
	}
	if strings.Contains(compact.Content, fingerprint) {
		t.Errorf("compact render inlines the verify-ui include (found %q); it must stay a Read directive", fingerprint)
	}
	abs := filepath.Join(skillDir, filepath.FromSlash(fvVerifyUIInclude))
	if !strings.Contains(compact.Content, "Read `"+abs+"`") {
		t.Errorf("compact render has no Read directive for %s", abs)
	}
	for _, want := range []string{
		"### Phase 2.45: Web UI Verification Gate (verify-ui)",
		"Trigger detection is deterministic (`nightgauge ci classify-ui-surface`), never LLM-judged.",
		"UI-relevant diff with no registered flow → record an explicit skip reason, never a silent pass.",
		"`verify-ui-gate-failed`",
	} {
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing the verify-ui blocking rule %q", want)
		}
	}
}

func TestCompactFeatureValidate_MarkerParity(t *testing.T) {
	full, compact := renderStagePair(t, "feature-validate")
	for _, m := range missingMarkers(full.Content, compact.Content) {
		t.Errorf("compact render is missing must-survive marker: %s", m)
	}
}

// TestCompactFeatureValidate_KeepsHonestyAndGates checks the rules #1663
// retains verbatim in BOTH renders: a rule the full render lacked would make
// the compact render stricter than full, not a faithful trim of it.
func TestCompactFeatureValidate_KeepsHonestyAndGates(t *testing.T) {
	full, compact := renderStagePair(t, "feature-validate")
	for _, want := range []string{
		fvHonestyRule,
		fvNoFlakyRule,
		// The validate-{N}.json contract, both enforcement points.
		"**This stage is NOT complete until `.nightgauge/pipeline/validate-{N}.json` exists on disk.**",
		`test -s ".nightgauge/pipeline/validate-${ISSUE_NUMBER}.json"`,
		`CONTEXT_FILE=".nightgauge/pipeline/validate-${ISSUE_NUMBER}.json"`,
		// Build, test and lint (CI parity) gates.
		"**Build hard gate**",
		"### Phase 2: Run Tests (Redundancy-Aware)",
		"**HARD GATE**: they must pass (after up to 3 auto-fix attempts) or `VALIDATION_STATUS=failed`",
		"**Gate**: if `VALIDATION_STATUS` is `\"failed\"`, do NOT commit or push.",
	} {
		if !strings.Contains(full.Content, want) {
			t.Errorf("full render is missing %q — the compact profile cannot keep what the base does not have", want)
		}
		if !strings.Contains(compact.Content, want) {
			t.Errorf("compact render is missing %q", want)
		}
	}
}

// ─── The AC gate and step wiring, re-pointed at the compact render ─────────

// phaseSection returns the text of compact from heading up to the next
// "### " heading.
func phaseSection(t *testing.T, content, heading string) string {
	t.Helper()
	i := strings.Index(content, heading)
	if i < 0 {
		t.Fatalf("render has no %q", heading)
	}
	rest := content[i+len(heading):]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

const fvACPhaseHeading = "### Phase 0.6: AC Completion Check (type:docs)"

// acIncludeFromRender returns the context-load include a render's Phase 0.6
// directive actually tells the model to Read — the file whose shell the
// AC-gate tests (feature_validate_ac_gate_test.go) execute.
func acIncludeFromRender(t *testing.T, r *Result) string {
	t.Helper()
	section := phaseSection(t, r.Content, fvACPhaseHeading)
	m := compactReadDirectiveRE.FindStringSubmatch(section)
	if m == nil {
		t.Fatalf("Phase 0.6 carries no Read directive")
	}
	data, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatalf("Phase 0.6 Read directive %q: %v", m[1], err)
	}
	return string(data)
}

// TestCompactFeatureValidate_StepWiring is the table-driven reuse of
// feature_validate_step_wiring_test.go: every "### Step N.N" the Phase 0.6
// include defines is named by the render's Phase 0.6 directive, for the full
// AND the compact render. Step 0.6.2b (#1233), the substantiation step that
// makes the gate satisfiable, is checked by name as well.
func TestCompactFeatureValidate_StepWiring(t *testing.T) {
	full, compact := renderStagePair(t, "feature-validate")
	headingRe := regexp.MustCompile(`(?m)^### Step (0\.6(?:\.[0-9]+)+[a-z]?):`)
	for _, tc := range []struct {
		name string
		r    *Result
	}{{"full", full}, {"compact", compact}} {
		t.Run(tc.name, func(t *testing.T) {
			section := phaseSection(t, tc.r.Content, fvACPhaseHeading)
			if !strings.Contains(section, "0.6.2b") {
				t.Error("Phase 0.6 does not name Step 0.6.2b — the AC gate has no writer and every type:docs issue with unticked criteria deadlocks (#1233)")
			}
			steps := headingRe.FindAllStringSubmatch(acIncludeFromRender(t, tc.r), -1)
			if len(steps) == 0 {
				t.Fatal("the Phase 0.6 include defines no '### Step 0.6.N:' headings")
			}
			for _, s := range steps {
				if !strings.Contains(section, s[1]) {
					t.Errorf("Step %s is defined in the Phase 0.6 include but not named by the %s render's directive", s[1], tc.name)
				}
			}
		})
	}
}

// TestCompactFeatureValidate_ACGateDoesNotFailOpen is the table-driven reuse
// of feature_validate_ac_gate_test.go's fail-open cases, run against the
// shell lifted from the include the COMPACT render's Phase 0.6 directive
// Reads: empty output, a non-zero exit, an unresolved binary and unparseable
// output must never pass the gate.
func TestCompactFeatureValidate_ACGateDoesNotFailOpen(t *testing.T) {
	_, compact := renderStagePair(t, "feature-validate")
	content := acIncludeFromRender(t, compact)
	subHead := "### Step 0.6.2b: Substantiate Unchecked Criteria Against the Change"
	script := fencedBashAfter(t, content, "### Step 0.6.2: Run AC Completion Check") + "\n" +
		fencedBashAfter(t, content, subHead) + "\n" +
		nthFencedBashAfter(t, content, subHead, 2) + "\n" +
		fencedBashAfter(t, content, "### Step 0.6.3: Gate on Result") + "\n"

	for _, tc := range []struct {
		name   string
		binary func(t *testing.T) string
		want   []string
	}{
		{"empty output", func(t *testing.T) string { return stubBinary(t, "", "", 0) }, []string{"COULD NOT RUN", "no output"}},
		{"non-zero exit", func(t *testing.T) string { return stubBinary(t, "", "502 Bad Gateway", 3) }, []string{"COULD NOT RUN", "exited 3", "502 Bad Gateway"}},
		{"unresolved binary", func(t *testing.T) string { return "" }, []string{"COULD NOT RUN", "binary could not be resolved"}},
		{"unparseable output", func(t *testing.T) string { return stubBinary(t, "not json at all", "", 0) }, []string{"COULD NOT RUN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			file := filepath.Join(work, "ac-gate.sh")
			if err := os.WriteFile(file, []byte("set -u\n"+script), 0o644); err != nil {
				t.Fatalf("write gate script: %v", err)
			}
			cmd := exec.Command("bash", file)
			cmd.Dir = work
			cmd.Env = []string{
				"PATH=" + sandboxPath(t),
				"HOME=" + t.TempDir(),
				"NIGHTGAUGE_BIN=" + tc.binary(t),
				"ISSUE_NUMBER=1663",
				"AC_CHECK_REQUIRED=true",
			}
			out, err := cmd.CombinedOutput()
			code := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("run gate: %v\n%s", err, out)
			}
			got := acGateResult{exitCode: code, output: string(out)}
			got.mustExit(t, tc.name, 1)
			got.mustContain(t, tc.name, tc.want...)
			if strings.Contains(got.output, "check passed") {
				t.Errorf("%s reported the gate as passed:\n%s", tc.name, got.output)
			}
		})
	}
}

func TestCompactFeatureValidate_ReadDirectivesAreAbsoluteAndExist(t *testing.T) {
	_, compact := renderStagePair(t, "feature-validate")
	assertCompactReadDirectives(t, compact)
}

func TestCompactFeatureValidate_CodeBlocksAreVerbatim(t *testing.T) {
	assertProfileCodeBlocksAreVerbatim(t, "feature-validate")
}
