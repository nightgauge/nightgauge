// Package scan provides deterministic dependency-scan invocations across
// supported language ecosystems (Node.js, Python, Go, Rust). The DepScanResult
// JSON schema is stable — field names and types must not change after first
// merge. Skills parse `nightgauge scan deps --json` output; any breaking
// change requires incrementing the V field.
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DepScanResult is the stable JSON output schema for `nightgauge scan deps`.
// Schema version 1 — do not rename or remove fields after first merge.
type DepScanResult struct {
	V          int                       `json:"v"`          // schema version, always 1
	Workdir    string                    `json:"workdir"`    // absolute path that was scanned
	Ecosystems map[string]EcosystemEntry `json:"ecosystems"` // per-ecosystem result, keyed by name
	Totals     Totals                    `json:"totals"`     // aggregated counts across ecosystems
	Warnings   []string                  `json:"warnings"`   // non-fatal scan warnings
}

// EcosystemEntry captures the per-ecosystem audit + outdated outcome.
// Vulnerabilities is nil when the audit step did not run (tool unavailable,
// --include-vulns=false, or detected=false).
type EcosystemEntry struct {
	Detected        bool       `json:"detected"`        // ecosystem files present in workdir
	Available       bool       `json:"available"`       // required CLI tool present on PATH
	Vulnerabilities *VulnCount `json:"vulnerabilities"` // nil when audit did not run
	Outdated        int        `json:"outdated"`        // count of outdated packages reported by tool
	Errors          []string   `json:"errors"`          // per-ecosystem warnings; never causes scan failure
}

// VulnCount holds per-severity vulnerability counts. Severities below moderate
// (info/none) are folded into Low — keeps the schema simple while still letting
// skills reason about the four canonical severities.
type VulnCount struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Moderate int `json:"moderate"`
	Low      int `json:"low"`
}

// Totals is the aggregate of all ecosystems' VulnCount + outdated values.
type Totals struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Moderate int `json:"moderate"`
	Low      int `json:"low"`
	Outdated int `json:"outdated"`
}

// Options controls a single scan run.
type Options struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
	// Ecosystems narrows the scan to the named subset. When empty, all
	// supported ecosystems are auto-detected from workdir contents.
	Ecosystems []string
	// IncludeVulns toggles the audit (vulnerability) step. When false, only
	// the per-ecosystem outdated step runs. Defaults to true at the CLI layer.
	IncludeVulns bool
	// Runner is the command runner used by the scanner. When nil, an
	// os/exec-backed runner is used. Tests inject canned outputs.
	Runner CommandRunner
}

// CommandRunner abstracts the execution of external CLI tools so unit tests can
// inject canned outputs without a real `npm`/`pip-audit`/`govulncheck`/`cargo`
// install. Implementations MUST capture stdout regardless of exit code — many
// audit tools (notably `npm audit`) exit non-zero when vulnerabilities are
// found, which is informational, not a failure.
type CommandRunner interface {
	// LookPath returns nil when name is reachable on PATH.
	LookPath(name string) error
	// Run executes name with args and returns (stdout, exitCode, err).
	// stdout is captured regardless of exit code. err is non-nil only for
	// I/O-level failures (process could not be started); non-zero exit codes
	// alone never produce an err.
	Run(ctx context.Context, dir, name string, args ...string) (stdout []byte, exitCode int, err error)
}

// supportedEcosystems is the canonical list of ecosystem keys this scanner
// understands. Order is the iteration order used for deterministic output.
var supportedEcosystems = []string{"nodejs", "python", "go", "rust"}

// RunDepScan executes a dependency scan and returns the structured result.
// The function never returns a non-nil error for tool-availability or audit
// outcome issues — those are recorded inside the ecosystem entries. err is
// reserved for hard input errors (invalid --ecosystems values) and unparseable
// internal state.
func RunDepScan(ctx context.Context, opts Options) (*DepScanResult, error) {
	workdir := opts.Workdir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	workdir = abs

	requested, err := resolveRequestedEcosystems(opts.Ecosystems)
	if err != nil {
		return nil, err
	}

	runner := opts.Runner
	if runner == nil {
		runner = execRunner{}
	}

	includeVulns := opts.IncludeVulns

	result := &DepScanResult{
		V:          1,
		Workdir:    workdir,
		Ecosystems: make(map[string]EcosystemEntry, len(supportedEcosystems)),
		Warnings:   []string{},
	}

	// Pre-populate every supported ecosystem so JSON consumers can pin to a
	// known shape regardless of which were requested or detected.
	for _, name := range supportedEcosystems {
		result.Ecosystems[name] = EcosystemEntry{Errors: []string{}}
	}

	for _, name := range supportedEcosystems {
		if !requested[name] {
			continue
		}
		entry := scanEcosystem(ctx, name, workdir, runner, includeVulns)
		result.Ecosystems[name] = entry
	}

	result.Totals = aggregateTotals(result.Ecosystems)

	return result, nil
}

// resolveRequestedEcosystems validates the --ecosystems input and returns the
// set of ecosystem keys to scan. An empty slice means "all supported".
func resolveRequestedEcosystems(requested []string) (map[string]bool, error) {
	out := make(map[string]bool, len(supportedEcosystems))
	if len(requested) == 0 {
		for _, name := range supportedEcosystems {
			out[name] = true
		}
		return out, nil
	}
	known := make(map[string]bool, len(supportedEcosystems))
	for _, name := range supportedEcosystems {
		known[name] = true
	}
	for _, raw := range requested {
		name := strings.TrimSpace(strings.ToLower(raw))
		if !known[name] {
			return nil, fmt.Errorf("unknown ecosystem %q (supported: %s)", raw, strings.Join(supportedEcosystems, ", "))
		}
		out[name] = true
	}
	return out, nil
}

// scanEcosystem runs the per-ecosystem detection + audit + outdated pipeline
// and returns the populated entry. It never panics or returns an error — every
// failure mode is recorded as a string in entry.Errors.
func scanEcosystem(ctx context.Context, name, workdir string, runner CommandRunner, includeVulns bool) EcosystemEntry {
	entry := EcosystemEntry{Errors: []string{}}
	switch name {
	case "nodejs":
		scanNodejs(ctx, workdir, runner, includeVulns, &entry)
	case "python":
		scanPython(ctx, workdir, runner, includeVulns, &entry)
	case "go":
		scanGo(ctx, workdir, runner, includeVulns, &entry)
	case "rust":
		scanRust(ctx, workdir, runner, includeVulns, &entry)
	}
	return entry
}

// --- Node.js ---

func scanNodejs(ctx context.Context, workdir string, runner CommandRunner, includeVulns bool, entry *EcosystemEntry) {
	entry.Detected = fileExists(workdir, "package.json")
	if !entry.Detected {
		return
	}
	if err := runner.LookPath("npm"); err != nil {
		entry.Errors = append(entry.Errors, "npm not on PATH")
		return
	}
	entry.Available = true

	if includeVulns {
		stdout, _, err := runner.Run(ctx, workdir, "npm", "audit", "--json")
		if err != nil {
			entry.Errors = append(entry.Errors, fmt.Sprintf("npm audit failed: %v", err))
		} else if vc, perr := parseNpmAudit(stdout); perr != nil {
			entry.Errors = append(entry.Errors, fmt.Sprintf("parse npm audit: %v", perr))
		} else {
			entry.Vulnerabilities = vc
		}
	}

	stdout, _, err := runner.Run(ctx, workdir, "npm", "outdated", "--json")
	if err != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("npm outdated failed: %v", err))
		return
	}
	count, perr := parseNpmOutdated(stdout)
	if perr != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("parse npm outdated: %v", perr))
		return
	}
	entry.Outdated = count
}

// parseNpmAudit handles both the npm v7+ schema (top-level "vulnerabilities"
// keyed by package, each with a "severity") and the legacy v6 schema
// (top-level "metadata.vulnerabilities" with severity counts). Empty stdout
// is treated as "no vulnerabilities" — npm audit emits empty output when the
// project has no dependencies.
func parseNpmAudit(stdout []byte) (*VulnCount, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return &VulnCount{}, nil
	}
	// npm v7+
	var v7 struct {
		Vulnerabilities map[string]struct {
			Severity string `json:"severity"`
		} `json:"vulnerabilities"`
		Metadata struct {
			Vulnerabilities map[string]int `json:"vulnerabilities"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(stdout, &v7); err != nil {
		return nil, err
	}
	vc := &VulnCount{}
	if len(v7.Vulnerabilities) > 0 {
		for _, v := range v7.Vulnerabilities {
			addSeverity(vc, v.Severity)
		}
		return vc, nil
	}
	// legacy v6 fallback
	for sev, count := range v7.Metadata.Vulnerabilities {
		for i := 0; i < count; i++ {
			addSeverity(vc, sev)
		}
	}
	return vc, nil
}

// parseNpmOutdated counts entries in `npm outdated --json` output. npm exits
// non-zero (1) when packages are outdated, but stdout is the same JSON object
// regardless. Empty stdout means no outdated packages.
func parseNpmOutdated(stdout []byte) (int, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return 0, nil
	}
	var m map[string]any
	if err := json.Unmarshal(stdout, &m); err != nil {
		return 0, err
	}
	return len(m), nil
}

// --- Python ---

func scanPython(ctx context.Context, workdir string, runner CommandRunner, includeVulns bool, entry *EcosystemEntry) {
	entry.Detected = fileExists(workdir, "requirements.txt") ||
		fileExists(workdir, "pyproject.toml") ||
		fileExists(workdir, "setup.py")
	if !entry.Detected {
		return
	}

	auditAvailable := runner.LookPath("pip-audit") == nil
	pipAvailable := runner.LookPath("pip") == nil
	entry.Available = auditAvailable || pipAvailable

	if includeVulns {
		if !auditAvailable {
			entry.Errors = append(entry.Errors, "pip-audit not on PATH")
		} else {
			stdout, _, err := runner.Run(ctx, workdir, "pip-audit", "--format", "json")
			if err != nil {
				entry.Errors = append(entry.Errors, fmt.Sprintf("pip-audit failed: %v", err))
			} else if vc, perr := parsePipAudit(stdout); perr != nil {
				entry.Errors = append(entry.Errors, fmt.Sprintf("parse pip-audit: %v", perr))
			} else {
				entry.Vulnerabilities = vc
			}
		}
	}

	if !pipAvailable {
		entry.Errors = append(entry.Errors, "pip not on PATH")
		return
	}
	stdout, _, err := runner.Run(ctx, workdir, "pip", "list", "--outdated", "--format", "json")
	if err != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("pip list --outdated failed: %v", err))
		return
	}
	count, perr := parsePipOutdated(stdout)
	if perr != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("parse pip outdated: %v", perr))
		return
	}
	entry.Outdated = count
}

// parsePipAudit reads pip-audit's JSON output. The schema is
// `{ "dependencies": [ { "name": ..., "vulns": [ { "id": ..., "fix_versions": [...] } ] } ] }`.
// Severity is not always present; pip-audit emits `aliases` and the GHSA db is
// the source of truth, but the JSON output does not include a severity field
// directly. We fold every reported vuln into Moderate to surface a count
// without misclassifying severity. Skills that need real severity should run
// `pip-audit --format cyclonedx-json` separately — that's out of scope for v1.
func parsePipAudit(stdout []byte) (*VulnCount, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return &VulnCount{}, nil
	}
	var doc struct {
		Dependencies []struct {
			Vulns []struct {
				ID string `json:"id"`
			} `json:"vulns"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return nil, err
	}
	vc := &VulnCount{}
	for _, d := range doc.Dependencies {
		vc.Moderate += len(d.Vulns)
	}
	return vc, nil
}

// parsePipOutdated counts entries in `pip list --outdated --format json` — a
// JSON array of objects.
func parsePipOutdated(stdout []byte) (int, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return 0, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(stdout, &arr); err != nil {
		return 0, err
	}
	return len(arr), nil
}

// --- Go ---

func scanGo(ctx context.Context, workdir string, runner CommandRunner, includeVulns bool, entry *EcosystemEntry) {
	entry.Detected = fileExists(workdir, "go.mod")
	if !entry.Detected {
		return
	}

	vulnAvailable := runner.LookPath("govulncheck") == nil
	goAvailable := runner.LookPath("go") == nil
	entry.Available = vulnAvailable || goAvailable

	if includeVulns {
		if !vulnAvailable {
			entry.Errors = append(entry.Errors, "govulncheck not on PATH")
		} else {
			stdout, _, err := runner.Run(ctx, workdir, "govulncheck", "-json", "./...")
			if err != nil {
				entry.Errors = append(entry.Errors, fmt.Sprintf("govulncheck failed: %v", err))
			} else if vc, perr := parseGovulncheck(stdout); perr != nil {
				entry.Errors = append(entry.Errors, fmt.Sprintf("parse govulncheck: %v", perr))
			} else {
				entry.Vulnerabilities = vc
			}
		}
	}

	if !goAvailable {
		entry.Errors = append(entry.Errors, "go not on PATH")
		return
	}
	stdout, _, err := runner.Run(ctx, workdir, "go", "list", "-m", "-u", "-json", "all")
	if err != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("go list -m -u failed: %v", err))
		return
	}
	count, perr := parseGoOutdated(stdout)
	if perr != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("parse go list: %v", perr))
		return
	}
	entry.Outdated = count
}

// parseGovulncheck reads govulncheck's NDJSON output. Each line is a JSON
// object; vulnerability records have an "osv" key containing severity-bearing
// affected ranges. govulncheck does not emit a top-level severity, so each
// reported finding is folded into High (matches the typical govulncheck use
// case where any reachable CVE is treated as serious).
func parseGovulncheck(stdout []byte) (*VulnCount, error) {
	vc := &VulnCount{}
	if len(stripWhitespace(stdout)) == 0 {
		return vc, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return vc, err
		}
		if _, ok := msg["finding"]; ok {
			vc.High++
		}
	}
	return vc, nil
}

// parseGoOutdated counts modules where Update is populated. `go list -m -u
// -json all` emits a stream of JSON objects (one per module); modules with
// available updates have an "Update" object.
func parseGoOutdated(stdout []byte) (int, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return 0, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	count := 0
	for {
		var mod struct {
			Update *struct {
				Version string `json:"Version"`
			} `json:"Update"`
		}
		if err := dec.Decode(&mod); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return count, err
		}
		if mod.Update != nil && mod.Update.Version != "" {
			count++
		}
	}
	return count, nil
}

// --- Rust ---

func scanRust(ctx context.Context, workdir string, runner CommandRunner, includeVulns bool, entry *EcosystemEntry) {
	entry.Detected = fileExists(workdir, "Cargo.toml")
	if !entry.Detected {
		return
	}

	auditAvailable := runner.LookPath("cargo-audit") == nil
	outdatedAvailable := runner.LookPath("cargo-outdated") == nil
	entry.Available = auditAvailable || outdatedAvailable

	if includeVulns {
		if !auditAvailable {
			entry.Errors = append(entry.Errors, "cargo-audit not on PATH")
		} else {
			stdout, _, err := runner.Run(ctx, workdir, "cargo", "audit", "--json")
			if err != nil {
				entry.Errors = append(entry.Errors, fmt.Sprintf("cargo audit failed: %v", err))
			} else if vc, perr := parseCargoAudit(stdout); perr != nil {
				entry.Errors = append(entry.Errors, fmt.Sprintf("parse cargo audit: %v", perr))
			} else {
				entry.Vulnerabilities = vc
			}
		}
	}

	if !outdatedAvailable {
		entry.Errors = append(entry.Errors, "cargo-outdated not on PATH")
		return
	}
	stdout, _, err := runner.Run(ctx, workdir, "cargo", "outdated", "--format", "json")
	if err != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("cargo outdated failed: %v", err))
		return
	}
	count, perr := parseCargoOutdated(stdout)
	if perr != nil {
		entry.Errors = append(entry.Errors, fmt.Sprintf("parse cargo outdated: %v", perr))
		return
	}
	entry.Outdated = count
}

// parseCargoAudit reads `cargo audit --json` output. Schema:
// `{ "vulnerabilities": { "list": [ { "advisory": { "severity": "..." } } ] } }`.
// Severity is optional; missing severities fold into Moderate.
func parseCargoAudit(stdout []byte) (*VulnCount, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return &VulnCount{}, nil
	}
	var doc struct {
		Vulnerabilities struct {
			List []struct {
				Advisory struct {
					Severity string `json:"severity"`
				} `json:"advisory"`
			} `json:"list"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return nil, err
	}
	vc := &VulnCount{}
	for _, v := range doc.Vulnerabilities.List {
		sev := v.Advisory.Severity
		if sev == "" {
			sev = "moderate"
		}
		addSeverity(vc, sev)
	}
	return vc, nil
}

// parseCargoOutdated counts entries in `cargo outdated --format json` output.
// Schema: `{ "dependencies": [ { "name": ..., "project": ..., "latest": ... } ] }`.
// Entries where project == latest are not reported by cargo-outdated, so the
// raw list length is the outdated count.
func parseCargoOutdated(stdout []byte) (int, error) {
	if len(stripWhitespace(stdout)) == 0 {
		return 0, nil
	}
	var doc struct {
		Dependencies []map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return 0, err
	}
	return len(doc.Dependencies), nil
}

// --- helpers ---

// addSeverity increments the appropriate severity bucket. Unknown severities
// fold into Low to keep the schema closed.
func addSeverity(vc *VulnCount, severity string) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		vc.Critical++
	case "high":
		vc.High++
	case "moderate", "medium":
		vc.Moderate++
	case "low", "info", "informational", "none", "":
		vc.Low++
	default:
		vc.Low++
	}
}

// aggregateTotals sums per-ecosystem counts. nil Vulnerabilities contribute 0.
func aggregateTotals(eco map[string]EcosystemEntry) Totals {
	t := Totals{}
	for _, e := range eco {
		if e.Vulnerabilities != nil {
			t.Critical += e.Vulnerabilities.Critical
			t.High += e.Vulnerabilities.High
			t.Moderate += e.Vulnerabilities.Moderate
			t.Low += e.Vulnerabilities.Low
		}
		t.Outdated += e.Outdated
	}
	return t
}

func fileExists(workdir, name string) bool {
	_, err := os.Stat(filepath.Join(workdir, name))
	return err == nil
}

func stripWhitespace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// --- exec runner (production wiring) ---

type execRunner struct{}

func (execRunner) LookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// exec.Cmd.Output() returns stdout bytes even when the process exits
	// non-zero (the stdout buffer is populated regardless). Audit tools like
	// `npm audit` and `npm outdated` exit non-zero by design when findings
	// exist, so a non-zero exit is informational here, not a failure.
	stdout, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout, ee.ExitCode(), nil
		}
		return nil, -1, err
	}
	return stdout, 0, nil
}
