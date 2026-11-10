package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// contractSchemaVersionRE finds the schema_version the validate-context
// writer stamps, so the test follows the include instead of hardcoding it.
var contractSchemaVersionRE = regexp.MustCompile(`schema_version: "[0-9.]+"`)

// TestFeatureValidatePromptCarriesItsOutputContract3031: at the default
// include budget the rendered feature-validate prompt supplies the Phase 6
// output contract, so a validate that never reads the file still has
// acceptance_criteria_verdicts and the writer's schema_version in front of it.
func TestFeatureValidatePromptCarriesItsOutputContract3031(t *testing.T) {
	root := realSkillsRoot(t)
	include, err := os.ReadFile(filepath.Join(root, "nightgauge-feature-validate", "_includes", "context-and-board.md"))
	if err != nil {
		t.Fatalf("read contract include: %v", err)
	}
	version := contractSchemaVersionRE.FindString(string(include))
	if version == "" {
		t.Fatal("context-and-board.md has no schema_version line")
	}

	res := mustRender(t, Options{Stage: "feature-validate", SkillsRoots: []string{root}, Headless: true})
	if !slices.Contains(res.SuppliedIncludes, "context-and-board.md") {
		t.Fatalf("context-and-board.md not supplied; supplied = %v", res.SuppliedIncludes)
	}
	for _, want := range []string{"acceptance_criteria_verdicts", "{index, text, status, evidence, pr_note}", version} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("rendered feature-validate prompt lacks %q", want)
		}
	}
	// Supplying the contract first must not push out what was already supplied.
	for _, name := range []string{"context-load.md", "test-setup.md", "build-verification.md", "dead-code.md", "baseline-comparison.md", "run-tests.md"} {
		if !slices.Contains(res.SuppliedIncludes, name) {
			t.Errorf("%s no longer supplied; supplied = %v", name, res.SuppliedIncludes)
		}
	}
}

// TestSupplyPhaseIncludesSuppliesMarkedFirst3031: an include carrying
// SupplyFirstMarker is supplied even when its directive comes last and the
// earlier, unmarked includes would have spent the budget; an unmarked one
// that no longer fits stays a read directive.
func TestSupplyPhaseIncludesSuppliesMarkedFirst3031(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "nightgauge-x")
	write(t, filepath.Join(skillDir, "_includes", "a.md"), "# A\n"+strings.Repeat("a", 40)+"\n")
	write(t, filepath.Join(skillDir, "_includes", "b.md"), "# B\n"+strings.Repeat("b", 40)+"\n")
	write(t, filepath.Join(skillDir, "_includes", "contract.md"), "# Contract\n"+SupplyFirstMarker+"\nverdicts\n")
	// A marker that is not on a line of its own does not count.
	write(t, filepath.Join(skillDir, "_includes", "inline.md"), "# I `"+SupplyFirstMarker+"`\n")
	body := "> **Read `_includes/a.md` (same directory as this SKILL.md) now.**\n" +
		"> **Read `_includes/b.md` (same directory as this SKILL.md) now.**\n" +
		"> **Read `_includes/inline.md` (same directory as this SKILL.md) now.**\n" +
		"> **Read `_includes/contract.md` (same directory as this SKILL.md) now.**\n"

	got, supplied := SupplyPhaseIncludes(body, skillDir, 100)
	if strings.Join(supplied, ",") != "contract.md,a.md" {
		t.Fatalf("supplied = %v, want [contract.md a.md]", supplied)
	}
	for _, want := range []string{
		"Follow `_includes/contract.md` (supplied in full",
		"Read `_includes/b.md` (same directory as this SKILL.md) now",
		"Read `_includes/inline.md` (same directory as this SKILL.md) now",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result lacks %q", want)
		}
	}
	if strings.Index(got, "### `_includes/contract.md`") > strings.Index(got, "### `_includes/a.md`") {
		t.Error("contract.md is not appended ahead of a.md")
	}
}
