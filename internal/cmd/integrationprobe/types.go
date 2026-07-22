// Package integrationprobe implements the deterministic platform-API probe
// behind `nightgauge integration probe-platform`. It absorbs the
// curl-loop previously inlined in
// skills/nightgauge-integration-audit/SKILL.md Phase 2 (lines 64–108)
// so that skill stops paying LLM tokens for shell work and Phase 4 gap
// analysis can ingest structured JSON instead of re-parsing prose.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B32.
package integrationprobe

// Category names emitted in ProbeResult.Category and ProbeReport.Categories.
const (
	CategoryWorking      = "WORKING"
	CategoryAuthRequired = "AUTH_REQUIRED"
	CategoryAuthMismatch = "AUTH_MISMATCH"
	CategoryNotFound     = "NOT_FOUND"
	CategoryBroken       = "BROKEN"
	CategoryStub         = "STUB"
)

// AllCategories lists every category in stable display order.
var AllCategories = []string{
	CategoryWorking,
	CategoryStub,
	CategoryAuthRequired,
	CategoryAuthMismatch,
	CategoryNotFound,
	CategoryBroken,
}

// AuthMode names accepted by --auth-mode.
const (
	AuthModeJWT     = "jwt"
	AuthModeLicense = "license"
	AuthModeNone    = "none"
)

// EndpointEntry is a single endpoint to probe.
type EndpointEntry struct {
	Method   string `yaml:"method" json:"method"`
	Path     string `yaml:"path" json:"path"`
	AuthMode string `yaml:"auth_mode,omitempty" json:"auth_mode,omitempty"`
}

// EndpointManifest is the YAML document describing endpoints to probe,
// grouped by category label (AUTH, PIPELINES, QUEUE, GITHUB, TEAM, ANALYTICS,
// ADMIN, HEALTH, ...).
type EndpointManifest struct {
	Version int                        `yaml:"version" json:"version"`
	Groups  map[string][]EndpointEntry `yaml:"groups" json:"groups"`
}

// ProbeResult captures the outcome of a single endpoint probe.
type ProbeResult struct {
	Group        string `json:"group"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	ResolvedPath string `json:"resolved_path"`
	StatusCode   int    `json:"status_code"`
	Category     string `json:"category"`
	BodyPreview  string `json:"body_preview,omitempty"`
	DurationMs   int    `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

// ProbeReport is the top-level JSON output for `integration probe-platform --json`.
type ProbeReport struct {
	V           int            `json:"v"` // schema version, currently 1
	BaseURL     string         `json:"base_url"`
	AuthMode    string         `json:"auth_mode"`
	Categories  map[string]int `json:"categories"`
	Results     []ProbeResult  `json:"results"`
	Unreachable bool           `json:"unreachable"`
	GeneratedAt string         `json:"generated_at"`
}
