package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
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

// TestReconcileACs_AllUndetectable_FocusesEveryCriterion pins the polarity of
// the all-undetectable route (#1011).
//
// The body is #228's three real acceptance criteria — ordinary prose checkboxes
// naming no file, symbol, npm script, doc section, workflow job or branch
// protection, so no rule in the library matches and every one classifies
// undetectable. That is the honest verdict, and it is also the case the route
// got backwards: it handed the planner an EMPTY focus list, the same answer
// all-satisfied gives, on the run whose entire purpose was to demonstrate the
// pipeline does good work.
func TestReconcileACs_AllUndetectable_FocusesEveryCriterion(t *testing.T) {
	body := "- [ ] Bootstrap supports cursor/offset continuation so a client can fetch the full corpus\n" +
		"- [ ] A large org fully bootstraps across pages (integration test)\n" +
		"- [ ] OpenAPI updated; contract test covers the multi-page path\n"
	res := ReconcileACs(t.TempDir(), 228, body)

	// The premise, asserted so a future change to the rule library that makes
	// one of these evaluable fails HERE with a clear reason rather than making
	// the real assertion below silently vacuous.
	if len(res.AcceptanceCriteria) != 3 {
		t.Fatalf("parsed %d criteria, want 3 — the fixture no longer exercises the branch",
			len(res.AcceptanceCriteria))
	}
	if res.AggregateStatus != AggUndetectable {
		t.Fatalf("aggregate = %s, want %s — the fixture no longer exercises the branch",
			res.AggregateStatus, AggUndetectable)
	}

	want := []int{0, 1, 2}
	got := res.SuggestedRoute.FocusACs
	if len(got) != len(want) {
		t.Fatalf("focus_acs = %v, want %v — \"none could be evaluated\" is maximum uncertainty "+
			"and must focus every criterion, not none", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("focus_acs = %v, want %v", got, want)
		}
	}
}

// TestReconcileACs_AllSatisfiedStillFocusesNothing is the control. Without it,
// the fix above could be "always focus everything", which would destroy the one
// state that legitimately focuses nothing.
func TestReconcileACs_AllSatisfiedStillFocusesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := ReconcileACs(dir, 14, "- [ ] File `x.md` exists\n")
	if res.AggregateStatus != AggAllSatisfied {
		t.Skipf("fixture did not reach all-satisfied (got %s); control not exercised", res.AggregateStatus)
	}
	if len(res.SuggestedRoute.FocusACs) != 0 {
		t.Errorf("focus_acs = %v, want empty — all-satisfied is the one state that focuses nothing",
			res.SuggestedRoute.FocusACs)
	}
}

func TestWriteACReconcile(t *testing.T) {
	dir := t.TempDir()
	res := ReconcileACs(dir, 13, "- [ ] File `x.md` exists\n")
	out := filepath.Join(layouttest.PipelineDir(t, dir), "ac-reconcile-13.json")
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

func TestParseACs_IgnoresDetailsAndDuplicates(t *testing.T) {
	live := "- [ ] One\n- [ ] Two\n- [x] Three\n- [ ] Four\n"
	archive := "\n<details><summary>Original Issue (before refinement)</summary>\n\n- [ ] One\n- [ ] Two\n- [ ] Only archived\n</DETAILS>\n"
	got := parseACs(live + archive + "- [ ] One\n")
	if len(got) != 4 {
		t.Fatalf("got %d criteria, want 4: %+v", len(got), got)
	}
	for i, c := range got {
		if c.index != i || c.text == "Only archived" {
			t.Errorf("criterion %d = %+v", i, c)
		}
	}
	if ACFingerprint(live) != ACFingerprint(live+archive) {
		t.Error("fingerprint must ignore the archived block")
	}
}

func TestParseACs_Forms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"numbered dot", "## Acceptance Criteria\n\n1. first\n2. second\n", []string{"first", "second"}},
		{"numbered paren", "## Acceptance Criteria\n1) first\n2) second\n", []string{"first", "second"}},
		{"bold prefixed", "## Acceptance criteria\n1. **AC1:** does x\n2. **AC2:** does y\n",
			[]string{"**AC1:** does x", "**AC2:** does y"}},
		{"mixed in order", "## Acceptance Criteria\n- [ ] box one\n1. num one\n* [x] box two\n",
			[]string{"box one", "num one", "box two"}},
		{"numbered outside section ignored", "## Reproduction steps\n1. run it\n2. see it\n", nil},
		{"section ends at next heading", "## Acceptance Criteria\n1. in\n## Notes\n1. out\n", []string{"in"}},
		{"sub-heading section", "## Spec\n### Acceptance Criteria (v2)\n1. in\n#### detail\n2. still in\n## Next\n3. out\n",
			[]string{"in", "still in"}},
		{"inline details mention", "## Acceptance Criteria\n- [ ] keeps the `<details>` exclusion\n- [ ] later\n" +
			"\n<details>\n<summary>old</summary>\n\n- [ ] archived\n</details>\n",
			[]string{"keeps the `<details>` exclusion", "later"}},
		{"details line inside backtick fence", "## Acceptance Criteria\n- [ ] first\n```html\n<details>\n```\n" +
			"- [ ] after fence\n</details>\n- [ ] last\n",
			[]string{"first", "after fence", "last"}},
		{"details line inside tilde fence", "~~~\n<details>\n~~~\n- [ ] kept\n</details>\n", []string{"kept"}},
		{"archive after fence still dropped", "```\nx\n```\n<details>\n- [ ] archived\n</details>\n- [ ] live\n",
			[]string{"live"}},
		{"unterminated details kept", "<details>\n- [ ] kept\n", []string{"kept"}},
		{"text after closing tag kept", "<details>\n- [ ] archived\n</details>\n- [ ] live\n", []string{"live"}},
		{"numbered with box", "## Acceptance Criteria\n1. [x] done\n2. [ ] todo\n", []string{"done", "todo"}},
		{"fenced comment does not close section", "## Acceptance Criteria\n1. first:\n   ```sh\n   # run it\n   1. output line\n   - [ ] example box\n   ```\n2. second\n",
			[]string{"first:", "second"}},
		{"tilde fence skipped", "## Acceptance Criteria\n~~~\n1. phantom\n~~~\n1. real\n", []string{"real"}},
		{"nested numbered sub-step not a criterion", "## Acceptance Criteria\n1. Do X:\n    1. sub-step\n2. Do Y\n",
			[]string{"Do X:", "Do Y"}},
		// The sub-step sits at the parent's content column (#2684).
		{"3-space sub-step at the content column", "## Acceptance Criteria\n1. Do X:\n   1. sub\n   2. sub two\n2. Do Y\n",
			[]string{"Do X:", "Do Y"}},
		{"sub-step after a blank line", "## Acceptance Criteria\n1. Do X:\n\n   1. sub\n", []string{"Do X:"}},
		{"sibling items", "## Acceptance Criteria\n1. a\n2. b\n", []string{"a", "b"}},
		{"4-column content keeps a 3-space item a sibling", "## Acceptance Criteria\n10. x\n   1. y\n", []string{"x", "y"}},
		{"2-space item below the column is a sibling", "## Acceptance Criteria\n1. a\n  2. b\n", []string{"a", "b"}},
		{"first item at 3 spaces counts", "## Acceptance Criteria\n   1. lone\n", []string{"lone"}},
		{"unindented line ends the item", "## Acceptance Criteria\n1. a\nNotes:\n   1. b\n", []string{"a", "b"}},
		{"heading ends the item", "## Acceptance Criteria\n1. a\n### More\n   1. b\n", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseACs(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d criteria %+v, want %v", len(got), got, tc.want)
			}
			for i, ac := range got {
				if ac.text != tc.want[i] || ac.index != i {
					t.Errorf("ac[%d] = %+v, want text %q", i, ac, tc.want[i])
				}
			}
		})
	}
	if got := parseACs("## Acceptance Criteria\n1. [x] done\n2. plain\n"); got[0].state != "checked" || got[1].state != "unchecked" {
		t.Errorf("states = %+v", got)
	}
}

func TestParseACs_Issue2665Body(t *testing.T) {
	body := "## Problem\n\nSomething.\n\n## Acceptance Criteria\n\n" +
		"1. **AC1:** pr-create verifies diff-checkable criteria against the branch diff.\n" +
		"2. **AC2:** a criterion naming a file the diff adds is verified.\n" +
		"3. **AC3:** a criterion naming a symbol the diff adds is verified.\n" +
		"4. **AC4:** runtime criteria stay not verifiable.\n" +
		"5. **AC5:** CHANGELOG entry under Unreleased.\n\n## Notes\n\n1. not a criterion\n"
	if got := parseACs(body); len(got) != 5 {
		t.Fatalf("got %d criteria, want 5: %+v", len(got), got)
	}
}

// TestParseACs_Issue2672Body is the #2679 regression: #2672's body, whose
// AC2 mentions `<details>` inline above an archived block, parses into six
// whole criteria.
func TestParseACs_Issue2672Body(t *testing.T) {
	raw, err := os.ReadFile("testdata/issue-2672-body.md")
	if err != nil {
		t.Fatal(err)
	}
	got := parseACs(string(raw))
	if len(got) != 6 {
		t.Fatalf("got %d criteria, want 6: %+v", len(got), got)
	}
	if want := "including the `<details>` exclusion and de-duplication."; !strings.HasSuffix(got[1].text, want) {
		t.Errorf("AC2 = %q, want suffix %q", got[1].text, want)
	}
}

func TestParseACs_NestedDetails(t *testing.T) {
	live := "- [ ] One\n"
	nested := "<details>\n- [ ] Outer\n<details open>\n- [ ] Inner\n</details>\n- [ ] After inner\n</details>\n- [ ] Two\n"
	if got := parseACs(live + nested); len(got) != 2 || got[1].text != "Two" {
		t.Errorf("nested: got %+v, want One and Two", got)
	}
	if got := parseACs("</details>\n" + live); len(got) != 1 {
		t.Errorf("stray close: got %+v, want One", got)
	}
}

// TestParseACs_ParityFixture shares its body with the SDK parser's parity
// test: a fence, nested <details>, a 4-space sub-step and a duplicate.
func TestParseACs_ParityFixture(t *testing.T) {
	got := parseACs("## Acceptance Criteria\n1. first\n   ```sh\n   # comment\n   1. output\n   ```\n    1. sub-step\n<details>\n- [ ] outer\n<details>\n- [ ] inner\n</details>\n- [ ] after inner\n</details>\n2. second\n- [ ]  first\n")
	if len(got) != 2 || got[0].text != "first" || got[1].text != "second" || got[1].index != 1 {
		t.Errorf("got %+v, want first and second", got)
	}
}
