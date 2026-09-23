package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
	yaml "gopkg.in/yaml.v3"
)

// ReadMachineString returns the scalar at dottedPath in the machine-tier file
// and the canonical path of that file. A missing file or key is not an error:
// it returns an empty value. The Linux legacy location is read the same way
// Load reads it.
func ReadMachineString(dottedPath string) (value, path string, err error) {
	path, err = machineConfigPathFn()
	if err != nil {
		return "", "", fmt.Errorf("resolve machine config path: %w", err)
	}
	data, err := readMachineConfigBytes()
	if errors.Is(err, errConfigNotFound) {
		return "", path, nil
	}
	if err != nil {
		return "", path, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", path, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return "", path, nil
	}
	cur := doc.Content[0]
	for _, key := range strings.Split(dottedPath, ".") {
		if cur.Kind != yaml.MappingNode {
			return "", path, nil
		}
		idx := mappingChildIndex(cur, key)
		if idx < 0 {
			return "", path, nil
		}
		cur = cur.Content[idx+1]
	}
	if cur.Kind != yaml.ScalarNode {
		return "", path, nil
	}
	return strings.TrimSpace(cur.Value), path, nil
}

// WriteMachinePrivateValue sets dottedPath in the machine-tier file to value,
// or removes it when value is empty, and returns the file's path.
//
// It is the write path for a credential that has no OS keychain to live in, so
// it is stricter than WriteRuntimeValue: it refuses a symlinked target (a link
// could point the credential at a world-readable file), installs the result
// atomically, and leaves the file at mode 0600 whatever mode it had before.
func WriteMachinePrivateValue(dottedPath, value string) (string, error) {
	path, err := machineConfigPathFn()
	if err != nil {
		return "", fmt.Errorf("resolve machine config path: %w", err)
	}
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode()&fs.ModeSymlink != 0:
		return path, fmt.Errorf("refusing to write a credential to %s: it is a symlink", path)
	case err == nil && !info.Mode().IsRegular():
		return path, fmt.Errorf("refusing to write a credential to %s: not a regular file", path)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return path, fmt.Errorf("stat %s: %w", path, err)
	}
	if errors.Is(err, fs.ErrNotExist) && value == "" {
		return path, nil // nothing to remove
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, fmt.Errorf("create config dir: %w", err)
	}
	root, err := readMappingRoot(path)
	if err != nil {
		return path, err
	}
	keys := strings.Split(dottedPath, ".")
	if value == "" {
		if !deleteMappingKey(root, keys) {
			return path, nil
		}
	} else {
		node, encErr := encodeValueNode(value)
		if encErr != nil {
			return path, encErr
		}
		if err := setMappingKey(root, keys, node); err != nil {
			return path, err
		}
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return path, fmt.Errorf("marshal config: %w", err)
	}
	if err := atomicfile.Write(path, out, 0o600); err != nil {
		return path, fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
