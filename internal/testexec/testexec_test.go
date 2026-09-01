package testexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file (and its parents) under dir.
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// flutterRepo builds a Flutter-shaped fixture whose `make test` recipe is the
// invocation under test.
func flutterRepo(t *testing.T, makeRecipe string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "pubspec.yaml", "name: fixture\n")
	writeFile(t, dir, "Makefile", "test:\n\t"+makeRecipe+"\n")
	return dir
}

func detect(t *testing.T, dir string, files ...string) (Result, error) {
	t.Helper()
	return Check(Options{Workspace: dir, IssueNumber: 314, ChangedFiles: files})
}

// TestDartDetector_TagExcluded is the load-bearing case: a suite carrying a tag
// the command excludes. This is the shape the downstream Flutter app merged.
func TestDartDetector_TagExcluded(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e")
	writeFile(t, dir, "integration_test/app_e2e/setup_flow_test.dart",
		"@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	res, err := detect(t, dir, "integration_test/app_e2e/setup_flow_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatalf("expected a blocking result, got %+v", res)
	}
	got := res.Unsatisfied[0]
	if got.Mechanism != MechanismExcludedTag {
		t.Errorf("mechanism = %q, want %q", got.Mechanism, MechanismExcludedTag)
	}
	if !strings.Contains(got.Evidence, "--exclude-tags=app-e2e") {
		t.Errorf("evidence does not name the exclusion mechanism: %q", got.Evidence)
	}
	if !strings.Contains(got.Remediation, "flutter test --tags=app-e2e") {
		t.Errorf("remediation is not a runnable command: %q", got.Remediation)
	}
}

// TestDartDetector_TagIncluded — the same tag, not excluded. Deleting the
// exclusion from the fixture's test command is the ONLY edit between this test
// and the one above, which is what makes the check capable of going red or
// green rather than always doing one of them.
func TestDartDetector_TagIncluded(t *testing.T) {
	dir := flutterRepo(t, "flutter test integration_test")
	writeFile(t, dir, "integration_test/app_e2e/setup_flow_test.dart",
		"@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	res, err := detect(t, dir, "integration_test/app_e2e/setup_flow_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() {
		t.Fatalf("expected no finding, got %+v", res.Excluded)
	}
}

// TestDartDetector_NoTagsPresent — an untagged file inside the command's paths
// is ordinary work and must produce nothing at all.
func TestDartDetector_NoTagsPresent(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e")
	writeFile(t, dir, "test/widget_test.dart", "void main() {}\n")

	res, err := detect(t, dir, "test/widget_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() {
		t.Fatalf("expected no finding, got %+v", res.Excluded)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("common case emitted warnings: %v", res.Warnings)
	}
}

// TestDartDetector_MalformedAnnotation — an annotation the parser cannot read
// makes the file untagged and warns. It must NOT make the file excluded: a
// parse failure is missing information, and this gate may only act on evidence.
func TestDartDetector_MalformedAnnotation(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e test integration_test")
	writeFile(t, dir, "integration_test/broken_test.dart",
		"@Tags(['app-e2e'\nlibrary;\n\nvoid main() {}\n")

	res, err := detect(t, dir, "integration_test/broken_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() {
		t.Fatalf("a malformed annotation must not produce an exclusion, got %+v", res.Excluded)
	}
}

// TestDartDetector_TagFilterExcludesUntagged — `--tags=unit` runs only tagged
// files, so an untagged suite added beside them never executes.
func TestDartDetector_TagFilterExcludesUntagged(t *testing.T) {
	dir := flutterRepo(t, "flutter test --tags=unit test")
	writeFile(t, dir, "test/scorecard_test.dart", "void main() {}\n")

	res, err := detect(t, dir, "test/scorecard_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatalf("expected a blocking result, got %+v", res)
	}
	if res.Unsatisfied[0].Mechanism != MechanismTagFilter {
		t.Errorf("mechanism = %q, want %q", res.Unsatisfied[0].Mechanism, MechanismTagFilter)
	}
}

// TestDartDetector_OutsideDefaultRoot — `flutter test` with no path argument
// runs `test/` and nothing else, so an untagged suite under `integration_test/`
// is unreachable regardless of tags. This mechanism hid the downstream app's
// suites as thoroughly as the tag exclusion did.
func TestDartDetector_OutsideDefaultRoot(t *testing.T) {
	dir := flutterRepo(t, "flutter test")
	writeFile(t, dir, "integration_test/scoring_flow_test.dart", "void main() {}\n")

	res, err := detect(t, dir, "integration_test/scoring_flow_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatalf("expected a blocking result, got %+v", res)
	}
	if res.Unsatisfied[0].Mechanism != MechanismOutsidePaths {
		t.Errorf("mechanism = %q, want %q", res.Unsatisfied[0].Mechanism, MechanismOutsidePaths)
	}
}

// TestSatisfiedByExecutionRecord — the gate's escape hatch is evidence, not an
// opt-out. Record a passing execution and the same fixture goes green.
func TestSatisfiedByExecutionRecord(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e")
	const file = "integration_test/app_e2e/setup_flow_test.dart"
	writeFile(t, dir, file, "@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	if err := AppendRecord(dir, 314, Record{
		File: file, Outcome: OutcomePass,
		Command: "flutter test --tags=app-e2e " + file,
	}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}

	res, err := detect(t, dir, file)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Blocked() {
		t.Fatalf("a passing execution record must satisfy the check: %+v", res.Unsatisfied)
	}
	if len(res.Satisfied) != 1 {
		t.Fatalf("satisfied = %d, want 1", len(res.Satisfied))
	}
}

// TestFailingExecutionRecordDoesNotSatisfy — "we ran it and it failed" is
// honest and still not a validated suite.
func TestFailingExecutionRecordDoesNotSatisfy(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e")
	const file = "integration_test/app_e2e/setup_flow_test.dart"
	writeFile(t, dir, file, "@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	if err := AppendRecord(dir, 314, Record{File: file, Outcome: OutcomeFail}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	res, err := detect(t, dir, file)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatal("a failing execution record must not satisfy the check")
	}
}

// TestCommonCaseIsSilent — a repo with no exclusions and an ordinary diff must
// produce a Result indistinguishable from one taken before this package
// existed: no findings, no warnings, no command chatter.
func TestCommonCaseIsSilent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	writeFile(t, dir, "src/thing.test.ts", "test('x', () => {});\n")

	res, err := Check(Options{Workspace: dir, IssueNumber: 1, ChangedFiles: []string{"src/thing.test.ts"}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() || len(res.Warnings) != 0 {
		t.Fatalf("common case is not silent: %+v", res)
	}
}

// TestNoDetectorForEcosystemIsQuiet — a Go repo has no detector, so the check
// says nothing rather than guessing.
func TestNoDetectorForEcosystemIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module fixture\n\ngo 1.23\n")
	writeFile(t, dir, "internal/thing_test.go", "package internal\n")

	res, err := Check(Options{Workspace: dir, IssueNumber: 1, ChangedFiles: []string{"internal/thing_test.go"}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() {
		t.Fatalf("expected silence for an ecosystem with no detector, got %+v", res.Excluded)
	}
}

// TestUnresolvableCommandIsQuiet — no manifest, no command, no evidence. An
// inability to tell must never read as an accusation.
func TestUnresolvableCommandIsQuiet(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "integration_test/a_test.dart", "@Tags(['e2e'])\nvoid main() {}\n")

	res, err := Check(Options{Workspace: dir, IssueNumber: 1, ChangedFiles: []string{"integration_test/a_test.dart"}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() {
		t.Fatalf("expected silence with no resolvable test command, got %+v", res.Excluded)
	}
}

// TestNonTestDartFileIgnored — this package answers "you built a test and
// nothing runs it", never "your code is uncovered".
func TestNonTestDartFileIgnored(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e")
	writeFile(t, dir, "lib/scorecard.dart", "class Scorecard {}\n")

	res, err := detect(t, dir, "lib/scorecard.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Quiet() {
		t.Fatalf("a non-test file must never be a finding, got %+v", res.Excluded)
	}
}

// TestExclusionChangeIsReEvaluated — the detector reads the command every run,
// so a repo that adds an exclusion after a green run is caught rather than
// grandfathered.
func TestExclusionChangeIsReEvaluated(t *testing.T) {
	dir := flutterRepo(t, "flutter test integration_test")
	const file = "integration_test/app_e2e/setup_flow_test.dart"
	writeFile(t, dir, file, "@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	if res, err := detect(t, dir, file); err != nil || !res.Quiet() {
		t.Fatalf("baseline should be quiet: %+v %v", res, err)
	}
	writeFile(t, dir, "Makefile", "test:\n\tflutter test --exclude-tags=app-e2e integration_test\n")
	res, err := detect(t, dir, file)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatal("a newly added exclusion must be detected on the next run")
	}
}

// TestConfiguredCommandOverridesRepoScan — a repo whose real invocation lives
// somewhere no scanner can see declares it in config.
func TestConfiguredCommandOverridesRepoScan(t *testing.T) {
	dir := flutterRepo(t, "flutter test integration_test")
	const file = "integration_test/app_e2e/setup_flow_test.dart"
	writeFile(t, dir, file, "@Tags(['app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	res, err := Check(Options{
		Workspace:         dir,
		IssueNumber:       314,
		ChangedFiles:      []string{file},
		ConfiguredCommand: "flutter test --exclude-tags=app-e2e integration_test",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatal("the configured command must take precedence over repo scanning")
	}
	if res.Command.Source != "config" {
		t.Errorf("source = %q, want config", res.Command.Source)
	}
}

// TestNpmScriptExpansion — `npm test` carries no flags; the exclusion lives in
// the script body. A detector handed only the entry point sees nothing, which
// is the exact failure shape this package exists to catch.
func TestNpmScriptExpansion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json",
		`{"scripts":{"test":"flutter test --exclude-tags=app-e2e"}}`)
	const file = "integration_test/app_e2e/a_test.dart"
	writeFile(t, dir, file, "@Tags(['app-e2e'])\nvoid main() {}\n")

	res, err := Check(Options{Workspace: dir, IssueNumber: 1, ChangedFiles: []string{file}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() {
		t.Fatalf("expected the expanded script body to be read, got %+v", res)
	}
}

func TestSplitTagExpression(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"app-e2e", []string{"app-e2e"}},
		{"a,b", []string{"a", "b"}},
		{"a || b", []string{"a", "b"}},
		{`"app-e2e"`, []string{"app-e2e"}},
		{"", nil},
		// An expression this function does not model yields nothing, which
		// makes files un-excluded rather than falsely excluded.
		{"a && !b", nil},
	}
	for _, c := range cases {
		got := splitTagExpression(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("splitTagExpression(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestReadRecords_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".nightgauge/pipeline/test-execution-7.jsonl",
		"not json\n{\"v\":1,\"file\":\"a_test.dart\",\"outcome\":\"pass\",\"recorded_at\":\"x\"}\n{\"v\":99,\"file\":\"b\",\"outcome\":\"pass\"}\n")

	got, err := ReadRecords(dir, 7)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 1 || got[0].File != "a_test.dart" {
		t.Fatalf("records = %+v, want just the readable v1 record", got)
	}
}

func TestAppendRecord_RejectsUnknownOutcome(t *testing.T) {
	dir := t.TempDir()
	if err := AppendRecord(dir, 1, Record{File: "a_test.dart", Outcome: "probably"}); err == nil {
		t.Fatal("expected an error for an unknown outcome")
	}
}

// TestDartDetector_MalformedAnnotationWarns exercises the detector directly,
// because Check deliberately drops warnings when nothing is excluded (the
// common case must stay silent). The warning still has to exist, or a repo full
// of unparseable annotations would look identical to one with none.
func TestDartDetector_MalformedAnnotationWarns(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e test integration_test")
	writeFile(t, dir, "integration_test/broken_test.dart",
		"@Tags(['app-e2e'\nlibrary;\n\nvoid main() {}\n")

	cmd := ResolveTestCommand(dir, "")
	excl, warns := DartDetector{}.Detect(dir, cmd, []string{"integration_test/broken_test.dart"})
	if len(excl) != 0 {
		t.Fatalf("malformed annotation must not produce an exclusion: %+v", excl)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "could not be parsed") {
		t.Fatalf("warnings = %v, want one parse warning", warns)
	}
}

// TestDartDetector_MultipleTagsFirstMatchWins covers a suite carrying several
// tags where only one is excluded.
func TestDartDetector_MultipleTagsFirstMatchWins(t *testing.T) {
	dir := flutterRepo(t, "flutter test --exclude-tags=app-e2e test integration_test")
	writeFile(t, dir, "integration_test/multi_test.dart",
		"@Tags(['slow', 'app-e2e'])\nlibrary;\n\nvoid main() {}\n")

	res, err := detect(t, dir, "integration_test/multi_test.dart")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Blocked() || res.Unsatisfied[0].Mechanism != MechanismExcludedTag {
		t.Fatalf("expected an excluded-tag finding, got %+v", res)
	}
}
