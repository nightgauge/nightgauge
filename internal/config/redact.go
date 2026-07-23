package config

import (
	"bytes"
	"fmt"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// RedactedValue is the fixed marker emitted by every read-only config surface
// in place of a credential value.
const RedactedValue = "[REDACTED]"

// secretLeafNames is the central config-secret classification. Keep this list
// semantic (leaf names), not command-specific dotted paths, so new providers
// receive safe output by default when they use the established credential
// vocabulary. Names that merely reference environment variables are
// intentionally excluded: displaying GITHUB_TOKEN is safe; resolving it is not.
var secretLeafNames = map[string]struct{}{
	"api_key":       {},
	"apikey":        {},
	"client_secret": {},
	"license_key":   {},
	"licensekey":    {},
	"password":      {},
	"private_key":   {},
	"secret":        {},
	"token":         {},
	"webhook_url":   {},
}

var secretMapNames = map[string]struct{}{
	"api_keys": {},
	"tokens":   {},
}

// IsSecretConfigPath reports whether a dotted config path contains a secret
// value. Environment-variable reference fields (*_env, token_env,
// secret_env_var, webhook_env) are deliberately not secret values.
func IsSecretConfigPath(path []string) bool {
	if len(path) == 0 {
		return false
	}
	leaf := normalizeSecretName(path[len(path)-1])
	if isEnvironmentReferenceName(leaf) {
		return false
	}
	if _, ok := secretLeafNames[leaf]; ok {
		return true
	}
	if len(path) > 1 {
		parent := normalizeSecretName(path[len(path)-2])
		_, ok := secretMapNames[parent]
		return ok
	}
	return false
}

func normalizeSecretName(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
}

func isEnvironmentReferenceName(name string) bool {
	return name == "token_env" || name == "secret_env_var" || name == "webhook_env" ||
		strings.HasSuffix(name, "_env")
}

// RedactYAML replaces every classified secret scalar/subtree in doc with the
// fixed marker. It mutates doc, so callers should parse an output-only copy and
// must never pass the node later written to disk.
func RedactYAML(doc *yaml.Node) {
	redactYAMLNode(doc, nil)
}

func redactYAMLNode(node *yaml.Node, path []string) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			redactYAMLNode(child, path)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			childPath := appendPath(path, key.Value)
			if IsSecretConfigPath(childPath) {
				redactNodeValue(value)
				continue
			}
			redactYAMLNode(value, childPath)
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			redactYAMLNode(child, path)
		}
	}
}

func appendPath(path []string, segment string) []string {
	// Let append own capacity arithmetic instead of computing len(path)+1 from
	// document-controlled depth.
	return append(append([]string(nil), path...), segment)
}

func redactNodeValue(node *yaml.Node) {
	node.Kind = yaml.ScalarNode
	node.Tag = "!!str"
	node.Value = RedactedValue
	node.Style = 0
	node.Content = nil
	node.Anchor = ""
	node.Alias = nil
}

// RedactYAMLBytes parses and redacts a YAML document for display. The returned
// bytes are output-only; callers retain the original bytes for persistence.
func RedactYAMLBytes(data []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse YAML for redaction: %w", err)
	}
	RedactYAML(&doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encode redacted YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close redacted YAML encoder: %w", err)
	}
	return normalizeTrailingNewline(buf.Bytes()), nil
}
