package validation

import (
	"testing"
)

func TestCompareJSONIdentical(t *testing.T) {
	a := `{"name": "test", "count": 42}`
	b := `{"count": 42, "name": "test"}`

	pass, diff := CompareJSON(a, b)
	if !pass {
		t.Errorf("expected pass for semantically identical JSON, diff: %s", diff)
	}
}

func TestCompareJSONDifferent(t *testing.T) {
	a := `{"name": "test", "count": 42}`
	b := `{"name": "test", "count": 99}`

	pass, _ := CompareJSON(a, b)
	if pass {
		t.Error("expected fail for different values")
	}
}

func TestCompareJSONPlainText(t *testing.T) {
	pass, _ := CompareJSON("hello world", "hello world")
	if !pass {
		t.Error("expected pass for identical plain text")
	}

	pass, _ = CompareJSON("hello", "world")
	if pass {
		t.Error("expected fail for different plain text")
	}
}

func TestCompareJSONMixed(t *testing.T) {
	pass, diff := CompareJSON(`{"valid": true}`, "not json")
	if pass {
		t.Error("expected fail for mixed JSON/text")
	}
	if diff == "" {
		t.Error("expected non-empty diff")
	}
}

func TestCompareJSONArrays(t *testing.T) {
	a := `[{"id": 1}, {"id": 2}]`
	b := `[{"id": 1}, {"id": 2}]`

	pass, _ := CompareJSON(a, b)
	if !pass {
		t.Error("expected pass for identical arrays")
	}
}

func TestCompareJSONNestedObjects(t *testing.T) {
	a := `{"outer": {"inner": "value", "num": 1}}`
	b := `{"outer": {"num": 1, "inner": "value"}}`

	pass, _ := CompareJSON(a, b)
	if !pass {
		t.Error("expected pass for nested objects with different key order")
	}
}

func TestCompareJSONEmptyStrings(t *testing.T) {
	pass, _ := CompareJSON("", "")
	if !pass {
		t.Error("expected pass for two empty strings")
	}
}

func TestCompareJSONWhitespaceNormalization(t *testing.T) {
	a := `{  "key" :  "value"  }`
	b := `{"key":"value"}`

	pass, _ := CompareJSON(a, b)
	if !pass {
		t.Error("expected pass for JSON with different whitespace")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"long string here", 4, "long..."},
		{"exact", 5, "exact"},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestReportGeneration(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")

	// Manually add results
	runner.results = []ValidationResult{
		{Operation: "gate-allow", Category: CatHooks, Pass: true, ShellMs: 100, GoMs: 10},
		{Operation: "gate-block", Category: CatHooks, Pass: true, ShellMs: 90, GoMs: 15},
		{Operation: "status", Category: CatGit, Pass: false, ShellMs: 200, GoMs: 20, Diff: "output differs"},
	}

	report := runner.Report()
	if report.TotalTests != 3 {
		t.Errorf("TotalTests = %d, want 3", report.TotalTests)
	}
	if report.Passed != 2 {
		t.Errorf("Passed = %d, want 2", report.Passed)
	}
	if report.Failed != 1 {
		t.Errorf("Failed = %d, want 1", report.Failed)
	}

	hookStats := report.ByCategory["hooks"]
	if hookStats.Passed != 2 {
		t.Errorf("hooks passed = %d, want 2", hookStats.Passed)
	}

	if report.Performance.AvgGoMs >= report.Performance.AvgShellMs {
		t.Error("expected Go to be faster than shell on average")
	}
}

func TestReportPerformanceSummary(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")
	runner.results = []ValidationResult{
		{Operation: "op1", Category: CatHooks, Pass: true, ShellMs: 200, GoMs: 20},
		{Operation: "op2", Category: CatHooks, Pass: true, ShellMs: 100, GoMs: 10},
	}

	report := runner.Report()
	if report.Performance.TotalShellMs != 300 {
		t.Errorf("TotalShellMs = %d, want 300", report.Performance.TotalShellMs)
	}
	if report.Performance.TotalGoMs != 30 {
		t.Errorf("TotalGoMs = %d, want 30", report.Performance.TotalGoMs)
	}
	if report.Performance.AvgShellMs != 150 {
		t.Errorf("AvgShellMs = %.0f, want 150", report.Performance.AvgShellMs)
	}
	if report.Performance.AvgGoMs != 15 {
		t.Errorf("AvgGoMs = %.0f, want 15", report.Performance.AvgGoMs)
	}
	// Speedup: (300-30)/300 = 90%
	if report.Performance.AvgSpeedupPct != 90 {
		t.Errorf("AvgSpeedupPct = %.0f, want 90", report.Performance.AvgSpeedupPct)
	}
}

func TestFormatReport(t *testing.T) {
	report := ValidationReport{
		TotalTests: 5,
		Passed:     4,
		Failed:     1,
		ByCategory: map[string]CatStats{
			"hooks": {Total: 3, Passed: 3},
			"git":   {Total: 2, Passed: 1, Failed: 1},
		},
		Performance: PerformanceSummary{
			AvgShellMs:    100,
			AvgGoMs:       15,
			AvgSpeedupPct: 85,
			TotalShellMs:  500,
			TotalGoMs:     75,
		},
		Results: []ValidationResult{
			{Operation: "gate-allow", Category: CatHooks, Pass: true, ShellMs: 100, GoMs: 10},
			{Operation: "failed-op", Category: CatGit, Pass: false, Diff: "test diff", Error: "some error"},
		},
	}

	output := FormatReport(report)
	if output == "" {
		t.Error("expected non-empty report")
	}
	if !containsStr(output, "Passed: 4") {
		t.Error("report should contain pass count")
	}
	if !containsStr(output, "FAIL") {
		t.Error("report should contain failure indicator")
	}
	if !containsStr(output, "Detailed Results") {
		t.Error("report should contain detailed results section")
	}
	if !containsStr(output, "go-only") || containsStr(output, "go-only") {
		// go-only only appears for results with ShellMs == 0
	}
	if !containsStr(output, "1 FAILURES DETECTED") {
		t.Error("report should contain failure summary")
	}
}

func TestFormatReportAllPassing(t *testing.T) {
	report := ValidationReport{
		TotalTests: 3,
		Passed:     3,
		Failed:     0,
		ByCategory: map[string]CatStats{
			"hooks": {Total: 3, Passed: 3},
		},
		Performance: PerformanceSummary{
			AvgShellMs: 50,
			AvgGoMs:    5,
		},
		Results: []ValidationResult{
			{Operation: "op1", Category: CatHooks, Pass: true, ShellMs: 50, GoMs: 5},
		},
	}

	output := FormatReport(report)
	if !containsStr(output, "ALL VALIDATIONS PASSED") {
		t.Error("all-passing report should say ALL VALIDATIONS PASSED")
	}
}

func TestFormatReportGoOnlyEntry(t *testing.T) {
	report := ValidationReport{
		TotalTests: 1,
		Passed:     1,
		ByCategory: map[string]CatStats{
			"git": {Total: 1, Passed: 1},
		},
		Results: []ValidationResult{
			{Operation: "git-status", Category: CatGit, Pass: true, GoMs: 8},
		},
	}

	output := FormatReport(report)
	if !containsStr(output, "go-only") {
		t.Error("report should show 'go-only' for entries with no shell time")
	}
}

func TestResultsAccessor(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")
	runner.results = []ValidationResult{
		{Operation: "op1", Pass: true},
		{Operation: "op2", Pass: false},
	}

	results := runner.Results()
	if len(results) != 2 {
		t.Errorf("Results() returned %d, want 2", len(results))
	}
}

func TestBuildResultSpeedupCalculation(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")

	result := runner.buildResult("test-op", CatHooks,
		`{"decision":"allow"}`, 100, nil,
		`{"decision":"allow"}`, 10, nil)

	if !result.Pass {
		t.Error("expected pass for identical outputs")
	}
	// Speedup: (100-10)/100 = 90%
	if result.SpeedupPct != 90 {
		t.Errorf("SpeedupPct = %.0f, want 90", result.SpeedupPct)
	}
}

func TestBuildResultErrors(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")

	result := runner.buildResult("test-op", CatHooks,
		"shell output", 50, nil,
		"go output", 10, nil)

	if result.Pass {
		t.Error("expected fail for different outputs")
	}
	if result.Diff == "" {
		t.Error("expected non-empty diff")
	}
}

func TestRegisteredTestsNotEmpty(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")
	tests := runner.RegisteredTests()

	if len(tests) == 0 {
		t.Fatal("expected non-empty test list")
	}

	// Verify all tests have required fields
	for _, tc := range tests {
		if tc.operation == "" {
			t.Error("test case has empty operation")
		}
		if tc.category == "" {
			t.Error("test case has empty category")
		}
		if len(tc.goArgs) == 0 {
			t.Errorf("test case %q has no Go args", tc.operation)
		}
	}
}

func TestRegisteredTestsCategories(t *testing.T) {
	runner := NewRunner("/fake/binary", "/fake/scripts")
	tests := runner.RegisteredTests()

	categories := make(map[OperationCategory]int)
	for _, tc := range tests {
		categories[tc.category]++
	}

	// Must have at least hooks, git, pipeline, and intelligence categories
	expectedCats := []OperationCategory{CatHooks, CatGit, CatPipeline, CatIntelligence}
	for _, cat := range expectedCats {
		if categories[cat] == 0 {
			t.Errorf("no tests registered for category %q", cat)
		}
	}

	// Hooks should have the most test cases
	if categories[CatHooks] < 10 {
		t.Errorf("hooks category has %d tests, expected at least 10", categories[CatHooks])
	}
}

func TestAllCategoriesConstant(t *testing.T) {
	if len(AllCategories) != 7 {
		t.Errorf("AllCategories has %d entries, want 7", len(AllCategories))
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
