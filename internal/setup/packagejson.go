package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// packageJSON is the minimal subset of package.json fields the setup verb
// needs. Only fields used by template emission decisions live here — adding
// new fields requires bumping the schema V on the public Result type.
type packageJSON struct {
	Engines struct {
		Node string `json:"node"`
	} `json:"engines"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// defaultNodeVersion is used when package.json lacks an engines.node entry or
// the entry contains no parsable major version. Matches the SKILL.md heredoc
// fallback (`echo "20"`).
const defaultNodeVersion = "20"

// nodeVersionRe captures the first integer in an engines.node specifier such
// as "^20", ">=18.0.0", "20.x", or "node@20". Mirrors the bash version's
// `(\d+)` regex so detection stays byte-identical.
var nodeVersionRe = regexp.MustCompile(`(\d+)`)

// readPackageJSON loads package.json under workdir and returns the detection
// summary. Missing or malformed files are non-fatal — they yield a
// DetectedDeps with PackageJSONFound=false (or true with defaults) plus a
// warning string. Hard errors only for unreadable files we cannot recover
// from.
func readPackageJSON(workdir string) (DetectedDeps, []string, error) {
	det := DetectedDeps{NodeVersion: defaultNodeVersion}
	warnings := []string{}

	path := filepath.Join(workdir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return det, warnings, nil
		}
		return det, warnings, fmt.Errorf("read package.json: %w", err)
	}
	det.PackageJSONFound = true

	var pkg packageJSON
	if jerr := json.Unmarshal(data, &pkg); jerr != nil {
		warnings = append(warnings, fmt.Sprintf("package.json malformed (%v) — using defaults", jerr))
		return det, warnings, nil
	}

	if pkg.Engines.Node != "" {
		if m := nodeVersionRe.FindString(pkg.Engines.Node); m != "" {
			det.NodeVersion = m
		}
	}

	if hasDep(pkg, "typescript") {
		det.HasTypeScript = true
	}
	if hasDep(pkg, "vitest") {
		det.HasVitest = true
	}
	if hasDep(pkg, "eslint") {
		det.HasESLint = true
	}
	if hasDep(pkg, "prettier") {
		det.HasPrettier = true
	}

	return det, warnings, nil
}

// hasDep reports whether name is present in either dependencies or
// devDependencies. Mirrors the {...p.devDependencies, ...p.dependencies}
// shape from the SKILL.md `node -e` probes.
func hasDep(pkg packageJSON, name string) bool {
	if _, ok := pkg.DevDependencies[name]; ok {
		return true
	}
	if _, ok := pkg.Dependencies[name]; ok {
		return true
	}
	return false
}
