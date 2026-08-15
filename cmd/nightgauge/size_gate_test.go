package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeProductionShapeConfig writes a .nightgauge/config.yaml in the exact
// shape the repository ships (top-level owner plus a project: block carrying
// owner/repo), which is what config.Load resolves DefaultRepo from.
func writeProductionShapeConfig(t *testing.T, dir string) {
	t.Helper()
	nightgaugeDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(nightgaugeDir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	body := "owner: nightgauge\nowner_type: org\nproject:\n  owner: nightgauge\n  repo: nightgauge\n"
	if err := os.WriteFile(filepath.Join(nightgaugeDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

// writeOwnerOnlyConfig writes a config that declares an owner but no repo at
// all, so no back-fill can supply --repo.
func writeOwnerOnlyConfig(t *testing.T, dir string) {
	t.Helper()
	nightgaugeDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(nightgaugeDir, 0o755); err != nil {
		t.Fatalf("mkdir .nightgauge: %v", err)
	}
	body := "owner: nightgauge\nowner_type: org\n"
	if err := os.WriteFile(filepath.Join(nightgaugeDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

// findSubcommand walks a path of subcommand names from root and returns the
// leaf command, failing the test when any hop is missing.
func findSubcommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cur := root
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("subcommand %q not found under %q", name, cur.Name())
		}
		cur = next
	}
	return cur
}

// TestSizeGateCheck_ConfigRepoBackfill is a regression test for #536: the
// size-gate check subcommand registered --repo with a bare
// cmd.Flags().StringVar, which does NOT carry repoBackfillAnnotation, so the
// root PersistentPreRunE back-filled --owner but never --repo. splitRepo then
// produced the slug "nightgauge/" and every issue fetch failed with "Could
// not resolve to a Repository with the name 'nightgauge/'".
//
// The assertion MUST run through the assembled root command's
// PersistentPreRunE — building sizeGateCheckCmd() in isolation never
// exercises the back-fill and would pass against the unfixed binary (the same
// trap documented on TestAttentionListRootCommand_IgnoresBareConfigRepo).
func TestSizeGateCheck_ConfigRepoBackfill(t *testing.T) {
	dir := chdirTemp(t)
	writeProductionShapeConfig(t, dir)

	root := rootCmd()
	check := findSubcommand(t, root, "size-gate", "check")

	if err := root.PersistentPreRunE(check, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}

	gotOwner := check.Flags().Lookup("owner").Value.String()
	gotRepo := check.Flags().Lookup("repo").Value.String()

	if gotOwner != "nightgauge" {
		t.Errorf("--owner after back-fill = %q, want %q", gotOwner, "nightgauge")
	}
	if gotRepo != "nightgauge" {
		t.Errorf("--repo after back-fill = %q, want %q — size-gate check must register --repo via repoNameFlag so PersistentPreRunE back-fills it (#536)", gotRepo, "nightgauge")
	}

	// AC-2: the slug handed to the GitHub API must be well formed — no
	// trailing "/" and no empty segment on either side.
	ownerPart, repoPart := splitRepo(gotOwner, gotRepo)
	slug := ownerPart + "/" + repoPart
	if strings.HasSuffix(slug, "/") {
		t.Errorf("resolved slug %q ends with %q — repo segment is empty", slug, "/")
	}
	for _, seg := range strings.Split(slug, "/") {
		if strings.TrimSpace(seg) == "" {
			t.Errorf("resolved slug %q has an empty segment", slug)
		}
	}
	if slug != "nightgauge/nightgauge" {
		t.Errorf("resolved slug = %q, want %q", slug, "nightgauge/nightgauge")
	}
}

// TestSizeGateCheck_RepoNotConfigured covers AC-4: when nothing supplies a
// repo, the command must fail fast with an actionable message naming both
// remedies instead of issuing a GitHub lookup for "nightgauge/".
func TestSizeGateCheck_RepoNotConfigured(t *testing.T) {
	dir := chdirTemp(t)
	writeOwnerOnlyConfig(t, dir)

	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"size-gate", "check", "--issue", "520", "--config", filepath.Join(dir, ".nightgauge", "config.yaml")})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error when no repo is configured, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "repo not configured") {
		t.Errorf("error = %q, want it to mention %q", msg, "repo not configured")
	}
	if !strings.Contains(msg, "--repo") {
		t.Errorf("error = %q, want it to name the --repo remedy", msg)
	}
	if !strings.Contains(msg, "config.yaml") {
		t.Errorf("error = %q, want it to name the config.yaml remedy", msg)
	}
	// The guard must return before any fetch is attempted: a GitHub lookup
	// would surface as a "fetch issue" / "Could not resolve to a Repository"
	// error instead of the guard's message.
	if strings.Contains(msg, "fetch issue") || strings.Contains(msg, "Could not resolve to a Repository") {
		t.Errorf("guard did not short-circuit before the GitHub fetch: %q", msg)
	}
}
