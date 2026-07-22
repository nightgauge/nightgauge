package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeRepoConfig writes a minimal .nightgauge/config.yaml for a member repo.
func writeRepoConfig(t *testing.T, repoRoot, owner, repo string, project int) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "owner: " + owner + "\nowner_type: org\nrepo: " + repo + "\nproject:\n  number: " + itoa(project) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func writeWorkspaceManifest(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".vscode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .vscode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nightgauge-workspace.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestReposFromWorkspaceManifest_NToOne verifies the N:1 topology: many repos
// (as child dirs of the workspace root) sharing a single project, including the
// primary repo at path ".". This is the AcmeApp layout that previously failed
// (nightgauge#3769) — the scheduler must be scoped to all member repos.
func TestReposFromWorkspaceManifest_NToOne(t *testing.T) {
	root := t.TempDir() // acts as the acmeapp-infra repo root (path ".")
	writeRepoConfig(t, root, "nightgauge", "acmeapp-infra", 6)
	writeRepoConfig(t, filepath.Join(root, "acmeapp-flutter"), "nightgauge", "acmeapp-flutter", 6)
	writeRepoConfig(t, filepath.Join(root, "acmeapp-platform"), "nightgauge", "acmeapp-platform", 6)

	writeWorkspaceManifest(t, root, `workspace:
  name: AcmeApp
repositories:
  - name: acmeapp-infra
    path: .
    project_number: 6
  - name: acmeapp-flutter
    path: acmeapp-flutter
    project_number: 6
  - name: acmeapp-platform
    path: acmeapp-platform
    project_number: 6
`)

	got := reposFromWorkspaceManifest(root, "nightgauge", 6)
	if len(got) != 3 {
		t.Fatalf("expected 3 repos, got %d: %+v", len(got), got)
	}
	names := make([]string, 0, len(got))
	for _, rc := range got {
		if rc.Owner != "nightgauge" {
			t.Errorf("repo %s: owner = %q, want nightgauge", rc.Name, rc.Owner)
		}
		if rc.Project != 6 {
			t.Errorf("repo %s: project = %d, want 6", rc.Name, rc.Project)
		}
		names = append(names, rc.Name)
	}
	sort.Strings(names)
	want := []string{"acmeapp-flutter", "acmeapp-infra", "acmeapp-platform"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("repo names = %v, want %v", names, want)
		}
	}
	// Regression guard: never the bare folder-name repo.
	for _, rc := range got {
		if rc.Name == filepath.Base(root) {
			t.Fatalf("scan set must not include the folder-name repo %q", rc.Name)
		}
	}
}

// TestReposFromWorkspaceManifest_PerRepoProject verifies the 1:1 topology where
// each member repo targets its own project (nightgauge' layout).
func TestReposFromWorkspaceManifest_PerRepoProject(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, root, "nightgauge", "nightgauge", 1)
	writeRepoConfig(t, filepath.Join(root, "platform"), "nightgauge", "acme-platform", 2)

	writeWorkspaceManifest(t, root, `repositories:
  - name: nightgauge
    path: .
    project_number: 1
  - name: acme-platform
    path: platform
    project_number: 2
`)

	got := reposFromWorkspaceManifest(root, "nightgauge", 1)
	if len(got) != 2 {
		t.Fatalf("expected 2 repos, got %d: %+v", len(got), got)
	}
	byName := map[string]int{}
	for _, rc := range got {
		byName[rc.Name] = rc.Project
	}
	if byName["nightgauge"] != 1 {
		t.Errorf("nightgauge project = %d, want 1", byName["nightgauge"])
	}
	if byName["acme-platform"] != 2 {
		t.Errorf("acme-platform project = %d, want 2", byName["acme-platform"])
	}
}

// TestReposFromWorkspaceManifest_NoManifest returns nil so the caller falls back
// to legacy sibling/folder detection.
func TestReposFromWorkspaceManifest_NoManifest(t *testing.T) {
	root := t.TempDir()
	if got := reposFromWorkspaceManifest(root, "nightgauge", 6); got != nil {
		t.Fatalf("expected nil with no manifest, got %+v", got)
	}
}
