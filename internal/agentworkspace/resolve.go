// Package agentworkspace resolves what the daemon's platform agent declares
// about the workspace it serves (nightgauge#2335): the owner/repo set and,
// when the workspace config names the workspace, the `workspace` block.
//
// Both are derived the way the VS Code extension derives them for its own
// agent (extension.ts registerOrSyncAgent, Repository.github and
// WorkspaceRegistrationPayloadBuilder), so the daemon and the extension
// declare the same workspace:
//
//  1. The workspace's repositories, each identified by its own
//     .nightgauge/config.yaml (the legacy nightgauge.yaml when that is
//     absent): the nested `github: {owner, repo}` block when the file has
//     one, otherwise top-level `owner` and `repo`. A repository that names
//     neither is not declared. The repositories are the manifest's
//     (.vscode/nightgauge-workspace.yaml) when there is a valid one,
//     otherwise the workspace root alone.
//  2. When no repository is identified that way, the `owner/repo` entries of
//     the effective `autonomous.enabled_repos`.
//
// A manifest with an empty list (valid only with `shared_project_number`) is
// the N:1 topology: the extension lists the project's linked repositories,
// but builds each one at the workspace root, so every one of them is
// identified by the root's own config. The root is therefore the one member
// here.
//
// The folders of a multi-root VS Code window that has no manifest are not
// visible to a daemon and are not mirrored. The extension's last fallback
// reads `platform.owner` and `platform.defaultRepo`, keys neither config
// schema defines, so it is not mirrored either.
package agentworkspace

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/workspacemanifest"
)

// manifest is the part of .vscode/nightgauge-workspace.yaml read here.
type manifest struct {
	Workspace struct {
		Name string `yaml:"name"`
	} `yaml:"workspace"`
	Repositories []struct {
		Path string `yaml:"path"`
	} `yaml:"repositories"`
}

// repoIdentity is the part of a repository's config read here. GitHub is a
// node so that its JavaScript truthiness can be judged: the extension uses
// the block whenever `cfg.github` is truthy, complete or not, and the flat
// keys only when it is not.
type repoIdentity struct {
	GitHub yaml.Node `yaml:"github"`
	Owner  any       `yaml:"owner"`
	Repo   any       `yaml:"repo"`
}

// Resolve returns the declaration for the workspace rooted at root. Like the
// extension's workspace detection, it treats a manifest that cannot be read
// or that fails the manifest rules (workspacemanifest.ValidateBytes, the Go
// mirror of the extension's validateWorkspaceConfig) as absent: the workspace
// is then the root alone, with no workspace block. A repository whose config
// cannot be read or parsed is skipped, as the extension skips it.
func Resolve(root string) (platform.WorkspaceDeclaration, error) {
	var decl platform.WorkspaceDeclaration

	repoRoots := []string{root}
	if m, ok := readManifest(root); ok {
		// A valid manifest names the workspace. With an empty list it is the
		// N:1 topology, whose members are all identified by the root's config.
		if len(m.Repositories) > 0 {
			repoRoots = repoRoots[:0]
			for _, entry := range m.Repositories {
				repoRoots = append(repoRoots, resolvePath(root, entry.Path))
			}
		}
		decl.Workspace = platform.WorkspaceBlock(m.Workspace.Name)
	}

	for _, repoRoot := range repoRoots {
		if r, ok := repoFromConfig(repoRoot); ok {
			decl.Repos = append(decl.Repos, r)
		}
	}
	if len(decl.Repos) == 0 {
		decl.Repos = enabledRepos(root)
	}
	return decl, nil
}

// readManifest reads and validates the workspace manifest, reporting false
// when there is none the extension would use.
func readManifest(root string) (manifest, bool) {
	var m manifest
	data, err := os.ReadFile(workspacemanifest.ManifestPath(root))
	if err != nil || workspacemanifest.ValidateBytes(data) != nil {
		return m, false
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return m, false
	}
	return m, true
}

// resolvePath resolves a manifest path against the workspace root the way
// Node's path.resolve does: an absolute path stands, a relative one joins.
func resolvePath(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// repoFromConfig reads a repository's owner/repo from its config file.
func repoFromConfig(repoRoot string) (platform.AgentRepo, bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".nightgauge", "config.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(repoRoot, ".nightgauge", "nightgauge.yaml"))
	}
	if err != nil {
		return platform.AgentRepo{}, false
	}
	var id repoIdentity
	if err := yaml.Unmarshal(data, &id); err != nil {
		return platform.AgentRepo{}, false
	}
	if jsTruthy(&id.GitHub) {
		var block struct {
			Owner any `yaml:"owner"`
			Repo  any `yaml:"repo"`
		}
		if id.GitHub.Kind != yaml.MappingNode || id.GitHub.Decode(&block) != nil {
			return platform.AgentRepo{}, false
		}
		return identified(block.Owner, block.Repo)
	}
	return identified(id.Owner, id.Repo)
}

// jsTruthy reports whether a YAML value would be truthy in JavaScript once
// parsed: an absent key, null, false, 0, NaN and the empty string are not;
// every mapping, sequence and other scalar is.
func jsTruthy(n *yaml.Node) bool {
	if n == nil || n.Kind == 0 {
		return false
	}
	if n.Kind != yaml.ScalarNode {
		return true
	}
	switch n.Tag {
	case "!!null":
		return false
	case "!!bool":
		var b bool
		return n.Decode(&b) == nil && b
	case "!!int", "!!float":
		var f float64
		return n.Decode(&f) == nil && f != 0 && !math.IsNaN(f)
	default:
		return n.Value != ""
	}
}

// identified accepts an owner and repo only when both are non-empty strings,
// the extension's test before it declares a repository.
func identified(owner, repo any) (platform.AgentRepo, bool) {
	o, ok1 := owner.(string)
	r, ok2 := repo.(string)
	if !ok1 || !ok2 || o == "" || r == "" {
		return platform.AgentRepo{}, false
	}
	return platform.AgentRepo{Owner: o, Repo: r}, true
}

// enabledRepos is the fallback: the effective autonomous.enabled_repos,
// keeping each entry that splits into a non-empty owner and repo.
func enabledRepos(root string) []platform.AgentRepo {
	cfg, err := config.Load(root)
	if err != nil || cfg == nil || cfg.Autonomous == nil {
		return nil
	}
	var repos []platform.AgentRepo
	for _, slug := range cfg.Autonomous.EnabledRepos {
		if strings.TrimSpace(slug) == "" {
			continue
		}
		parts := strings.Split(slug, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		repos = append(repos, platform.AgentRepo{Owner: parts[0], Repo: parts[1]})
	}
	return repos
}
