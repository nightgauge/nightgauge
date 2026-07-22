package aggregatefindings

import (
	"fmt"
	"strings"
	"time"
)

// NormalizeSeverity maps a health-check status string to a canonical severity.
// Unknown inputs default to "info" (safest assumption — not silently dropped).
// Mapping matches SKILL.md lines 275–280 (status string, not score range).
func NormalizeSeverity(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "critical":
		return SeverityCritical
	case "poor":
		return SeverityHigh
	case "fair":
		return SeverityMedium
	case "good":
		return SeverityLow
	case "excellent":
		return SeverityInfo
	default:
		return SeverityInfo
	}
}

// DedupKey returns the deduplication key for a finding. The key is
// source_dimension + "::" + normalized(title) where normalization lowercases
// and trims whitespace. The source_dimension prefix namespaces findings so
// that same-title findings in different dimensions are not collapsed.
func DedupKey(sourceDimension, title string) string {
	return strings.ToLower(strings.TrimSpace(sourceDimension)) +
		"::" +
		strings.ToLower(strings.TrimSpace(title))
}

// Aggregate loads findings from all three assessment sources, deduplicates
// overlapping entries (keeping the finding with the longer recommendation as a
// deterministic proxy for specificity), and returns a Result.
//
// At least one source file must be present; if all three are missing an error
// is returned. Missing individual files are reported in Result.SourcesMissing.
func Aggregate(workdir string) (*Result, error) {
	type sourceLoad struct {
		name   string
		loader func(string) ([]Finding, error)
	}
	sources := []sourceLoad{
		{"health-check", LoadHealthReport},
		{"security-audit", LoadSecurityAudit},
		{"test-scaffold", LoadTestScaffold},
	}

	var all []Finding
	var read, missing []string

	for _, s := range sources {
		findings, err := s.loader(workdir)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", s.name, err)
		}
		if findings == nil {
			missing = append(missing, s.name)
		} else {
			read = append(read, s.name)
			all = append(all, findings...)
		}
	}

	if len(read) == 0 {
		return nil, fmt.Errorf("no assessment files found in %s/.nightgauge/ — run health-check, security-audit, or test-scaffold first", workdir)
	}

	// Deduplicate: key → winner finding; track merged IDs.
	type dedupEntry struct {
		finding    Finding
		mergedFrom []string
	}
	seen := make(map[string]*dedupEntry)
	var order []string // preserve insertion order for stable output

	for _, f := range all {
		key := DedupKey(f.SourceDimension, f.Title)
		if existing, ok := seen[key]; ok {
			// Keep the finding with the longer recommendation (deterministic proxy).
			if len(f.Recommendation) > len(existing.finding.Recommendation) {
				existing.mergedFrom = append(existing.mergedFrom, existing.finding.ID)
				existing.finding = f
			} else {
				existing.mergedFrom = append(existing.mergedFrom, f.ID)
			}
		} else {
			seen[key] = &dedupEntry{finding: f}
			order = append(order, key)
		}
	}

	deduped := make([]Finding, 0, len(order))
	for _, key := range order {
		e := seen[key]
		f := e.finding
		if len(e.mergedFrom) > 0 {
			f.MergedFrom = e.mergedFrom
		}
		deduped = append(deduped, f)
	}

	bySeverity := map[string]int{
		SeverityCritical: 0,
		SeverityHigh:     0,
		SeverityMedium:   0,
		SeverityLow:      0,
		SeverityInfo:     0,
	}
	for _, f := range deduped {
		if _, ok := bySeverity[f.Severity]; ok {
			bySeverity[f.Severity]++
		} else {
			bySeverity[f.Severity]++
		}
	}

	totalBefore := len(all)
	afterDedup := len(deduped)
	dedupRate := 0.0
	if totalBefore > 0 {
		dedupRate = float64(totalBefore-afterDedup) / float64(totalBefore)
	}

	return &Result{
		V:              1,
		SourcesRead:    read,
		SourcesMissing: missing,
		Findings:       deduped,
		Summary: Summary{
			TotalFindings:     totalBefore,
			AfterDedup:        afterDedup,
			BySeverity:        bySeverity,
			DeduplicationRate: dedupRate,
		},
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}
