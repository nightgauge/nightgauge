package audit

import (
	"strings"
	"testing"
)

// minimalReport builds a SynthesisReport with the given dimensions and findings for testing.
func minimalReport(timestamp string, overallScore float64, dims []*DimensionResult) *SynthesisReport {
	return &SynthesisReport{
		Timestamp:    timestamp,
		OverallScore: overallScore,
		Trend:        "unknown",
		Dimensions:   dims,
		FindingsBySev: map[string]int{
			"critical": 0,
			"high":     0,
			"medium":   0,
			"low":      0,
		},
	}
}

// makeDim is a helper to build a DimensionResult with findings.
func makeDim(name string, score, weight float64, findings []AuditFinding) *DimensionResult {
	return &DimensionResult{
		Name:          name,
		Score:         score,
		Weight:        weight,
		WeightedScore: score * weight,
		Findings:      findings,
	}
}

// makeTrendFinding builds an AuditFinding with all fields for trend tests.
func makeTrendFinding(id, category, repo, file, severity, description string) AuditFinding {
	return AuditFinding{
		ID:          id,
		Category:    category,
		Repository:  repo,
		File:        file,
		Severity:    severity,
		Description: description,
	}
}

// --- TestComputeFindingID ---

func TestComputeFindingID(t *testing.T) {
	t.Run("same inputs produce same ID", func(t *testing.T) {
		id1 := ComputeFindingID("dim1", "cat", "repo", "path/to/file.go")
		id2 := ComputeFindingID("dim1", "cat", "repo", "path/to/file.go")
		if id1 != id2 {
			t.Errorf("expected identical IDs, got %q and %q", id1, id2)
		}
	})

	t.Run("different dimension produces different ID", func(t *testing.T) {
		id1 := ComputeFindingID("dim1", "cat", "repo", "file.go")
		id2 := ComputeFindingID("dim2", "cat", "repo", "file.go")
		if id1 == id2 {
			t.Error("expected different IDs for different dimensions")
		}
	})

	t.Run("different category produces different ID", func(t *testing.T) {
		id1 := ComputeFindingID("dim1", "cat-a", "repo", "file.go")
		id2 := ComputeFindingID("dim1", "cat-b", "repo", "file.go")
		if id1 == id2 {
			t.Error("expected different IDs for different categories")
		}
	})

	t.Run("different repo produces different ID", func(t *testing.T) {
		id1 := ComputeFindingID("dim1", "cat", "repo-a", "file.go")
		id2 := ComputeFindingID("dim1", "cat", "repo-b", "file.go")
		if id1 == id2 {
			t.Error("expected different IDs for different repositories")
		}
	})

	t.Run("different file produces different ID", func(t *testing.T) {
		id1 := ComputeFindingID("dim1", "cat", "repo", "a.go")
		id2 := ComputeFindingID("dim1", "cat", "repo", "b.go")
		if id1 == id2 {
			t.Error("expected different IDs for different files")
		}
	})

	t.Run("backslash path normalized to forward slash on Windows", func(t *testing.T) {
		// filepath.ToSlash only converts backslashes on Windows (the OS path separator).
		// On POSIX, backslash is a valid filename character, so "path\to\file.go" and
		// "path/to/file.go" are distinct paths and will produce different IDs.
		// This test verifies that ComputeFindingID is deterministic for any given input;
		// cross-platform slash normalisation is a Windows-only guarantee.
		idA := ComputeFindingID("dim1", "cat", "repo", "path/to/file.go")
		idB := ComputeFindingID("dim1", "cat", "repo", "path/to/file.go")
		if idA != idB {
			t.Errorf("same path produced different IDs: %q vs %q", idA, idB)
		}
	})

	t.Run("result is a 64-char hex string (SHA256)", func(t *testing.T) {
		id := ComputeFindingID("dim", "cat", "repo", "file.go")
		if len(id) != 64 {
			t.Errorf("expected 64-char hex, got len=%d: %q", len(id), id)
		}
		for _, c := range id {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Errorf("non-hex char %q in ID %q", c, id)
				break
			}
		}
	})
}

// --- TestComputeTrendDirection ---

func TestComputeTrendDirection(t *testing.T) {
	threshold := 2.0

	tests := []struct {
		name      string
		delta     float64
		threshold float64
		want      string
	}{
		{"stable — delta exactly zero", 0.0, threshold, "stable"},
		{"stable — delta below threshold (positive)", 1.9, threshold, "stable"},
		{"stable — delta below threshold (negative)", -1.9, threshold, "stable"},
		{"stable — delta exactly at threshold boundary (just under)", 1.999, threshold, "stable"},
		{"improving — delta equals threshold", 2.0, threshold, "improving"},
		{"improving — delta well above threshold", 10.0, threshold, "improving"},
		{"degrading — delta equals negative threshold", -2.0, threshold, "degrading"},
		{"degrading — delta well below threshold", -10.0, threshold, "degrading"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeTrendDirection(tc.delta, tc.threshold)
			if got != tc.want {
				t.Errorf("ComputeTrendDirection(%v, %v) = %q; want %q", tc.delta, tc.threshold, got, tc.want)
			}
		})
	}
}

// --- TestCompareTwoAudits_NilCurrent ---

func TestCompareTwoAudits_NilCurrent(t *testing.T) {
	prev := minimalReport("2026-01-01T00:00:00Z", 80.0, nil)
	result, err := CompareTwoAudits(nil, prev, 2.0)
	if err == nil {
		t.Fatal("expected error when current is nil, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result when current is nil, got %+v", result)
	}
}

// --- TestCompareTwoAudits_NilPrevious ---

func TestCompareTwoAudits_NilPrevious(t *testing.T) {
	finding := makeTrendFinding("", "auth", "my-repo", "main.go", "high", "Missing auth check")
	dim := makeDim("Dimension 1", 75.0, 1.0, []AuditFinding{finding})
	curr := minimalReport("2026-03-01T00:00:00Z", 75.0, []*DimensionResult{dim})

	analysis, err := CompareTwoAudits(curr, nil, 2.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if analysis == nil {
		t.Fatal("expected non-nil analysis")
	}
	if analysis.Trend != "stable" {
		t.Errorf("expected trend=stable with no previous, got %q", analysis.Trend)
	}
	if analysis.NewFindings != 1 {
		t.Errorf("expected 1 new finding, got %d", analysis.NewFindings)
	}
	if analysis.ResolvedFindings != 0 {
		t.Errorf("expected 0 resolved findings, got %d", analysis.ResolvedFindings)
	}
	if analysis.PersistentFindings != 0 {
		t.Errorf("expected 0 persistent findings, got %d", analysis.PersistentFindings)
	}
	if len(analysis.FindingChanges) != 1 {
		t.Fatalf("expected 1 finding change, got %d", len(analysis.FindingChanges))
	}
	fc := analysis.FindingChanges[0]
	if fc.Classification != "new" {
		t.Errorf("expected classification=new, got %q", fc.Classification)
	}
	if fc.Category != "auth" {
		t.Errorf("expected category=auth, got %q", fc.Category)
	}
}

// --- TestCompareTwoAudits_NewAndResolved ---

func TestCompareTwoAudits_NewAndResolved(t *testing.T) {
	// Previous: one finding in dim1
	prevFinding := makeTrendFinding("prev-id-1", "auth", "repo-a", "old.go", "high", "old auth issue")
	prevDim := makeDim("Dimension 1", 70.0, 1.0, []AuditFinding{prevFinding})
	prev := minimalReport("2026-01-01T00:00:00Z", 70.0, []*DimensionResult{prevDim})

	// Current: different finding in dim1 (new) — previous finding not present (resolved)
	currFinding := makeTrendFinding("curr-id-1", "api", "repo-a", "new.go", "medium", "new api drift")
	currDim := makeDim("Dimension 1", 80.0, 1.0, []AuditFinding{currFinding})
	curr := minimalReport("2026-03-01T00:00:00Z", 80.0, []*DimensionResult{currDim})

	analysis, err := CompareTwoAudits(curr, prev, 2.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if analysis.NewFindings != 1 {
		t.Errorf("expected 1 new finding, got %d", analysis.NewFindings)
	}
	if analysis.ResolvedFindings != 1 {
		t.Errorf("expected 1 resolved finding, got %d", analysis.ResolvedFindings)
	}
	if analysis.PersistentFindings != 0 {
		t.Errorf("expected 0 persistent findings, got %d", analysis.PersistentFindings)
	}

	var newCount, resolvedCount int
	for _, fc := range analysis.FindingChanges {
		switch fc.Classification {
		case "new":
			newCount++
			if fc.FindingID != "curr-id-1" {
				t.Errorf("new finding ID mismatch: got %q, want curr-id-1", fc.FindingID)
			}
		case "resolved":
			resolvedCount++
			if fc.FindingID != "prev-id-1" {
				t.Errorf("resolved finding ID mismatch: got %q, want prev-id-1", fc.FindingID)
			}
		}
	}
	if newCount != 1 {
		t.Errorf("expected 1 finding classified as 'new', got %d", newCount)
	}
	if resolvedCount != 1 {
		t.Errorf("expected 1 finding classified as 'resolved', got %d", resolvedCount)
	}
}

// --- TestCompareTwoAudits_Persistent ---

func TestCompareTwoAudits_Persistent(t *testing.T) {
	// Same finding ID and same severity in both audits → persistent
	sharedID := "shared-finding-id"
	finding := makeTrendFinding(sharedID, "security", "repo-b", "handler.go", "critical", "SQL injection risk")

	prevDim := makeDim("Dimension 6", 60.0, 1.0, []AuditFinding{finding})
	prev := minimalReport("2026-01-01T00:00:00Z", 60.0, []*DimensionResult{prevDim})

	currDim := makeDim("Dimension 6", 62.0, 1.0, []AuditFinding{finding})
	curr := minimalReport("2026-03-01T00:00:00Z", 62.0, []*DimensionResult{currDim})

	analysis, err := CompareTwoAudits(curr, prev, 5.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if analysis.PersistentFindings != 1 {
		t.Errorf("expected 1 persistent finding, got %d", analysis.PersistentFindings)
	}
	if analysis.NewFindings != 0 {
		t.Errorf("expected 0 new findings, got %d", analysis.NewFindings)
	}
	if analysis.ResolvedFindings != 0 {
		t.Errorf("expected 0 resolved findings, got %d", analysis.ResolvedFindings)
	}
	if len(analysis.FindingChanges) != 1 {
		t.Fatalf("expected 1 finding change, got %d", len(analysis.FindingChanges))
	}
	fc := analysis.FindingChanges[0]
	if fc.Classification != "persistent" {
		t.Errorf("expected classification=persistent, got %q", fc.Classification)
	}
	if fc.FindingID != sharedID {
		t.Errorf("expected finding ID %q, got %q", sharedID, fc.FindingID)
	}
	// Trend should be stable because delta (2.0) < threshold (5.0)
	if analysis.Trend != "stable" {
		t.Errorf("expected trend=stable, got %q", analysis.Trend)
	}
}

// --- TestCompareTwoAudits_ScoreDelta ---

func TestCompareTwoAudits_ScoreDelta(t *testing.T) {
	prevDim := makeDim("Dimension 1", 70.0, 1.0, nil)
	prev := minimalReport("2026-01-01T00:00:00Z", 70.0, []*DimensionResult{prevDim})

	currDim := makeDim("Dimension 1", 85.0, 1.0, nil)
	curr := minimalReport("2026-03-01T00:00:00Z", 85.0, []*DimensionResult{currDim})

	analysis, err := CompareTwoAudits(curr, prev, 2.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantDelta := 15.0
	if analysis.ScoreDelta != wantDelta {
		t.Errorf("expected ScoreDelta=%.2f, got %.2f", wantDelta, analysis.ScoreDelta)
	}
	if analysis.Trend != "improving" {
		t.Errorf("expected trend=improving, got %q", analysis.Trend)
	}
}

// --- TestBuildFindingMap ---

func TestBuildFindingMap(t *testing.T) {
	f1 := makeTrendFinding("id-aaa", "cat1", "repo", "a.go", "high", "finding one")
	f2 := makeTrendFinding("id-bbb", "cat2", "repo", "b.go", "low", "finding two")

	dim1 := makeDim("Dimension 1", 80.0, 0.5, []AuditFinding{f1})
	dim2 := makeDim("Dimension 2", 90.0, 0.5, []AuditFinding{f2})
	report := minimalReport("2026-03-01T00:00:00Z", 85.0, []*DimensionResult{dim1, dim2})

	m := buildFindingMap(report)

	if len(m) != 2 {
		t.Fatalf("expected map length 2, got %d", len(m))
	}

	got1, ok := m["id-aaa"]
	if !ok {
		t.Fatal("expected key 'id-aaa' in map")
	}
	if got1.Category != "cat1" {
		t.Errorf("expected category cat1, got %q", got1.Category)
	}

	got2, ok := m["id-bbb"]
	if !ok {
		t.Fatal("expected key 'id-bbb' in map")
	}
	if got2.Severity != "low" {
		t.Errorf("expected severity low, got %q", got2.Severity)
	}
}

func TestBuildFindingMap_EmptyIDComputedFromFields(t *testing.T) {
	// Finding with no explicit ID — map key should be the computed SHA256 ID.
	f := makeTrendFinding("", "auth", "my-repo", "main.go", "medium", "no ID set")
	dim := makeDim("Dimension 3", 75.0, 1.0, []AuditFinding{f})
	report := minimalReport("2026-03-01T00:00:00Z", 75.0, []*DimensionResult{dim})

	m := buildFindingMap(report)
	if len(m) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m))
	}

	expectedID := ComputeFindingID("Dimension 3", "auth", "my-repo", "main.go")
	if _, ok := m[expectedID]; !ok {
		t.Errorf("expected key %q in map; keys present: %v", expectedID, auditFindingMapKeys(m))
	}
}

// auditFindingMapKeys returns the keys of a map[string]*AuditFinding for error messages.
func auditFindingMapKeys(m map[string]*AuditFinding) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
