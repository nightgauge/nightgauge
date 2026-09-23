package config

import (
	"fmt"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// repoTierProtectedKey is one machine-owned block that a repository-controlled
// tier (.nightgauge/config.yaml, .nightgauge/config.local.yaml) may not carry
// freely.
type repoTierProtectedKey struct {
	// root is the top-level YAML key.
	root string
	// hardStrip deletes the whole block from the project and local tiers
	// before the merge (#1049), so the machine tier always wins.
	hardStrip bool
	// secrets are the credential-bearing paths under root, dot-separated,
	// where "*" matches every key of a mapping. A non-empty value at one of
	// these paths in a repository tier must be an env: reference (#2023).
	secrets []string
}

// repoTierProtectedKeys is the single list both protections read: the strip in
// LoadMerged (via isHardStrippedMachineKey) and ValidateRepoTierSecrets. A key
// added here is covered by whichever protections its entry names, and a block
// cannot be stripped under one name and secret-checked under another.
//
// The secret check runs before the strip, so a plaintext license key in the
// project file is refused rather than silently discarded: it is already in the
// repository's history, and only the operator can rotate it.
var repoTierProtectedKeys = []repoTierProtectedKey{
	{root: "platform", hardStrip: true, secrets: []string{"license_key"}},
	{root: "github_auth", secrets: []string{"token", "tokens.*"}},
}

// envRefPrefix marks a config value that names an environment variable
// instead of holding the secret itself. See resolveEnvRef.
const envRefPrefix = "env:"

// ValidateRepoTierSecrets refuses a repository-tier config file that holds a
// credential in plaintext. data is the file's content and path is where it was
// read from; path appears in the error so the operator knows which file to
// edit.
//
// Every credential path in repoTierProtectedKeys must be empty or an env:
// reference. The error names the file and each offending key and never the
// value, not even a prefix. YAML that does not parse is left for the parser to
// report.
//
// The machine tier is never passed here: a plaintext value in that file (mode
// 0600, outside every repository) is accepted.
func ValidateRepoTierSecrets(data []byte, path string) error {
	root := parseYAMLRoot(data)
	if root == nil {
		return nil
	}
	var offending []string
	for _, k := range repoTierProtectedKeys {
		block := findChildByKey(root, k.root)
		if block == nil {
			continue
		}
		for _, secret := range k.secrets {
			offending = append(offending, plaintextSecretPaths(block, k.root, strings.Split(secret, "."))...)
		}
	}
	if len(offending) == 0 {
		return nil
	}
	sort.Strings(offending)
	machine := "the machine-tier config file"
	if p, err := machineConfigPathFn(); err == nil && p != "" {
		machine = p
	}
	return fmt.Errorf("config: %s holds a plaintext secret at %s (value redacted). "+
		"This file belongs to the repository, so it accepts only an environment reference such as "+
		"`%s: env:MY_VARIABLE`. Replace the value with an env: reference, or move it to %s, "+
		"which is outside every repository; for GitHub, naming the account in github_user lets "+
		"the gh CLI supply the token from the OS keychain. If the value was ever committed, rotate it",
		path, strings.Join(offending, ", "), offending[0], machine)
}

// plaintextSecretPaths returns the dotted paths under node, reached by segs,
// whose scalar value is non-empty and not an env: reference. prefix is the
// dotted path of node itself.
func plaintextSecretPaths(node *yaml.Node, prefix string, segs []string) []string {
	// Follow an alias to its anchor, so `token: *pat` is judged by the value
	// it resolves to rather than waved through as a non-scalar.
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node == nil {
		return nil
	}
	if len(segs) == 0 {
		if node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
			return nil
		}
		v := strings.TrimSpace(node.Value)
		if v == "" || strings.HasPrefix(v, envRefPrefix) {
			return nil
		}
		return []string{prefix}
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if segs[0] != "*" && key != segs[0] {
			continue
		}
		out = append(out, plaintextSecretPaths(node.Content[i+1], prefix+"."+key, segs[1:])...)
	}
	return out
}
