package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// --- DetectE2E tests ---

func TestDetectE2E_NoFramework_NotDetected(t *testing.T) {
	dir := t.TempDir()
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Detected {
		t.Error("expected Detected=false for empty directory")
	}
	if len(result.Frameworks) != 0 {
		t.Errorf("expected no frameworks, got %v", result.Frameworks)
	}
	if result.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestDetectE2E_PlaywrightConfig_Detected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "playwright.config.ts", "export default {};")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Detected {
		t.Error("expected Detected=true for playwright.config.ts")
	}
	if !contains(result.Frameworks, "playwright") {
		t.Errorf("expected frameworks to contain playwright, got %v", result.Frameworks)
	}
	if len(result.ConfigFiles) == 0 {
		t.Error("expected non-empty config_files")
	}
}

func TestDetectE2E_PlaywrightConfigVariants(t *testing.T) {
	variants := []string{
		"playwright.config.ts",
		"playwright.config.js",
		"playwright.config.mts",
		"playwright.config.mjs",
	}
	for _, v := range variants {
		dir := t.TempDir()
		writeFile(t, dir, v, "")
		if !hasPlaywrightConfig(dir) {
			t.Errorf("expected hasPlaywrightConfig=true for %s", v)
		}
	}
}

func TestDetectE2E_CypressConfig_Detected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "cypress.config.json", "{}")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Detected {
		t.Error("expected Detected=true for cypress.config.json")
	}
	if !contains(result.Frameworks, "cypress") {
		t.Errorf("expected frameworks to contain cypress, got %v", result.Frameworks)
	}
}

func TestDetectE2E_CypressConfigVariants(t *testing.T) {
	variants := []string{
		"cypress.config.ts",
		"cypress.config.js",
		"cypress.config.json",
		"cypress.json",
	}
	for _, v := range variants {
		dir := t.TempDir()
		writeFile(t, dir, v, "")
		if !hasCypressConfig(dir) {
			t.Errorf("expected hasCypressConfig=true for %s", v)
		}
	}
}

func TestDetectE2E_VitestConfig_Detected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "vitest.config.ts", "")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Detected {
		t.Error("expected Detected=true for vitest.config.ts")
	}
	if !contains(result.Frameworks, "vitest") {
		t.Errorf("expected frameworks to contain vitest, got %v", result.Frameworks)
	}
}

func TestDetectE2E_VitestConfigVariants(t *testing.T) {
	variants := []string{
		"vitest.config.ts",
		"vitest.config.js",
		"vitest.config.mts",
		"vitest.config.mjs",
	}
	for _, v := range variants {
		dir := t.TempDir()
		writeFile(t, dir, v, "")
		if !hasVitestConfig(dir) {
			t.Errorf("expected hasVitestConfig=true for %s", v)
		}
	}
}

func TestDetectE2E_JestConfig_Detected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "jest.config.js", "")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Detected {
		t.Error("expected Detected=true for jest.config.js")
	}
	if !contains(result.Frameworks, "jest") {
		t.Errorf("expected frameworks to contain jest, got %v", result.Frameworks)
	}
}

func TestDetectE2E_JestConfigVariants(t *testing.T) {
	variants := []string{
		"jest.config.ts",
		"jest.config.js",
		"jest.config.json",
		"jest.config.mjs",
	}
	for _, v := range variants {
		dir := t.TempDir()
		writeFile(t, dir, v, "")
		if !hasJestConfig(dir) {
			t.Errorf("expected hasJestConfig=true for %s", v)
		}
	}
}

func TestDetectE2E_GoTest_Detected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\ngo 1.21\n")
	writeFile(t, dir, "foo_test.go", "package main\nfunc TestFoo(t *testing.T) {}")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Detected {
		t.Error("expected Detected=true for go.mod + *_test.go")
	}
	if !contains(result.Frameworks, "go") {
		t.Errorf("expected frameworks to contain go, got %v", result.Frameworks)
	}
}

func TestDetectE2E_GoMod_NoTestFiles_NotDetected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/test\ngo 1.21\n")
	if hasGoTest(dir) {
		t.Error("expected hasGoTest=false for go.mod without _test.go files")
	}
}

func TestDetectE2E_MultipleFrameworks_ReturnsAll(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "playwright.config.ts", "")
	writeFile(t, dir, "cypress.config.ts", "")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result.Frameworks, "playwright") {
		t.Error("expected playwright in frameworks")
	}
	if !contains(result.Frameworks, "cypress") {
		t.Error("expected cypress in frameworks")
	}
	if len(result.Frameworks) < 2 {
		t.Errorf("expected at least 2 frameworks, got %v", result.Frameworks)
	}
}

func TestDetectE2E_E2EDirectory_Detected(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "e2e"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result.TestDirs, "e2e") {
		t.Errorf("expected test_dirs to contain e2e, got %v", result.TestDirs)
	}
}

func TestDetectE2E_TestDirs_AllCollected(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"e2e", "tests/e2e", "test/e2e"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.TestDirs) != 3 {
		t.Errorf("expected 3 test dirs, got %v", result.TestDirs)
	}
}

func TestDetectE2E_PlaywrightTakesPrecedenceOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "playwright.config.ts", "")
	writeFile(t, dir, "cypress.config.ts", "")
	result, err := DetectE2E(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Playwright must appear before cypress in the frameworks list.
	if len(result.Frameworks) < 2 {
		t.Fatalf("expected at least 2 frameworks, got %v", result.Frameworks)
	}
	if result.Frameworks[0] != "playwright" {
		t.Errorf("expected playwright first, got %v", result.Frameworks)
	}
}

// --- RunE2E tests ---

func TestRunE2E_NoFramework_Skipped(t *testing.T) {
	dir := t.TempDir()
	result, err := RunE2E(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Error("expected Ran=false when no framework detected")
	}
	if result.Status != "skipped" {
		t.Errorf("expected status=skipped, got %s", result.Status)
	}
}

func TestRunE2E_UnknownFramework_Skipped(t *testing.T) {
	dir := t.TempDir()
	result, err := RunE2E(context.Background(), dir, "unknown-framework")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Error("expected Ran=false for unknown framework")
	}
	if result.Status != "skipped" {
		t.Errorf("expected status=skipped, got %s", result.Status)
	}
}

func TestFrameworkCommand_AllFrameworks(t *testing.T) {
	cases := []struct {
		framework string
		wantCmd   string
	}{
		{"playwright", "npx"},
		{"cypress", "npx"},
		{"vitest", "npx"},
		{"jest", "npx"},
		{"go", "go"},
		{"unknown", ""},
	}
	for _, c := range cases {
		cmd, _ := frameworkCommand(c.framework)
		if cmd != c.wantCmd {
			t.Errorf("frameworkCommand(%q) cmd = %q, want %q", c.framework, cmd, c.wantCmd)
		}
	}
}

// --- helpers ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
