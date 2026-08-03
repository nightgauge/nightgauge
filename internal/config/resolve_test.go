package config

import (
	"os"
	"path/filepath"
	"strings"
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

	report, err := FindWorkspaceProjectMappingMismatches(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("got %d mismatches, want 1: %+v", len(report.Mismatches), report.Mismatches)
	}
	m := report.Mismatches[0]
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

	report, err := FindWorkspaceProjectMappingMismatches(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.OK() {
		t.Fatalf("want a clean report, got %+v", report)
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

// #280: a repo the check could not evaluate must be REPORTED, not skipped.
// The pre-fix version dropped it with a comment claiming the resolver's own
// error surfaced it — but that error was discarded on the same line, so a
// workspace whose siblings had no runtime mapping reported a clean result and
// doctor rendered it as "workspace manifest and runtime config agree".
func TestFindWorkspaceProjectMappingMismatches_UnresolvableIsReported(t *testing.T) {
	dir := t.TempDir()
	vscodeDir := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	// platform declares a board in the manifest; the runtime config has no
	// mapping for it at all. This is the live shape of the nightgauge
	// workspace that #280 was filed from.
	manifest := `
workspace:
  name: test-workspace
repositories:
  - name: acme/platform
    path: ../platform
    project_number: 4
`
	if err := os.WriteFile(filepath.Join(vscodeDir, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := &Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3} // no Autonomous.Repositories entry

	report, err := FindWorkspaceProjectMappingMismatches(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.OK() {
		t.Fatal("a repo with no runtime mapping is not a clean bill of health")
	}
	if len(report.Unresolvable) != 1 {
		t.Fatalf("want 1 unresolvable repo, got %+v", report.Unresolvable)
	}
	u := report.Unresolvable[0]
	if u.Repo != "acme/platform" || u.ManifestProject != 4 {
		t.Errorf("got %+v, want acme/platform manifest project 4", u)
	}
	if u.Err == "" {
		t.Error("the resolver's message must be carried, not discarded")
	}
	// It must reach the string-rendering callers (doctor, scheduler) too.
	problems, err := CheckWorkspaceProjectMapping(cfg, dir)
	if err != nil {
		t.Fatalf("CheckWorkspaceProjectMapping: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "acme/platform") {
		t.Fatalf("unresolvable repo must surface to string callers; got %v", problems)
	}
}
