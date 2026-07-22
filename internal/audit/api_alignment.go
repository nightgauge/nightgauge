package audit

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ApiAlignmentService orchestrates extraction and alignment across repos.
type ApiAlignmentService struct {
	angularRepo  string
	flutterRepo  string
	platformRepo string
}

// NewApiAlignmentService creates a service with the given repo paths.
// Paths may be absolute or relative (caller's responsibility to resolve).
func NewApiAlignmentService(angularRepo, flutterRepo, platformRepo string) *ApiAlignmentService {
	return &ApiAlignmentService{
		angularRepo:  angularRepo,
		flutterRepo:  flutterRepo,
		platformRepo: platformRepo,
	}
}

// Run executes the full three-phase extraction + alignment and returns the report.
func (s *ApiAlignmentService) Run() (*ApiAlignmentReport, error) {
	report := &ApiAlignmentReport{
		Dimension: "api-alignment",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Phase 1: Extract canonical platform routes
	canonical, platformOK := ExtractPlatformRoutes(s.platformRepo)
	report.AuditedRepos.Platform = platformOK
	if !platformOK {
		fmt.Printf("WARNING: Platform repo not accessible at %s — skipping platform extraction\n", s.platformRepo)
	}

	// Phase 2: Extract Angular client endpoints
	angularEndpoints, angularOK := ExtractAngularEndpoints(s.angularRepo)
	report.AuditedRepos.Angular = angularOK
	if !angularOK {
		fmt.Printf("WARNING: Angular repo not accessible at %s — skipping Angular extraction\n", s.angularRepo)
	}

	// Phase 3a: Extract Flutter Dio endpoints
	flutterEndpoints, flutterOK := ExtractFlutterEndpoints(s.flutterRepo)
	// Phase 3b: Extract Flutter direct GitHub calls
	githubFindings, _ := ExtractFlutterDirectGitHubCalls(s.flutterRepo)
	report.AuditedRepos.Flutter = flutterOK || len(githubFindings) > 0

	if !flutterOK {
		fmt.Printf("WARNING: Flutter repo not accessible at %s — skipping Flutter extraction\n", s.flutterRepo)
	}

	// Build alignment findings
	var findings []Finding

	if platformOK {
		// Align Angular endpoints
		for _, ep := range angularEndpoints {
			fs := classifyEndpoint(ep, canonical)
			findings = append(findings, fs...)
		}
		// Align Flutter endpoints
		for _, ep := range flutterEndpoints {
			fs := classifyEndpoint(ep, canonical)
			findings = append(findings, fs...)
		}
	} else {
		// No platform routes — report all client endpoints as NOT_FOUND with note
		for _, ep := range angularEndpoints {
			findings = append(findings, notFoundFinding(ep, "Platform repo unavailable — cannot verify endpoint"))
		}
		for _, ep := range flutterEndpoints {
			findings = append(findings, notFoundFinding(ep, "Platform repo unavailable — cannot verify endpoint"))
		}
	}

	// Add direct GitHub call findings
	findings = append(findings, githubFindings...)

	report.Findings = findings
	report.Summary = buildSummary(findings)

	return report, nil
}

// classifyEndpoint compares a client endpoint against canonical routes and
// returns zero or more findings.
func classifyEndpoint(ep ClientEndpoint, canonical map[string]CanonicalEndpoint) []Finding {
	normalPath := NormalizePath(ep.Path)
	normalMethod := NormalizeMethod(ep.Method)

	// Exact match key
	exactKey := normalMethod + ":" + normalPath

	if canon, ok := canonical[exactKey]; ok {
		// Path and method match — check auth
		if authMismatch(ep.AuthType, canon.AuthType) {
			return []Finding{authMismatchFinding(ep, canon)}
		}
		return nil // perfect match
	}

	// Check if same path exists with different method
	for _, canon := range canonical {
		if wildcardPath(NormalizePath(canon.Path)) == wildcardPath(normalPath) {
			// Same path (wildcard-normalized), different method
			return []Finding{methodMismatchFinding(ep, canon)}
		}
	}

	// Check for path similarity (version prefix, singular/plural)
	for _, canon := range canonical {
		similar, detail := PathSimilarity(ep.Path, canon.Path)
		if similar {
			return []Finding{pathMismatchFinding(ep, canon, detail)}
		}
	}

	// No match found
	return []Finding{notFoundFinding(ep, fmt.Sprintf("Client calls %s %s but this endpoint does not exist in platform routes", normalMethod, ep.Path))}
}

func authMismatch(clientAuth, canonAuth string) bool {
	// Treat "unknown" as potentially mismatched — conservative
	if clientAuth == "unknown" || canonAuth == "unknown" {
		return false // can't determine, skip
	}
	// Only report mismatch when one requires auth and the other doesn't
	clientRequires := clientAuth == "bearer" || clientAuth == "api_key"
	canonRequires := canonAuth == "bearer" || canonAuth == "api_key"
	return clientRequires != canonRequires
}

func authMismatchFinding(ep ClientEndpoint, canon CanonicalEndpoint) Finding {
	client := ep.Client
	ln := ep.LineNumber
	return Finding{
		Category:          "AUTH_MISMATCH",
		Severity:          "high",
		Client:            &client,
		DetectedEndpoint:  ep.Path,
		DetectedMethod:    ep.Method,
		DetectedAuth:      ep.AuthType,
		CanonicalEndpoint: canon.Path,
		CanonicalMethod:   canon.Method,
		CanonicalAuth:     canon.AuthType,
		SourceFile:        ep.SourceFile,
		LineNumber:        &ln,
		Detail:            fmt.Sprintf("Client sends %s auth but platform requires %s", ep.AuthType, canon.AuthType),
		Suggestion:        "Update client to include the correct Authorization header",
		Approximate:       ep.Approximate,
	}
}

func methodMismatchFinding(ep ClientEndpoint, canon CanonicalEndpoint) Finding {
	client := ep.Client
	ln := ep.LineNumber
	return Finding{
		Category:          "METHOD_MISMATCH",
		Severity:          "high",
		Client:            &client,
		DetectedEndpoint:  ep.Path,
		DetectedMethod:    ep.Method,
		CanonicalEndpoint: canon.Path,
		CanonicalMethod:   canon.Method,
		CanonicalAuth:     canon.AuthType,
		SourceFile:        ep.SourceFile,
		LineNumber:        &ln,
		Detail:            fmt.Sprintf("Client uses %s but platform expects %s for %s", ep.Method, canon.Method, ep.Path),
		Suggestion:        fmt.Sprintf("Change client method from %s to %s", ep.Method, canon.Method),
		Approximate:       ep.Approximate,
	}
}

func pathMismatchFinding(ep ClientEndpoint, canon CanonicalEndpoint, detail string) Finding {
	client := ep.Client
	ln := ep.LineNumber
	d := fmt.Sprintf("Path mismatch: client uses %s, platform has %s", ep.Path, canon.Path)
	if detail != "" {
		d = d + " (" + detail + ")"
	}
	return Finding{
		Category:          "PATH_MISMATCH",
		Severity:          "medium",
		Client:            &client,
		DetectedEndpoint:  ep.Path,
		DetectedMethod:    ep.Method,
		DetectedAuth:      ep.AuthType,
		CanonicalEndpoint: canon.Path,
		CanonicalMethod:   canon.Method,
		CanonicalAuth:     canon.AuthType,
		SourceFile:        ep.SourceFile,
		LineNumber:        &ln,
		Detail:            d,
		Suggestion:        fmt.Sprintf("Update client endpoint from %s to %s", ep.Path, canon.Path),
		Approximate:       ep.Approximate,
	}
}

func notFoundFinding(ep ClientEndpoint, detail string) Finding {
	client := ep.Client
	ln := ep.LineNumber
	return Finding{
		Category:         "NOT_FOUND",
		Severity:         "medium",
		Client:           &client,
		DetectedEndpoint: ep.Path,
		DetectedMethod:   ep.Method,
		DetectedAuth:     ep.AuthType,
		SourceFile:       ep.SourceFile,
		LineNumber:       &ln,
		Detail:           detail,
		Suggestion:       "Verify endpoint exists in platform routes or remove the client call",
		Approximate:      ep.Approximate,
	}
}

func buildSummary(findings []Finding) Summary {
	s := Summary{
		TotalFindings: len(findings),
		ByCategory:    make(map[string]int),
		BySeverity:    make(map[string]int),
	}
	for _, f := range findings {
		s.ByCategory[f.Category]++
		s.BySeverity[f.Severity]++
		if f.Client != nil {
			switch *f.Client {
			case "angular":
				s.ByClient.Angular++
			case "flutter":
				s.ByClient.Flutter++
			}
		}
	}
	return s
}

// ResolveRepoPaths resolves default sibling repo paths relative to the given
// git root directory.
func ResolveRepoPaths(gitRoot, angularRepo, flutterRepo, platformRepo string) (string, string, string) {
	parent := filepath.Dir(gitRoot)

	if angularRepo == "" || angularRepo == "../acme-dashboard" {
		angularRepo = filepath.Join(parent, "acme-dashboard")
	} else if !filepath.IsAbs(angularRepo) {
		angularRepo = filepath.Join(gitRoot, angularRepo)
	}

	if flutterRepo == "" || flutterRepo == "../acme-mobile" {
		flutterRepo = filepath.Join(parent, "acme-mobile")
	} else if !filepath.IsAbs(flutterRepo) {
		flutterRepo = filepath.Join(gitRoot, flutterRepo)
	}

	if platformRepo == "" || platformRepo == "../acme-platform" {
		platformRepo = filepath.Join(parent, "acme-platform")
	} else if !filepath.IsAbs(platformRepo) {
		platformRepo = filepath.Join(gitRoot, platformRepo)
	}

	return angularRepo, flutterRepo, platformRepo
}

// FormatReport prints a human-readable summary of the report.
func FormatReport(report *ApiAlignmentReport) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("API Alignment Report — %s\n", report.Timestamp))
	sb.WriteString(fmt.Sprintf("Audited repos: angular=%v flutter=%v platform=%v\n",
		report.AuditedRepos.Angular, report.AuditedRepos.Flutter, report.AuditedRepos.Platform))
	sb.WriteString(fmt.Sprintf("Total findings: %d\n\n", report.Summary.TotalFindings))

	for _, f := range report.Findings {
		client := "unknown"
		if f.Client != nil {
			client = *f.Client
		}
		sb.WriteString(fmt.Sprintf("  [%s/%s] %s %s %s\n", f.Category, f.Severity, client, f.DetectedMethod, f.DetectedEndpoint))
		sb.WriteString(fmt.Sprintf("    %s\n", f.Detail))
		if f.Suggestion != "" {
			sb.WriteString(fmt.Sprintf("    → %s\n", f.Suggestion))
		}
	}

	sb.WriteString(fmt.Sprintf("\nBy category: %v\n", report.Summary.ByCategory))
	sb.WriteString(fmt.Sprintf("By severity: %v\n", report.Summary.BySeverity))
	return sb.String()
}
