package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"path/filepath"
	"sort"
)

// TrendAnalysis is the output of comparing two synthesis reports.
type TrendAnalysis struct {
	CurrentTimestamp   string                     `json:"current_timestamp"`
	PreviousTimestamp  string                     `json:"previous_timestamp"`
	CurrentScore       float64                    `json:"current_score"`
	PreviousScore      float64                    `json:"previous_score"`
	ScoreDelta         float64                    `json:"score_delta"`
	Trend              string                     `json:"trend"` // "improving", "degrading", "stable"
	NewFindings        int                        `json:"new_findings"`
	ResolvedFindings   int                        `json:"resolved_findings"`
	PersistentFindings int                        `json:"persistent_findings"`
	ByDimension        map[string]*DimensionTrend `json:"by_dimension"`
	FindingChanges     []*FindingChange           `json:"finding_changes"`
}

// DimensionTrend holds trend data for a single dimension.
type DimensionTrend struct {
	CurrentScore    float64 `json:"current_score"`
	PreviousScore   float64 `json:"previous_score"`
	Delta           float64 `json:"delta"`
	Trend           string  `json:"trend"`
	NewCount        int     `json:"new_count"`
	ResolvedCount   int     `json:"resolved_count"`
	PersistentCount int     `json:"persistent_count"`
}

// FindingChange records how a specific finding changed between audits.
type FindingChange struct {
	FindingID           string `json:"finding_id"`
	Category            string `json:"category"`
	Repository          string `json:"repository"`
	File                string `json:"file"`
	Classification      string `json:"classification"` // "new", "resolved", "persistent", "regressed"
	Severity            string `json:"severity"`
	CurrentDescription  string `json:"current_description,omitempty"`
	PreviousDescription string `json:"previous_description,omitempty"`
}

// ComputeFindingID produces a deterministic SHA256-based ID for a finding.
func ComputeFindingID(dimension, category, repo, file string) string {
	normalized := filepath.ToSlash(filepath.Clean(file))
	raw := fmt.Sprintf("%s|%s|%s|%s", dimension, category, repo, normalized)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ComputeTrendDirection returns "stable", "improving", or "degrading" based on the
// score delta relative to threshold.
func ComputeTrendDirection(scoreDelta float64, threshold float64) string {
	if math.Abs(scoreDelta) < threshold {
		return "stable"
	}
	if scoreDelta > 0 {
		return "improving"
	}
	return "degrading"
}

// CompareTwoAudits compares current and previous SynthesisReports and returns a TrendAnalysis.
func CompareTwoAudits(current, previous *SynthesisReport, stableThreshold float64) (*TrendAnalysis, error) {
	if current == nil {
		return nil, fmt.Errorf("current SynthesisReport must not be nil")
	}

	analysis := &TrendAnalysis{
		CurrentTimestamp: current.Timestamp,
		CurrentScore:     current.OverallScore,
		ByDimension:      make(map[string]*DimensionTrend),
		FindingChanges:   []*FindingChange{},
	}

	// No previous — classify all current findings as new, trend is stable (no baseline).
	if previous == nil {
		analysis.Trend = "stable"
		for _, dim := range current.Dimensions {
			dt := &DimensionTrend{
				CurrentScore: dim.Score,
				Trend:        "stable",
			}
			for _, f := range dim.Findings {
				id := f.ID
				if id == "" {
					id = ComputeFindingID(dim.Name, f.Category, f.Repository, f.File)
				}
				analysis.FindingChanges = append(analysis.FindingChanges, &FindingChange{
					FindingID:          id,
					Category:           f.Category,
					Repository:         f.Repository,
					File:               f.File,
					Classification:     "new",
					Severity:           f.Severity,
					CurrentDescription: f.Description,
				})
				dt.NewCount++
				analysis.NewFindings++
			}
			analysis.ByDimension[dim.Name] = dt
		}
		sortFindingChanges(analysis.FindingChanges)
		return analysis, nil
	}

	analysis.PreviousTimestamp = previous.Timestamp
	analysis.PreviousScore = previous.OverallScore
	analysis.ScoreDelta = current.OverallScore - previous.OverallScore
	analysis.Trend = ComputeTrendDirection(analysis.ScoreDelta, stableThreshold)

	prevMap := buildFindingMap(previous)
	currMap := buildFindingMap(current)

	// Build a set of dimension scores from previous for delta computation.
	prevDimScores := make(map[string]float64)
	for _, dim := range previous.Dimensions {
		prevDimScores[dim.Name] = dim.Score
	}

	// Classify current findings.
	for _, dim := range current.Dimensions {
		dt, ok := analysis.ByDimension[dim.Name]
		if !ok {
			dt = &DimensionTrend{
				CurrentScore:  dim.Score,
				PreviousScore: prevDimScores[dim.Name],
			}
			dt.Delta = dt.CurrentScore - dt.PreviousScore
			dt.Trend = ComputeTrendDirection(dt.Delta, stableThreshold)
			analysis.ByDimension[dim.Name] = dt
		}

		for _, f := range dim.Findings {
			id := f.ID
			if id == "" {
				id = ComputeFindingID(dim.Name, f.Category, f.Repository, f.File)
			}

			fc := &FindingChange{
				FindingID:          id,
				Category:           f.Category,
				Repository:         f.Repository,
				File:               f.File,
				Severity:           f.Severity,
				CurrentDescription: f.Description,
			}

			if prev, exists := prevMap[id]; exists {
				if prev.Severity == f.Severity {
					fc.Classification = "persistent"
					fc.PreviousDescription = prev.Description
					dt.PersistentCount++
					analysis.PersistentFindings++
				} else {
					fc.Classification = "regressed"
					fc.PreviousDescription = prev.Description
					dt.NewCount++
					analysis.NewFindings++
				}
			} else {
				fc.Classification = "new"
				dt.NewCount++
				analysis.NewFindings++
			}

			analysis.FindingChanges = append(analysis.FindingChanges, fc)
		}
	}

	// Classify resolved findings (in previous but not in current).
	prevDimNames := make(map[string]string) // findingID → dimension name
	for _, dim := range previous.Dimensions {
		for _, f := range dim.Findings {
			id := f.ID
			if id == "" {
				id = ComputeFindingID(dim.Name, f.Category, f.Repository, f.File)
			}
			prevDimNames[id] = dim.Name
		}
	}

	for id, f := range prevMap {
		if _, exists := currMap[id]; !exists {
			dimName := prevDimNames[id]
			fc := &FindingChange{
				FindingID:           id,
				Category:            f.Category,
				Repository:          f.Repository,
				File:                f.File,
				Classification:      "resolved",
				Severity:            f.Severity,
				PreviousDescription: f.Description,
			}
			analysis.FindingChanges = append(analysis.FindingChanges, fc)
			analysis.ResolvedFindings++

			if dt, ok := analysis.ByDimension[dimName]; ok {
				dt.ResolvedCount++
			} else {
				analysis.ByDimension[dimName] = &DimensionTrend{
					PreviousScore: prevDimScores[dimName],
					Trend:         "stable",
					ResolvedCount: 1,
				}
			}
		}
	}

	// Fill in any previous dimensions missing from current.
	for _, dim := range previous.Dimensions {
		if _, ok := analysis.ByDimension[dim.Name]; !ok {
			analysis.ByDimension[dim.Name] = &DimensionTrend{
				PreviousScore: dim.Score,
				Trend:         "stable",
			}
		}
	}

	sortFindingChanges(analysis.FindingChanges)
	return analysis, nil
}

// buildFindingMap returns a map of finding ID → *AuditFinding for all findings in the report.
func buildFindingMap(report *SynthesisReport) map[string]*AuditFinding {
	m := make(map[string]*AuditFinding)
	for _, dim := range report.Dimensions {
		for i := range dim.Findings {
			f := &dim.Findings[i]
			id := f.ID
			if id == "" {
				id = ComputeFindingID(dim.Name, f.Category, f.Repository, f.File)
			}
			m[id] = f
		}
	}
	return m
}

// classifySeverityOrder returns a sort order for severity strings (lower = higher priority).
func classifySeverityOrder(sev string) int {
	switch sev {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

// classificationOrder returns sort order for finding classifications.
func classificationOrder(cls string) int {
	switch cls {
	case "new":
		return 0
	case "regressed":
		return 1
	case "persistent":
		return 2
	case "resolved":
		return 3
	default:
		return 4
	}
}

// sortFindingChanges sorts finding changes: new first, then by severity (critical first).
func sortFindingChanges(changes []*FindingChange) {
	sort.SliceStable(changes, func(i, j int) bool {
		ci := classificationOrder(changes[i].Classification)
		cj := classificationOrder(changes[j].Classification)
		if ci != cj {
			return ci < cj
		}
		return classifySeverityOrder(changes[i].Severity) < classifySeverityOrder(changes[j].Severity)
	})
}
