package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/credshape"
)

// repoTierProtectedKey is one machine-owned block that a repository-controlled
// tier (.nightgauge/config.yaml, .nightgauge/config.local.yaml, the legacy
// .nightgauge/config.json) may not carry freely.
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
// repoTierCredentialFields must name a field for every secret path listed;
// TestRepoTierProtectedKeysShareOneList enforces that.
//
// The secret check runs before the strip, so a plaintext license key in the
// project file is refused rather than silently discarded: it is already in the
// repository's history, and only the operator can rotate it.
var repoTierProtectedKeys = []repoTierProtectedKey{
	{root: "platform", hardStrip: true, secrets: []string{"license_key"}},
	{root: "github_auth", secrets: []string{"token", "tokens.*", "app.private_key", "app.private_key_path"}},
}

// ErrRepoTierCredential marks a load refused because a repository tier holds a
// credential in plaintext. Callers that would otherwise fall back to another
// identity on a load error can test for it with errors.Is.
var ErrRepoTierCredential = errors.New("plaintext credential in a repository config file")

// envRefPrefix marks a config value that names an environment variable
// instead of holding the secret itself. See resolveEnvRef.
const envRefPrefix = "env:"

// repoTierCredentialView decodes a repository tier the way the loader does —
// yaml.v3 resolving aliases and `<<` merge keys — but into yaml.Node leaves, so
// no value can fail to decode and surface in a decoder error. Every field here
// is a credential; the tags match yamlConfigNested and yamlConfigFlat.
type repoTierCredentialView struct {
	GitHubAuth struct {
		Token  yaml.Node            `yaml:"token"`
		Tokens map[string]yaml.Node `yaml:"tokens"`
		App    struct {
			PrivateKey     yaml.Node `yaml:"private_key"`
			PrivateKeyPath yaml.Node `yaml:"private_key_path"`
		} `yaml:"app"`
	} `yaml:"github_auth"`
	Platform struct {
		LicenseKey yaml.Node `yaml:"license_key"`
	} `yaml:"platform"`
}

// credentialValue is one credential leaf as the loader will see it.
type credentialValue struct {
	path string
	node yaml.Node
}

func repoTierCredentialFields(v *repoTierCredentialView) []credentialValue {
	out := []credentialValue{
		{path: "github_auth.token", node: v.GitHubAuth.Token},
		{path: "github_auth.app.private_key", node: v.GitHubAuth.App.PrivateKey},
		{path: "github_auth.app.private_key_path", node: v.GitHubAuth.App.PrivateKeyPath},
		{path: "platform.license_key", node: v.Platform.LicenseKey},
	}
	for owner, n := range v.GitHubAuth.Tokens {
		out = append(out, credentialValue{path: "github_auth.tokens." + redactCredentialShaped(owner), node: n})
	}
	return out
}

// ValidateRepoTierSecrets refuses a repository-tier config file that holds a
// credential in plaintext. data is the file's content and path is where it was
// read from; path appears in the error so the operator knows which file to
// edit.
//
// Every credential the loader would read — including one reached through a
// YAML alias or `<<` merge key — must be empty or an env: reference whose
// variable name is not itself shaped like a token (`env:ghp_…`). The
// error names the file and each offending key and never the value, not even a
// prefix. A file that is not YAML is left for the parser to report.
//
// A path that is the machine-tier file (run from $HOME, the project file IS
// the machine file) is not a repository tier and is accepted.
func ValidateRepoTierSecrets(data []byte, path string) error {
	if IsMachineTierFile(path) {
		return nil
	}
	var probe yaml.Node
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil // not YAML: the parser reports it, and a syntax error quotes no value
	}
	var view repoTierCredentialView
	if err := yaml.Unmarshal(data, &view); err != nil {
		// A credential block of the wrong shape. yaml.v3's type errors quote
		// the offending scalar, which may be the credential: never pass them on.
		return repoTierCredentialError(path, []string{"github_auth / platform (the block is not a mapping of strings)"})
	}
	var offending []string
	for _, f := range repoTierCredentialFields(&view) {
		if !plaintextCredential(&f.node) {
			continue
		}
		offending = append(offending, f.path)
	}
	if len(offending) == 0 {
		return nil
	}
	sort.Strings(offending)
	return repoTierCredentialError(path, offending)
}

// plaintextCredential reports whether a decoded credential leaf is anything
// other than absent, empty, or a well-formed env: reference. The tag is
// ignored: `token: !!null ghp_…` carries the value as surely as a plain string.
func plaintextCredential(n *yaml.Node) bool {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n == nil || n.Kind == 0 {
		return false
	}
	if n.Kind != yaml.ScalarNode {
		return true // a mapping or sequence where a string belongs
	}
	v := strings.TrimSpace(n.Value)
	if v == "" {
		return false
	}
	if !strings.HasPrefix(v, envRefPrefix) {
		return true
	}
	name := strings.TrimSpace(strings.TrimPrefix(v, envRefPrefix))
	return name == "" || credshape.Contains(name)
}

// ValidateLegacyJSONCredentials applies the repository-tier rule to the legacy
// .nightgauge/config.json, decoded (as the loader decodes it, case-insensitive
// keys included) into cfg. It refuses a plaintext token and then strips the
// platform block, which a repository file may never set.
func ValidateLegacyJSONCredentials(cfg *Config, path string) error {
	if cfg == nil {
		return nil
	}
	var offending []string
	check := func(key, v string) {
		n := yaml.Node{Kind: yaml.ScalarNode, Value: v}
		if plaintextCredential(&n) {
			offending = append(offending, key)
		}
	}
	if cfg.GitHubAuth != nil {
		check("githubAuth.token", cfg.GitHubAuth.Token)
		for owner, v := range cfg.GitHubAuth.Tokens {
			check("githubAuth.tokens."+redactCredentialShaped(owner), v)
		}
	}
	check("licenseKey", cfg.LicenseKey)
	if len(offending) > 0 && !IsMachineTierFile(path) {
		sort.Strings(offending)
		return repoTierCredentialError(path, offending)
	}
	// platform is hard-stripped from every repository tier (#1049).
	cfg.LicenseKey = ""
	cfg.PlatformURL = ""
	cfg.PlatformEnabled = nil
	return nil
}

func repoTierCredentialError(path string, offending []string) error {
	machine := "the machine-tier config file"
	if p, err := machineConfigPathInUse(); err == nil && p != "" {
		machine = p
	}
	return fmt.Errorf("%w: %s holds a plaintext secret at %s (value redacted). "+
		"This file belongs to the repository, so it accepts only an environment reference such as "+
		"`token: env:MY_VARIABLE`. For GitHub, run `nightgauge forge auth refresh` "+
		"(it stores the gh token in the OS keychain and removes literal GitHub tokens from these files), "+
		"or name the account in github_user; for any credential, move the literal value to %s, "+
		"which is outside every repository. If the value was ever committed, rotate it",
		ErrRepoTierCredential, path, strings.Join(offending, ", "), machine)
}

// machineConfigPathInUse is the machine-tier file the loader actually reads:
// the canonical path, or the Linux legacy ~/.nightgauge/config.yaml when only
// that one exists.
func machineConfigPathInUse() (string, error) {
	path, err := machineConfigPathFn()
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if legacy := legacyMachineConfigPath(); legacy != "" && legacy != path {
			if _, legacyErr := os.Stat(legacy); legacyErr == nil {
				return legacy, nil
			}
		}
	}
	return path, nil
}

// IsMachineTierFile reports whether path is the machine-tier config file,
// canonical or Linux legacy, compared after resolving symlinks. Run from the
// home directory, `.nightgauge/config.yaml` IS the machine file; it is not a
// repository tier and may hold a literal credential.
func IsMachineTierFile(path string) bool {
	if path == "" {
		return false
	}
	target := canonicalFilePath(path)
	var candidates []string
	if p, err := machineConfigPathFn(); err == nil && p != "" {
		candidates = append(candidates, p)
	}
	if legacy := legacyMachineConfigPath(); legacy != "" {
		candidates = append(candidates, legacy)
	}
	for _, c := range candidates {
		if canonicalFilePath(c) == target {
			return true
		}
	}
	return false
}

func canonicalFilePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// The file may not exist; resolve its directory so /var vs /private/var
	// style aliases still compare equal.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return filepath.Clean(abs)
}

// redactCredentialShaped returns s, or a placeholder when s is itself shaped
// like a credential (a token pasted where a name belongs), so no message built
// from config names can carry one.
func redactCredentialShaped(s string) string {
	if credshape.Contains(s) {
		return "<redacted: credential-shaped name>"
	}
	return s
}
