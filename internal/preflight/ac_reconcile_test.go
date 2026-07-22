package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseACs(t *testing.T) {
	body := `## Acceptance Criteria

- [ ] New file ` + "`docs/FOO.md`" + ` exists
- [x] npm run lint passes
* [X] Symbol added
- [ ]
Some prose, not a checkbox.
`
	acs := parseACs(body)
	if len(acs) != 3 {
		t.Fatalf("parsed %d ACs, want 3: %+v", len(acs), acs)
	}
	if acs[0].state != "unchecked" || acs[1].state != "checked" || acs[2].state != "checked" {
		t.Errorf("checkbox states wrong: %+v", acs)
	}
	if acs[2].index != 2 {
		t.Errorf("indices must be sequential, got %+v", acs)
	}
}

func TestReconcileACs_FileExists(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "docs/FOO.md", "# Foo\n")

	res := ReconcileACs(dir, 42, "- [ ] New file `docs/FOO.md` exists\n- [ ] New file `docs/MISSING.md` created\n")
	if len(res.AcceptanceCriteria) != 2 {
		t.Fatalf("want 2 criteria, got %+v", res.AcceptanceCriteria)
	}
	first, second := res.AcceptanceCriteria[0], res.AcceptanceCriteria[1]
	if first.Classification != ACSatisfied || first.RuleApplied == nil || *first.RuleApplied != "file-exists" {
		t.Errorf("first = %+v, want satisfied via file-exists", first)
	}
	if len(first.Evidence) != 1 || first.Evidence[0] != "docs/FOO.md" {
		t.Errorf("first evidence = %v", first.Evidence)
	}
	if second.Classification != ACUnsatisfied {
		t.Errorf("second = %+v, want unsatisfied", second)
	}
	if res.AggregateStatus != AggPartial {
		t.Errorf("aggregate = %s, want partial", res.AggregateStatus)
	}
	if res.SuggestedRoute.Approach != "standard" || len(res.SuggestedRoute.FocusACs) != 1 || res.SuggestedRoute.FocusACs[0] != 1 {
		t.Errorf("route = %+v", res.SuggestedRoute)
	}
}

func TestReconcileACs_AllSatisfied_VerifyAndClose(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, ".github/workflows/ci.yml", "name: CI\njobs:\n  build-and-test:\n    steps: []\n")
	writeTreeFile(t, dir, "package.json", `{"scripts":{"lint":"eslint ."}}`)

	body := "- [ ] Workflow .github/workflows/ci.yml has job `build-and-test`\n" +
		"- [ ] Script `lint` runs via npm run lint\n"
	res := ReconcileACs(dir, 7, body)
	if res.AggregateStatus != AggAllSatisfied {
		t.Fatalf("aggregate = %s, want all-satisfied: %+v", res.AggregateStatus, res.AcceptanceCriteria)
	}
	if res.SuggestedRoute.Approach != "verify-and-close" {
		t.Errorf("approach = %s, want verify-and-close", res.SuggestedRoute.Approach)
	}
}

func TestReconcileACs_MostlySatisfied_NarrowScope(t *testing.T) {
	dir := t.TempDir()
	// 4 satisfied file-exists + 1 undetectable (no rule matches) = 80%
	// satisfied, zero unsatisfied → mostly-satisfied / narrow-scope.
	for _, f := range []string{"a.md", "b.md", "c.md", "d.md"} {
		writeTreeFile(t, dir, f, "x\n")
	}
	body := "- [ ] File `a.md` exists\n" +
		"- [ ] File `b.md` exists\n" +
		"- [ ] File `c.md` exists\n" +
		"- [ ] File `d.md` exists\n" +
		"- [ ] The dashboard feels snappier\n"
	res := ReconcileACs(dir, 8, body)
	if res.AggregateStatus != AggMostlySatisfied {
		t.Fatalf("aggregate = %s, want mostly-satisfied: %+v", res.AggregateStatus, res.AcceptanceCriteria)
	}
	if res.SuggestedRoute.Approach != "narrow-scope" {
		t.Errorf("approach = %s, want narrow-scope", res.SuggestedRoute.Approach)
	}
	if len(res.SuggestedRoute.FocusACs) != 1 || res.SuggestedRoute.FocusACs[0] != 4 {
		t.Errorf("focus_acs = %v, want [4]", res.SuggestedRoute.FocusACs)
	}
}

func TestReconcileACs_NoACs(t *testing.T) {
	res := ReconcileACs(t.TempDir(), 9, "Just prose. No checkboxes here.")
	if res.AggregateStatus != AggNoACsDetected {
		t.Errorf("aggregate = %s, want no-acs-detected", res.AggregateStatus)
	}
	if res.SuggestedRoute.Approach != "standard" {
		t.Errorf("approach = %s, want standard", res.SuggestedRoute.Approach)
	}
}

func TestReconcileACs_GrepForSymbol(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "src/service.go", "package src\n\nfunc ResolveThing() {}\n")

	res := ReconcileACs(dir, 10, "- [ ] Added function `ResolveThing`\n- [ ] Added function `MissingThing`\n")
	first, second := res.AcceptanceCriteria[0], res.AcceptanceCriteria[1]
	if first.Classification != ACSatisfied || *first.RuleApplied != "grep-for-symbol" {
		t.Errorf("first = %+v, want satisfied via grep-for-symbol", first)
	}
	if second.Classification != ACUnsatisfied {
		t.Errorf("second = %+v, want unsatisfied", second)
	}
}

func TestReconcileACs_DocSection(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "docs/GUIDE.md", "# Guide\n\n## Setup\n\ntext\n")

	body := "- [ ] Documented in docs/GUIDE.md under section `Setup`\n" +
		"- [ ] Documented in docs/GUIDE.md under section `Missing Part`\n"
	res := ReconcileACs(dir, 11, body)
	first, second := res.AcceptanceCriteria[0], res.AcceptanceCriteria[1]
	if first.Classification != ACSatisfied || *first.RuleApplied != "doc-section-present" {
		t.Errorf("first = %+v, want satisfied via doc-section-present", first)
	}
	if second.Classification != ACUnsatisfied {
		t.Errorf("second = %+v", second)
	}
}

func TestReconcileACs_BranchProtection_Undetectable(t *testing.T) {
	res := ReconcileACs(t.TempDir(), 12, "- [ ] Branch protection on main requires required check `CI`\n")
	c := res.AcceptanceCriteria[0]
	if c.RuleApplied == nil || *c.RuleApplied != "branch-protection-rule-present" {
		t.Fatalf("rule = %v, want branch-protection-rule-present", c.RuleApplied)
	}
	// Offline deterministic gate: matched but not evaluated via forge API.
	if c.Classification != ACUndetectable {
		t.Errorf("classification = %s, want undetectable", c.Classification)
	}
}

func TestWriteACReconcile(t *testing.T) {
	dir := t.TempDir()
	res := ReconcileACs(dir, 13, "- [ ] File `x.md` exists\n")
	out := filepath.Join(dir, ".nightgauge", "pipeline", "ac-reconcile-13.json")
	if err := WriteACReconcile(res, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The consuming skill reads these exact jq paths.
	for _, key := range []string{`"aggregate_status"`, `"suggested_route"`, `"focus_acs"`, `"schema_version": "1.0"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("report missing %s: %s", key, data)
		}
	}
}
