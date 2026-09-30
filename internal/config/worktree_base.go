package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/configpath"
	"github.com/nightgauge/nightgauge/internal/layout"
	yaml "gopkg.in/yaml.v3"
)

// ReadWorktreeBaseSetting reads pipeline.worktree_base for the repository at
// workspaceRoot, with the file and line it came from (ADR-024 § 9).
//
// It is a machine- or local-tier key: an absolute path on one machine, which a
// committed team file cannot express for everyone. The local tier
// (.nightgauge/config.local.yaml) wins over the machine tier. A value in the
// committed team file (.nightgauge/config.yaml) is an error wrapping
// layout.ErrWorktreeBase that names the file, the line and the fix; it is
// never silently used or ignored.
//
// Unset in both permitted tiers returns the zero setting, which
// layout.WorktreeBase resolves to STATE/worktrees/<repo-key>.
func ReadWorktreeBaseSetting(workspaceRoot string) (layout.WorktreeBaseSetting, error) {
	project := ProjectConfigPath(workspaceRoot)
	if s, err := worktreeBaseFromFile(project); err != nil {
		return layout.WorktreeBaseSetting{}, err
	} else if s.Value != "" {
		machine, _ := configpath.MachineConfigPath()
		return layout.WorktreeBaseSetting{}, fmt.Errorf(
			"%w: %s = %q is set in the committed team config %s:%d; it is a machine- or local-tier key. "+
				"Delete it there, and to use a custom location set an absolute path outside the repository in %s or %s",
			layout.ErrWorktreeBase, layout.WorktreeBaseKey, s.Value, s.Source, s.Line,
			machine, LocalConfigPath(workspaceRoot))
	}
	if s, err := worktreeBaseFromFile(LocalConfigPath(workspaceRoot)); err != nil || s.Value != "" {
		return s, err
	}
	machine, err := machineConfigFileInUse()
	if err != nil || machine == "" {
		return layout.WorktreeBaseSetting{}, nil
	}
	return worktreeBaseFromFile(machine)
}

// ResolveWorktreeBase is the one worktree-base resolver: the configured
// setting (ReadWorktreeBaseSetting) resolved by layout.WorktreeBase. Every
// caller that creates, names or finds a pipeline worktree by path goes
// through it; `nightgauge worktree base` prints it for the extension.
func ResolveWorktreeBase(workspaceRoot string) (string, error) {
	setting, err := ReadWorktreeBaseSetting(workspaceRoot)
	if err != nil {
		return "", err
	}
	return layout.WorktreeBase(workspaceRoot, setting)
}

// machineConfigFileInUse is the machine-tier file the loader reads.
func machineConfigFileInUse() (string, error) {
	return machineConfigPathFn()
}

// worktreeBaseFromFile reads pipeline.worktree_base from one YAML file. A
// missing file, a missing key and a null value all return the zero setting.
func worktreeBaseFromFile(path string) (layout.WorktreeBaseSetting, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return layout.WorktreeBaseSetting{}, nil
		}
		return layout.WorktreeBaseSetting{}, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return layout.WorktreeBaseSetting{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return layout.WorktreeBaseSetting{}, nil
	}
	pipeline := mappingValue(doc.Content[0], "pipeline")
	value := mappingValue(pipeline, "worktree_base")
	if value == nil || value.Tag == "!!null" {
		return layout.WorktreeBaseSetting{}, nil
	}
	source := path
	if abs, err := filepath.Abs(path); err == nil {
		source = abs
	}
	if value.Kind != yaml.ScalarNode {
		return layout.WorktreeBaseSetting{}, fmt.Errorf("%w: %s in %s:%d is not a string",
			layout.ErrWorktreeBase, layout.WorktreeBaseKey, source, value.Line)
	}
	return layout.WorktreeBaseSetting{Value: value.Value, Source: source, Line: value.Line}, nil
}

// mappingValue returns the value node of key in a mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
