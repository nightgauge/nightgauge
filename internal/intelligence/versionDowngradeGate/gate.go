// Package versionDowngradeGate implements the version-downgrade preflight gate.
//
// It compares the working tree's tsconfig*.json and package.json files against
// a baseline (typically the merge base on main) and fails when any of the
// following moves to an older value:
//
//   - tsconfig compilerOptions.target (lexicographic on ES20xx)
//   - tsconfig compilerOptions.lib entries (lexicographic per entry)
//   - package.json dependencies / devDependencies / peerDependencies range
//     minimums (semver)
//   - package.json engines.node minimum (semver)
//
// The gate is bypassed when an `allow_downgrade` flag is set in the dev
// context or when the issue carries the configured bypass label.
//
// See Issue #3042.
package versionDowngradeGate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// EnforcementMode controls how the gate reacts when a downgrade is detected.
const (
	EnforcementWarn   = "warn"
	EnforcementStrict = "strict"
)

// Field name constants used in VersionDowngrade.Field for stable consumer
// parsing in auto-retro and CI/CD.
const (
	FieldTSTarget    = "compilerOptions.target"
	FieldTSLibPrefix = "compilerOptions.lib"
	FieldEnginesNode = "engines.node"
	FieldDepsPrefix  = "dependencies"
	FieldDevDeps     = "devDependencies"
	FieldPeerDeps    = "peerDependencies"
)

// GateConfig holds configuration for the version-downgrade gate.
type GateConfig struct {
	// Enabled is the master toggle. When false, Evaluate always returns Allowed=true.
	Enabled bool
	// EnforcementMode is "warn" (log only) or "strict" (block PR). Default: "warn".
	EnforcementMode string
	// BypassLabel is the label name that, when present on the issue, bypasses
	// the gate entirely. Default: "version:downgrade-allowed".
	BypassLabel string
}

// DefaultGateConfig returns a GateConfig with safe defaults: gate disabled,
// warn-mode enforcement, conventional bypass label.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		Enabled:         false,
		EnforcementMode: EnforcementWarn,
		BypassLabel:     "version:downgrade-allowed",
	}
}

// VersionDowngrade is one detected downgrade, populated for every offending
// field. Auto-retro consumes this struct directly — do not break the JSON
// shape without coordinating the consumer change.
type VersionDowngrade struct {
	File     string `json:"file"`
	Field    string `json:"field"`
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
}

// GateResult is the outcome of an evaluation.
type GateResult struct {
	Allowed         bool               `json:"allowed"`
	Bypassed        bool               `json:"bypassed,omitempty"`
	Reason          string             `json:"reason"`
	Downgrades      []VersionDowngrade `json:"downgrades,omitempty"`
	EnforcementMode string             `json:"enforcement_mode"`
	SuggestedAction string             `json:"suggested_action,omitempty"`
}

// FileSnapshot captures one side of a comparison: a single tsconfig*.json or
// package.json file's bytes plus its repo-relative path.
type FileSnapshot struct {
	Path  string
	Bytes []byte
}

// EvaluateInput bundles the baseline and current snapshots plus bypass inputs.
//
// Baseline and Current map repo-relative file paths to their bytes. Files that
// exist only in Current (newly added) are skipped — there is no baseline to
// compare against. Files that exist only in Baseline (deleted) are also
// skipped — file removal is out of scope for this gate.
type EvaluateInput struct {
	BaselineTSConfigs   map[string][]byte
	CurrentTSConfigs    map[string][]byte
	BaselinePackageJSON []byte
	CurrentPackageJSON  []byte
	PackageJSONPath     string
	IssueLabels         []string
	AllowDowngradeFlag  bool
}

// Evaluator is a gate runner.
type Evaluator struct {
	cfg GateConfig
}

// NewEvaluator constructs an Evaluator with the provided config.
func NewEvaluator(cfg GateConfig) *Evaluator {
	return &Evaluator{cfg: cfg}
}

// Evaluate compares baseline and current snapshots and returns a GateResult.
// The function is pure — no I/O, no clock, no env reads.
func (e *Evaluator) Evaluate(in EvaluateInput) *GateResult {
	mode := e.cfg.EnforcementMode
	if mode != EnforcementStrict {
		mode = EnforcementWarn
	}
	result := &GateResult{
		Allowed:         true,
		EnforcementMode: mode,
	}

	if !e.cfg.Enabled {
		result.Reason = "gate disabled in config"
		return result
	}

	// Bypass: explicit context flag from feature-dev.
	if in.AllowDowngradeFlag {
		result.Bypassed = true
		result.Reason = "bypassed via allow_downgrade context flag"
		return result
	}

	// Bypass: GitHub label on the issue.
	if e.cfg.BypassLabel != "" && hasLabel(in.IssueLabels, e.cfg.BypassLabel) {
		result.Bypassed = true
		result.Reason = fmt.Sprintf("bypass label %q present on issue", e.cfg.BypassLabel)
		return result
	}

	var downgrades []VersionDowngrade

	// tsconfig*.json comparisons.
	for _, path := range sortedKeys(in.CurrentTSConfigs) {
		baseBytes, ok := in.BaselineTSConfigs[path]
		if !ok {
			continue // newly added file — no baseline to compare against
		}
		downgrades = append(downgrades, compareTSConfig(path, baseBytes, in.CurrentTSConfigs[path])...)
	}

	// package.json comparison.
	if len(in.BaselinePackageJSON) > 0 && len(in.CurrentPackageJSON) > 0 {
		path := in.PackageJSONPath
		if path == "" {
			path = "package.json"
		}
		downgrades = append(downgrades, comparePackageJSON(path, in.BaselinePackageJSON, in.CurrentPackageJSON)...)
	}

	if len(downgrades) == 0 {
		result.Reason = "no version downgrades detected"
		return result
	}

	result.Downgrades = downgrades
	result.SuggestedAction = fmt.Sprintf(
		"Revert the downgrade(s) listed above, or apply the %q label to the issue if the downgrade is intentional.",
		e.cfg.BypassLabel,
	)

	if mode == EnforcementStrict {
		result.Allowed = false
		result.Reason = fmt.Sprintf("%d version downgrade(s) detected (strict mode blocks PR)", len(downgrades))
		return result
	}

	result.Reason = fmt.Sprintf("%d version downgrade(s) detected (warn mode — PR allowed)", len(downgrades))
	return result
}

// tsconfigDoc is the subset of a tsconfig.json we read. Only compilerOptions
// fields we actually compare are decoded; other keys are ignored.
type tsconfigDoc struct {
	CompilerOptions struct {
		Target string   `json:"target"`
		Lib    []string `json:"lib"`
	} `json:"compilerOptions"`
}

// compareTSConfig parses both byte slices as tsconfig.json and returns any
// detected downgrades. Parse failures on either side return no downgrades —
// a malformed tsconfig is a different problem and not this gate's domain.
func compareTSConfig(path string, oldBytes, newBytes []byte) []VersionDowngrade {
	var oldDoc, newDoc tsconfigDoc
	if err := decodeJSONLenient(oldBytes, &oldDoc); err != nil {
		return nil
	}
	if err := decodeJSONLenient(newBytes, &newDoc); err != nil {
		return nil
	}

	var out []VersionDowngrade

	// Target: lexicographic comparison on uppercased value (ES2020 < ES2021 < ESNext).
	if isTargetDowngrade(oldDoc.CompilerOptions.Target, newDoc.CompilerOptions.Target) {
		out = append(out, VersionDowngrade{
			File:     path,
			Field:    FieldTSTarget,
			OldValue: oldDoc.CompilerOptions.Target,
			NewValue: newDoc.CompilerOptions.Target,
		})
	}

	// Lib entries: an entry from the baseline that is missing or replaced by a
	// lexicographically-smaller entry counts as a downgrade. Dropping a
	// baseline entry without replacement also counts as a downgrade since the
	// project loses access to that lib's type declarations.
	for _, removed := range removedOrDowngradedLibEntries(oldDoc.CompilerOptions.Lib, newDoc.CompilerOptions.Lib) {
		out = append(out, VersionDowngrade{
			File:     path,
			Field:    FieldTSLibPrefix + "[" + removed.label + "]",
			OldValue: removed.oldValue,
			NewValue: removed.newValue,
		})
	}

	return out
}

// isTargetDowngrade reports whether new is strictly older than old. Empty values
// on either side are treated as "no comparison" and never count as a downgrade.
func isTargetDowngrade(oldVal, newVal string) bool {
	o := strings.ToUpper(strings.TrimSpace(oldVal))
	n := strings.ToUpper(strings.TrimSpace(newVal))
	if o == "" || n == "" {
		return false
	}
	if o == n {
		return false
	}
	// "ESNEXT" is always the newest; never a downgrade target.
	if n == "ESNEXT" {
		return false
	}
	if o == "ESNEXT" {
		return true
	}
	return n < o
}

// libDelta describes one lib-entry change for downgrade reporting.
type libDelta struct {
	label    string // index suffix, e.g. "es2021" or "removed:dom"
	oldValue string
	newValue string
}

// removedOrDowngradedLibEntries returns deltas for any baseline entry that is
// missing in the current set or replaced by a lexicographically-smaller entry
// of the same family (e.g. "es2022" → "es2021"). Newly-added entries are
// always allowed.
//
// The "family" of an entry is its non-numeric prefix (e.g. "es", "dom",
// "webworker"). Comparison is case-insensitive.
func removedOrDowngradedLibEntries(oldLib, newLib []string) []libDelta {
	if len(oldLib) == 0 {
		return nil
	}
	newSet := map[string]string{} // lower -> original
	for _, e := range newLib {
		newSet[strings.ToLower(strings.TrimSpace(e))] = e
	}

	// Group new entries by family for downgrade-within-family detection.
	newByFamily := map[string][]string{}
	for _, e := range newLib {
		fam := familyOf(e)
		newByFamily[fam] = append(newByFamily[fam], strings.ToLower(strings.TrimSpace(e)))
	}

	var out []libDelta
	seen := map[string]bool{}
	for _, oldEntry := range oldLib {
		key := strings.ToLower(strings.TrimSpace(oldEntry))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if _, present := newSet[key]; present {
			continue // unchanged
		}

		fam := familyOf(oldEntry)
		fams := newByFamily[fam]
		if len(fams) == 0 {
			// Entire family removed — downgrade.
			out = append(out, libDelta{
				label:    "removed:" + key,
				oldValue: oldEntry,
				newValue: "",
			})
			continue
		}
		// Family present but specific entry missing. If any new entry in the
		// family is lexicographically >= the removed one, treat as upgrade
		// (allowed). Otherwise downgrade.
		maxNew := ""
		for _, n := range fams {
			if n > maxNew {
				maxNew = n
			}
		}
		if maxNew >= key {
			continue
		}
		out = append(out, libDelta{
			label:    key,
			oldValue: oldEntry,
			newValue: maxNew,
		})
	}
	return out
}

// familyOf extracts the non-numeric prefix from a lib entry, lowercased.
// Examples: "ES2021" → "es", "DOM.Iterable" → "dom.iterable", "WebWorker" →
// "webworker", "ESNext" → "esnext".
func familyOf(entry string) string {
	s := strings.ToLower(strings.TrimSpace(entry))
	end := len(s)
	for i, r := range s {
		if r >= '0' && r <= '9' {
			end = i
			break
		}
	}
	return strings.TrimRight(s[:end], ".")
}

// packageJSONDoc is the subset of package.json the gate parses.
type packageJSONDoc struct {
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
	Engines          map[string]string `json:"engines"`
}

// comparePackageJSON returns any downgrades found between baseline and current
// package.json bytes.
func comparePackageJSON(path string, oldBytes, newBytes []byte) []VersionDowngrade {
	var oldDoc, newDoc packageJSONDoc
	if err := decodeJSONLenient(oldBytes, &oldDoc); err != nil {
		return nil
	}
	if err := decodeJSONLenient(newBytes, &newDoc); err != nil {
		return nil
	}

	var out []VersionDowngrade
	out = append(out, compareDepMap(path, FieldDepsPrefix, oldDoc.Dependencies, newDoc.Dependencies)...)
	out = append(out, compareDepMap(path, FieldDevDeps, oldDoc.DevDependencies, newDoc.DevDependencies)...)
	out = append(out, compareDepMap(path, FieldPeerDeps, oldDoc.PeerDependencies, newDoc.PeerDependencies)...)

	// engines.node — minimum version in the range.
	if oldNode := strings.TrimSpace(oldDoc.Engines["node"]); oldNode != "" {
		if newNode := strings.TrimSpace(newDoc.Engines["node"]); newNode != "" {
			if isRangeMinDowngrade(oldNode, newNode) {
				out = append(out, VersionDowngrade{
					File:     path,
					Field:    FieldEnginesNode,
					OldValue: oldNode,
					NewValue: newNode,
				})
			}
		}
	}

	return out
}

// compareDepMap returns downgrades for any dep present in both maps whose
// minimum-version moved backward. Newly-added or removed deps are not flagged.
func compareDepMap(path, kind string, oldDeps, newDeps map[string]string) []VersionDowngrade {
	if len(oldDeps) == 0 || len(newDeps) == 0 {
		return nil
	}
	var out []VersionDowngrade
	for _, name := range sortedStringKeys(oldDeps) {
		oldRange, hadOld := oldDeps[name]
		newRange, hadNew := newDeps[name]
		if !hadOld || !hadNew {
			continue
		}
		oldRange = strings.TrimSpace(oldRange)
		newRange = strings.TrimSpace(newRange)
		if oldRange == "" || newRange == "" || oldRange == newRange {
			continue
		}
		if isRangeMinDowngrade(oldRange, newRange) {
			out = append(out, VersionDowngrade{
				File:     path,
				Field:    fmt.Sprintf("%s.%s", kind, name),
				OldValue: oldRange,
				NewValue: newRange,
			})
		}
	}
	return out
}

// isRangeMinDowngrade reports whether the minimum version of newRange is
// strictly less than the minimum version of oldRange. Unparseable ranges on
// either side return false — the gate refuses to fail on what it cannot read.
func isRangeMinDowngrade(oldRange, newRange string) bool {
	oldMin, ok := minVersionFromRange(oldRange)
	if !ok {
		return false
	}
	newMin, ok := minVersionFromRange(newRange)
	if !ok {
		return false
	}
	return newMin.LessThan(oldMin)
}

// minVersionFromRange extracts the lower-bound version from an npm-style
// range expression. Supported forms (composite forms use the leftmost token):
//
//   - "1.2.3"       → 1.2.3
//   - "^1.2.3"      → 1.2.3
//   - "~1.2.3"      → 1.2.3
//   - ">=1.2.3"     → 1.2.3
//   - ">1.2.3"      → 1.2.3 (treated as the same lower bound; gate is
//     conservative and only fails on strict drops)
//   - ">=1.2.3 <2"  → 1.2.3
//   - "1.x" / "1.*" → 1.0.0
//   - "*", "x", "" → unparseable (returns false)
//
// The returned semver.Version is always stripped of prerelease/build metadata
// so comparisons compare on (Major, Minor, Patch) only.
func minVersionFromRange(rangeExpr string) (*semver.Version, bool) {
	r := strings.TrimSpace(rangeExpr)
	if r == "" || r == "*" || strings.EqualFold(r, "x") || strings.EqualFold(r, "latest") {
		return nil, false
	}
	// Handle workspace, file, npm:, git:, http: protocols — out of scope.
	if strings.ContainsAny(r, ":") && !strings.HasPrefix(r, ">=") && !strings.HasPrefix(r, ">") {
		return nil, false
	}

	// Hyphen ranges: "1.2.3 - 2.3.4" → leftmost.
	if strings.Contains(r, " - ") {
		r = strings.SplitN(r, " - ", 2)[0]
	}

	// Composite ranges separated by space or "||" — take the first non-upper
	// constraint.
	tokens := splitRangeTokens(r)
	for _, tok := range tokens {
		if v, ok := versionFromConstraint(tok); ok {
			return v, true
		}
	}
	return nil, false
}

// splitRangeTokens splits a range expression on "||" and whitespace.
func splitRangeTokens(r string) []string {
	r = strings.ReplaceAll(r, "||", " ")
	parts := strings.Fields(r)
	return parts
}

// versionFromConstraint parses a single constraint token into a version,
// stripping leading operators. Upper-bound-only tokens (<, <=) are skipped.
var operatorPrefix = regexp.MustCompile(`^(>=|<=|>|<|=|\^|~|v)`)

func versionFromConstraint(tok string) (*semver.Version, bool) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil, false
	}
	// Reject upper-bound operators outright.
	if strings.HasPrefix(tok, "<") {
		return nil, false
	}
	stripped := operatorPrefix.ReplaceAllString(tok, "")
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return nil, false
	}
	// Replace x/X wildcards with 0 so semver can parse.
	stripped = strings.ReplaceAll(stripped, ".x", ".0")
	stripped = strings.ReplaceAll(stripped, ".X", ".0")
	stripped = strings.ReplaceAll(stripped, ".*", ".0")
	v, err := semver.NewVersion(stripped)
	if err != nil {
		return nil, false
	}
	// Drop prerelease/build metadata so comparisons are on (Major, Minor, Patch).
	cleaned, err := semver.NewVersion(fmt.Sprintf("%d.%d.%d", v.Major(), v.Minor(), v.Patch()))
	if err != nil {
		return nil, false
	}
	return cleaned, true
}

// decodeJSONLenient decodes JSON allowing extra fields. tsconfigs occasionally
// contain // comments — those are not handled here; the working tree's
// tsconfig.json is expected to be standard JSON when the gate runs.
func decodeJSONLenient(data []byte, dst interface{}) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	return dec.Decode(dst)
}

// hasLabel reports whether target appears in labels (case-sensitive).
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// sortedKeys returns the keys of m in ascending order for deterministic output.
func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedStringKeys returns the keys of m in ascending order.
func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
