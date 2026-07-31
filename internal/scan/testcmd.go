// This file adds the test-command-selection verb to package scan. The
// TestCommandResult JSON schema is stable — field names and types must not
// change after first merge. Skills parse `nightgauge scan testcmd --json`
// output; any breaking change requires incrementing the V field.
package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TestCommandResult is the stable JSON output schema for
// `nightgauge scan testcmd`. Schema version 1 — do not rename or remove
// fields after first merge.
type TestCommandResult struct {
	V              int      `json:"v"`                         // schema version, always 1
	Workdir        string   `json:"workdir"`                   // absolute path that was scanned
	Command        string   `json:"command"`                   // resolved test command, or "" when nothing was determinable
	Source         string   `json:"source"`                    // "scripts.test" | "poetry_scripts" | "tox" | "makefile" | "framework_fallback" | ""
	PackageManager string   `json:"package_manager,omitempty"` // npm | yarn | pnpm | bun (nodejs only)
	Framework      string   `json:"framework,omitempty"`       // detected test framework, for output parsing / targeted reruns — never used to choose the entrypoint when a declared command exists
	Warnings       []string `json:"warnings"`
}

// TestCommandOptions controls a single test-command-selection run.
type TestCommandOptions struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
}

// nodejsPackageManagerByLockfile maps the lockfile picked by pickLockfile to
// the package-manager invocation prefix used to run `scripts.test`.
var nodejsPackageManagerByLockfile = map[string]string{
	"package-lock.json":   "npm",
	"npm-shrinkwrap.json": "npm",
	"yarn.lock":           "yarn",
	"pnpm-lock.yaml":      "pnpm",
	"bun.lockb":           "bun",
}

// nodejsTestFrameworkDeps lists dependency names checked (in this priority
// order) against package.json's combined dependencies/devDependencies to
// report a Framework for output parsing / targeted reruns. This is NOT used
// to choose the entrypoint — see the package doc comment on Source.
var nodejsTestFrameworkDeps = []string{"playwright", "cypress", "vitest", "jest", "mocha"}

// makefileTestTargetRe matches a `test:` target line at the start of a
// Makefile line (optionally followed by prerequisites), tolerating leading
// whitespace but not a tab-indented recipe line.
var makefileTestTargetRe = regexp.MustCompile(`(?m)^test\s*:`)

// SelectTestCommand determines the test command a repo declares, preferring
// it over an inferred test-framework binary per the precedence documented on
// each branch below. The function never returns a non-nil error for
// malformed manifests — those are recorded in Warnings. err is reserved for
// hard input errors (invalid workdir).
func SelectTestCommand(_ context.Context, opts TestCommandOptions) (*TestCommandResult, error) {
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

	result := &TestCommandResult{
		V:        1,
		Workdir:  workdir,
		Warnings: []string{},
	}

	// Precedence 1: nodejs — package.json `scripts.test`.
	if fileExists(workdir, "package.json") {
		framework, warn := detectNodejsFramework(workdir)
		if warn != "" {
			result.Warnings = append(result.Warnings, warn)
		}
		result.Framework = framework

		hasTestScript, warn := packageJSONHasTestScript(workdir)
		if warn != "" {
			result.Warnings = append(result.Warnings, warn)
		}
		if hasTestScript {
			pm := "npm"
			if lf := pickLockfile(workdir, "nodejs"); lf != "" {
				if mapped, ok := nodejsPackageManagerByLockfile[lf]; ok {
					pm = mapped
				}
			}
			result.PackageManager = pm
			result.Command = pm + " test"
			result.Source = "scripts.test"
			return result, nil
		}

		if framework != "" {
			result.Command = "npx " + framework
			result.Source = "framework_fallback"
			return result, nil
		}
	}

	// Precedence 2: python — poetry scripts, then tox, then pytest fallback.
	if fileExists(workdir, "pyproject.toml") {
		data, err := os.ReadFile(filepath.Join(workdir, "pyproject.toml"))
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read pyproject.toml: %v", err))
		} else {
			content := string(data)
			if hasPoetryTestScript(content) {
				result.Command = "poetry run test"
				result.Source = "poetry_scripts"
				return result, nil
			}
			if hasToxEnvironment(content) || fileExists(workdir, "tox.ini") {
				result.Command = "tox"
				result.Source = "tox"
				return result, nil
			}
		}
		result.Framework = "pytest"
		result.Command = "pytest"
		result.Source = "framework_fallback"
		return result, nil
	}

	// Precedence 3: Makefile `test:` target, any ecosystem.
	if fileExists(workdir, "Makefile") {
		data, err := os.ReadFile(filepath.Join(workdir, "Makefile"))
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read Makefile: %v", err))
		} else if makefileTestTargetRe.MatchString(string(data)) {
			result.Command = "make test"
			result.Source = "makefile"
			return result, nil
		}
	}

	// Precedence 4: go — `go test ./...` when go.mod is present.
	if fileExists(workdir, "go.mod") {
		result.Framework = "go-test"
		result.Command = "go test ./..."
		result.Source = "framework_fallback"
		return result, nil
	}

	result.Warnings = append(result.Warnings, "no declared test command or recognized ecosystem manifest found")
	return result, nil
}

// packageJSONHasTestScript reports whether package.json declares a
// `scripts.test` entry. Per npm convention, a placeholder script
// (`"echo \"Error: no test specified\" && exit 1"`) is npm's own default and
// is excluded so it doesn't masquerade as a declared command.
func packageJSONHasTestScript(workdir string) (bool, string) {
	data, err := os.ReadFile(filepath.Join(workdir, "package.json"))
	if err != nil {
		return false, fmt.Sprintf("read package.json: %v", err)
	}
	var top struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return false, fmt.Sprintf("parse package.json: %v", err)
	}
	testScript, ok := top.Scripts["test"]
	if !ok {
		return false, ""
	}
	if strings.Contains(testScript, "no test specified") {
		return false, ""
	}
	return strings.TrimSpace(testScript) != "", ""
}

// detectNodejsFramework reports the first known test framework found in
// package.json's combined dependencies/devDependencies, in priority order.
// Returns "" when none are declared.
func detectNodejsFramework(workdir string) (string, string) {
	data, err := os.ReadFile(filepath.Join(workdir, "package.json"))
	if err != nil {
		return "", fmt.Sprintf("read package.json: %v", err)
	}
	var top struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return "", fmt.Sprintf("parse package.json: %v", err)
	}
	for _, name := range nodejsTestFrameworkDeps {
		if _, ok := top.Dependencies[name]; ok {
			return name, ""
		}
		if _, ok := top.DevDependencies[name]; ok {
			return name, ""
		}
	}
	return "", ""
}

// hasPoetryTestScript reports whether pyproject.toml declares a
// `[tool.poetry.scripts]` entry named `test`. Uses a minimal line scanner —
// consistent with parseCargoWorkspaceMembers in ecosystem.go, adding a TOML
// library dependency just for this check is overkill.
func hasPoetryTestScript(content string) bool {
	lines := strings.Split(content, "\n")
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inSection = trimmed == "[tool.poetry.scripts]"
			continue
		}
		if !inSection {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		key = strings.Trim(key, `"'`)
		if key == "test" {
			return true
		}
	}
	return false
}

// hasToxEnvironment reports whether pyproject.toml declares a `[tool.tox]`
// section (the PEP 518-style inline tox config, as opposed to a standalone
// tox.ini).
func hasToxEnvironment(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[tool.tox") {
			return true
		}
	}
	return false
}
