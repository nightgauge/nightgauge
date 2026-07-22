package integrationprobe

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultManifestBytes []byte

// DefaultManifest returns a fresh copy of the embedded default manifest.
// Each call returns an independent value so callers may mutate it freely.
func DefaultManifest() (*EndpointManifest, error) {
	return parseManifest(defaultManifestBytes)
}

// LoadManifest reads and parses an endpoint manifest from disk. When path
// is empty the embedded default manifest is returned.
func LoadManifest(path string) (*EndpointManifest, error) {
	if path == "" {
		return DefaultManifest()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %q: %w", path, err)
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("parse manifest %q: %w", path, err)
	}
	return m, nil
}

// parseManifest decodes a manifest YAML document with strict
// unknown-field rejection.
func parseManifest(data []byte) (*EndpointManifest, error) {
	var m EndpointManifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("yaml decode: %w", err)
	}
	if m.Version == 0 {
		return nil, fmt.Errorf("manifest missing required field: version")
	}
	if len(m.Groups) == 0 {
		return nil, fmt.Errorf("manifest has no groups")
	}
	for label, entries := range m.Groups {
		if len(entries) == 0 {
			return nil, fmt.Errorf("manifest group %q is empty", label)
		}
		for i, e := range entries {
			if e.Method == "" {
				return nil, fmt.Errorf("manifest group %q entry %d: method is required", label, i)
			}
			if e.Path == "" {
				return nil, fmt.Errorf("manifest group %q entry %d: path is required", label, i)
			}
		}
	}
	return &m, nil
}
