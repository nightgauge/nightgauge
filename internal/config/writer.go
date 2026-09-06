package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// ProjectConfigPath returns the committed team-tier file for a workspace.
//
// It is deliberately NOT a write target for any runtime path: the file is
// checked in, it carries the public-core safety defaults, and a background
// rewrite of it leaves every checkout permanently dirty (#1516).
func ProjectConfigPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
}

// LocalConfigPath returns the gitignored local-tier file for a workspace —
// the default write target for anything the runtime or the UI persists.
func LocalConfigPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "config.local.yaml")
}

// IsMachineTierKey reports whether a dotted YAML key path belongs in the
// machine-tier file (~/.nightgauge/config.yaml) rather than in the workspace.
// A path underneath a machine-tier key counts too, so both `platform` and
// `platform.license_key` route to the machine file.
func IsMachineTierKey(dottedPath string) bool {
	for _, key := range MachineTierKeys {
		if dottedPath == key || strings.HasPrefix(dottedPath, key+".") {
			return true
		}
	}
	return false
}

// RuntimeWriteTarget returns the file a runtime/UI write of dottedPath must
// land in: the machine-tier file for a machine-tier key, otherwise the
// workspace's local tier. It never returns the committed team file.
func RuntimeWriteTarget(workspaceRoot, dottedPath string) (string, error) {
	if IsMachineTierKey(dottedPath) {
		path, err := machineConfigPathFn()
		if err != nil {
			return "", fmt.Errorf("resolve machine config path: %w", err)
		}
		return path, nil
	}
	return LocalConfigPath(workspaceRoot), nil
}

// WriteRuntimeValue persists one dotted key to the tier it belongs in and
// returns the file it wrote.
//
// The write is a yaml.Node round-trip, so every key the write does not touch
// keeps its own comments. Only the addressed leaf changes.
//
// value must be a scalar, a []string, or nil (which deletes the key).
func WriteRuntimeValue(workspaceRoot, dottedPath string, value any) (string, error) {
	path, err := RuntimeWriteTarget(workspaceRoot, dottedPath)
	if err != nil {
		return "", err
	}
	if err := writeNodeValue(path, strings.Split(dottedPath, "."), value); err != nil {
		return "", err
	}
	return path, nil
}

// writeNodeValue applies one leaf change to the YAML document at path,
// creating the file (and its directory) when absent.
func writeNodeValue(path string, keys []string, value any) error {
	if len(keys) == 0 {
		return fmt.Errorf("write config value: empty key path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	root, err := readMappingRoot(path)
	if err != nil {
		return err
	}

	if value == nil {
		if !deleteMappingKey(root, keys) {
			return nil // nothing to remove
		}
	} else {
		valueNode, encErr := encodeValueNode(value)
		if encErr != nil {
			return encErr
		}
		if err := setMappingKey(root, keys, valueNode); err != nil {
			return err
		}
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func readMappingRoot(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("parse %s: top level is not a mapping", path)
		}
		return root, nil
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
}

func encodeValueNode(value any) (*yaml.Node, error) {
	node := &yaml.Node{}
	if err := node.Encode(value); err != nil {
		return nil, fmt.Errorf("encode config value: %w", err)
	}
	return node, nil
}

func mappingChildIndex(node *yaml.Node, key string) int {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func setMappingKey(root *yaml.Node, keys []string, value *yaml.Node) error {
	cur := root
	for i, key := range keys {
		idx := mappingChildIndex(cur, key)
		if i == len(keys)-1 {
			if idx >= 0 {
				// Preserve the existing key's comments; replace only the value.
				value.HeadComment = cur.Content[idx+1].HeadComment
				value.LineComment = cur.Content[idx+1].LineComment
				value.FootComment = cur.Content[idx+1].FootComment
				cur.Content[idx+1] = value
			} else {
				cur.Content = append(cur.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
					value,
				)
			}
			return nil
		}
		if idx >= 0 && cur.Content[idx+1].Kind == yaml.MappingNode {
			cur = cur.Content[idx+1]
			continue
		}
		next := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if idx >= 0 {
			cur.Content[idx+1] = next
		} else {
			cur.Content = append(cur.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				next,
			)
		}
		cur = next
	}
	return nil
}

func deleteMappingKey(root *yaml.Node, keys []string) bool {
	cur := root
	for i, key := range keys {
		idx := mappingChildIndex(cur, key)
		if idx < 0 {
			return false
		}
		if i == len(keys)-1 {
			cur.Content = append(cur.Content[:idx], cur.Content[idx+2:]...)
			return true
		}
		if cur.Content[idx+1].Kind != yaml.MappingNode {
			return false
		}
		cur = cur.Content[idx+1]
	}
	return false
}
