package aggregatefindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// --- internal JSON shapes ---

// healthReport is the top-level shape of the health-report.json report.
// Fields beyond what is extracted are ignored; omitempty guards against missing
// optional keys in older report versions.
type healthReport struct {
	Dimensions map[string]healthDimension `json:"dimensions"`
}

type healthDimension struct {
	Status   string          `json:"status"`
	Findings []healthFinding `json:"findings"`
}

type healthFinding struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	Recommendation string `json:"recommendation"`
}

// securityReport is the top-level shape of the security-audit.json report.
type securityReport struct {
	Dimensions map[string]securityDimension `json:"dimensions"`
}

type securityDimension struct {
	Findings []securityFinding `json:"findings"`
}

type securityFinding struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	Recommendation string `json:"recommendation"`
	Severity       string `json:"severity"`
}

// testScaffoldReport is the top-level shape of
// the test-scaffold-report.json report.
type testScaffoldReport struct {
	Gaps            []testGap            `json:"gaps"`
	Recommendations []testRecommendation `json:"recommendations"`
}

type testGap struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	Recommendation string `json:"recommendation"`
	Priority       string `json:"priority"`
}

type testRecommendation struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	Recommendation string `json:"recommendation"`
	Priority       string `json:"priority"`
}

// --- loaders ---

// openReport opens a generated report of the checkout workdir is in,
// CHECKOUT/reports/<name> (ADR-024 § 7). It returns (nil, nil) when the report
// does not exist, including for a workdir outside git, which has no
// per-checkout directory and so no reports.
func openReport(workdir, name string) (*os.File, error) {
	root, err := filepath.Abs(workdir)
	if err != nil {
		return nil, err
	}
	path, err := layout.ReportPath(root, name)
	if errors.Is(err, layout.ErrNotGitRepository) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return f, err
}

// LoadHealthReport reads the checkout's health-report.json and
// converts each dimension's findings into normalized Finding values. The
// dimension's status string drives severity (see NormalizeSeverity). Returns
// (nil, nil) when the file does not exist.
func LoadHealthReport(workdir string) ([]Finding, error) {
	f, err := openReport(workdir, layout.ReportHealth)
	if f == nil || err != nil {
		return nil, err
	}
	defer f.Close()

	var report healthReport
	if err := json.NewDecoder(f).Decode(&report); err != nil {
		return nil, fmt.Errorf("health-report.json: %w", err)
	}

	var findings []Finding
	for dim, d := range report.Dimensions {
		severity := NormalizeSeverity(d.Status)
		for i, hf := range d.Findings {
			findings = append(findings, Finding{
				ID:              fmt.Sprintf("health-check::%s::%d", dim, i),
				Title:           hf.Title,
				Description:     hf.Description,
				Recommendation:  hf.Recommendation,
				Source:          "health-check",
				SourceDimension: dim,
				Severity:        severity,
			})
		}
	}
	return findings, nil
}

// LoadSecurityAudit reads the checkout's security-audit.json.
// Security audit findings already carry normalized severity — passed through
// unchanged. Returns (nil, nil) when the file does not exist.
func LoadSecurityAudit(workdir string) ([]Finding, error) {
	f, err := openReport(workdir, layout.ReportSecurityAudit)
	if f == nil || err != nil {
		return nil, err
	}
	defer f.Close()

	var report securityReport
	if err := json.NewDecoder(f).Decode(&report); err != nil {
		return nil, fmt.Errorf("security-audit.json: %w", err)
	}

	var findings []Finding
	for dim, d := range report.Dimensions {
		for i, sf := range d.Findings {
			severity := sf.Severity
			if severity == "" {
				severity = SeverityInfo
			}
			findings = append(findings, Finding{
				ID:              fmt.Sprintf("security-audit::%s::%d", dim, i),
				Title:           sf.Title,
				Description:     sf.Description,
				Recommendation:  sf.Recommendation,
				Source:          "security-audit",
				SourceDimension: dim,
				Severity:        severity,
			})
		}
	}
	return findings, nil
}

// LoadTestScaffold reads the checkout's test-scaffold-report.json. Test
// scaffold priority levels map 1:1 to severity. Both the gaps and
// recommendations arrays are consumed. Returns (nil, nil) when the file does
// not exist.
func LoadTestScaffold(workdir string) ([]Finding, error) {
	f, err := openReport(workdir, layout.ReportTestScaffold)
	if f == nil || err != nil {
		return nil, err
	}
	defer f.Close()

	var report testScaffoldReport
	if err := json.NewDecoder(f).Decode(&report); err != nil {
		return nil, fmt.Errorf("test-scaffold-report.json: %w", err)
	}

	var findings []Finding
	for i, g := range report.Gaps {
		severity := g.Priority
		if severity == "" {
			severity = SeverityInfo
		}
		findings = append(findings, Finding{
			ID:              fmt.Sprintf("test-scaffold::gaps::%d", i),
			Title:           g.Title,
			Description:     g.Description,
			Recommendation:  g.Recommendation,
			Source:          "test-scaffold",
			SourceDimension: "gaps",
			Severity:        severity,
		})
	}
	for i, r := range report.Recommendations {
		severity := r.Priority
		if severity == "" {
			severity = SeverityInfo
		}
		findings = append(findings, Finding{
			ID:              fmt.Sprintf("test-scaffold::recommendations::%d", i),
			Title:           r.Title,
			Description:     r.Description,
			Recommendation:  r.Recommendation,
			Source:          "test-scaffold",
			SourceDimension: "recommendations",
			Severity:        severity,
		})
	}
	return findings, nil
}
