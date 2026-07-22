package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestResolveSchedulerIdentity_RootConfigWins verifies that a root config with a
// usable Owner+ProjectNumber is used unchanged and the manifest is ignored. This
// is the single-repo / configured-multi-repo no-regression path (#3860 AC #3).
func TestResolveSchedulerIdentity_RootConfigWins(t *testing.T) {
	root := t.TempDir()
	// A manifest pointing at a DIFFERENT identity must NOT win over root config.
	writeRepoConfig(t, filepath.Join(root, "other"), "OtherOwner", "other-repo", 99)
	writeWorkspaceManifest(t, root, `repositories:
  - name: other-repo
    path: other
    project_number: 99
routing:
  default_repository: other-repo
`)

	cfg := &config.Config{Owner: "nightgauge", OwnerType: "org", ProjectNumber: 1}
	got := resolveSchedulerIdentity(root, cfg)

	if got.Source != "root-config" {
		t.Fatalf("Source = %q, want root-config", got.Source)
	}
	if got.Owner != "nightgauge" || got.ProjectNumber != 1 || got.OwnerType != "org" {
		t.Fatalf("identity = %+v, want nightgauge/org/1", got)
	}
	if !got.Resolvable() {
		t.Fatalf("Resolvable() = false, want true")
	}
}

// TestResolveSchedulerIdentity_ManifestDefaultRepository verifies that with no
// usable root config, the manifest's routing.default_repository member is
// selected and its own config supplies the canonical owner/project (#3860 AC #1).
func TestResolveSchedulerIdentity_ManifestDefaultRepository(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, filepath.Join(root, "acmesvc-tracker"), "acmebot", "acmesvc-tracker", 1)
	writeRepoConfig(t, filepath.Join(root, "acmesvc-api"), "acmebot", "acmesvc-api", 1)

	writeWorkspaceManifest(t, root, `repositories:
  - name: acmesvc-api
    path: acmesvc-api
    role: primary
    project_number: 1
  - name: acmesvc-tracker
    path: acmesvc-tracker
    role: primary
    project_number: 1
routing:
  default_repository: acmesvc-tracker
`)

	// cfg = DefaultConfig() shape: Owner set, ProjectNumber 0 (no root config).
	cfg := config.DefaultConfig()
	got := resolveSchedulerIdentity(root, cfg)

	if got.Source != "manifest:acmesvc-tracker" {
		t.Fatalf("Source = %q, want manifest:acmesvc-tracker", got.Source)
	}
	if got.Owner != "acmebot" || got.ProjectNumber != 1 || got.OwnerType != "org" {
		t.Fatalf("identity = %+v, want acmebot/org/1", got)
	}
}

// TestResolveSchedulerIdentity_ManifestRolePrimaryFallback verifies that with no
// default_repository, the first role:primary member is selected (#3860 AC #1).
func TestResolveSchedulerIdentity_ManifestRolePrimaryFallback(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, filepath.Join(root, "member-a"), "AcmeOrg", "member-a", 5)
	writeRepoConfig(t, filepath.Join(root, "member-b"), "AcmeOrg", "member-b", 5)

	writeWorkspaceManifest(t, root, `repositories:
  - name: member-a
    path: member-a
    project_number: 5
  - name: member-b
    path: member-b
    role: primary
    project_number: 5
`)

	got := resolveSchedulerIdentity(root, config.DefaultConfig())
	if got.Source != "manifest:member-b" {
		t.Fatalf("Source = %q, want manifest:member-b (first role:primary)", got.Source)
	}
	if got.Owner != "AcmeOrg" || got.ProjectNumber != 5 {
		t.Fatalf("identity = %+v, want AcmeOrg/5", got)
	}
}

// TestResolveSchedulerIdentity_ManifestFirstEntryFallback verifies that with no
// default_repository and no role, the first repo entry is selected (#3860 AC #1).
func TestResolveSchedulerIdentity_ManifestFirstEntryFallback(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, filepath.Join(root, "first"), "AcmeOrg", "first", 7)
	writeRepoConfig(t, filepath.Join(root, "second"), "AcmeOrg", "second", 7)

	writeWorkspaceManifest(t, root, `repositories:
  - name: first
    path: first
    project_number: 7
  - name: second
    path: second
    project_number: 7
`)

	got := resolveSchedulerIdentity(root, config.DefaultConfig())
	if got.Source != "manifest:first" {
		t.Fatalf("Source = %q, want manifest:first", got.Source)
	}
	if got.ProjectNumber != 7 {
		t.Fatalf("ProjectNumber = %d, want 7", got.ProjectNumber)
	}
}

// TestResolveSchedulerIdentity_MemberProjectFromManifestEntry verifies that when
// the selected member's config lacks a ProjectNumber, the manifest entry's
// project_number is used as the fallback.
func TestResolveSchedulerIdentity_MemberProjectFromManifestEntry(t *testing.T) {
	root := t.TempDir()
	// Member config supplies owner but ProjectNumber 0 (no project block).
	memberDir := filepath.Join(root, "member")
	writeRepoConfigNoProject(t, memberDir, "AcmeOrg", "member")

	writeWorkspaceManifest(t, root, `repositories:
  - name: member
    path: member
    project_number: 42
`)

	got := resolveSchedulerIdentity(root, config.DefaultConfig())
	if got.Source != "manifest:member" {
		t.Fatalf("Source = %q, want manifest:member", got.Source)
	}
	if got.ProjectNumber != 42 {
		t.Fatalf("ProjectNumber = %d, want 42 (from manifest entry)", got.ProjectNumber)
	}
	if got.Owner != "AcmeOrg" {
		t.Fatalf("Owner = %q, want AcmeOrg", got.Owner)
	}
}

// TestResolveSchedulerIdentity_Unresolvable verifies that with no root config and
// no manifest, the identity is unresolved with a non-empty Detail that drives the
// startup warning and the improved IPC error (#3860 AC #2).
func TestResolveSchedulerIdentity_Unresolvable(t *testing.T) {
	root := t.TempDir() // empty — no config, no manifest

	got := resolveSchedulerIdentity(root, config.DefaultConfig())
	if got.Source != "none" {
		t.Fatalf("Source = %q, want none", got.Source)
	}
	if got.ProjectNumber != 0 {
		t.Fatalf("ProjectNumber = %d, want 0", got.ProjectNumber)
	}
	if got.Detail == "" {
		t.Fatalf("Detail is empty, want a reason naming the missing config")
	}
	if got.Resolvable() {
		t.Fatalf("Resolvable() = true, want false")
	}
}

// TestResolveSchedulerIdentity_ManifestMemberNoOwner verifies that a manifest
// member yielding no owner+project leaves the identity unresolved (AC #2).
func TestResolveSchedulerIdentity_ManifestMemberNoOwner(t *testing.T) {
	root := t.TempDir()
	// Manifest references a member dir with no config.yaml and no project_number.
	writeWorkspaceManifest(t, root, `repositories:
  - name: ghost
    path: ghost
`)

	got := resolveSchedulerIdentity(root, config.DefaultConfig())
	if got.Source != "none" {
		t.Fatalf("Source = %q, want none", got.Source)
	}
	if got.Detail == "" {
		t.Fatalf("Detail is empty, want a reason naming the unresolved member")
	}
}

// writeRepoConfigNoProject writes a member config with an owner but no project
// block, so resolveSchedulerIdentity must fall back to the manifest entry's
// project_number.
func writeRepoConfigNoProject(t *testing.T, repoRoot, owner, repo string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "owner: " + owner + "\nowner_type: org\nrepo: " + repo + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
