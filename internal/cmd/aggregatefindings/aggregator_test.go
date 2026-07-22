package aggregatefindings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// --- NormalizeSeverity ---

func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"critical", SeverityCritical},
		{"Critical", SeverityCritical},
		{"CRITICAL", SeverityCritical},
		{"poor", SeverityHigh},
		{"Poor", SeverityHigh},
		{"fair", SeverityMedium},
		{"Fair", SeverityMedium},
		{"good", SeverityLow},
		{"Good", SeverityLow},
		{"excellent", SeverityInfo},
		{"Excellent", SeverityInfo},
		// Unknown inputs default to info
		{"unknown", SeverityInfo},
		{"", SeverityInfo},
		{"n/a", SeverityInfo},
		{"  poor  ", SeverityHigh}, // whitespace trimmed
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := NormalizeSeverity(tc.input)
			if got != tc.want {
				t.Errorf("NormalizeSeverity(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- DedupKey ---

func TestDedupKey(t *testing.T) {
	cases := []struct {
		dim, title string
		want       string
	}{
		{"dependency_health", "Outdated packages", "dependency_health::outdated packages"},
		{"Dependency_Health", "Outdated Packages", "dependency_health::outdated packages"},
		{"  dep  ", "  title  ", "dep::title"},
		{"sec", "CVE-2024-1234", "sec::cve-2024-1234"},
	}
	for _, tc := range cases {
		got := DedupKey(tc.dim, tc.title)
		if got != tc.want {
			t.Errorf("DedupKey(%q, %q) = %q; want %q", tc.dim, tc.title, got, tc.want)
		}
	}
}

// --- LoadHealthReport ---

func TestLoadHealthReport_Missing(t *testing.T) {
	dir := t.TempDir()
	findings, err := LoadHealthReport(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findings != nil {
		t.Errorf("expected nil findings for missing file, got %v", findings)
	}
}

func TestLoadHealthReport_Malformed(t *testing.T) {
	dir := t.TempDir()
	ib := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(ib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ib, "health-report.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadHealthReport(dir)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestLoadHealthReport_Valid(t *testing.T) {
	dir := t.TempDir()
	ib := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(ib, 0o755); err != nil {
		t.Fatal(err)
	}

	report := map[string]interface{}{
		"dimensions": map[string]interface{}{
			"dependency_health": map[string]interface{}{
				"status": "poor",
				"findings": []map[string]interface{}{
					{
						"title":          "Outdated lodash",
						"description":    "lodash is 4 major versions behind",
						"recommendation": "Run npm update lodash",
					},
				},
			},
			"code_quality": map[string]interface{}{
				"status": "good",
				"findings": []map[string]interface{}{
					{
						"title":          "Unused imports",
						"description":    "3 files have unused imports",
						"recommendation": "Remove unused imports",
					},
				},
			},
		},
	}
	b, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(ib, "health-report.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := LoadHealthReport(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	bySeverity := map[string]int{}
	for _, f := range findings {
		if f.Source != "health-check" {
			t.Errorf("expected source health-check, got %q", f.Source)
		}
		bySeverity[f.Severity]++
	}
	if bySeverity[SeverityHigh] != 1 {
		t.Errorf("expected 1 high severity finding (from 'poor' status), got %d", bySeverity[SeverityHigh])
	}
	if bySeverity[SeverityLow] != 1 {
		t.Errorf("expected 1 low severity finding (from 'good' status), got %d", bySeverity[SeverityLow])
	}
}

// --- LoadSecurityAudit ---

func TestLoadSecurityAudit_Missing(t *testing.T) {
	dir := t.TempDir()
	findings, err := LoadSecurityAudit(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findings != nil {
		t.Errorf("expected nil findings for missing file, got %v", findings)
	}
}

// --- LoadTestScaffold ---

func TestLoadTestScaffold_Missing(t *testing.T) {
	dir := t.TempDir()
	findings, err := LoadTestScaffold(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findings != nil {
		t.Errorf("expected nil findings for missing file, got %v", findings)
	}
}

// --- Aggregate ---

func writeFixture(t *testing.T, dir, name string, v interface{}) {
	t.Helper()
	ib := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(ib, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ib, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAggregate_AllThree(t *testing.T) {
	dir := t.TempDir()

	writeFixture(t, dir, "health-report.json", map[string]interface{}{
		"dimensions": map[string]interface{}{
			"dependency_health": map[string]interface{}{
				"status": "critical",
				"findings": []map[string]interface{}{
					{"title": "CVE in dep", "description": "critical vuln", "recommendation": "update now"},
				},
			},
		},
	})

	writeFixture(t, dir, "security-audit.json", map[string]interface{}{
		"dimensions": map[string]interface{}{
			"owasp_top10": map[string]interface{}{
				"findings": []map[string]interface{}{
					{"title": "SQL injection", "description": "unsafe query", "recommendation": "use parameterized queries", "severity": "high"},
				},
			},
		},
	})

	writeFixture(t, dir, "test-scaffold-report.json", map[string]interface{}{
		"gaps": []map[string]interface{}{
			{"title": "Missing auth tests", "description": "auth module untested", "recommendation": "add unit tests", "priority": "high"},
		},
		"recommendations": []map[string]interface{}{},
	})

	result, err := Aggregate(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.V != 1 {
		t.Errorf("expected v=1, got %d", result.V)
	}
	if len(result.SourcesRead) != 3 {
		t.Errorf("expected 3 sources read, got %d", len(result.SourcesRead))
	}
	if len(result.SourcesMissing) != 0 {
		t.Errorf("expected 0 missing sources, got %v", result.SourcesMissing)
	}
	if result.Summary.TotalFindings != 3 {
		t.Errorf("expected 3 total findings, got %d", result.Summary.TotalFindings)
	}
	if result.Summary.AfterDedup != 3 {
		t.Errorf("expected 3 after dedup (no overlaps), got %d", result.Summary.AfterDedup)
	}
}

func TestAggregate_OnlyHealth(t *testing.T) {
	dir := t.TempDir()

	writeFixture(t, dir, "health-report.json", map[string]interface{}{
		"dimensions": map[string]interface{}{
			"code_quality": map[string]interface{}{
				"status": "fair",
				"findings": []map[string]interface{}{
					{"title": "TODOs found", "description": "10 TODO comments", "recommendation": "resolve TODOs"},
				},
			},
		},
	})

	result, err := Aggregate(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.SourcesRead) != 1 || result.SourcesRead[0] != "health-check" {
		t.Errorf("expected sources_read=[health-check], got %v", result.SourcesRead)
	}
	if len(result.SourcesMissing) != 2 {
		t.Errorf("expected 2 missing sources, got %v", result.SourcesMissing)
	}
	if result.Summary.TotalFindings != 1 {
		t.Errorf("expected 1 finding, got %d", result.Summary.TotalFindings)
	}
}

func TestAggregate_Deduplication(t *testing.T) {
	dir := t.TempDir()

	// Same dimension + title in health-check and security-audit → dedup
	// Security audit has longer recommendation → wins
	writeFixture(t, dir, "health-report.json", map[string]interface{}{
		"dimensions": map[string]interface{}{
			"dependency_health": map[string]interface{}{
				"status": "poor",
				"findings": []map[string]interface{}{
					{"title": "Vulnerable lodash", "description": "CVE in lodash", "recommendation": "update"},
				},
			},
		},
	})

	writeFixture(t, dir, "security-audit.json", map[string]interface{}{
		"dimensions": map[string]interface{}{
			"dependency_health": map[string]interface{}{
				"findings": []map[string]interface{}{
					{
						"title":          "Vulnerable lodash",
						"description":    "CVE-2024-1234 in lodash <4.17.21",
						"recommendation": "Run npm install lodash@latest and pin to 4.17.21 or higher in package.json",
						"severity":       "high",
					},
				},
			},
		},
	})

	result, err := Aggregate(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.TotalFindings != 2 {
		t.Errorf("expected 2 total (before dedup), got %d", result.Summary.TotalFindings)
	}
	if result.Summary.AfterDedup != 1 {
		t.Errorf("expected 1 after dedup, got %d", result.Summary.AfterDedup)
	}
	if result.Summary.DeduplicationRate <= 0 {
		t.Errorf("expected deduplication_rate > 0, got %f", result.Summary.DeduplicationRate)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding after dedup, got %d", len(result.Findings))
	}
	winner := result.Findings[0]
	if len(winner.MergedFrom) == 0 {
		t.Error("expected merged_from to be populated for deduplicated finding")
	}
	// Winner has the longer recommendation
	if len(winner.Recommendation) < 20 {
		t.Errorf("expected winner to have longer recommendation, got %q", winner.Recommendation)
	}
}

func TestAggregate_AllMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := Aggregate(dir)
	if err == nil {
		t.Fatal("expected error when all sources missing, got nil")
	}
}
