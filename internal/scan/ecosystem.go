// Package scan also exposes the ecosystem-detection verb. The
// EcosystemScanResult JSON schema is stable — field names and types must not
// change after first merge. Skills parse `nightgauge scan ecosystem
// --json` output; any breaking change requires incrementing the V field.
package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EcosystemScanResult is the stable JSON output schema for
// `nightgauge scan ecosystem`. Schema version 1 — do not rename or remove
// fields after first merge.
type EcosystemScanResult struct {
	V            int               `json:"v"`             // schema version, always 1
	Workdir      string            `json:"workdir"`       // absolute path that was scanned
	Ecosystems   []string          `json:"ecosystems"`    // sorted list of detected ecosystem names
	IsMonorepo   bool              `json:"is_monorepo"`   // true if any workspace marker found
	MonorepoKind string            `json:"monorepo_kind"` // "" | nodejs-workspaces | cargo-workspace | go-workspace | mixed
	Packages     []string          `json:"packages"`      // workspace package paths relative to workdir, sorted
	Lockfile     string            `json:"lockfile"`      // lockfile of first detected ecosystem (alphabetical), or ""
	Lockfiles    map[string]string `json:"lockfiles"`     // per-ecosystem lockfile path; always populated for all 5 ecosystems
	Warnings     []string          `json:"warnings"`      // non-fatal scan warnings
}

// EcosystemOptions controls a single ecosystem-scan run.
type EcosystemOptions struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
}

// supportedEcosystemDetections is the canonical list of ecosystems the scanner
// understands. The slice ordering is the iteration order used for deterministic
// output; the public Ecosystems[] is sorted alphabetically before return.
var supportedEcosystemDetections = []string{"go", "java", "nodejs", "python", "rust"}

// ecosystemManifests lists the files that, if present in the workdir root,
// indicate the named ecosystem is in use. Any one match counts as detected.
var ecosystemManifests = map[string][]string{
	"nodejs": {"package.json"},
	"python": {"pyproject.toml", "setup.py", "requirements.txt"},
	"go":     {"go.mod"},
	"rust":   {"Cargo.toml"},
	"java":   {"pom.xml", "build.gradle", "build.gradle.kts"},
}

// ecosystemLockfilePriority lists candidate lockfiles in priority order — the
// first file present in workdir is reported as the lockfile for that
// ecosystem. java has no canonical single lockfile (Maven/Gradle), so it
// always reports an empty string.
var ecosystemLockfilePriority = map[string][]string{
	"nodejs": {"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"},
	"python": {"poetry.lock", "Pipfile.lock", "uv.lock", "requirements.txt"},
	"go":     {"go.sum"},
	"rust":   {"Cargo.lock"},
	"java":   nil,
}

// RunEcosystemScan executes an ecosystem detection scan and returns the
// structured result. The function never returns a non-nil error for malformed
// manifests or unparseable workspace declarations — those are recorded in
// Warnings. err is reserved for hard input errors (invalid workdir).
func RunEcosystemScan(_ context.Context, opts EcosystemOptions) (*EcosystemScanResult, error) {
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

	result := &EcosystemScanResult{
		V:          1,
		Workdir:    workdir,
		Ecosystems: []string{},
		Packages:   []string{},
		Lockfiles:  make(map[string]string, len(supportedEcosystemDetections)),
		Warnings:   []string{},
	}

	// Always populate every supported ecosystem's lockfile entry so JSON
	// consumers can pin to a known shape.
	for _, name := range supportedEcosystemDetections {
		result.Lockfiles[name] = ""
	}

	detected := detectEcosystems(workdir)
	result.Ecosystems = detected

	for _, name := range detected {
		if lf := pickLockfile(workdir, name); lf != "" {
			result.Lockfiles[name] = lf
		}
	}

	if len(detected) > 0 {
		// Convenience: lockfile of the first (alphabetical) detected ecosystem.
		result.Lockfile = result.Lockfiles[detected[0]]
	}

	kind, packages, warnings := detectMonorepo(workdir)
	result.IsMonorepo = kind != ""
	result.MonorepoKind = kind
	result.Packages = packages
	if len(warnings) > 0 {
		result.Warnings = append(result.Warnings, warnings...)
	}

	return result, nil
}

// detectEcosystems returns the alphabetically sorted list of ecosystems whose
// manifest files are present in workdir.
func detectEcosystems(workdir string) []string {
	out := []string{}
	for _, name := range supportedEcosystemDetections {
		for _, manifest := range ecosystemManifests[name] {
			if fileExists(workdir, manifest) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// pickLockfile returns the first lockfile from the ecosystem's priority list
// that exists in workdir, or "" if none.
func pickLockfile(workdir, ecosystem string) string {
	for _, lf := range ecosystemLockfilePriority[ecosystem] {
		if fileExists(workdir, lf) {
			return lf
		}
	}
	return ""
}

// detectMonorepo inspects workdir for workspace markers and returns the
// monorepo discriminator, the sorted list of workspace package paths, and any
// parse warnings encountered. Returns ("", nil, nil) when no markers are
// found.
func detectMonorepo(workdir string) (kind string, packages []string, warnings []string) {
	kinds := []string{}
	pkgSet := map[string]struct{}{}

	if fileExists(workdir, "package.json") {
		members, ws, warns := readNodejsWorkspaces(workdir)
		warnings = append(warnings, warns...)
		if ws {
			kinds = append(kinds, "nodejs-workspaces")
			for _, m := range members {
				pkgSet[m] = struct{}{}
			}
		}
	}

	if fileExists(workdir, "Cargo.toml") {
		members, ws, warns := readCargoWorkspace(workdir)
		warnings = append(warnings, warns...)
		if ws {
			kinds = append(kinds, "cargo-workspace")
			for _, m := range members {
				pkgSet[m] = struct{}{}
			}
		}
	}

	if fileExists(workdir, "go.work") {
		members, warns := readGoWorkUse(workdir)
		warnings = append(warnings, warns...)
		kinds = append(kinds, "go-workspace")
		for _, m := range members {
			pkgSet[m] = struct{}{}
		}
	}

	if len(kinds) == 0 {
		return "", []string{}, warnings
	}

	packages = make([]string, 0, len(pkgSet))
	for p := range pkgSet {
		packages = append(packages, p)
	}
	sort.Strings(packages)

	if len(kinds) == 1 {
		return kinds[0], packages, warnings
	}
	return "mixed", packages, warnings
}

// readNodejsWorkspaces parses package.json's `workspaces` field. Both the
// array form (`"workspaces": ["packages/*"]`) and the object form
// (`"workspaces": {"packages": ["packages/*"]}`) are supported. Glob entries
// are expanded against workdir; only directories that contain a package.json
// are included as workspace members.
func readNodejsWorkspaces(workdir string) (members []string, hasWorkspaces bool, warnings []string) {
	data, err := os.ReadFile(filepath.Join(workdir, "package.json"))
	if err != nil {
		return nil, false, []string{fmt.Sprintf("read package.json: %v", err)}
	}

	// Use json.RawMessage to differentiate array vs object form for workspaces.
	var top struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, false, []string{fmt.Sprintf("parse package.json: %v", err)}
	}
	if len(top.Workspaces) == 0 {
		return nil, false, nil
	}

	patterns := parseNodejsWorkspacePatterns(top.Workspaces)
	if patterns == nil {
		// Workspaces field present but unrecognized shape — record warning
		// and treat as no workspaces declared.
		return nil, false, []string{"package.json: unrecognized 'workspaces' shape"}
	}

	members = expandWorkspaceGlobs(workdir, patterns, "package.json")
	return members, true, warnings
}

// parseNodejsWorkspacePatterns extracts the glob patterns from either the
// array or object form of `workspaces`. Returns nil for an unrecognized
// shape.
func parseNodejsWorkspacePatterns(raw json.RawMessage) []string {
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Packages
	}
	return nil
}

// readCargoWorkspace parses Cargo.toml's `[workspace] members = [...]` table
// using a minimal line scanner — adding a TOML library dependency just for
// workspace detection is overkill, and Cargo's workspace block has a regular
// shape in practice.
func readCargoWorkspace(workdir string) (members []string, hasWorkspace bool, warnings []string) {
	data, err := os.ReadFile(filepath.Join(workdir, "Cargo.toml"))
	if err != nil {
		return nil, false, []string{fmt.Sprintf("read Cargo.toml: %v", err)}
	}

	patterns, hasWorkspace := parseCargoWorkspaceMembers(string(data))
	if !hasWorkspace {
		return nil, false, nil
	}
	members = expandWorkspaceGlobs(workdir, patterns, "Cargo.toml")
	return members, true, warnings
}

// parseCargoWorkspaceMembers walks the file looking for a `[workspace]`
// section. Within that section, the `members = [ ... ]` array literal is
// parsed (single-line or multi-line forms). The function tolerates comments
// and whitespace.
func parseCargoWorkspaceMembers(content string) (members []string, hasWorkspace bool) {
	lines := strings.Split(content, "\n")
	inWorkspace := false
	inMembers := false
	var buf strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Strip trailing line comments only — not in-string `#` (rare in
		// member paths but possible). For the workspace block this is safe.
		if idx := strings.Index(trimmed, "#"); idx >= 0 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inWorkspace = trimmed == "[workspace]"
			inMembers = false
			continue
		}
		if !inWorkspace {
			continue
		}
		hasWorkspace = true

		if !inMembers {
			if strings.HasPrefix(trimmed, "members") {
				eq := strings.Index(trimmed, "=")
				if eq < 0 {
					continue
				}
				rhs := strings.TrimSpace(trimmed[eq+1:])
				// Three shapes: complete on one line `members = ["a", "b"]`,
				// opens on this line `members = [`, or value continues.
				if strings.HasPrefix(rhs, "[") {
					inMembers = true
					buf.WriteString(rhs[1:])
					if strings.HasSuffix(rhs, "]") {
						// single-line form
						s := buf.String()
						buf.Reset()
						s = strings.TrimSuffix(s, "]")
						return splitTomlStringList(s), true
					}
				}
			}
			continue
		}

		// In multi-line members array — collect until we hit `]`.
		if strings.Contains(trimmed, "]") {
			idx := strings.Index(trimmed, "]")
			buf.WriteString(trimmed[:idx])
			members = splitTomlStringList(buf.String())
			return members, true
		}
		buf.WriteString(trimmed)
		buf.WriteString(" ")
	}

	return nil, hasWorkspace
}

// splitTomlStringList parses the body of a TOML array of strings (the
// content between `[` and `]`). Quoted strings (single or double) are
// extracted; commas and whitespace are separators.
func splitTomlStringList(body string) []string {
	out := []string{}
	var cur strings.Builder
	inString := false
	var quote byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inString {
			if c == quote {
				out = append(out, cur.String())
				cur.Reset()
				inString = false
				continue
			}
			cur.WriteByte(c)
			continue
		}
		if c == '"' || c == '\'' {
			inString = true
			quote = c
		}
	}
	return out
}

// readGoWorkUse parses go.work and returns the `use ( ./a ./b )` directive
// paths. Both single-line `use ./pkg` and multi-line `use (\n ./a\n ./b\n)`
// forms are supported. Comments (lines starting with `//`) are ignored.
func readGoWorkUse(workdir string) (members []string, warnings []string) {
	data, err := os.ReadFile(filepath.Join(workdir, "go.work"))
	if err != nil {
		return nil, []string{fmt.Sprintf("read go.work: %v", err)}
	}

	parsed := parseGoWorkUse(string(data))
	// go.work paths are typically `./packages/foo` — strip the leading `./`
	// so the public Packages[] uses workspace-relative paths consistent with
	// nodejs/cargo handling.
	for _, p := range parsed {
		members = append(members, strings.TrimPrefix(p, "./"))
	}
	return members, nil
}

// parseGoWorkUse is a minimal line-scanner over a go.work file body.
func parseGoWorkUse(content string) []string {
	out := []string{}
	lines := strings.Split(content, "\n")
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Strip trailing line comments
		if idx := strings.Index(trimmed, "//"); idx >= 0 {
			trimmed = strings.TrimSpace(trimmed[:idx])
			if trimmed == "" {
				continue
			}
		}
		if inBlock {
			if trimmed == ")" {
				inBlock = false
				continue
			}
			out = append(out, trimmed)
			continue
		}
		if strings.HasPrefix(trimmed, "use") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "use"))
			switch {
			case rest == "(":
				inBlock = true
			case strings.HasPrefix(rest, "("):
				// `use ( ./a ./b )` on a single line, or block with content
				// on opening line.
				rest = strings.TrimPrefix(rest, "(")
				if strings.HasSuffix(rest, ")") {
					rest = strings.TrimSuffix(rest, ")")
					for _, part := range strings.Fields(rest) {
						out = append(out, part)
					}
				} else {
					inBlock = true
					for _, part := range strings.Fields(rest) {
						out = append(out, part)
					}
				}
			case rest != "":
				// `use ./pkg`
				out = append(out, rest)
			}
		}
	}
	return out
}

// expandWorkspaceGlobs expands each glob pattern relative to workdir and
// returns the workspace member paths (relative to workdir). Each match is
// validated by checking that the directory contains the canonical manifest
// file (canonicalManifest) — this filters out glob hits that aren't actual
// workspace packages (e.g., `packages/*` matching a docs directory).
func expandWorkspaceGlobs(workdir string, patterns []string, canonicalManifest string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, pat := range patterns {
		// filepath.Glob runs against the OS filesystem; pattern is joined with
		// workdir to scope it.
		matches, err := filepath.Glob(filepath.Join(workdir, pat))
		if err != nil {
			continue
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || !info.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(m, canonicalManifest)); err != nil {
				continue
			}
			rel, err := filepath.Rel(workdir, m)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if _, dup := seen[rel]; dup {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}
