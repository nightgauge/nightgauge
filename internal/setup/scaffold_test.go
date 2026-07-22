package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePackageJSON writes a package.json under dir with the given dep map.
// Empty deps maps produce an empty object so package.json itself is present
// (PackageJSONFound stays true).
func writePackageJSON(t *testing.T, dir string, deps, devDeps map[string]string, engines string) {
	t.Helper()
	body := `{`
	if engines != "" {
		body += `"engines":{"node":"` + engines + `"},`
	}
	body += `"dependencies":` + depsToJSON(deps) + `,`
	body += `"devDependencies":` + depsToJSON(devDeps) + `}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func depsToJSON(m map[string]string) string {
	if len(m) == 0 {
		return `{}`
	}
	parts := []string{}
	for k, v := range m {
		parts = append(parts, `"`+k+`":"`+v+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestRunScaffoldTooling_FreshDirNoPackageJSON(t *testing.T) {
	dir := t.TempDir()
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	if len(res.Outcomes) != 5 {
		t.Fatalf("got %d outcomes, want 5", len(res.Outcomes))
	}
	wantOutcomes := map[string]string{
		TemplateKeyTsconfig: OutcomeCreated,
		TemplateKeyVitest:   OutcomeSkippedMissingDep,
		TemplateKeyESLint:   OutcomeSkippedMissingDep,
		TemplateKeyPrettier: OutcomeSkippedMissingDep,
		TemplateKeyCI:       OutcomeCreated,
	}
	for _, o := range res.Outcomes {
		if want := wantOutcomes[o.Key]; want != o.Outcome {
			t.Errorf("key=%s outcome=%s, want %s", o.Key, o.Outcome, want)
		}
	}
	// Files actually present:
	for _, name := range []string{"tsconfig.json", ".github/workflows/ci.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to exist: %v", name, err)
		}
	}
}

func TestRunScaffoldTooling_FullDevDeps(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir,
		map[string]string{"typescript": "^5"},
		map[string]string{"vitest": "^1", "eslint": "^9", "prettier": "^3"},
		"^20")
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, o := range res.Outcomes {
		if o.Outcome != OutcomeCreated {
			t.Errorf("key=%s outcome=%s, want created (reason=%s)", o.Key, o.Outcome, o.Reason)
		}
		if o.Bytes == 0 {
			t.Errorf("key=%s bytes=0, want >0", o.Key)
		}
	}
	if !res.Detected.HasTypeScript || !res.Detected.HasVitest || !res.Detected.HasESLint || !res.Detected.HasPrettier {
		t.Errorf("Detected deps incomplete: %+v", res.Detected)
	}
	if res.Detected.NodeVersion != "20" {
		t.Errorf("NodeVersion = %q, want 20", res.Detected.NodeVersion)
	}
}

func TestRunScaffoldTooling_BrownfieldSkipExisting(t *testing.T) {
	dir := t.TempDir()
	preExisting := []byte(`{"compilerOptions":{"target":"ES5"}}`)
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), preExisting, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{TemplateKeyTsconfig},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Outcome != OutcomeSkippedExisting {
		t.Fatalf("expected single skipped_existing outcome, got %+v", res.Outcomes)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "tsconfig.json"))
	if string(got) != string(preExisting) {
		t.Errorf("tsconfig.json was modified — brownfield safety failed")
	}
}

func TestRunScaffoldTooling_ESLintLegacyDetection(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, nil, map[string]string{"eslint": "^9"}, "")
	if err := os.WriteFile(filepath.Join(dir, ".eslintrc.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{TemplateKeyESLint},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Outcome != OutcomeSkippedExisting {
		t.Fatalf("expected eslint skipped due to legacy .eslintrc.json, got %+v", res.Outcomes)
	}
	if _, err := os.Stat(filepath.Join(dir, "eslint.config.js")); !os.IsNotExist(err) {
		t.Errorf("eslint.config.js was created despite legacy config — brownfield safety failed")
	}
}

func TestRunScaffoldTooling_PrettierLegacyDetection(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, nil, map[string]string{"prettier": "^3"}, "")
	if err := os.WriteFile(filepath.Join(dir, "prettier.config.js"), []byte(`module.exports = {};`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{TemplateKeyPrettier},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Outcome != OutcomeSkippedExisting {
		t.Fatalf("expected prettier skipped due to legacy prettier.config.js, got %+v", res.Outcomes)
	}
}

func TestRunScaffoldTooling_SelectSubset(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, nil, map[string]string{"vitest": "^1"}, "")
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{"tsconfig", "vitest"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(res.Outcomes))
	}
	keys := []string{res.Outcomes[0].Key, res.Outcomes[1].Key}
	if keys[0] != "tsconfig" || keys[1] != "vitest" {
		t.Errorf("Outcomes order = %v, want [tsconfig, vitest]", keys)
	}
}

func TestRunScaffoldTooling_UnknownSelectKey(t *testing.T) {
	dir := t.TempDir()
	_, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{"tsconfig", "foo"},
	})
	if err == nil {
		t.Fatalf("expected error for unknown key, got nil")
	}
	// No files should be written when select fails.
	if _, statErr := os.Stat(filepath.Join(dir, "tsconfig.json")); !os.IsNotExist(statErr) {
		t.Errorf("tsconfig.json was created despite select error: %v", statErr)
	}
}

func TestRunScaffoldTooling_CIYAMLNodeVersionSubstitution(t *testing.T) {
	dir := t.TempDir()
	writePackageJSON(t, dir, nil, nil, ">=18.0.0")
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{TemplateKeyCI},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Outcome != OutcomeCreated {
		t.Fatalf("expected ci created, got %+v", res.Outcomes)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "node-version: '18'") {
		t.Errorf("CI YAML missing node-version: '18' substitution; got:\n%s", got)
	}
	// GitHub Actions expression must survive verbatim.
	if !strings.Contains(got, "ci-${{ github.ref }}") {
		t.Errorf("CI YAML did not preserve ${{ github.ref }} literal; got:\n%s", got)
	}
}

func TestRunScaffoldTooling_CIYAMLDefaultNodeVersion(t *testing.T) {
	dir := t.TempDir()
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{TemplateKeyCI},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Detected.NodeVersion != defaultNodeVersion {
		t.Errorf("Detected.NodeVersion = %q, want %q", res.Detected.NodeVersion, defaultNodeVersion)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".github/workflows/ci.yml"))
	if !strings.Contains(string(body), "node-version: '20'") {
		t.Errorf("CI YAML default Node version missing; got:\n%s", string(body))
	}
}

func TestRunScaffoldTooling_DryRun(t *testing.T) {
	dir := t.TempDir()
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{
		Workdir: dir,
		Select:  []string{TemplateKeyTsconfig, TemplateKeyCI},
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, o := range res.Outcomes {
		if o.Outcome != OutcomeCreated {
			t.Errorf("key=%s outcome=%s, want created", o.Key, o.Outcome)
		}
		if o.Bytes <= 0 {
			t.Errorf("key=%s bytes=%d, want >0 even in dry-run", o.Key, o.Bytes)
		}
		if o.Reason != "dry-run" {
			t.Errorf("key=%s reason=%q, want dry-run", o.Key, o.Reason)
		}
	}
	// Nothing should hit disk.
	if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote tsconfig.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github/workflows/ci.yml")); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote ci.yml: %v", err)
	}
}

func TestRunScaffoldTooling_MalformedPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{not"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected warning for malformed package.json")
	}
	// Tsconfig and CI still emit (no devDep gate).
	gotTsconfig := false
	for _, o := range res.Outcomes {
		if o.Key == TemplateKeyTsconfig && o.Outcome == OutcomeCreated {
			gotTsconfig = true
		}
	}
	if !gotTsconfig {
		t.Errorf("expected tsconfig to still emit despite malformed package.json")
	}
}

func TestRunScaffoldTooling_TemplatesByteForByte(t *testing.T) {
	// Each template literal must match the embedded fixture byte-for-byte —
	// future template edits land in the embed file only.
	dir := t.TempDir()
	writePackageJSON(t, dir, nil, map[string]string{"vitest": "^1", "eslint": "^9", "prettier": "^3"}, "")
	_, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cases := []struct {
		written  string
		embedded string
	}{
		{"tsconfig.json", "templates/tsconfig.json"},
		{"vitest.config.ts", "templates/vitest.config.ts"},
		{"eslint.config.js", "templates/eslint.config.js"},
		{".prettierrc", "templates/prettierrc.json"},
	}
	for _, tc := range cases {
		got, err := os.ReadFile(filepath.Join(dir, tc.written))
		if err != nil {
			t.Errorf("read %s: %v", tc.written, err)
			continue
		}
		want, err := templatesFS.ReadFile(tc.embedded)
		if err != nil {
			t.Errorf("read embed %s: %v", tc.embedded, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s emitted bytes do not match embedded fixture", tc.written)
		}
	}
}

func TestRunScaffoldTooling_UnknownWorkdir(t *testing.T) {
	_, err := RunScaffoldTooling(context.Background(), ScaffoldToolingOptions{Workdir: "/does/not/exist/anywhere"})
	if err == nil {
		t.Fatalf("expected error for unresolvable workdir")
	}
}
