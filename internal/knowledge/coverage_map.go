package knowledge

import (
	"regexp"
	"strings"
	"time"
)

// CriteriaCoverage is one AC bullet's coverage result.
type CriteriaCoverage struct {
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
	Status   string   `json:"status"` // "covered" | "no_evidence"
}

// Violation is a detected decision constraint violation.
type Violation struct {
	Constraint     string   `json:"constraint"`
	ViolatingFiles []string `json:"violating_files"`
	Severity       string   `json:"severity"` // always "warn"
}

// CoverageMap is the output written to coverage-map-{N}.json.
type CoverageMap struct {
	Issue      int                `json:"issue"`
	Criteria   []CriteriaCoverage `json:"criteria"`
	Violations []Violation        `json:"violations"`
	CreatedAt  string             `json:"created_at"`
}

var tokenSplitRe = regexp.MustCompile(`[^a-z0-9]+`)

// TokenizeText lowercases and splits on non-alphanumeric chars.
// Filters tokens shorter than 3 characters.
func TokenizeText(text string) []string {
	lower := strings.ToLower(text)
	parts := tokenSplitRe.Split(lower, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 3 {
			out = append(out, p)
		}
	}
	return out
}

// TokenOverlap counts how many tokens from queryTokens appear in targetTokens.
func TokenOverlap(queryTokens, targetTokens []string) int {
	targetSet := make(map[string]struct{}, len(targetTokens))
	for _, t := range targetTokens {
		targetSet[t] = struct{}{}
	}
	count := 0
	for _, q := range queryTokens {
		if _, ok := targetSet[q]; ok {
			count++
		}
	}
	return count
}

// ParseACsFromPRD extracts bullet points from the "## Acceptance Criteria"
// section. Handles both "- [ ] text" and "- text" bullet formats.
// Returns the text after the checkbox marker (or plain bullet dash).
func ParseACsFromPRD(content string) []string {
	lines := strings.Split(content, "\n")
	inSection := false
	var acs []string

	checkboxRe := regexp.MustCompile(`^\s*-\s+\[[ xX]\]\s+(.+)`)
	bulletRe := regexp.MustCompile(`^\s*-\s+(.+)`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect section header.
		if strings.HasPrefix(trimmed, "## Acceptance Criteria") {
			inSection = true
			continue
		}
		// Stop at the next ## heading.
		if inSection && strings.HasPrefix(trimmed, "## ") {
			break
		}
		if !inSection {
			continue
		}

		// Try checkbox format first.
		if m := checkboxRe.FindStringSubmatch(line); m != nil {
			acs = append(acs, strings.TrimSpace(m[1]))
			continue
		}
		// Then plain bullet.
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			acs = append(acs, strings.TrimSpace(m[1]))
		}
	}
	return acs
}

// ParseDecisionConstraints extracts constraint text from decisions.md ADR
// blocks. Looks for lines after "**Decision:**" or "Decision:" headers within
// "## ADR-" sections. Returns constraint strings.
func ParseDecisionConstraints(content string) []string {
	lines := strings.Split(content, "\n")
	inADR := false
	afterDecision := false
	var constraints []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## ADR-") {
			inADR = true
			afterDecision = false
			continue
		}
		// New top-level section ends ADR block.
		if inADR && strings.HasPrefix(trimmed, "## ") && !strings.HasPrefix(trimmed, "## ADR-") {
			inADR = false
			afterDecision = false
			continue
		}
		if !inADR {
			continue
		}

		// Detect decision header lines.
		if trimmed == "**Decision:**" || trimmed == "Decision:" ||
			strings.HasPrefix(trimmed, "**Decision:**") || strings.HasPrefix(trimmed, "Decision:") {
			afterDecision = true
			// Inline text after the header.
			var inline string
			if strings.HasPrefix(trimmed, "**Decision:**") {
				inline = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Decision:**"))
			} else {
				inline = strings.TrimSpace(strings.TrimPrefix(trimmed, "Decision:"))
			}
			if inline != "" {
				constraints = append(constraints, inline)
				afterDecision = false // single-line form consumed
			}
			continue
		}
		// Collect the next non-empty line after the decision header.
		if afterDecision {
			if trimmed != "" {
				constraints = append(constraints, trimmed)
				afterDecision = false
			}
		}
	}
	return constraints
}

// antiPatternRe matches common direct HTTP call patterns in TS/JS files that
// should be routed through the Go IPC layer instead.
var antiPatternRe = regexp.MustCompile(`fetch\(|axios\.|XMLHttpRequest|http\.request`)

// ComputeCoverageMap computes AC coverage for the given issue.
//
// Parameters:
//   - issueNumber: used in output
//   - prdContent: full content of PRD.md
//   - decisionsContent: full content of decisions.md
//   - testNames: list of test describe/it names from diff
//   - changedFileContents: map from filepath to file content for diff analysis
func ComputeCoverageMap(
	issueNumber int,
	prdContent, decisionsContent string,
	testNames []string,
	changedFileContents map[string]string,
) *CoverageMap {
	acs := ParseACsFromPRD(prdContent)
	constraints := ParseDecisionConstraints(decisionsContent)

	// Pre-tokenize test names.
	testTokenSets := make([][]string, len(testNames))
	for i, t := range testNames {
		testTokenSets[i] = TokenizeText(t)
	}

	// Pre-tokenize changed file contents.
	type fileTokens struct {
		path   string
		tokens []string
	}
	fileTokenSets := make([]fileTokens, 0, len(changedFileContents))
	for path, content := range changedFileContents {
		fileTokenSets = append(fileTokenSets, fileTokens{
			path:   path,
			tokens: TokenizeText(content),
		})
	}

	criteria := make([]CriteriaCoverage, 0, len(acs))
	for _, ac := range acs {
		acTokens := TokenizeText(ac)
		var evidence []string

		// Check test name coverage (threshold: ≥2 token overlap).
		for i, ts := range testTokenSets {
			if TokenOverlap(acTokens, ts) >= 2 {
				evidence = append(evidence, testNames[i])
			}
		}

		// Check code coverage (threshold: ≥3 token overlap).
		for _, ft := range fileTokenSets {
			if TokenOverlap(acTokens, ft.tokens) >= 3 {
				evidence = append(evidence, ft.path)
			}
		}

		status := "no_evidence"
		if len(evidence) > 0 {
			status = "covered"
		}
		criteria = append(criteria, CriteriaCoverage{
			Text:     ac,
			Evidence: evidence,
			Status:   status,
		})
	}

	// Violation detection: scan TS/JS files for direct HTTP call anti-patterns
	// when decision constraints reference IPC or HTTP routing concerns.
	var violations []Violation
	for _, constraint := range constraints {
		constraintLower := strings.ToLower(constraint)
		// Only check when the constraint is about IPC / HTTP routing.
		if !strings.Contains(constraintLower, "ipc") &&
			!strings.Contains(constraintLower, "http") &&
			!strings.Contains(constraintLower, "fetch") &&
			!strings.Contains(constraintLower, "request") {
			continue
		}

		var violatingFiles []string
		for path, content := range changedFileContents {
			ext := strings.ToLower(path)
			if !strings.HasSuffix(ext, ".ts") && !strings.HasSuffix(ext, ".js") &&
				!strings.HasSuffix(ext, ".tsx") && !strings.HasSuffix(ext, ".jsx") {
				continue
			}
			if antiPatternRe.MatchString(content) {
				violatingFiles = append(violatingFiles, path)
			}
		}
		if len(violatingFiles) > 0 {
			violations = append(violations, Violation{
				Constraint:     constraint,
				ViolatingFiles: violatingFiles,
				Severity:       "warn",
			})
		}
	}

	return &CoverageMap{
		Issue:      issueNumber,
		Criteria:   criteria,
		Violations: violations,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}
