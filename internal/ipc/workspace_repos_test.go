package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/workspacemanifest/wsmfixture"
)

// newRepoWorkspace lays out a workspace whose manifest is the realistic
// fixture, plus sibling git checkouts for the named repos, and returns a Server
// rooted there.
func newRepoWorkspace(t *testing.T, siblings ...string) (*Server, string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "alpha")
	for _, d := range []string{filepath.Join(root, ".vscode"), filepath.Join(root, ".git")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(wsmfixture.Realistic), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range siblings {
		if err := os.MkdirAll(filepath.Join(parent, s, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{workspaceRoot: root, methods: map[string]Handler{}}
	return s, root
}

func TestWorkspaceRepoList_ReportsConfiguredEntries(t *testing.T) {
	s, _ := newRepoWorkspace(t, "beta", "gamma")

	raw, err := s.handleWorkspaceRepoList(context.Background(), nil)
	if err != nil {
		t.Fatalf("repoList: %v", err)
	}
	res := raw.(*WorkspaceRepoListResult)
	if res.Unmanaged {
		t.Fatal("a workspace with a manifest must not report Unmanaged")
	}

	names := map[string]WorkspaceRepoDescriptor{}
	for _, d := range res.Configured {
		names[d.Name] = d
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if _, ok := names[want]; !ok {
			t.Errorf("configured list is missing %q; got %+v", want, res.Configured)
		}
	}
	// The fixture points routing.default_repository at alpha and one pattern at
	// gamma. Both must surface, so removal can warn before orphaning them.
	if refs := names["alpha"].RoutingRefs; len(refs) == 0 {
		t.Error("alpha is routing.default_repository but reported no routing refs")
	}
	if refs := names["gamma"].RoutingRefs; len(refs) == 0 {
		t.Error("gamma is a pattern's preferred_repo but reported no routing refs")
	}
	if names["beta"].RoutingRefs != nil {
		t.Errorf("beta is referenced by nothing; got refs %v", names["beta"].RoutingRefs)
	}
}

// A repository listed in the manifest whose directory has gone missing must be
// reported as such rather than silently rendered as healthy.
func TestWorkspaceRepoList_FlagsAMissingCheckout(t *testing.T) {
	s, _ := newRepoWorkspace(t, "beta") // gamma deliberately absent

	raw, err := s.handleWorkspaceRepoList(context.Background(), nil)
	if err != nil {
		t.Fatalf("repoList: %v", err)
	}
	res := raw.(*WorkspaceRepoListResult)
	for _, d := range res.Configured {
		if d.Name == "gamma" && d.Exists {
			t.Error("gamma has no directory but was reported as existing")
		}
		if d.Name == "beta" && !d.Exists {
			t.Error("beta has a directory but was reported as missing")
		}
	}
}

func TestWorkspaceRepoList_NoManifestIsUnmanagedNotAnError(t *testing.T) {
	root := t.TempDir()
	s := &Server{workspaceRoot: root, methods: map[string]Handler{}}

	raw, err := s.handleWorkspaceRepoList(context.Background(), nil)
	if err != nil {
		t.Fatalf("a workspace with no manifest is single-repo mode, not an error: %v", err)
	}
	if res := raw.(*WorkspaceRepoListResult); !res.Unmanaged {
		t.Error("expected Unmanaged=true when no manifest exists")
	}
}

// project_number: 0 must be unreachable from the panel. The UI is expected to
// refuse submission, but the daemon refuses too — a guard that lives only in
// the UI is not a guard.
func TestWorkspaceRepoAdd_RefusesANegativeProject(t *testing.T) {
	s, _ := newRepoWorkspace(t, "delta")
	params, _ := json.Marshal(WorkspaceRepoAddParams{Name: "delta", Path: "../delta", Project: -1})

	_, err := s.handleWorkspaceRepoAdd(context.Background(), params)
	if err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("error = %v, want a positive-integer refusal", err)
	}
}

func TestWorkspaceRepoAdd_WritesAValidEntry(t *testing.T) {
	s, root := newRepoWorkspace(t, "delta")
	params, _ := json.Marshal(WorkspaceRepoAddParams{Name: "delta", Path: "../delta", Role: "primary", Project: 9})

	raw, err := s.handleWorkspaceRepoAdd(context.Background(), params)
	if err != nil {
		t.Fatalf("repoAdd: %v", err)
	}
	res := raw.(*WorkspaceRepoAddResult)
	if !res.OK || res.Entry.Name != "delta" || res.Entry.ProjectNumber != 9 {
		t.Errorf("unexpected result %+v", res)
	}
	// Adding a repo does not install board-sync automation; the operator has to
	// be told, or the new repo's board silently never reconciles.
	if !strings.Contains(res.BoardSyncNote, "provision-board-sync") {
		t.Errorf("BoardSyncNote = %q, want it to name provision-board-sync", res.BoardSyncNote)
	}

	body, rerr := os.ReadFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(body), "name: delta") {
		t.Error("delta is not in the manifest on disk")
	}
}

// Removing a repository routing still points at would leave routing naming
// something the manifest does not declare.
func TestWorkspaceRepoRemove_RefusesAnOrphaningRemovalAndNamesTheReference(t *testing.T) {
	s, _ := newRepoWorkspace(t, "beta", "gamma")
	params, _ := json.Marshal(WorkspaceRepoRemoveParams{Name: "gamma"})

	_, err := s.handleWorkspaceRepoRemove(context.Background(), params)
	if err == nil {
		t.Fatal("removing a routing-referenced repository must be refused")
	}
	if !strings.Contains(err.Error(), "routing.patterns[web].preferred_repo") {
		t.Errorf("error = %q, want it to NAME the routing reference", err)
	}
}

func TestWorkspaceRepoRemove_ForceOverridesTheRoutingRefusal(t *testing.T) {
	s, root := newRepoWorkspace(t, "beta", "gamma")
	params, _ := json.Marshal(WorkspaceRepoRemoveParams{Name: "gamma", Force: true})

	if _, err := s.handleWorkspaceRepoRemove(context.Background(), params); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"))
	if strings.Contains(string(body), "name: gamma") {
		t.Error("gamma is still in the manifest after a forced remove")
	}
}

func TestWorkspaceRepoRemove_UnreferencedRepoNeedsNoForce(t *testing.T) {
	s, root := newRepoWorkspace(t, "beta", "gamma")
	params, _ := json.Marshal(WorkspaceRepoRemoveParams{Name: "beta"})

	if _, err := s.handleWorkspaceRepoRemove(context.Background(), params); err != nil {
		t.Fatalf("remove: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"))
	if strings.Contains(string(body), "name: beta") {
		t.Error("beta is still in the manifest")
	}
	// The comment block owned by the first entry documents the whole list and
	// must survive removal of a later entry.
	if !strings.Contains(string(body), "project_number") {
		t.Error("the manifest's explanatory comment block was destroyed by removal")
	}
}

func TestWorkspaceRepoRemove_UnknownRepoErrors(t *testing.T) {
	s, _ := newRepoWorkspace(t)
	params, _ := json.Marshal(WorkspaceRepoRemoveParams{Name: "nope"})
	if _, err := s.handleWorkspaceRepoRemove(context.Background(), params); err == nil {
		t.Fatal("removing a repository that is not in the manifest must error")
	}
}

// A VSCode workspace folder that is NOT a sibling of the workspace root is
// still an unwatched repository. The sibling scan cannot reach it, so the
// caller passes it explicitly and it must be resolved the same way.
func TestWorkspaceRepoList_IncludesCallerSuppliedFoldersOutsideTheSiblingScan(t *testing.T) {
	s, root := newRepoWorkspace(t)

	// A checkout somewhere else entirely, with a git remote the spec resolver
	// can read.
	far := filepath.Join(t.TempDir(), "faraway")
	if err := os.MkdirAll(filepath.Join(far, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = git@github.com:acme/faraway.git\n"
	if err := os.WriteFile(filepath.Join(far, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(WorkspaceRepoListParams{Folders: []string{far}})
	raw, err := s.handleWorkspaceRepoList(context.Background(), params)
	if err != nil {
		t.Fatalf("repoList: %v", err)
	}
	res := raw.(*WorkspaceRepoListResult)

	var found *WorkspaceRepoCandidate
	for i := range res.Candidates {
		if res.Candidates[i].Name == "faraway" {
			found = &res.Candidates[i]
		}
	}
	if found == nil {
		t.Fatalf("a workspace folder outside the sibling scan was not offered as a candidate; got %+v", res.Candidates)
	}
	// Its path must be relative to the workspace root, like every other entry.
	rel, _ := filepath.Rel(root, far)
	if found.Path != rel {
		t.Errorf("Path = %q, want %q (relative to the workspace root)", found.Path, rel)
	}
}

// Malformed params must degrade discovery, not blank the section.
func TestWorkspaceRepoList_ToleratesMalformedParams(t *testing.T) {
	s, _ := newRepoWorkspace(t, "beta")
	raw, err := s.handleWorkspaceRepoList(context.Background(), json.RawMessage(`{"folders": "not-an-array"}`))
	if err != nil {
		t.Fatalf("malformed params must not fail the listing: %v", err)
	}
	if len(raw.(*WorkspaceRepoListResult).Configured) == 0 {
		t.Error("configured list was blanked by malformed params")
	}
}
