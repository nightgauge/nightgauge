package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// E2EDetectResult is the structured output of e2e framework detection.
type E2EDetectResult struct {
	Detected    bool     `json:"detected"`
	Frameworks  []string `json:"frameworks"`
	ConfigFiles []string `json:"config_files"`
	TestDirs    []string `json:"test_dirs"`
	Timestamp   string   `json:"timestamp"`
}

// E2ERunResult is the structured output of an e2e test run.
type E2ERunResult struct {
	Ran       bool     `json:"ran"`
	Status    string   `json:"status"` // "passed" | "failed" | "skipped"
	Framework string   `json:"framework"`
	Commands  []string `json:"commands"`
	Output    string   `json:"output"`
	Timestamp string   `json:"timestamp"`
}

// DetectE2E scans workdir for E2E test frameworks.
// Detection order: Playwright > Cypress > Vitest > Jest > Go test.
func DetectE2E(_ context.Context, workdir string) (E2EDetectResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	result := E2EDetectResult{
		Detected:    false,
		Frameworks:  []string{},
		ConfigFiles: []string{},
		TestDirs:    []string{},
		Timestamp:   ts,
	}

	// Collect test directories.
	for _, dir := range []string{"e2e", "tests/e2e", "test/e2e"} {
		if fileExists(filepath.Join(workdir, dir)) {
			result.TestDirs = append(result.TestDirs, dir)
		}
	}

	// Playwright.
	if cfgs := playwrightConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "playwright")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Cypress.
	if cfgs := cypressConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "cypress")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Vitest.
	if cfgs := vitestConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "vitest")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Jest.
	if cfgs := jestConfigs(workdir); len(cfgs) > 0 {
		result.Frameworks = append(result.Frameworks, "jest")
		result.ConfigFiles = append(result.ConfigFiles, cfgs...)
	}

	// Go test.
	if hasGoTest(workdir) {
		result.Frameworks = append(result.Frameworks, "go")
	}

	result.Detected = len(result.Frameworks) > 0
	return result, nil
}

// RunE2E executes the E2E test suite in workdir.
// If framework is non-empty it skips detection and uses the specified framework.
// Detection order mirrors DetectE2E: first detected framework wins when auto-detecting.
func RunE2E(ctx context.Context, workdir, framework string) (E2ERunResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)

	if framework == "" {
		detected, err := DetectE2E(ctx, workdir)
		if err != nil {
			return E2ERunResult{Ran: false, Status: "skipped", Timestamp: ts}, err
		}
		if len(detected.Frameworks) == 0 {
			return E2ERunResult{Ran: false, Status: "skipped", Timestamp: ts}, nil
		}
		framework = detected.Frameworks[0]
	}

	cmd, args := frameworkCommand(framework)
	if cmd == "" {
		return E2ERunResult{
			Ran:       false,
			Status:    "skipped",
			Framework: framework,
			Timestamp: ts,
		}, nil
	}

	commandStr := cmd
	for _, a := range args {
		commandStr += " " + a
	}

	out, err := runCmd(ctx, workdir, cmd, args...)
	ts = time.Now().UTC().Format(time.RFC3339)
	status := "passed"
	if err != nil {
		status = "failed"
	}

	return E2ERunResult{
		Ran:       true,
		Status:    status,
		Framework: framework,
		Commands:  []string{commandStr},
		Output:    out,
		Timestamp: ts,
	}, nil
}

// frameworkCommand returns the command and arguments for the given framework.
func frameworkCommand(framework string) (string, []string) {
	switch framework {
	case "playwright":
		return "npx", []string{"playwright", "test"}
	case "cypress":
		return "npx", []string{"cypress", "run"}
	case "vitest":
		return "npx", []string{"vitest", "run", "--run"}
	case "jest":
		return "npx", []string{"jest", "e2e"}
	case "go":
		return "go", []string{"test", "-run", "E2E", "./..."}
	}
	return "", nil
}

// hasPlaywrightConfig returns true if any playwright config file exists in workdir.
func hasPlaywrightConfig(workdir string) bool {
	return len(playwrightConfigs(workdir)) > 0
}

// hasCypressConfig returns true if any cypress config file exists in workdir.
func hasCypressConfig(workdir string) bool {
	return len(cypressConfigs(workdir)) > 0
}

// hasVitestConfig returns true if any vitest config file exists in workdir.
func hasVitestConfig(workdir string) bool {
	return len(vitestConfigs(workdir)) > 0
}

// hasJestConfig returns true if any jest config file exists in workdir.
func hasJestConfig(workdir string) bool {
	return len(jestConfigs(workdir)) > 0
}

// hasGoTest returns true if workdir contains a go.mod and at least one _test.go file.
func hasGoTest(workdir string) bool {
	if !fileExists(filepath.Join(workdir, "go.mod")) {
		return false
	}
	found := false
	_ = filepath.WalkDir(workdir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.IsDir() && len(d.Name()) > 8 && d.Name()[len(d.Name())-8:] == "_test.go" {
			found = true
		}
		return nil
	})
	return found
}

func playwrightConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"playwright.config.ts",
		"playwright.config.js",
		"playwright.config.mts",
		"playwright.config.mjs",
	})
}

func cypressConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"cypress.config.ts",
		"cypress.config.js",
		"cypress.config.json",
		"cypress.json",
	})
}

func vitestConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"vitest.config.ts",
		"vitest.config.js",
		"vitest.config.mts",
		"vitest.config.mjs",
	})
}

func jestConfigs(workdir string) []string {
	return existingFiles(workdir, []string{
		"jest.config.ts",
		"jest.config.js",
		"jest.config.json",
		"jest.config.mjs",
	})
}

func existingFiles(workdir string, names []string) []string {
	var found []string
	for _, name := range names {
		p := filepath.Join(workdir, name)
		if fileExists(p) {
			found = append(found, p)
		}
	}
	return found
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCmd(ctx context.Context, workdir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workdir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
