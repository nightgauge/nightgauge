package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DimensionInput is what each dimension producer writes to disk.
type DimensionInput struct {
	Dimension string       `json:"dimension"`
	Findings  []RawFinding `json:"findings"`
	Score     float64      `json:"score"`
	Weight    float64      `json:"weight"`
}

// RawFinding is the finding format produced by each dimension implementation.
type RawFinding struct {
	Category           string   `json:"category"`
	Repository         string   `json:"repository"`
	File               string   `json:"file"`
	Severity           string   `json:"severity"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	BlockedBy          []string `json:"blockedBy,omitempty"`
}

// SynthesisReport is the unified output.
type SynthesisReport struct {
	Timestamp     string             `json:"timestamp"`
	OverallScore  float64            `json:"overall_score"`
	Trend         string             `json:"trend"` // "improving", "degrading", "stable", "unknown"
	Dimensions    []*DimensionResult `json:"dimensions"`
	FindingsBySev map[string]int     `json:"findings_by_severity"`
	TotalFindings int                `json:"total_findings"`
}

// DimensionResult is a single dimension's contribution to the synthesis.
type DimensionResult struct {
	Name          string         `json:"name"`
	Score         float64        `json:"score"`
	Weight        float64        `json:"weight"`
	WeightedScore float64        `json:"weighted_score"`
	Findings      []AuditFinding `json:"findings"`
}

// AuditFinding is a normalized finding with a stable ID.
type AuditFinding struct {
	ID                 string   `json:"id"` // stable SHA256 hash
	Category           string   `json:"category"`
	Repository         string   `json:"repository"`
	File               string   `json:"file"`
	Severity           string   `json:"severity"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	BlockedBy          []string `json:"blocked_by,omitempty"`
}

// dimensionNameMap maps "Dimension N" prefixes to short keys.
var dimensionNameMap = map[string]string{
	"dimension 1": "api_alignment",
	"dimension 2": "lifecycle",
	"dimension 3": "documentation",
	"dimension 4": "feature_parity",
	"dimension 5": "test_coverage",
	"dimension 6": "security",
	"dimension 7": "dependencies",
	"dimension 8": "ci_cd",
}

// NormalizeDimensionName extracts a short name from a dimension string.
// "Dimension 1: API Alignment" → "api_alignment"
// "Dimension 1" → "api_alignment"
// Falls back to lowercased/underscored input if no mapping exists.
func NormalizeDimensionName(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	// Try prefix matching against the known map.
	for prefix, short := range dimensionNameMap {
		if lower == prefix || strings.HasPrefix(lower, prefix+":") || strings.HasPrefix(lower, prefix+" ") {
			return short
		}
	}
	// Fallback: replace non-alphanumeric runs with underscores.
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, lower)
	// Collapse repeated underscores.
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	return strings.Trim(result, "_")
}

// computeFindingIDLocal returns a stable SHA256-based ID for a finding.
func computeFindingIDLocal(dimension, category, repo, file string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s", dimension, category, repo, filepath.ToSlash(file))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LoadDimensionFiles reads all dimension-*.json files from inputDir.
// It returns loaded dimensions and any non-fatal warnings for missing/invalid files.
func LoadDimensionFiles(inputDir string) ([]*DimensionInput, []string, error) {
	pattern := filepath.Join(inputDir, "dimension-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("glob %q: %w", pattern, err)
	}

	var dims []*DimensionInput
	var warnings []string

	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not read %s: %v", path, err))
			continue
		}
		var d DimensionInput
		if err := json.Unmarshal(data, &d); err != nil {
			warnings = append(warnings, fmt.Sprintf("invalid JSON in %s: %v", path, err))
			continue
		}
		if d.Dimension == "" {
			warnings = append(warnings, fmt.Sprintf("missing 'dimension' field in %s", path))
			continue
		}
		dims = append(dims, &d)
	}

	return dims, warnings, nil
}

// ValidateWeights checks that the sum of all dimension weights is within ±0.01 of 1.0.
// configWeights overrides per-dimension weights when provided.
func ValidateWeights(dimensions []*DimensionInput, configWeights map[string]float64) error {
	var sum float64
	var detail strings.Builder

	for _, d := range dimensions {
		shortName := NormalizeDimensionName(d.Dimension)
		w := d.Weight
		if configWeights != nil {
			if override, ok := configWeights[shortName]; ok {
				w = override
			}
		}
		fmt.Fprintf(&detail, "  %s (%s): %.4f\n", d.Dimension, shortName, w)
		sum += w
	}

	if math.Abs(sum-1.0) > 0.01 {
		return fmt.Errorf("dimension weights sum to %.4f (must be within ±0.01 of 1.0):\n%s", sum, detail.String())
	}
	return nil
}

// SynthesizeReport produces a unified SynthesisReport from dimension inputs.
// Trend is always set to "unknown" — trend comparison is handled separately.
func SynthesizeReport(dimensions []*DimensionInput, configWeights map[string]float64) (*SynthesisReport, error) {
	if err := ValidateWeights(dimensions, configWeights); err != nil {
		return nil, err
	}

	findingsBySev := map[string]int{
		"critical": 0,
		"high":     0,
		"medium":   0,
		"low":      0,
	}
	totalFindings := 0

	var dimResults []*DimensionResult

	for _, d := range dimensions {
		shortName := NormalizeDimensionName(d.Dimension)
		w := d.Weight
		if configWeights != nil {
			if override, ok := configWeights[shortName]; ok {
				w = override
			}
		}

		var findings []AuditFinding
		for _, rf := range d.Findings {
			id := computeFindingIDLocal(d.Dimension, rf.Category, rf.Repository, rf.File)
			findings = append(findings, AuditFinding{
				ID:                 id,
				Category:           rf.Category,
				Repository:         rf.Repository,
				File:               rf.File,
				Severity:           rf.Severity,
				Description:        rf.Description,
				AcceptanceCriteria: rf.AcceptanceCriteria,
				BlockedBy:          rf.BlockedBy,
			})
			sev := strings.ToLower(rf.Severity)
			findingsBySev[sev]++
			totalFindings++
		}

		dimResults = append(dimResults, &DimensionResult{
			Name:          d.Dimension,
			Score:         d.Score,
			Weight:        w,
			WeightedScore: math.Round(d.Score*w*100) / 100,
			Findings:      findings,
		})
	}

	overallScore := ComputeWeightedScore(dimResults)

	return &SynthesisReport{
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		OverallScore:  overallScore,
		Trend:         "unknown",
		Dimensions:    dimResults,
		FindingsBySev: findingsBySev,
		TotalFindings: totalFindings,
	}, nil
}

// ComputeWeightedScore returns the sum of (score × weight) rounded to 2 decimal places.
func ComputeWeightedScore(dimensions []*DimensionResult) float64 {
	var sum float64
	for _, d := range dimensions {
		sum += d.Score * d.Weight
	}
	return math.Round(sum*100) / 100
}

// severityOrder defines sorting priority for findings.
var severityOrder = map[string]int{
	"critical": 0,
	"high":     1,
	"medium":   2,
	"low":      3,
	"info":     4,
}

func severityRank(s string) int {
	if r, ok := severityOrder[strings.ToLower(s)]; ok {
		return r
	}
	return 99
}

// GenerateMarkdownReport renders the SynthesisReport as a markdown string.
func GenerateMarkdownReport(report *SynthesisReport, warnings []string) string {
	var sb strings.Builder

	// Parse timestamp for display.
	ts := report.Timestamp
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		ts = t.Format("2006-01-02")
	}

	sb.WriteString("# Product Audit Report\n\n")
	sb.WriteString(fmt.Sprintf("**Date:** %s\n\n", ts))

	// Executive Summary
	sb.WriteString("## Executive Summary\n\n")
	trendStr := report.Trend
	if trendStr == "unknown" {
		trendStr = "unknown (no historical baseline)"
	}
	sb.WriteString(fmt.Sprintf(
		"The product audit produced an overall score of **%.2f / 100** with a trend of **%s**. "+
			"A total of **%d findings** were identified across all audited dimensions.\n\n",
		report.OverallScore, trendStr, report.TotalFindings,
	))

	// Overall Score
	sb.WriteString("## Overall Score\n\n")
	sb.WriteString(fmt.Sprintf("**%.2f / 100**\n\n", report.OverallScore))
	sb.WriteString("| Threshold | Guidance |\n")
	sb.WriteString("|-----------|----------|\n")
	sb.WriteString("| ≥ 90      | Excellent — minimal action required |\n")
	sb.WriteString("| 75 – 89   | Good — address high-severity findings |\n")
	sb.WriteString("| 60 – 74   | Fair — plan remediation sprint |\n")
	sb.WriteString("| < 60      | Poor — immediate attention needed |\n")
	sb.WriteString("\n")

	// Dimension Scores table
	sb.WriteString("## Dimension Scores\n\n")
	sb.WriteString("| Dimension | Score | Weight | Weighted Score |\n")
	sb.WriteString("|-----------|------:|-------:|---------------:|\n")
	for _, d := range report.Dimensions {
		sb.WriteString(fmt.Sprintf("| %s | %.2f | %.2f | %.2f |\n",
			d.Name, d.Score, d.Weight, d.WeightedScore))
	}
	sb.WriteString("\n")

	// Findings by Severity
	sb.WriteString("## Findings by Severity\n\n")
	sb.WriteString("| Severity | Count |\n")
	sb.WriteString("|----------|------:|\n")
	sevOrder := []string{"critical", "high", "medium", "low"}
	for _, sev := range sevOrder {
		count := report.FindingsBySev[sev]
		sb.WriteString(fmt.Sprintf("| %s | %d |\n", strings.Title(sev), count))
	}
	sb.WriteString("\n")

	// Detailed Findings
	sb.WriteString("## Detailed Findings\n\n")
	for _, dim := range report.Dimensions {
		if len(dim.Findings) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n\n", dim.Name))

		// Sort findings by severity.
		sorted := make([]AuditFinding, len(dim.Findings))
		copy(sorted, dim.Findings)
		sort.Slice(sorted, func(i, j int) bool {
			ri := severityRank(sorted[i].Severity)
			rj := severityRank(sorted[j].Severity)
			if ri != rj {
				return ri < rj
			}
			return sorted[i].ID < sorted[j].ID
		})

		for _, f := range sorted {
			sb.WriteString(fmt.Sprintf("**[%s]** `%s` — %s\n\n",
				strings.ToUpper(f.Severity), f.Category, f.Description))
			if f.Repository != "" || f.File != "" {
				loc := f.Repository
				if f.File != "" {
					loc = loc + "/" + f.File
				}
				sb.WriteString(fmt.Sprintf("- Location: `%s`\n", loc))
			}
			if len(f.AcceptanceCriteria) > 0 {
				sb.WriteString("- Acceptance criteria:\n")
				for _, ac := range f.AcceptanceCriteria {
					sb.WriteString(fmt.Sprintf("  - %s\n", ac))
				}
			}
			if len(f.BlockedBy) > 0 {
				sb.WriteString(fmt.Sprintf("- Blocked by: %s\n", strings.Join(f.BlockedBy, ", ")))
			}
			sb.WriteString(fmt.Sprintf("- Finding ID: `%s`\n\n", f.ID))
		}
	}

	// Warnings
	if len(warnings) > 0 {
		sb.WriteString("## Warnings\n\n")
		for _, w := range warnings {
			sb.WriteString(fmt.Sprintf("- %s\n", w))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// WriteSynthesisOutputs writes the JSON and markdown reports to outputDir.
// Returns the paths written.
func WriteSynthesisOutputs(report *SynthesisReport, markdownReport string, outputDir string) (jsonPath, mdPath string, err error) {
	if err = os.MkdirAll(outputDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create output dir %q: %w", outputDir, err)
	}

	// Derive date suffix from report timestamp.
	dateSuffix := "unknown"
	if t, parseErr := time.Parse(time.RFC3339, report.Timestamp); parseErr == nil {
		dateSuffix = t.UTC().Format("2006-01-02")
	}

	jsonPath = filepath.Join(outputDir, fmt.Sprintf("product-audit-%s.json", dateSuffix))
	mdPath = filepath.Join(outputDir, fmt.Sprintf("product-audit-%s.md", dateSuffix))

	// Write JSON.
	jsonData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal report JSON: %w", err)
	}
	if err = os.WriteFile(jsonPath, jsonData, 0o644); err != nil {
		return "", "", fmt.Errorf("write JSON report: %w", err)
	}

	// Write markdown.
	if err = os.WriteFile(mdPath, []byte(markdownReport), 0o644); err != nil {
		return "", "", fmt.Errorf("write markdown report: %w", err)
	}

	return jsonPath, mdPath, nil
}
