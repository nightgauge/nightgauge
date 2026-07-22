package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunBuild_NoFiles_Skipped(t *testing.T) {
	dir := t.TempDir()
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Error("expected Ran=false for empty directory")
	}
	if result.Status != "skipped" {
		t.Errorf("expected status=skipped, got %s", result.Status)
	}
	if result.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestRunBuild_GoMod_Detected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Ran {
		t.Error("expected Ran=true for directory with go.mod")
	}
	if len(result.Commands) == 0 || result.Commands[0] != "go build ./..." {
		t.Errorf("expected commands=[go build ./...], got %v", result.Commands)
	}
	if result.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestRunBuild_PackageJSON_WithBuildScript(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"scripts":{"build":"echo built"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Ran {
		t.Error("expected Ran=true for package.json with build script")
	}
	if len(result.Commands) == 0 || result.Commands[0] != "npm run build" {
		t.Errorf("expected commands=[npm run build], got %v", result.Commands)
	}
}

func TestRunBuild_PackageJSON_NoBuildScript_Skipped(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"scripts":{"test":"echo test"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Error("expected Ran=false for package.json without build script")
	}
	if result.Status != "skipped" {
		t.Errorf("expected status=skipped, got %s", result.Status)
	}
}

func TestIsStaleSDK(t *testing.T) {
	cases := []struct {
		output string
		want   bool
	}{
		{"RECOVERABLE: stale_sdk_dist detected", true},
		{"Error: SDK dist/index.js not found", true},
		{"SDK dist is stale — rebuild required", true},
		{"Build failed: some other error", false},
		{"", false},
	}
	for _, c := range cases {
		got := isStaleSDK(c.output)
		if got != c.want {
			t.Errorf("isStaleSDK(%q) = %v, want %v", c.output, got, c.want)
		}
	}
}

func TestHasBuildScript(t *testing.T) {
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "package.json")

	if err := os.WriteFile(pkgPath, []byte(`{"scripts":{"build":"tsc","test":"vitest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasBuildScript(pkgPath) {
		t.Error("expected hasBuildScript=true")
	}

	if err := os.WriteFile(pkgPath, []byte(`{"scripts":{"test":"vitest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if hasBuildScript(pkgPath) {
		t.Error("expected hasBuildScript=false")
	}
}

func TestRunBuild_Pubspec_FlutterAnalyze(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pubspec.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// #195: pubspec.yaml must route to the Flutter compile-correctness gate
	// instead of returning skipped (internal/ci already knew flutter).
	if !result.Ran {
		t.Error("expected Ran=true for directory with pubspec.yaml")
	}
	if len(result.Commands) == 0 || result.Commands[0] != "flutter analyze" {
		t.Errorf("expected commands=[flutter analyze], got %v", result.Commands)
	}
	if result.Status == "skipped" {
		t.Errorf("status = skipped — flutter project must not be skipped")
	}
}

func TestRunBuild_PubspecTakesPrecedenceOverPackageJSON(t *testing.T) {
	// A Flutter repo with tooling package.json (e.g. for husky) is a Flutter
	// repo — detection order matches internal/ci via the shared detector.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pubspec.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"echo built"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Commands) == 0 || result.Commands[0] != "flutter analyze" {
		t.Errorf("expected flutter analyze to win, got %v", result.Commands)
	}
}

func TestGoModTakesPrecedenceOverPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"npm run something"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunBuild(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// go.mod takes precedence
	if len(result.Commands) == 0 || result.Commands[0] != "go build ./..." {
		t.Errorf("expected go.mod to take precedence, got commands=%v", result.Commands)
	}
}
