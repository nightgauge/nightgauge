package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRepoConfig(t *testing.T, root, owner, repo string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "owner: " + owner + "\ndefaultRepo: " + repo + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkspaceRepoPaths_DiscoversSiblingsAndManifest covers the registry CLI
// mode had to build for itself (#882). Without it the CLI's repo-path resolver
// stays nil, and a run queued with `queue add <N> --repo <other/repo>` has no
// way to name the target repo's filesystem root.
func TestWorkspaceRepoPaths_DiscoversSiblingsAndManifest(t *testing.T) {
	parent := t.TempDir()
	launch := filepath.Join(parent, "launch")
	sibling := filepath.Join(parent, "sibling")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	for _, d := range []string{launch, sibling, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRepoConfig(t, launch, "owner", "launch")
	writeRepoConfig(t, sibling, "owner", "sibling")
	writeRepoConfig(t, elsewhere, "owner", "elsewhere")

	// A manifest entry pointing outside the parent directory — a repo the
	// sibling scan alone cannot see.
	vscode := filepath.Join(launch, ".vscode")
	if err := os.MkdirAll(vscode, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "repositories:\n  - name: elsewhere\n    path: " + elsewhere + "\n"
	if err := os.WriteFile(filepath.Join(vscode, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := WorkspaceRepoPaths(launch)

	for slug, want := range map[string]string{
		"owner/launch":    launch,
		"owner/sibling":   sibling,
		"owner/elsewhere": elsewhere,
	} {
		got, ok := paths[slug]
		if !ok {
			t.Errorf("%s missing from the workspace repo registry: %v", slug, paths)
			continue
		}
		wantAbs, _ := filepath.Abs(want)
		gotAbs, _ := filepath.Abs(got)
		if gotAbs != wantAbs {
			t.Errorf("%s = %q, want %q", slug, gotAbs, wantAbs)
		}
	}

	// A repo the workspace does not contain is ABSENT, not defaulted. Absence
	// is what makes the scheduler refuse instead of rooting the run at the
	// launch repo.
	if p, ok := paths["owner/unknown"]; ok {
		t.Errorf("an unknown repo resolved to %q; it must be absent so the run fails closed (#882)", p)
	}
}
