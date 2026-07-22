// Package audit provides cross-repo API endpoint extraction and alignment checking.
package audit

// CanonicalEndpoint represents a platform-defined API route.
type CanonicalEndpoint struct {
	Path       string
	Method     string
	AuthType   string // "bearer", "api_key", "none", "unknown"
	SourceFile string
	LineNumber int
}

// ClientEndpoint represents an HTTP call made by a client (Angular or Flutter).
type ClientEndpoint struct {
	Client      string // "angular" | "flutter"
	Path        string
	Method      string
	AuthType    string // "bearer", "api_key", "none", "unknown"
	SourceFile  string
	LineNumber  int
	Approximate bool // true if URL contained template literals that were partially resolved
}

// Finding represents a single alignment mismatch or informational note.
type Finding struct {
	Category          string  `json:"category"` // PATH_MISMATCH, METHOD_MISMATCH, AUTH_MISMATCH, NOT_FOUND, DIRECT_GITHUB_CALL
	Severity          string  `json:"severity"` // high, medium, low, info
	Client            *string `json:"client"`   // "angular" | "flutter" | null
	DetectedEndpoint  string  `json:"detected_endpoint"`
	DetectedMethod    string  `json:"detected_method"`
	DetectedAuth      string  `json:"detected_auth,omitempty"`
	CanonicalEndpoint string  `json:"canonical_endpoint,omitempty"`
	CanonicalMethod   string  `json:"canonical_method,omitempty"`
	CanonicalAuth     string  `json:"canonical_auth,omitempty"`
	SourceFile        string  `json:"source_file"`
	LineNumber        *int    `json:"line_number,omitempty"`
	Detail            string  `json:"detail"`
	Suggestion        string  `json:"suggestion,omitempty"`
	Approximate       bool    `json:"approximate,omitempty"`
}

// AuditedRepos tracks which repos were successfully scanned.
type AuditedRepos struct {
	Angular  bool `json:"angular"`
	Flutter  bool `json:"flutter"`
	Platform bool `json:"platform"`
}

// Summary holds aggregate counts.
type Summary struct {
	TotalFindings int            `json:"total_findings"`
	ByCategory    map[string]int `json:"by_category"`
	BySeverity    map[string]int `json:"by_severity"`
	ByClient      struct {
		Angular int `json:"angular"`
		Flutter int `json:"flutter"`
	} `json:"by_client"`
}

// ApiAlignmentReport is the top-level output for Dimension 1 of the product audit.
type ApiAlignmentReport struct {
	Dimension    string       `json:"dimension"`
	Timestamp    string       `json:"timestamp"`
	AuditedRepos AuditedRepos `json:"audited_repos"`
	Findings     []Finding    `json:"findings"`
	Summary      Summary      `json:"summary"`
}
