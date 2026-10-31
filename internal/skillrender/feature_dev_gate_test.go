package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── #2784: one gate per tree ──────────────────────────────────────────────
//
// feature-dev runs focused checks and receives every include; the complete gate
// is feature-validate's, and that stage's Phase 2.5 asks the ledger first.

var fdIncludes = []string{
	"context-and-feedback-intake.md",
	"plan-knowledge-and-standards.md",
	"implementation-and-testing.md",
	"review-and-correction.md",
	"context-and-epilogue.md",
}

// defaultRender renders stage as dispatch does, with no explicit include budget.
func defaultRender(t *testing.T, stage string) *Result {
	t.Helper()
	return mustRender(t, Options{Stage: stage, SkillsRoots: []string{realSkillsRoot(t)}})
}

func TestFeatureDevRender_SuppliesEveryInclude(t *testing.T) {
	full := defaultRender(t, "feature-dev")
	if len(full.SuppliedIncludes) != len(fdIncludes) {
		t.Fatalf("SuppliedIncludes = %v, want all of %v", full.SuppliedIncludes, fdIncludes)
	}
	for _, name := range fdIncludes {
		if !regexp.MustCompile("### `[^`]*_includes/" + regexp.QuoteMeta(name) + "`").MatchString(full.Content) {
			t.Errorf("full render does not supply %s", name)
		}
	}
	if m := phaseReadNowRE.FindString(full.Content); m != "" {
		t.Errorf("full render still directs a read of an include: %q", m)
	}
}

// Other stages keep the shared budget: feature-validate's includes exceed it, so
// it leaves some as reads. If the exemption leaked, it would supply them all.
func TestIncludeBudget_OnlyFeatureDevIsExempt(t *testing.T) {
	every := mustRender(t, Options{Stage: "feature-validate", SkillsRoots: []string{realSkillsRoot(t)}, IncludeBudget: EveryPhaseInclude})
	if got := defaultRender(t, "feature-validate"); len(got.SuppliedIncludes) >= len(every.SuppliedIncludes) {
		t.Errorf("feature-validate supplied %d of %d includes, so its budget grew",
			len(got.SuppliedIncludes), len(every.SuppliedIncludes))
	}
	if got := includeBudget(Options{Stage: "feature-validate"}); got != 0 {
		t.Errorf("feature-validate budget = %d, want the default (0)", got)
	}
	if got := includeBudget(Options{Stage: "feature-dev"}); got != EveryPhaseInclude {
		t.Errorf("feature-dev budget = %d", got)
	}
	if got := includeBudget(Options{Stage: "feature-dev", IncludeBudget: -1}); got != -1 {
		t.Errorf("an explicit budget lost to the exemption: %d", got)
	}
}

// A gate command only counts when the stage is told to run it: prose naming
// `ci-local.sh` as feature-validate's is exactly what the stage must say.
func TestFeatureDev_RunsNoGateCommand(t *testing.T) {
	full, compact := defaultRender(t, "feature-dev"), mustRender(t, Options{
		Stage: "feature-dev", SkillsRoots: []string{realSkillsRoot(t)}, Profile: ProfileCompact,
	})
	fence := regexp.MustCompile("(?s)```bash\n(.*?)```")
	gate := regexp.MustCompile(`(?m)^[^#\n]*\b(ci parity-check|ci-local\.sh)`)
	for name, content := range map[string]string{"full": full.Content, "compact": compact.Content} {
		for _, block := range fence.FindAllStringSubmatch(content, -1) {
			if hit := gate.FindString(block[1]); hit != "" {
				t.Errorf("%s render runs a gate: %s", name, hit)
			}
		}
	}
}

// The skill, its profile and the include must agree on Step 6.4.
func TestFeatureDev_StepSixFourAgrees(t *testing.T) {
	const heading = "Verify Focused Checks Pass (HARD GATE)"
	for _, rel := range []string{"SKILL.md", "_profiles/compact.md", "_includes/review-and-correction.md"} {
		data, err := os.ReadFile(filepath.Join(realSkillsRoot(t), "nightgauge-feature-dev", rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), heading) {
			t.Errorf("%s does not carry %q", rel, heading)
		}
		if strings.Contains(string(data), "CI-parity HARD GATE") {
			t.Errorf("%s still names the full gate as this stage's", rel)
		}
	}
}

func TestCompactFeatureDev_ReadsTheReviewInclude(t *testing.T) {
	_, compact := renderStagePair(t, "feature-dev")
	if !strings.Contains(compact.Content, "review-and-correction.md") {
		t.Error("compact render lost its directive for review-and-correction.md")
	}
}

// The three arms of Phase 2.5, in the order that makes them one gate per tree.
func TestFeatureValidate_PhaseTwoFiveRunsOneGate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(realSkillsRoot(t), "nightgauge-feature-validate", "_includes", "ci-and-knowledge.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	order := []string{
		"nightgauge ci gate-status --kind parity --json",
		`CI_PARITY_COMMANDS=("$CI_GATE_COMMAND")`,
		"--event finish --run-id \"$CI_GATE_RUN\" --kind parity",
		`if [ "$CI_GATE_REUSABLE" = "true" ]; then`,
		`status: "skipped"`,
		"--event start --kind parity",
	}
	at := -1
	for _, want := range order {
		i := strings.Index(body[at+1:], want)
		if i < 0 {
			t.Fatalf("Phase 2.5 lost, or reordered, %q", want)
		}
		at += i + 1
	}
	// A gate that records itself must not get a second start: that start would
	// never finish, and would count as a killed gate.
	if !strings.Contains(body, `if [ "$CI_GATE_RECORDS_ITSELF" != "true" ] || [ -z "$CI_GATE_COMMAND" ]; then
    CI_GATE_RUN=$(nightgauge ci gate-record --event start`) {
		t.Error("the parity start is not guarded against a self-recording gate")
	}
	if !strings.Contains(body, "NIGHTGAUGE_PROGRESS:") {
		t.Error("the gate runs with no declared log")
	}
}

func TestFeatureValidateCompact_NamesTheOneGate(t *testing.T) {
	_, compact := renderStagePair(t, "feature-validate")
	if !strings.Contains(compact.Content, "nightgauge ci gate-status") {
		t.Error("compact Phase 2.5 does not name the gate-status check")
	}
}
