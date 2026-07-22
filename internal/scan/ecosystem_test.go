package scan

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeFile creates a file at dir/relPath (parent dirs created as needed)
// with the given content.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func runEcoScan(t *testing.T, dir string) *EcosystemScanResult {
	t.Helper()
	res, err := RunEcosystemScan(context.Background(), EcosystemOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunEcosystemScan: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

func TestRunEcosystemScan_NodejsOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"x"}`)

	res := runEcoScan(t, dir)
	if !reflect.DeepEqual(res.Ecosystems, []string{"nodejs"}) {
		t.Errorf("ecosystems = %v, want [nodejs]", res.Ecosystems)
	}
	if res.IsMonorepo {
		t.Errorf("is_monorepo = true, want false")
	}
	if res.MonorepoKind != "" {
		t.Errorf("monorepo_kind = %q, want \"\"", res.MonorepoKind)
	}
	if len(res.Packages) != 0 {
		t.Errorf("packages = %v, want empty", res.Packages)
	}
}

func TestRunEcosystemScan_PythonAllManifests(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "")
	writeFile(t, dir, "setup.py", "")
	writeFile(t, dir, "requirements.txt", "")

	res := runEcoScan(t, dir)
	if !reflect.DeepEqual(res.Ecosystems, []string{"python"}) {
		t.Errorf("ecosystems = %v, want [python]", res.Ecosystems)
	}
	// requirements.txt is the lowest-priority lockfile candidate but the
	// only present one — should be picked.
	if got := res.Lockfiles["python"]; got != "requirements.txt" {
		t.Errorf("python lockfile = %q, want requirements.txt", got)
	}
}

func TestRunEcosystemScan_NodejsWorkspacesArrayForm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"workspaces":["packages/*"]}`)
	writeFile(t, dir, "packages/foo/package.json", `{"name":"foo"}`)
	writeFile(t, dir, "packages/bar/package.json", `{"name":"bar"}`)
	// Decoy: directory matching the glob but no package.json — must be filtered.
	if err := os.MkdirAll(filepath.Join(dir, "packages/docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := runEcoScan(t, dir)
	if !res.IsMonorepo || res.MonorepoKind != "nodejs-workspaces" {
		t.Errorf("kind = %q, is_monorepo=%v", res.MonorepoKind, res.IsMonorepo)
	}
	want := []string{"packages/bar", "packages/foo"}
	if !reflect.DeepEqual(res.Packages, want) {
		t.Errorf("packages = %v, want %v", res.Packages, want)
	}
}

func TestRunEcosystemScan_NodejsWorkspacesObjectForm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"workspaces":{"packages":["apps/*"],"nohoist":["**/foo"]}}`)
	writeFile(t, dir, "apps/web/package.json", `{}`)
	writeFile(t, dir, "apps/cli/package.json", `{}`)

	res := runEcoScan(t, dir)
	if res.MonorepoKind != "nodejs-workspaces" {
		t.Errorf("kind = %q, want nodejs-workspaces", res.MonorepoKind)
	}
	want := []string{"apps/cli", "apps/web"}
	if !reflect.DeepEqual(res.Packages, want) {
		t.Errorf("packages = %v, want %v", res.Packages, want)
	}
}

func TestRunEcosystemScan_CargoWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", `
[workspace]
members = ["crates/a", "crates/b"]
`)
	writeFile(t, dir, "crates/a/Cargo.toml", "[package]\nname = \"a\"")
	writeFile(t, dir, "crates/b/Cargo.toml", "[package]\nname = \"b\"")

	res := runEcoScan(t, dir)
	if res.MonorepoKind != "cargo-workspace" {
		t.Errorf("kind = %q, want cargo-workspace", res.MonorepoKind)
	}
	want := []string{"crates/a", "crates/b"}
	if !reflect.DeepEqual(res.Packages, want) {
		t.Errorf("packages = %v, want %v", res.Packages, want)
	}
}

func TestRunEcosystemScan_CargoWorkspaceMultilineMembers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", `
[workspace]
members = [
  "alpha",   # comment
  "beta",
]
`)
	writeFile(t, dir, "alpha/Cargo.toml", "[package]")
	writeFile(t, dir, "beta/Cargo.toml", "[package]")

	res := runEcoScan(t, dir)
	if res.MonorepoKind != "cargo-workspace" {
		t.Errorf("kind = %q, want cargo-workspace", res.MonorepoKind)
	}
	want := []string{"alpha", "beta"}
	if !reflect.DeepEqual(res.Packages, want) {
		t.Errorf("packages = %v, want %v", res.Packages, want)
	}
}

func TestRunEcosystemScan_GoWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.work", `
go 1.22

use (
    ./svc-a
    ./svc-b
)
`)
	writeFile(t, dir, "svc-a/go.mod", "module a\n")
	writeFile(t, dir, "svc-b/go.mod", "module b\n")

	res := runEcoScan(t, dir)
	if res.MonorepoKind != "go-workspace" {
		t.Errorf("kind = %q, want go-workspace", res.MonorepoKind)
	}
	want := []string{"svc-a", "svc-b"}
	if !reflect.DeepEqual(res.Packages, want) {
		t.Errorf("packages = %v, want %v", res.Packages, want)
	}
}

func TestRunEcosystemScan_MixedMonorepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"workspaces":["pkgs/*"]}`)
	writeFile(t, dir, "pkgs/web/package.json", `{}`)
	writeFile(t, dir, "Cargo.toml", `
[workspace]
members = ["crates/core"]
`)
	writeFile(t, dir, "crates/core/Cargo.toml", "[package]")

	res := runEcoScan(t, dir)
	if res.MonorepoKind != "mixed" {
		t.Errorf("kind = %q, want mixed", res.MonorepoKind)
	}
	want := []string{"crates/core", "pkgs/web"}
	if !reflect.DeepEqual(res.Packages, want) {
		t.Errorf("packages = %v, want %v", res.Packages, want)
	}
}

func TestRunEcosystemScan_LockfilePicking(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{}`)
	// Highest-priority + lowest-priority both present — highest wins.
	writeFile(t, dir, "package-lock.json", "{}")
	writeFile(t, dir, "yarn.lock", "")

	res := runEcoScan(t, dir)
	if res.Lockfiles["nodejs"] != "package-lock.json" {
		t.Errorf("nodejs lockfile = %q, want package-lock.json", res.Lockfiles["nodejs"])
	}
	if res.Lockfile != "package-lock.json" {
		t.Errorf("Lockfile (top-level) = %q, want package-lock.json", res.Lockfile)
	}
}

func TestRunEcosystemScan_NothingDetected(t *testing.T) {
	dir := t.TempDir()

	res := runEcoScan(t, dir)
	if len(res.Ecosystems) != 0 {
		t.Errorf("ecosystems = %v, want []", res.Ecosystems)
	}
	if res.IsMonorepo {
		t.Errorf("is_monorepo = true, want false")
	}
	if res.MonorepoKind != "" {
		t.Errorf("monorepo_kind = %q, want \"\"", res.MonorepoKind)
	}
	if res.Lockfile != "" {
		t.Errorf("lockfile = %q, want \"\"", res.Lockfile)
	}
	for name, lf := range res.Lockfiles {
		if lf != "" {
			t.Errorf("lockfiles[%s] = %q, want \"\"", name, lf)
		}
	}
}

func TestRunEcosystemScan_DeterministicOrdering(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{}`)
	writeFile(t, dir, "go.mod", "module x\n")
	writeFile(t, dir, "Cargo.toml", `[package]`)
	writeFile(t, dir, "pyproject.toml", "")

	first := runEcoScan(t, dir)
	second := runEcoScan(t, dir)
	if !reflect.DeepEqual(first.Ecosystems, second.Ecosystems) {
		t.Errorf("ecosystems not deterministic: %v vs %v", first.Ecosystems, second.Ecosystems)
	}
	want := []string{"go", "nodejs", "python", "rust"}
	if !reflect.DeepEqual(first.Ecosystems, want) {
		t.Errorf("ecosystems = %v, want %v", first.Ecosystems, want)
	}
}

func TestRunEcosystemScan_JavaDetected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pom.xml", "<project/>")

	res := runEcoScan(t, dir)
	if !reflect.DeepEqual(res.Ecosystems, []string{"java"}) {
		t.Errorf("ecosystems = %v, want [java]", res.Ecosystems)
	}
	if res.Lockfiles["java"] != "" {
		t.Errorf("java lockfile = %q, want \"\" (no canonical lockfile for Maven/Gradle)", res.Lockfiles["java"])
	}
	if res.Lockfile != "" {
		t.Errorf("top-level lockfile = %q, want \"\"", res.Lockfile)
	}
}

func TestRunEcosystemScan_JavaGradle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle.kts", "")

	res := runEcoScan(t, dir)
	if !reflect.DeepEqual(res.Ecosystems, []string{"java"}) {
		t.Errorf("ecosystems = %v, want [java]", res.Ecosystems)
	}
}

func TestRunEcosystemScan_MalformedPackageJson(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"workspaces": [`) // truncated/invalid

	res := runEcoScan(t, dir)
	// Detection by file existence still succeeds.
	if !reflect.DeepEqual(res.Ecosystems, []string{"nodejs"}) {
		t.Errorf("ecosystems = %v, want [nodejs]", res.Ecosystems)
	}
	if res.IsMonorepo {
		t.Errorf("is_monorepo = true on malformed package.json, want false")
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected a warning for malformed package.json")
	}
}

func TestRunEcosystemScan_LockfilesShapeAlwaysComplete(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module x\n")

	res := runEcoScan(t, dir)
	for _, eco := range []string{"nodejs", "python", "go", "rust", "java"} {
		if _, ok := res.Lockfiles[eco]; !ok {
			t.Errorf("lockfiles missing key %q (skills require fixed shape)", eco)
		}
	}
}

func TestRunEcosystemScan_FirstDetectedLockfile(t *testing.T) {
	dir := t.TempDir()
	// Both go and nodejs detected; alphabetically go is first → top-level
	// lockfile should be go.sum.
	writeFile(t, dir, "go.mod", "module x")
	writeFile(t, dir, "go.sum", "")
	writeFile(t, dir, "package.json", `{}`)
	writeFile(t, dir, "package-lock.json", `{}`)

	res := runEcoScan(t, dir)
	if res.Lockfile != "go.sum" {
		t.Errorf("Lockfile (top-level) = %q, want go.sum (first alphabetical detected)", res.Lockfile)
	}
}
