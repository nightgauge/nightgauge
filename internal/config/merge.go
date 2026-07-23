package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	yaml "gopkg.in/yaml.v3"
)

// errConfigNotFound is the sentinel returned when a tier file is absent.
// Callers treat this as "skip this tier" rather than a hard error.
var errConfigNotFound = errors.New("config file not found")

// MachineTierKeys lists the dotted YAML key paths that belong to the
// machine tier (~/.nightgauge/config.yaml) per
// docs/SETTINGS_ARCHITECTURE.md. Presence of any of these keys in the
// project YAML (.nightgauge/config.yaml) shadows a developer's
// machine setting and is almost always a mistake — the loader emits a
// startup warning in that case.
//
// Updated for #3641 (autonomous policy reclassification): autonomy
// preferences must be Machine-tier so they apply consistently across
// git worktrees, where the per-workspace-folder runtime memento is
// invisible to spawned binaries.
//
// Each entry is a dotted path matching the YAML schema (snake_case).
// The check is structural — a path matches when every segment exists
// in the project YAML's mapping nodes.
var MachineTierKeys = []string{
	"github_user",
	"github_auth",
	"notifications.discord.enabled",
	"lm_studio",
	"autonomous.enabled_repos",
	"ui.core.adapter",
	"ui.core.default_model",
	"ui.core.fallback_model",
	"ui.core.auth_provider",
	"platform",
	// autonomous.repositories.* — every entry in the map counts; handled
	// specially in warnMachineKeysInProjectYAML.
	//
	// Note: autonomous.max_concurrent is intentionally NOT here. It is
	// already deprecated in favor of pipeline.max_concurrent (see
	// docs/DEPRECATIONS.md) — adding it as a machine-tier key would steer
	// users toward a setting they should be removing entirely.
	"pipeline.max_concurrent",
}

// machineConfigPathFn lets tests override the resolved machine-tier
// path. Production code uses defaultMachineConfigPath.
var machineConfigPathFn = defaultMachineConfigPath
var machineGOOSFn = func() string { return runtime.GOOS }

func defaultMachineConfigPath() (string, error) {
	// Env-override parity with the TS globalConfigResolver
	// (packages/nightgauge-vscode/src/utils/globalConfigResolver.ts):
	// NIGHTGAUGE_CONFIG_HOME wins, then XDG_CONFIG_HOME/nightgauge,
	// then the ~/.nightgauge default. This also lets tests point the
	// machine tier at a fixture directory instead of the developer's real
	// ~/.nightgauge/config.yaml.
	if dir := os.Getenv("NIGHTGAUGE_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "config.yaml"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "nightgauge", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch machineGOOSFn() {
	case "linux":
		return filepath.Join(home, ".config", "nightgauge", "config.yaml"), nil
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "nightgauge", "config.yaml"), nil
	default:
		return filepath.Join(home, ".nightgauge", "config.yaml"), nil
	}
}

// MachineConfigPath returns the absolute path of the machine-tier config
// file (~/.nightgauge/config.yaml on POSIX). Exposed so external
// callers — e.g. the IPC server's hot-reload watcher — can resolve the
// same path the loader uses.
func MachineConfigPath() (string, error) {
	return machineConfigPathFn()
}

// SwapMachineConfigPathForTest replaces the resolver used by Load and
// MachineConfigPath with the supplied function. Returns a cleanup that
// restores the previous resolver. Intended only for tests in external
// packages (the in-package merge_test.go reaches machineConfigPathFn
// directly).
func SwapMachineConfigPathForTest(fn func() (string, error)) func() {
	prev := machineConfigPathFn
	machineConfigPathFn = fn
	return func() { machineConfigPathFn = prev }
}

// readMachineConfigBytes returns the raw bytes of the machine-tier YAML
// file, or (nil, errConfigNotFound) if it does not exist.
func readMachineConfigBytes() ([]byte, error) {
	path, err := machineConfigPathFn()
	if err != nil {
		// Home directory unresolvable — skip the tier silently.
		return nil, errConfigNotFound
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Linux used ~/.nightgauge before the XDG path was standardized.
			// Read that location only as a compatibility fallback when the
			// canonical path is absent; all new writes target the canonical path.
			if legacy := legacyMachineConfigPath(); legacy != "" && legacy != path {
				if legacyData, legacyErr := os.ReadFile(legacy); legacyErr == nil {
					log.Printf("WARN config: using legacy machine config %s; move it to %s", legacy, path)
					return legacyData, nil
				}
			}
			return nil, errConfigNotFound
		}
		return nil, fmt.Errorf("read machine config %q: %w", path, err)
	}
	return data, nil
}

func legacyMachineConfigPath() string {
	if os.Getenv("NIGHTGAUGE_CONFIG_HOME") != "" || os.Getenv("XDG_CONFIG_HOME") != "" ||
		machineGOOSFn() != "linux" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".nightgauge", "config.yaml")
}

// readProjectConfigBytes returns the raw bytes of the project-tier YAML
// file, or (nil, errConfigNotFound) if it does not exist.
func readProjectConfigBytes(workspaceRoot string) ([]byte, error) {
	path := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errConfigNotFound
		}
		return nil, fmt.Errorf("read project config %q: %w", path, err)
	}
	return data, nil
}

// readLocalConfigBytes returns the raw bytes of the local-tier YAML
// file (.nightgauge/config.local.yaml), or (nil, errConfigNotFound).
func readLocalConfigBytes(workspaceRoot string) ([]byte, error) {
	path := filepath.Join(workspaceRoot, ".nightgauge", "config.local.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errConfigNotFound
		}
		return nil, fmt.Errorf("read local config %q: %w", path, err)
	}
	return data, nil
}

// LoadMerged reads configuration from all three persistent tiers and
// returns the merged effective config. Precedence (later overrides earlier):
//
//  1. Machine: ~/.nightgauge/config.yaml — per-developer preferences
//     (autonomy policy, install state). Cross-worktree consistent.
//  2. Project: .nightgauge/config.yaml — team-shared policy. Committed.
//  3. Local: .nightgauge/config.local.yaml — gitignored per-checkout overrides.
//
// Missing tiers are skipped silently. If only one tier exists, the merge
// is trivial. When project YAML contains a key classified as machine-tier
// (per MachineTierKeys), LoadMerged emits a structured warning — the
// project value will shadow the developer's machine setting, which is
// almost always a setup mistake.
//
// See docs/SETTINGS_ARCHITECTURE.md for the tier model and the
// per-key classification table.
func LoadMerged(workspaceRoot string) (*Config, error) {
	machineData, machineErr := readMachineConfigBytes()
	if machineErr != nil && !errors.Is(machineErr, errConfigNotFound) {
		return nil, machineErr
	}
	projectData, projectErr := readProjectConfigBytes(workspaceRoot)
	if projectErr != nil && !errors.Is(projectErr, errConfigNotFound) {
		return nil, projectErr
	}
	localData, localErr := readLocalConfigBytes(workspaceRoot)
	if localErr != nil && !errors.Is(localErr, errConfigNotFound) {
		return nil, localErr
	}

	hasMachine := machineErr == nil
	hasProject := projectErr == nil
	hasLocal := localErr == nil

	// Platform credentials and preferences are machine-owned. Never allow a
	// repository-controlled file (including local checkout overrides) to shadow
	// the machine identity used by the backend.
	if hasProject {
		projectData = removeTopLevelYAMLKey(projectData, "platform")
	}
	if hasLocal {
		localData = removeTopLevelYAMLKey(localData, "platform")
	}

	// No tier present at all — return defaults.
	if !hasMachine && !hasProject && !hasLocal {
		return DefaultConfig(), nil
	}

	// Surface shadow warnings only when both machine and project YAML define
	// the same key (actual conflict), not just when the project has the key.
	if hasMachine && hasProject {
		warnMachineKeysInProjectYAML(projectData, machineData)
	}

	// Build merged YAML node tree.
	root, err := mergeYAMLDocuments(machineData, projectData, localData)
	if err != nil {
		return nil, err
	}

	// Serialize merged document back to bytes for the existing parser.
	mergedBytes, err := yaml.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("serialize merged config: %w", err)
	}
	return parseYAML(mergedBytes)
}

func removeTopLevelYAMLKey(data []byte, key string) []byte {
	if len(data) == 0 {
		return data
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return data
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return data
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			out, err := yaml.Marshal(&doc)
			if err == nil {
				return out
			}
			return data
		}
	}
	return data
}

// mergeYAMLDocuments deep-merges up to three YAML documents in tier order
// (machine, project, local) and returns the merged root mapping node.
// Any input may be nil — nil tiers are skipped. The function expects each
// non-nil document to be a top-level mapping node (the standard config
// shape); empty bytes produce an empty mapping.
func mergeYAMLDocuments(tiers ...[]byte) (*yaml.Node, error) {
	merged := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	for _, data := range tiers {
		if len(data) == 0 {
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse tier yaml: %w", err)
		}
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		mergeMappingInto(merged, root)
	}

	return merged, nil
}

// mergeMappingInto deep-merges keys from src into dst. Both must be
// MappingNodes. Conflict resolution:
//   - Both values are mappings → recurse.
//   - Otherwise → src replaces dst (scalars and sequences replace whole).
//
// Sequences intentionally replace rather than concatenate. A user who
// wants the union of machine and project lists must spell that out
// explicitly — implicit list concatenation produces non-obvious results
// for things like enabled_repos (where duplicates would surprise).
func mergeMappingInto(dst, src *yaml.Node) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		srcKey := src.Content[i]
		srcVal := src.Content[i+1]

		existingIdx := -1
		for j := 0; j+1 < len(dst.Content); j += 2 {
			if dst.Content[j].Value == srcKey.Value {
				existingIdx = j
				break
			}
		}

		if existingIdx < 0 {
			dst.Content = append(dst.Content, cloneNode(srcKey), cloneNode(srcVal))
			continue
		}

		dstVal := dst.Content[existingIdx+1]
		if dstVal.Kind == yaml.MappingNode && srcVal.Kind == yaml.MappingNode {
			mergeMappingInto(dstVal, srcVal)
		} else {
			dst.Content[existingIdx+1] = cloneNode(srcVal)
		}
	}
}

// cloneNode returns a deep copy of a yaml.Node. yaml.v3 does not provide
// one. We use it so the merged document doesn't share node pointers with
// any of the tier documents (avoiding accidental mutation aliasing).
func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	clone := *n
	if len(n.Content) > 0 {
		clone.Content = make([]*yaml.Node, len(n.Content))
		for i, c := range n.Content {
			clone.Content[i] = cloneNode(c)
		}
	}
	return &clone
}

// warnedShadowKeys deduplicates the machine-tier shadow warnings so each
// distinct conflicting key is logged at most once per process. Config is
// re-merged on essentially every IPC call (each board.list resolves config),
// so without this guard a single conflicting key floods the daemon's stderr —
// e.g. the ~3s "WARN config: github_user ..." storm reported in #360. The
// warning is advisory and unchanging, so once per process is sufficient.
var warnedShadowKeys sync.Map

// warnShadowOnce logs msg the first time it is called for key, and is a no-op
// on every subsequent call for the same key within the process lifetime.
func warnShadowOnce(key, msg string) {
	if _, seen := warnedShadowKeys.LoadOrStore(key, struct{}{}); seen {
		return
	}
	log.Print(msg)
}

// resetShadowWarnDedup clears the once-per-process dedup set. Tests that assert
// the shadow warning fires call this first so a prior test (or a `-count>1`
// run) that already logged the same key does not suppress the expected output.
func resetShadowWarnDedup() {
	warnedShadowKeys.Range(func(k, _ any) bool {
		warnedShadowKeys.Delete(k)
		return true
	})
}

// warnMachineKeysInProjectYAML emits one log warning per machine-tier
// key that appears in BOTH the project and machine YAML (an actual shadow
// conflict). Keys present only in the project YAML — with no corresponding
// machine setting — are not warned about, since there is nothing to shadow.
//
// Each distinct conflicting key is logged only once per process (see
// warnShadowOnce / #360), not once per config merge.
func warnMachineKeysInProjectYAML(projectData, machineData []byte) {
	if len(projectData) == 0 {
		return
	}
	projectRoot := parseYAMLRoot(projectData)
	machineRoot := parseYAMLRoot(machineData)
	if projectRoot == nil {
		return
	}

	for _, key := range MachineTierKeys {
		segs := strings.Split(key, ".")
		if nodeHasPath(projectRoot, segs) && nodeHasPath(machineRoot, segs) {
			warnShadowOnce("key:"+key, fmt.Sprintf("WARN config: %s is in project YAML but is owned by the machine tier (~/.nightgauge/config.yaml). The project value shadows your machine setting. See docs/SETTINGS_ARCHITECTURE.md.", key))
		}
	}

	// autonomous.repositories.<repo> — warn only for slugs present in both
	// machine and project YAML (actual conflict).
	projectRepos := autonomousRepositoryKeys(projectRoot)
	machineRepos := autonomousRepositoryKeys(machineRoot)
	for slug := range projectRepos {
		if machineRepos[slug] {
			warnShadowOnce("repo:"+slug, fmt.Sprintf("WARN config: autonomous.repositories.%s is in project YAML but is owned by the machine tier. The project value shadows your machine setting. See docs/SETTINGS_ARCHITECTURE.md.", slug))
		}
	}
}

// parseYAMLRoot parses YAML bytes and returns the top-level mapping node,
// or nil if the document is empty or not a mapping.
func parseYAMLRoot(data []byte) *yaml.Node {
	if len(data) == 0 {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

// autonomousRepositoryKeys returns a set of repo slugs defined under
// autonomous.repositories in the given mapping root. Returns nil if the
// section is absent.
func autonomousRepositoryKeys(root *yaml.Node) map[string]bool {
	if root == nil {
		return nil
	}
	autoNode := findChildByKey(root, "autonomous")
	if autoNode == nil || autoNode.Kind != yaml.MappingNode {
		return nil
	}
	reposNode := findChildByKey(autoNode, "repositories")
	if reposNode == nil || reposNode.Kind != yaml.MappingNode {
		return nil
	}
	slugs := make(map[string]bool, len(reposNode.Content)/2)
	for i := 0; i+1 < len(reposNode.Content); i += 2 {
		slugs[reposNode.Content[i].Value] = true
	}
	return slugs
}

// nodeHasPath returns true when the given dotted-path segments resolve
// to a non-null value in the mapping tree rooted at node.
func nodeHasPath(node *yaml.Node, segments []string) bool {
	cur := node
	for _, seg := range segments {
		if cur == nil || cur.Kind != yaml.MappingNode {
			return false
		}
		next := findChildByKey(cur, seg)
		if next == nil {
			return false
		}
		cur = next
	}
	if cur == nil {
		return false
	}
	// Null scalar (`key: ~` / `key:`) is treated as absent.
	if cur.Kind == yaml.ScalarNode && (cur.Tag == "!!null" || cur.Value == "") {
		return false
	}
	return true
}

// findChildByKey returns the value node for the given key within a
// MappingNode, or nil if the key is absent.
func findChildByKey(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
