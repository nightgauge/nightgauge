package scan

import (
	"context"
	"testing"
)

func runTestCmdScan(t *testing.T, dir string) *TestCommandResult {
	t.Helper()
	res, err := SelectTestCommand(context.Background(), TestCommandOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("SelectTestCommand: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

// TestSelectTestCommand_AngularBuilderShape reproduces the exact shape issue
// #221 was filed about: vitest present in devDependencies (so the old
// dependency-scan would have inferred `npx vitest run`) but `scripts.test`
// is the Angular-builder wrapper `ng test`. The declared script must win.
func TestSelectTestCommand_AngularBuilderShape(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
		"name": "dashboard",
		"devDependencies": {"vitest": "^1.0.0", "@angular/build": "^18.0.0"},
		"scripts": {"test": "ng test"}
	}`)
	writeFile(t, dir, "package-lock.json", "{}")

	res := runTestCmdScan(t, dir)
	if res.Source != "scripts.test" {
		t.Fatalf("source = %q, want scripts.test", res.Source)
	}
	if res.Command != "npm test" {
		t.Fatalf("command = %q, want %q (never npx vitest run)", res.Command, "npm test")
	}
	if res.PackageManager != "npm" {
		t.Errorf("package_manager = %q, want npm", res.PackageManager)
	}
	// Framework is still reported (for output parsing / targeted reruns) but
	// must not have driven Command selection.
	if res.Framework != "vitest" {
		t.Errorf("framework = %q, want vitest", res.Framework)
	}
}

func TestSelectTestCommand_NodejsScriptsTestPnpm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"scripts": {"test": "vitest run"}}`)
	writeFile(t, dir, "pnpm-lock.yaml", "")

	res := runTestCmdScan(t, dir)
	if res.Command != "pnpm test" {
		t.Fatalf("command = %q, want %q", res.Command, "pnpm test")
	}
	if res.Source != "scripts.test" {
		t.Errorf("source = %q, want scripts.test", res.Source)
	}
}

func TestSelectTestCommand_NodejsNoTestScript_FallsBackToFramework(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"devDependencies": {"jest": "^29.0.0"}}`)

	res := runTestCmdScan(t, dir)
	if res.Source != "framework_fallback" {
		t.Fatalf("source = %q, want framework_fallback", res.Source)
	}
	if res.Command != "npx jest" {
		t.Errorf("command = %q, want %q", res.Command, "npx jest")
	}
}

func TestSelectTestCommand_NodejsPlaceholderTestScript_FallsBackToFramework(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
		"scripts": {"test": "echo \"Error: no test specified\" && exit 1"},
		"devDependencies": {"mocha": "^10.0.0"}
	}`)

	res := runTestCmdScan(t, dir)
	if res.Source != "framework_fallback" {
		t.Fatalf("source = %q, want framework_fallback (npm placeholder must not count as declared)", res.Source)
	}
	if res.Command != "npx mocha" {
		t.Errorf("command = %q, want %q", res.Command, "npx mocha")
	}
}

func TestSelectTestCommand_PythonPoetryScript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", `
[tool.poetry]
name = "x"

[tool.poetry.scripts]
test = "pytest:main"
`)

	res := runTestCmdScan(t, dir)
	if res.Source != "poetry_scripts" {
		t.Fatalf("source = %q, want poetry_scripts", res.Source)
	}
	if res.Command != "poetry run test" {
		t.Errorf("command = %q, want %q", res.Command, "poetry run test")
	}
}

func TestSelectTestCommand_PythonToxSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", `
[tool.tox]
legacy_tox_ini = "[tox]\nenvlist = py311\n"
`)

	res := runTestCmdScan(t, dir)
	if res.Source != "tox" {
		t.Fatalf("source = %q, want tox", res.Source)
	}
	if res.Command != "tox" {
		t.Errorf("command = %q, want tox", res.Command)
	}
}

func TestSelectTestCommand_PythonToxIniFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.poetry]\nname = \"x\"\n")
	writeFile(t, dir, "tox.ini", "[tox]\nenvlist = py311\n")

	res := runTestCmdScan(t, dir)
	if res.Source != "tox" {
		t.Fatalf("source = %q, want tox", res.Source)
	}
}

func TestSelectTestCommand_PythonFallsBackToPytest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.poetry]\nname = \"x\"\n")

	res := runTestCmdScan(t, dir)
	if res.Source != "framework_fallback" {
		t.Fatalf("source = %q, want framework_fallback", res.Source)
	}
	if res.Command != "pytest" {
		t.Errorf("command = %q, want pytest", res.Command)
	}
}

func TestSelectTestCommand_MakefileTestTarget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n")

	res := runTestCmdScan(t, dir)
	if res.Source != "makefile" {
		t.Fatalf("source = %q, want makefile", res.Source)
	}
	if res.Command != "make test" {
		t.Errorf("command = %q, want %q", res.Command, "make test")
	}
}

func TestSelectTestCommand_MakefileWithoutTestTarget_FallsBackToGoTest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "build:\n\tgo build ./...\n")
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")

	res := runTestCmdScan(t, dir)
	if res.Source != "framework_fallback" {
		t.Fatalf("source = %q, want framework_fallback", res.Source)
	}
	if res.Command != "go test ./..." {
		t.Errorf("command = %q, want %q", res.Command, "go test ./...")
	}
}

func TestSelectTestCommand_GoModOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")

	res := runTestCmdScan(t, dir)
	if res.Source != "framework_fallback" {
		t.Fatalf("source = %q, want framework_fallback", res.Source)
	}
	if res.Command != "go test ./..." {
		t.Errorf("command = %q, want %q", res.Command, "go test ./...")
	}
	if res.Framework != "go-test" {
		t.Errorf("framework = %q, want go-test", res.Framework)
	}
}

func TestSelectTestCommand_NothingDetected(t *testing.T) {
	dir := t.TempDir()

	res := runTestCmdScan(t, dir)
	if res.Command != "" {
		t.Errorf("command = %q, want empty", res.Command)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a warning when nothing is detected")
	}
}
