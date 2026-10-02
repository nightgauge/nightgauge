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
// Which root the declaration is resolved from matters as much as how. The
// extension resolves its workspace from its window: the git root of the
// window's first folder, or that folder when it is not in a repository, and,
// for a multi-root window with no manifest whose every folder has a project
// config, from each folder. The daemon's `--workspace` is instead the first
// folder that has a project config, which can be another folder, so the
// extension hands the daemon its window's folders (WindowFoldersEnv) and
// ResolveWindow resolves from them as the extension does. A daemon started
// without them (`nightgauge serve` from a terminal) declares its
// `--workspace` root (Resolve).
//
// The extension's last fallback reads `platform.owner` and
// `platform.defaultRepo`, keys neither config schema defines, so it is not
// mirrored. Nor is a window whose folders share a name: the extension keys
// the folders by name and keeps the last, and a folder's name can be set in
// the .code-workspace file, which the daemon does not read.
package agentworkspace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/config"
	gitops "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/workspacemanifest"
)

// WindowFoldersEnv names the environment variable through which the VS Code
// extension tells the daemon it starts which folders its window has open: a
// JSON array of absolute paths, in window order (IpcClientBase
// WINDOW_FOLDERS_ENV_VAR).
const WindowFoldersEnv = "NIGHTGAUGE_WINDOW_FOLDERS"

// WindowFolders returns the folders WindowFoldersEnv names, or nil when it is
// unset or is not a JSON array of non-empty strings.
func WindowFolders(getenv func(string) string) []string {
	raw := strings.TrimSpace(getenv(WindowFoldersEnv))
	if raw == "" {
		return nil
	}
	var folders []string
	if err := json.Unmarshal([]byte(raw), &folders); err != nil {
		return nil
	}
	for _, f := range folders {
		if f == "" {
			return nil
		}
	}
	return folders
}

// ResolveServed returns what the daemon declares: the extension's
// declaration for the window that started it when it named the window's
// folders, otherwise the declaration for the daemon's own workspace root.
func ResolveServed(window []string, workspaceRoot string) (platform.WorkspaceDeclaration, error) {
	if len(window) > 0 {
		return ResolveWindow(window)
	}
	return Resolve(workspaceRoot)
}

// ResolveWindow returns the declaration the extension makes for a window
// with these folders open, the first first (getWorkspaceRoot,
// getNightgaugeRoot, WorkspaceManager and detectWorkspaceType):
//
//   - the workspace root is the git root of the first folder, as this
//     daemon's own `git.root` answers the extension, or the folder itself
//     when that fails;
//   - a valid manifest at the root decides the members, as in Resolve;
//   - otherwise, when the window has two or more folders and every one has a
//     project config, each folder is a member;
//   - otherwise the root alone is.
func ResolveWindow(folders []string) (platform.WorkspaceDeclaration, error) {
	if len(folders) == 0 {
		return platform.WorkspaceDeclaration{}, nil
	}
	var autoDetected []string
	if len(folders) >= 2 && everyHasProjectConfig(folders) {
		autoDetected = folders
	}
	return resolve(windowRoot(folders[0]), autoDetected), nil
}

// windowRoot is the extension's workspace root for a window whose first
// folder is first: getNightgaugeRoot asks the daemon's git.root, which is
// gitops.Service.Root, and falls back to the folder when that fails or is
// empty. The same call here keeps the two in step.
func windowRoot(first string) string {
	svc, err := gitops.NewService(first)
	if err != nil {
		return first
	}
	root, err := svc.Root()
	if err != nil || root == "" {
		return first
	}
	return root
}

// everyHasProjectConfig reports whether every folder has a project config,
// the current or the legacy file (autoDetectMultiWorkspace).
func everyHasProjectConfig(folders []string) bool {
	for _, folder := range folders {
		if !exists(filepath.Join(folder, ".nightgauge", "config.yaml")) &&
			!exists(filepath.Join(folder, ".nightgauge", "nightgauge.yaml")) {
			return false
		}
	}
	return true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

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
// extension's workspace detection, it does not use a manifest that cannot be
// read or that fails the manifest rules (workspacemanifest.ValidateBytes, the
// Go mirror of the extension's validateWorkspaceConfig): the workspace is
// then the root alone, with no workspace block, and ResolveWindow does not
// try the window's folders either. A repository whose config cannot be read
// or parsed is skipped, as the extension skips it.
func Resolve(root string) (platform.WorkspaceDeclaration, error) {
	return resolve(root, nil), nil
}

// resolve declares the workspace rooted at root. autoDetected, when set, is
// the members of a multi-root window with no manifest (ResolveWindow).
func resolve(root string, autoDetected []string) platform.WorkspaceDeclaration {
	var decl platform.WorkspaceDeclaration

	repoRoots := []string{root}
	switch m, found := readManifest(root); found {
	case manifestValid:
		// A valid manifest names the workspace. With an empty list it is the
		// N:1 topology, whose members are all identified by the root's config.
		if len(m.Repositories) > 0 {
			repoRoots = repoRoots[:0]
			for _, entry := range m.Repositories {
				repoRoots = append(repoRoots, resolvePath(root, entry.Path))
			}
		}
		decl.Workspace = platform.WorkspaceBlock(m.Workspace.Name)
	case manifestAbsent:
		if len(autoDetected) > 0 {
			repoRoots = autoDetected
		}
	case manifestInvalid:
		// The extension's detection throws on a manifest it cannot use and
		// falls back to the root alone, without trying the window's folders.
	}

	for _, repoRoot := range repoRoots {
		if r, ok := repoFromConfig(repoRoot); ok {
			decl.Repos = append(decl.Repos, r)
		}
	}
	if len(decl.Repos) == 0 {
		decl.Repos = enabledRepos(root)
	}
	return decl
}

// manifestState is what readManifest found at the workspace root.
type manifestState int

const (
	// manifestAbsent: no manifest file.
	manifestAbsent manifestState = iota
	// manifestInvalid: a file the extension cannot use (unreadable, not
	// YAML, or failing the manifest rules).
	manifestInvalid
	// manifestValid: a manifest the extension uses.
	manifestValid
)

// readManifest reads and validates the workspace manifest.
func readManifest(root string) (manifest, manifestState) {
	var m manifest
	path := workspacemanifest.ManifestPath(root)
	if !exists(path) {
		return m, manifestAbsent
	}
	data, err := os.ReadFile(path)
	if err != nil || workspacemanifest.ValidateBytes(data) != nil {
		return m, manifestInvalid
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return m, manifestInvalid
	}
	return m, manifestValid
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
