package ipc

// Workspace repository management over IPC (#705).
//
// The Settings panel can configure a repository but could not change WHICH
// repositories exist: the only repo-shaped control chose among entries that
// were already listed. Bringing a repo under management meant hand-editing
// .vscode/nightgauge-workspace.yaml and reloading the window, which is
// documented nowhere in the UI.
//
// These three methods front the deterministic writer. The panel never parses,
// serializes or edits manifest YAML — it asks for a list and requests
// mutations, and every guard (duplicate name, real git checkout, non-zero
// project) lives once in workspacemanifest.AddRepo.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/workspacemanifest"
)

// boardSyncNote is surfaced after every successful add. Adding a repository
// does not install the board-sync workflows into it — provision-board-sync
// generates those from the manifest and must be re-run — and an operator who
// assumes otherwise gets a repo whose board silently never reconciles.
const boardSyncNote = "Board-sync automation is generated from the manifest and is not installed automatically. " +
	"Run `nightgauge workspace provision-board-sync --write` to refresh it for the new repository."

// resolveProjectFor funnels through the single authoritative repo→project
// resolver, per the single-resolver contract in docs/MULTI_REPO_WORKSPACE.md.
// Never reads repositories[].project_number directly: that field is not an
// independent authority, and `nightgauge doctor` fails when it disagrees with
// the resolved value.
func resolveProjectFor(root, owner, name string) (int, error) {
	cfg, err := config.Load(root)
	if err != nil || cfg == nil {
		return 0, fmt.Errorf("no config loaded for repo→project resolution: %w", err)
	}
	if owner == "" {
		owner = cfg.Owner
	}
	return config.ResolveRepoProjectNumber(cfg, config.RepoProjectQuery{
		Owner:    owner,
		Repo:     name,
		StartDir: root,
	})
}

func splitSpec(spec string) (owner, name string) {
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return "", spec
}

// describeRepo enriches one manifest entry for display.
func describeRepo(root string, m *workspacemanifest.Manifest, e workspacemanifest.Entry) WorkspaceRepoDescriptor {
	d := WorkspaceRepoDescriptor{
		Name:          e.Name,
		Path:          e.Path,
		Role:          e.Role,
		ProjectNumber: e.ProjectNumber,
	}

	abs := m.ResolveEntryPath(e.Path)
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		d.Exists = true
	}

	if n, err := resolveProjectFor(root, "", e.Name); err == nil {
		d.ResolvedProject = n
	}

	if m.RoutingDefault() == e.Name {
		d.RoutingRefs = append(d.RoutingRefs, "routing.default_repository")
	}
	for _, id := range m.RoutingPatternsFor(e.Name) {
		d.RoutingRefs = append(d.RoutingRefs, fmt.Sprintf("routing.patterns[%s].preferred_repo", id))
	}
	return d
}

// handleWorkspaceRepoList implements workspace.repoList.
func (s *Server) handleWorkspaceRepoList(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var params WorkspaceRepoListParams
	if len(raw) > 0 {
		// A malformed params blob must not fail the whole listing: the folder
		// hint is an enrichment, so losing it should degrade discovery rather
		// than blank the section.
		_ = json.Unmarshal(raw, &params)
	}

	root := s.workspaceRootPath()
	path := workspacemanifest.ManifestPath(root)

	m, err := workspacemanifest.Load(path)
	if err != nil {
		// No manifest is a legitimate state (single-repo mode), not an
		// error the panel should render as a failure.
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			return &WorkspaceRepoListResult{ManifestPath: path, Unmanaged: true}, nil
		}
		return nil, err
	}

	out := &WorkspaceRepoListResult{ManifestPath: path}
	for _, e := range m.Entries {
		out.Configured = append(out.Configured, describeRepo(root, m, e))
	}

	// Candidates come from the coverage-gap producer's own scanner rather than
	// a second implementation, so the panel and the Action Center cannot
	// disagree about which folders exist — unioned with the caller's own folder
	// list, which reaches workspace folders the sibling scan cannot.
	paths := map[string]string{}
	order := []string{}
	remember := func(spec, path string) {
		if spec == "" {
			return
		}
		if _, seen := paths[spec]; seen {
			if path != "" {
				paths[spec] = path
			}
			return
		}
		paths[spec] = path
		order = append(order, spec)
	}

	if discovered, derr := sweep.DiscoverLocalCheckouts(root); derr == nil {
		for _, spec := range discovered {
			p := ""
			if e, eerr := workspacemanifest.DeriveEntry(m, spec, "primary"); eerr == nil {
				p = e.Path
			}
			remember(spec, p)
		}
	}
	for _, dir := range params.Folders {
		spec, ok := sweep.RepoSpecForDir(dir)
		if !ok {
			continue
		}
		p := ""
		if rel, rerr := filepath.Rel(root, dir); rerr == nil {
			p = rel
		}
		remember(spec, p)
	}

	configured := sweep.ConfiguredRepos(root)
	for _, spec := range order {
		owner, name := splitSpec(spec)
		if _, already := m.Find(name); already {
			continue
		}
		if repoInList(configured, spec) {
			continue
		}
		c := WorkspaceRepoCandidate{Name: name, Spec: spec, Path: paths[spec]}
		n, perr := resolveProjectFor(root, owner, name)
		if perr != nil || n <= 0 {
			c.BoardUnavailable = boardUnavailableReason(perr)
		} else {
			c.SuggestedProject = n
		}
		out.Candidates = append(out.Candidates, c)
	}
	return out, nil
}

// handleWorkspaceRepoAdd implements workspace.repoAdd.
func (s *Server) handleWorkspaceRepoAdd(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p WorkspaceRepoAddParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	root := s.workspaceRootPath()
	m, err := workspacemanifest.Load(workspacemanifest.ManifestPath(root))
	if err != nil {
		return nil, err
	}

	// A board is required at entry. The panel is expected to refuse
	// submission without one, but the daemon refuses too: this is the only
	// place that can guarantee project_number: 0 is unreachable, and a
	// guard that lives only in the UI is not a guard.
	if p.Project < 0 {
		return nil, fmt.Errorf("project must be a positive integer: %d would resolve to project 0 and silently misroute issues", p.Project)
	}

	path := strings.TrimSpace(p.Path)
	if path == "" {
		derived, derr := workspacemanifest.DeriveEntry(m, p.Name, p.Role)
		if derr != nil {
			return nil, derr
		}
		path = derived.Path
	}

	entry, err := workspacemanifest.AddRepo(m, workspacemanifest.Entry{
		Name:          strings.TrimSpace(p.Name),
		Path:          path,
		Role:          strings.TrimSpace(p.Role),
		ProjectNumber: p.Project,
	}, func(name string) (int, error) {
		owner, _ := splitSpec(p.Name)
		return resolveProjectFor(root, owner, name)
	})
	if err != nil {
		return nil, err
	}

	reread, rerr := workspacemanifest.Load(workspacemanifest.ManifestPath(root))
	if rerr != nil {
		return nil, rerr
	}
	return &WorkspaceRepoAddResult{
		OK:            true,
		Entry:         describeRepo(root, reread, entry),
		BoardSyncNote: boardSyncNote,
	}, nil
}

// handleWorkspaceRepoRemove implements workspace.repoRemove.
func (s *Server) handleWorkspaceRepoRemove(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p WorkspaceRepoRemoveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	root := s.workspaceRootPath()
	path := workspacemanifest.ManifestPath(root)
	m, err := workspacemanifest.Load(path)
	if err != nil {
		return nil, err
	}
	if _, ok := m.Find(name); !ok {
		return nil, fmt.Errorf("repository %q is not in the manifest", name)
	}

	// Removing a repository the routing section still points at would leave
	// routing naming something the manifest does not declare. Refuse and
	// NAME the references, so the operator can fix routing first.
	var refs []string
	if m.RoutingDefault() == name {
		refs = append(refs, "routing.default_repository")
	}
	for _, id := range m.RoutingPatternsFor(name) {
		refs = append(refs, fmt.Sprintf("routing.patterns[%s].preferred_repo", id))
	}
	if len(refs) > 0 && !p.Force {
		return nil, fmt.Errorf("repository %q is still referenced by %s — "+
			"removing it would leave routing pointing at a repository the manifest does not declare. "+
			"Update the routing section first, or confirm removal anyway", name, strings.Join(refs, ", "))
	}

	keptComment, err := m.RemoveEntry(name)
	if err != nil {
		return nil, err
	}
	if err := m.Write(); err != nil {
		return nil, err
	}
	return &WorkspaceRepoRemoveResult{OK: true, KeptComment: keptComment, ManifestPath: path}, nil
}

// boardUnavailableReason renders the resolver's failure for the panel, which
// must state that a board has to be provisioned first rather than silently
// accepting the entry.
func boardUnavailableReason(err error) string {
	base := "No project board resolves for this repository. Provision a board for it first — " +
		"an entry without a project number would resolve to project 0 and silently misroute every issue it produces."
	if err != nil {
		return base + " (resolver: " + err.Error() + ")"
	}
	return base
}

// repoInList is the descriptor-side twin of attention.RepoInConfiguredSet,
// matching on the canonical spec or the bare name.
func repoInList(configured []string, spec string) bool {
	_, name := splitSpec(spec)
	for _, c := range configured {
		if strings.EqualFold(c, spec) {
			return true
		}
		if _, cname := splitSpec(c); strings.EqualFold(cname, name) {
			return true
		}
	}
	return false
}
