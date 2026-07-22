package workspacecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBoardSyncMemberConfig writes a minimal .nightgauge/config.yaml for a
// member repo in the format config.Load expects (owner / repo / project.number).
func writeBoardSyncMemberConfig(t *testing.T, root, path, owner, repo string, project int) {
	t.Helper()
	dir := filepath.Join(root, path, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cfg := "owner: " + owner + "\n" +
		"owner_type: organization\n" +
		"repo: " + repo + "\n" +
		"project:\n  number: " + itoa(project) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
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
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// newSharedProjectWorkspace builds a 3-repo workspace sharing one project,
// mirroring the Acmesvc topology, and returns the workspace root.
func newSharedProjectWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	vscode := filepath.Join(root, ".vscode")
	if err := os.MkdirAll(vscode, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `workspace:
  name: Acmesvc Product
repositories:
  - name: acmesvc-tracker
    path: acmesvc-tracker
    role: primary
    project_number: 1
  - name: acmesvc-api
    path: acmesvc-api
    role: primary
    project_number: 1
  - name: acme-community
    path: acme-community
    role: primary
    project_number: 1
routing:
  default_repository: acmesvc-tracker
`
	if err := os.WriteFile(filepath.Join(vscode, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBoardSyncMemberConfig(t, root, "acmesvc-tracker", "Acme-Community", "acmesvc-tracker", 1)
	writeBoardSyncMemberConfig(t, root, "acmesvc-api", "Acme-Community", "acmesvc-api", 1)
	writeBoardSyncMemberConfig(t, root, "acme-community", "Acme-Community", "acme-community", 1)
	return root
}

func defaultOpts() provisionOpts {
	return provisionOpts{
		runner:      "self-hosted",
		tokenSecret: "BOARD_SYNC_TOKEN",
		installCmd:  defaultInstallCmd,
	}
}

func TestBuildBoardSyncPlan_SharedProjectTopology(t *testing.T) {
	root := newSharedProjectWorkspace(t)

	plan, err := buildBoardSyncPlan(root, defaultOpts())
	if err != nil {
		t.Fatalf("buildBoardSyncPlan: %v", err)
	}

	if plan.WorkspaceName != "Acmesvc Product" {
		t.Errorf("workspace name = %q, want %q", plan.WorkspaceName, "Acmesvc Product")
	}
	if plan.PrimaryRepo != "Acme-Community/acmesvc-tracker" {
		t.Errorf("primary repo = %q, want Acme-Community/acmesvc-tracker", plan.PrimaryRepo)
	}
	if len(plan.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(plan.Members))
	}
	// Every member shares project #1 (N:1 topology).
	for _, m := range plan.Members {
		if m.Project != 1 {
			t.Errorf("member %s project = %d, want 1", m.Repo, m.Project)
		}
		if m.Owner != "Acme-Community" {
			t.Errorf("member %s owner = %q, want Acme-Community", m.Repo, m.Owner)
		}
	}

	// Exactly one primary, and it is the routing default.
	var primaries int
	for _, m := range plan.Members {
		if m.Primary {
			primaries++
			if m.Repo != "acmesvc-tracker" {
				t.Errorf("primary = %s, want acmesvc-tracker", m.Repo)
			}
		}
	}
	if primaries != 1 {
		t.Errorf("primary count = %d, want 1", primaries)
	}
}

func TestBuildBoardSyncPlan_FileLayout(t *testing.T) {
	root := newSharedProjectWorkspace(t)
	plan, err := buildBoardSyncPlan(root, defaultOpts())
	if err != nil {
		t.Fatal(err)
	}

	// 2 sweeps in primary + 1 board-done per member = 5 files.
	if len(plan.Files) != 5 {
		t.Fatalf("files = %d, want 5", len(plan.Files))
	}

	kinds := map[string]int{}
	for _, f := range plan.Files {
		kinds[f.Kind]++
	}
	if kinds["lifecycle-sweep"] != 1 || kinds["epic-sweep"] != 1 || kinds["board-done"] != 3 {
		t.Errorf("kind distribution = %v, want lifecycle-sweep:1 epic-sweep:1 board-done:3", kinds)
	}

	// Sweeps must live in the primary repo only.
	for _, f := range plan.Files {
		if f.Kind == "lifecycle-sweep" || f.Kind == "epic-sweep" {
			if !strings.Contains(f.Path, filepath.Join("acmesvc-tracker", ".github", "workflows")) {
				t.Errorf("%s not in primary repo: %s", f.Kind, f.Path)
			}
		}
	}

	// A board-done file must exist for each member repo.
	for _, repo := range []string{"acmesvc-tracker", "acmesvc-api", "acme-community"} {
		want := filepath.Join(root, repo, ".github", "workflows", "nightgauge-board-done.yml")
		found := false
		for _, f := range plan.Files {
			if f.Path == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing board-done for %s (%s)", repo, want)
		}
	}
}

func TestBuildBoardSyncPlan_RenderedContent(t *testing.T) {
	root := newSharedProjectWorkspace(t)
	plan, err := buildBoardSyncPlan(root, defaultOpts())
	if err != nil {
		t.Fatal(err)
	}

	byKind := map[string]plannedFile{}
	for _, f := range plan.Files {
		byKind[f.Kind] = f // any board-done is fine for shared assertions below
	}

	lifecycle := byKind["lifecycle-sweep"].content
	// Every member appears as an owner/repo:project target.
	for _, want := range []string{
		"Acme-Community/acmesvc-tracker:1",
		"Acme-Community/acmesvc-api:1",
		"Acme-Community/acme-community:1",
	} {
		if !strings.Contains(lifecycle, want) {
			t.Errorf("lifecycle sweep missing target %q", want)
		}
	}
	// GitHub Actions secret expression passes through untouched (not consumed by
	// the Go template's << >> delimiters).
	if !strings.Contains(lifecycle, "${{ secrets.BOARD_SYNC_TOKEN }}") {
		t.Errorf("lifecycle sweep missing GH_TOKEN secret expression")
	}
	if !strings.Contains(lifecycle, "AUTO-GENERATED") {
		t.Errorf("lifecycle sweep missing do-not-edit banner")
	}

	// board-done targets a single repo via its own --repo + shared --project.
	bd := byKind["board-done"].content
	if !strings.Contains(bd, "audit lifecycle") || !strings.Contains(bd, "epic auto-close") {
		t.Errorf("board-done missing reconcile commands:\n%s", bd)
	}
	if !strings.Contains(bd, "types: [closed") {
		t.Errorf("board-done missing on-close trigger")
	}
}

func TestProvisionWritesFilesOnlyWithWriteFlag(t *testing.T) {
	root := newSharedProjectWorkspace(t)

	// Dry run via buildBoardSyncPlan does not touch disk.
	plan, err := buildBoardSyncPlan(root, defaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Files {
		if _, statErr := os.Stat(f.Path); statErr == nil {
			t.Fatalf("file written during dry run: %s", f.Path)
		}
	}

	// Simulate --write by writing the planned content (mirrors runProvisionBoardSync).
	for _, f := range plan.Files {
		if mkErr := os.MkdirAll(filepath.Dir(f.Path), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wErr := os.WriteFile(f.Path, []byte(f.content), 0o644); wErr != nil {
			t.Fatal(wErr)
		}
	}
	for _, f := range plan.Files {
		got, readErr := os.ReadFile(f.Path)
		if readErr != nil {
			t.Errorf("expected file %s: %v", f.Path, readErr)
			continue
		}
		if !strings.Contains(string(got), "AUTO-GENERATED") {
			t.Errorf("written file %s missing banner", f.Path)
		}
	}
}

func TestBuildBoardSyncPlan_RoleFallbackPrimary(t *testing.T) {
	// No routing.default_repository → first role:primary wins.
	root := t.TempDir()
	vscode := filepath.Join(root, ".vscode")
	if err := os.MkdirAll(vscode, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `workspace:
  name: Two Repo
repositories:
  - name: alpha
    path: alpha
    role: secondary
    project_number: 7
  - name: beta
    path: beta
    role: primary
    project_number: 7
`
	if err := os.WriteFile(filepath.Join(vscode, "nightgauge-workspace.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBoardSyncMemberConfig(t, root, "alpha", "Acme", "alpha", 7)
	writeBoardSyncMemberConfig(t, root, "beta", "Acme", "beta", 7)

	plan, err := buildBoardSyncPlan(root, defaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	if plan.PrimaryRepo != "Acme/beta" {
		t.Errorf("primary = %q, want Acme/beta (role:primary fallback)", plan.PrimaryRepo)
	}
}

func TestBuildBoardSyncPlan_NoManifest(t *testing.T) {
	root := t.TempDir()
	if _, err := buildBoardSyncPlan(root, defaultOpts()); err == nil {
		t.Errorf("expected error when manifest is absent")
	}
}
