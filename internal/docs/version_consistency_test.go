package docs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionConsistency_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	if res.ProjectType != "unknown" {
		t.Errorf("ProjectType = %q, want %q", res.ProjectType, "unknown")
	}
	if res.SourceVersion != "" {
		t.Errorf("SourceVersion = %q, want empty", res.SourceVersion)
	}
	if res.MismatchesCount != 0 {
		t.Errorf("MismatchesCount = %d, want 0", res.MismatchesCount)
	}
}

func TestVersionConsistency_NodeMatchingVersions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"foo","version":"1.2.3"}`)
	writeFile(t, dir, "README.md", "# Foo\n\nversion: 1.2.3\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "nodejs" {
		t.Errorf("ProjectType = %q, want nodejs", res.ProjectType)
	}
	if res.SourceVersion != "1.2.3" {
		t.Errorf("SourceVersion = %q, want 1.2.3", res.SourceVersion)
	}
	if res.MismatchesCount != 0 {
		t.Errorf("MismatchesCount = %d, want 0", res.MismatchesCount)
	}
}

func TestVersionConsistency_NodeMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"foo","version":"2.0.0"}`)
	writeFile(t, dir, "README.md", "Install version: 1.9.9 to get started.\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MismatchesCount != 1 {
		t.Errorf("MismatchesCount = %d, want 1", res.MismatchesCount)
	}
	if len(res.Mismatches) == 0 {
		t.Fatal("expected at least one mismatch")
	}
	m := res.Mismatches[0]
	if m.FoundVersion != "1.9.9" {
		t.Errorf("FoundVersion = %q, want 1.9.9", m.FoundVersion)
	}
	if m.ExpectedVersion != "2.0.0" {
		t.Errorf("ExpectedVersion = %q, want 2.0.0", m.ExpectedVersion)
	}
	if m.File != "README.md" {
		t.Errorf("File = %q, want README.md", m.File)
	}
}

func TestVersionConsistency_MultipleMismatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version":"3.0.0"}`)
	writeFile(t, dir, "docs/INSTALL.md", "Use version: 2.9.0 for setup.\n")
	writeFile(t, dir, "docs/UPGRADE.md", "Upgrading from version: 2.8.0.\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MismatchesCount < 2 {
		t.Errorf("MismatchesCount = %d, want >= 2", res.MismatchesCount)
	}
}

func TestVersionConsistency_PythonPyproject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.poetry]\nname = \"mylib\"\nversion = \"0.5.1\"\n")
	writeFile(t, dir, "README.md", "Current version: 0.5.1 is stable.\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "python" {
		t.Errorf("ProjectType = %q, want python", res.ProjectType)
	}
	if res.SourceVersion != "0.5.1" {
		t.Errorf("SourceVersion = %q, want 0.5.1", res.SourceVersion)
	}
	if res.MismatchesCount != 0 {
		t.Errorf("MismatchesCount = %d, want 0", res.MismatchesCount)
	}
}

func TestVersionConsistency_RustCargoToml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"myapp\"\nversion = \"0.1.0\"\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "rust" {
		t.Errorf("ProjectType = %q, want rust", res.ProjectType)
	}
	if res.SourceVersion != "0.1.0" {
		t.Errorf("SourceVersion = %q, want 0.1.0", res.SourceVersion)
	}
}

func TestVersionConsistency_GoModNoVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module github.com/example/mymod\n\ngo 1.21\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "go" {
		t.Errorf("ProjectType = %q, want go", res.ProjectType)
	}
	// go.mod has no version line → empty source version, no mismatches possible
	if res.SourceVersion != "" {
		t.Errorf("SourceVersion = %q, want empty (no version in go.mod)", res.SourceVersion)
	}
}

func TestVersionConsistency_SkillsDirectory(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills", "my-skill")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, skillsDir, "SKILL.md", "---\nversion: \"1.5.0\"\n---\n# Skill\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "skills" {
		t.Errorf("ProjectType = %q, want skills", res.ProjectType)
	}
	if res.SourceVersion != "1.5.0" {
		t.Errorf("SourceVersion = %q, want 1.5.0", res.SourceVersion)
	}
}

func TestVersionConsistency_CodeFenceSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version":"2.0.0"}`)
	// Version inside a code fence should NOT be flagged as a mismatch.
	writeFile(t, dir, "README.md", "Example:\n```\nversion: 1.0.0\n```\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MismatchesCount != 0 {
		t.Errorf("MismatchesCount = %d, want 0 (version in code fence should be skipped)", res.MismatchesCount)
	}
}

func TestVersionConsistency_YearLikeNotFlagged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version":"1.0.0"}`)
	// "version: 2026.1" looks like a year.version, should be ignored.
	writeFile(t, dir, "docs/notes.md", "API version: 2026.1 compatibility note.\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MismatchesCount != 0 {
		t.Errorf("MismatchesCount = %d, want 0 (year-like version should not be flagged)", res.MismatchesCount)
	}
}

func TestVersionConsistency_InvalidJSONPackage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `NOT JSON`)

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should produce a warning and continue without crashing.
	if len(res.Warnings) == 0 {
		t.Error("expected at least one warning for invalid JSON")
	}
	if res.SourceVersion != "" {
		t.Errorf("SourceVersion = %q, want empty", res.SourceVersion)
	}
}

func TestVersionConsistency_DefaultsToCurrentDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version":"1.0.0"}`)
	// Save/restore CWD.
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Skipf("cannot chdir: %v", err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Root == "" {
		t.Error("Root should be set when no explicit root given")
	}
}

func TestVersionConsistency_NodeMismatch_LineNumber(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version":"4.0.0"}`)
	writeFile(t, dir, "CHANGELOG.md", "# Changelog\n\nRelease version: 3.9.9 notes here.\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MismatchesCount == 0 {
		t.Fatal("expected mismatch")
	}
	if res.Mismatches[0].Line != 3 {
		t.Errorf("Line = %d, want 3", res.Mismatches[0].Line)
	}
}

func TestVersionConsistency_NodeNoVersionField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"foo","dependencies":{}}`)

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "nodejs" {
		t.Errorf("ProjectType = %q, want nodejs", res.ProjectType)
	}
	if res.SourceVersion != "" {
		t.Errorf("SourceVersion = %q, want empty", res.SourceVersion)
	}
}

func TestVersionConsistency_DotnetCsproj(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "MyApp.csproj", "<Project>\n  <PropertyGroup>\n    <Version>5.0.1</Version>\n  </PropertyGroup>\n</Project>\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "dotnet" {
		t.Errorf("ProjectType = %q, want dotnet", res.ProjectType)
	}
	if res.SourceVersion != "5.0.1" {
		t.Errorf("SourceVersion = %q, want 5.0.1", res.SourceVersion)
	}
}

func TestVersionConsistency_VERSIONFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "VERSION", "0.9.2\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectType != "go" {
		t.Errorf("ProjectType = %q, want go", res.ProjectType)
	}
	if res.SourceVersion != "0.9.2" {
		t.Errorf("SourceVersion = %q, want 0.9.2", res.SourceVersion)
	}
}

func TestVersionConsistency_NodeSkipsDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version":"1.0.0"}`)
	// File inside node_modules should be skipped.
	nmDir := filepath.Join(dir, "node_modules", "some-pkg")
	if err := os.MkdirAll(nmDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nmDir, "README.md", "version: 0.0.1\n")

	res, err := VersionConsistency(context.Background(), VersionConsistencyOptions{Root: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MismatchesCount != 0 {
		t.Errorf("MismatchesCount = %d, want 0 (node_modules should be skipped)", res.MismatchesCount)
	}
}
