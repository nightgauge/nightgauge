package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRepoProjectNumber_LocalRepo(t *testing.T) {
	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 7}
	got, err := ResolveRepoProjectNumber(cfg, "acme", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 7 {
		t.Errorf("got %d, want 7 (local repo's cfg.ProjectNumber)", got)
	}
}

func TestResolveRepoProjectNumber_CrossRepoMapped(t *testing.T) {
	cfg := &Config{
		Owner:       "acme",
		DefaultRepo: "web",
		Autonomous: &AutonomousConfig{
			Repositories: map[string]*RepositoryConfig{
				"acme/platform": {ProjectNumber: 12},
			},
		},
	}
	got, err := ResolveRepoProjectNumber(cfg, "acme", "platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 12 {
		t.Errorf("got %d, want 12", got)
	}
}

func TestResolveRepoProjectNumber_CrossRepoUnmapped(t *testing.T) {
	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 7}
	_, err := ResolveRepoProjectNumber(cfg, "acme", "platform")
	if err == nil {
		t.Fatal("expected an error for an unmapped cross-repo target")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a non-empty error naming the config path to fix")
	}
}

func TestFindWorkspaceProjectMappingMismatches(t *testing.T) {
	dir := t.TempDir()
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	manifest := `
workspace:
  name: test-workspace
repositories:
  - name: acme/platform
    path: .
    project_number: 1
`
	if err := os.WriteFile(filepath.Join(vscodeDir, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := &Config{
		Owner:       "acme",
		DefaultRepo: "web",
		Autonomous: &AutonomousConfig{
			Repositories: map[string]*RepositoryConfig{
				"acme/platform": {ProjectNumber: 4},
			},
		},
	}

	mismatches, err := FindWorkspaceProjectMappingMismatches(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("got %d mismatches, want 1: %+v", len(mismatches), mismatches)
	}
	m := mismatches[0]
	if m.Repo != "acme/platform" || m.ManifestProject != 1 || m.ResolvedProject != 4 {
		t.Errorf("got %+v, want {acme/platform 1 4}", m)
	}
}

func TestFindWorkspaceProjectMappingMismatches_Agree(t *testing.T) {
	dir := t.TempDir()
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	manifest := `
workspace:
  name: test-workspace
repositories:
  - name: acme/platform
    path: .
    project_number: 4
`
	if err := os.WriteFile(filepath.Join(vscodeDir, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := &Config{
		Owner:       "acme",
		DefaultRepo: "web",
		Autonomous: &AutonomousConfig{
			Repositories: map[string]*RepositoryConfig{
				"acme/platform": {ProjectNumber: 4},
			},
		},
	}

	mismatches, err := FindWorkspaceProjectMappingMismatches(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("got %d mismatches, want 0: %+v", len(mismatches), mismatches)
	}
}

func TestFindWorkspaceProjectMappingMismatches_NoManifest(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 4}
	_, err := FindWorkspaceProjectMappingMismatches(cfg, dir)
	if err == nil {
		t.Fatal("expected an error when no workspace manifest exists")
	}
}
