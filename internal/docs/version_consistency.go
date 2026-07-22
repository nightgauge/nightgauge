// Package docs provides deterministic markdown documentation operations.
// VersionConsistencyResult JSON schema is stable — field names and types must
// not change after first merge. Skills parse `nightgauge docs
// version-consistency --json` output; any breaking change requires
// incrementing the V field.
//
// The version-consistency verb replaces the bash project-type detection and
// version extraction prose in update-docs Phase 4.6 (audit row B36).
package docs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// VersionConsistencyResult is the stable JSON output schema for
// `nightgauge docs version-consistency`. Schema version 1 — do not
// rename or remove fields after first merge.
type VersionConsistencyResult struct {
	V               int               `json:"v"`                // schema version, always 1
	Root            string            `json:"root"`             // absolute path scanned
	ProjectType     string            `json:"project_type"`     // nodejs|python|go|rust|dotnet|skills|unknown
	SourceFile      string            `json:"source_file"`      // file the authoritative version was read from
	SourceVersion   string            `json:"source_version"`   // version string extracted from source file
	Mismatches      []VersionMismatch `json:"mismatches"`       // one entry per stale reference
	MismatchesCount int               `json:"mismatches_count"` // len(Mismatches)
	Warnings        []string          `json:"warnings"`         // non-fatal scan warnings
}

// VersionMismatch records a single outdated version reference in a scanned
// markdown file.
type VersionMismatch struct {
	File            string `json:"file"`             // path relative to Root
	Line            int    `json:"line"`             // 1-based line number
	Context         string `json:"context"`          // raw line text (trimmed)
	FoundVersion    string `json:"found_version"`    // version string found in the file
	ExpectedVersion string `json:"expected_version"` // authoritative version
}

// VersionConsistencyOptions controls a single version-consistency run.
type VersionConsistencyOptions struct {
	// Root is the directory tree to scan. When empty, the caller's CWD is used.
	Root string
}

// semverLikeRe matches bare semver-like strings (1.2.3 or 1.2 or just 1).
var semverLikeRe = regexp.MustCompile(`\b(\d+\.\d+(?:\.\d+)?(?:-[A-Za-z0-9.]+)?)\b`)

// versionKeywordRe matches lines that reference a version keyword followed by
// an optional separator, then a semver string. Used to locate version mentions
// in prose markdown without false-positives from arbitrary numbers.
var versionKeywordRe = regexp.MustCompile(`(?i)\bv(?:ersion)?\s*[=:@\s"'` + "`" + `]\s*` + `(\d+\.\d+(?:\.\d+)?(?:-[A-Za-z0-9.]+)?)`)

// fenceOpenRe matches the opening of a markdown code fence.
var fenceOpenRe = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})")

// VersionConsistency detects version mismatches across a project tree.
func VersionConsistency(_ context.Context, opts VersionConsistencyOptions) (*VersionConsistencyResult, error) {
	root := opts.Root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve cwd: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q: %w", root, err)
	}

	result := &VersionConsistencyResult{
		V:    1,
		Root: abs,
	}

	// Detect project type and extract source version.
	pt, sourceFile, sourceVer, warn := detectProjectType(abs)
	if warn != "" {
		result.Warnings = append(result.Warnings, warn)
	}
	result.ProjectType = pt
	result.SourceFile = sourceFile
	result.SourceVersion = sourceVer

	if sourceVer == "" {
		// No authoritative version found — nothing to compare against.
		return result, nil
	}

	// Walk markdown files and find stale version references.
	err = filepath.Walk(abs, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("walk error at %s: %v", path, werr))
			return nil
		}
		if info.IsDir() {
			if _, skip := versionSkipDirs[info.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		rel, _ := filepath.Rel(abs, path)
		// Skip the source file itself.
		if rel == result.SourceFile || filepath.Base(rel) == filepath.Base(result.SourceFile) {
			return nil
		}

		mismatches, w := scanMarkdownForVersionMismatches(path, rel, sourceVer)
		result.Warnings = append(result.Warnings, w...)
		result.Mismatches = append(result.Mismatches, mismatches...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %q: %w", abs, err)
	}

	result.MismatchesCount = len(result.Mismatches)
	return result, nil
}

// versionSkipDirs mirrors the skip list used across other docs commands.
var versionSkipDirs = map[string]struct{}{
	"node_modules":     {},
	".git":             {},
	"dist":             {},
	"build":            {},
	".nightgauge": {},
}

// detectProjectType determines the project type and returns:
//   - project type slug
//   - relative path of the source-of-truth file
//   - authoritative version string
//   - optional warning
func detectProjectType(root string) (pt, sourceFile, version, warning string) {
	// Ordered detection: npm > python > go > rust > dotnet > skills
	checks := []struct {
		marker  string
		pt      string
		extract func(path string) (string, string)
	}{
		{"package.json", "nodejs", extractNPMVersion},
		{"pyproject.toml", "python", extractTOMLVersion},
		{"setup.py", "python", extractSetupPyVersion},
		{"Cargo.toml", "rust", extractTOMLVersion},
		{"go.mod", "go", extractGoModVersion},
		{"VERSION", "go", extractPlainVersion},
	}

	for _, c := range checks {
		p := filepath.Join(root, c.marker)
		if _, err := os.Stat(p); err == nil {
			ver, warn := c.extract(p)
			return c.pt, c.marker, ver, warn
		}
	}

	// Skills directory check.
	if _, err := os.Stat(filepath.Join(root, "skills")); err == nil {
		ver, warn := extractSkillsVersion(root)
		return "skills", "skills/", ver, warn
	}

	// Check for any *.csproj.
	matches, _ := filepath.Glob(filepath.Join(root, "*.csproj"))
	if len(matches) > 0 {
		rel, _ := filepath.Rel(root, matches[0])
		ver, warn := extractCSProjVersion(matches[0])
		return "dotnet", rel, ver, warn
	}

	return "unknown", "", "", ""
}

// extractNPMVersion reads the "version" field from package.json.
func extractNPMVersion(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Sprintf("%s: could not read: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Sprintf("%s: invalid JSON: %v", path, err)
	}
	if v, ok := m["version"].(string); ok && v != "" {
		return v, ""
	}
	return "", ""
}

// extractTOMLVersion reads a `version = "x.y.z"` line from a TOML file.
func extractTOMLVersion(path string) (string, string) {
	return extractLineVersion(path, regexp.MustCompile(`(?i)^version\s*=\s*["']?(\d+\.\d+(?:\.\d+)?)["']?`))
}

// extractGoModVersion reads the `module` line — Go modules don't embed a
// version. Fall back to a `version = "x.y.z"` comment if present.
func extractGoModVersion(path string) (string, string) {
	ver, warn := extractLineVersion(path, regexp.MustCompile(`(?i)^//\s*version\s*[=:]\s*["']?(\d+\.\d+(?:\.\d+)?)["']?`))
	if ver != "" {
		return ver, warn
	}
	// No version embedded in go.mod — return empty (no mismatches possible).
	return "", ""
}

// extractPlainVersion reads the entire file, trims it, and validates it
// looks like a semver string.
func extractPlainVersion(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Sprintf("%s: could not read: %v", path, err)
	}
	v := strings.TrimSpace(string(data))
	if semverLikeRe.MatchString(v) {
		return v, ""
	}
	return "", ""
}

// extractSetupPyVersion reads `version='x.y.z'` or `version="x.y.z"` from
// setup.py.
func extractSetupPyVersion(path string) (string, string) {
	return extractLineVersion(path, regexp.MustCompile(`(?i)version\s*=\s*["'](\d+\.\d+(?:\.\d+)?)["']`))
}

// extractCSProjVersion reads `<Version>x.y.z</Version>` from a .csproj file.
func extractCSProjVersion(path string) (string, string) {
	return extractLineVersion(path, regexp.MustCompile(`(?i)<Version>(\d+\.\d+(?:\.\d+)?)</Version>`))
}

// extractSkillsVersion reads the version field from the first SKILL.md found
// under the skills/ directory.
func extractSkillsVersion(root string) (string, string) {
	skillsDir := filepath.Join(root, "skills")
	var found string
	_ = filepath.Walk(skillsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !info.IsDir() && info.Name() == "SKILL.md" {
			found = path
		}
		return nil
	})
	if found == "" {
		return "", ""
	}
	// SKILL.md frontmatter: `version: "x.y.z"` or `version: x.y.z`
	return extractLineVersion(found, regexp.MustCompile(`(?i)^\s*version\s*:\s*["']?(\d+\.\d+(?:\.\d+)?)["']?`))
}

// extractLineVersion opens path and returns the first capture group from re.
func extractLineVersion(path string, re *regexp.Regexp) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Sprintf("%s: could not read: %v", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := re.FindStringSubmatch(scanner.Text()); len(m) == 2 {
			return m[1], ""
		}
	}
	return "", ""
}

// scanMarkdownForVersionMismatches returns all lines in path that mention a
// version number different from sourceVer. Code fences are skipped.
func scanMarkdownForVersionMismatches(absPath, relPath, sourceVer string) ([]VersionMismatch, []string) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: could not read: %v", relPath, err)}
	}
	defer f.Close()

	var mismatches []VersionMismatch
	inFence := false
	lineNum := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if fenceOpenRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		// Extract all semver-like strings from version-keyword contexts.
		for _, m := range versionKeywordRe.FindAllStringSubmatch(line, -1) {
			found := m[1]
			if found == sourceVer {
				continue
			}
			// Exclude year-only values that look like years (e.g. 2026.1).
			if isYearLike(found) {
				continue
			}
			mismatches = append(mismatches, VersionMismatch{
				File:            relPath,
				Line:            lineNum,
				Context:         strings.TrimSpace(line),
				FoundVersion:    found,
				ExpectedVersion: sourceVer,
			})
		}
	}
	return mismatches, nil
}

// isYearLike returns true when the first numeric segment looks like a
// calendar year (>= 2000), heuristically avoiding false-positives on dates
// embedded in prose like "2026.1".
func isYearLike(v string) bool {
	parts := strings.SplitN(v, ".", 2)
	if n, err := strconv.Atoi(parts[0]); err == nil && n >= 2000 {
		return true
	}
	return false
}
