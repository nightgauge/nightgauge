package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/credshape"
)

// workspaceFS is the workspace root as the read tools see it. Every path is
// resolved inside it twice: lexically and through its symlinks, before
// anything is opened, and again by the kernel, because every open goes
// through an os.Root that refuses a path leaving it however it is built. The
// containment is asserted at each step, never assumed from one.
type workspaceFS struct {
	// dir is the root's absolute path with every symlink resolved.
	dir  string
	root *os.Root
}

func openWorkspace(dir string) (*workspaceFS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, err
	}
	return &workspaceFS{dir: real, root: root}, nil
}

func (w *workspaceFS) close() {
	if w != nil && w.root != nil {
		_ = w.root.Close()
	}
}

var (
	errOutside = fail(CodeOutsideWorkspace, "the path is outside the workspace root; give a path relative to it, with no '..' that leaves it")
	errSecret  = fail(CodeSecretPath, "the path is a secret-bearing file, which a conversation never reads")
	errMissing = fail(CodeNotFound, "nothing exists at that path in the workspace")
)

// resolve returns where p lands inside the root, relative to it, once every
// symlink is followed. It refuses an absolute path, a path that climbs out,
// a symlink that resolves outside, and a secret-bearing path, whether the
// path names it or a symlink leads to it.
func (w *workspaceFS) resolve(p string) (string, *Error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || p == "./" {
		return ".", nil
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	if filepath.IsAbs(clean) || !filepath.IsLocal(clean) {
		return "", errOutside
	}
	if secretPath(clean) {
		return "", errSecret
	}
	real, err := filepath.EvalSymlinks(filepath.Join(w.dir, clean))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errMissing
		}
		return "", errOutside
	}
	rel, err := filepath.Rel(w.dir, real)
	if err != nil {
		return "", errOutside
	}
	if rel == "." {
		return ".", nil
	}
	if !filepath.IsLocal(rel) {
		return "", errOutside
	}
	if secretPath(rel) {
		return "", errSecret
	}
	return rel, nil
}

// abs is a resolved path's absolute location.
func (w *workspaceFS) abs(rel string) string { return filepath.Join(w.dir, rel) }

// openErr maps an os.Root error to a tool error.
func openErr(err error) *Error {
	if errors.Is(err, fs.ErrNotExist) {
		return errMissing
	}
	return errOutside
}

// secretExtensions and secretNames are the secret-bearing files the read
// tools refuse anywhere in the workspace, compared case-insensitively
// because a case-insensitive filesystem opens .ENV as .env.
var (
	secretExtensions = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore"}
	secretNames      = map[string]bool{
		".netrc": true, ".npmrc": true, ".pypirc": true, ".git-credentials": true,
		"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	}
)

// secretPath reports whether a workspace-relative path names a
// secret-bearing file: anything inside .git (its config, its hooks, its
// objects), an environment file (.env*), a key or certificate store
// (*.pem, *.key and the like), or a credential dotfile. The credential
// fields of the .nightgauge config files are not refused but redacted
// (redactConfig).
func secretPath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(strings.ToLower(rel)), "/")
	for _, part := range parts {
		if part == ".git" {
			return true
		}
	}
	base := parts[len(parts)-1]
	if strings.HasPrefix(base, ".env") || secretNames[base] {
		return true
	}
	for _, ext := range secretExtensions {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// nightgaugeConfig reports whether a workspace-relative path is a
// Nightgauge config file (.nightgauge/config.yaml, config.local.yaml, the
// legacy config.json), whose credential fields are redacted when read.
func nightgaugeConfig(rel string) bool {
	parts := strings.Split(filepath.ToSlash(strings.ToLower(rel)), "/")
	n := len(parts)
	if n < 2 || parts[n-2] != ".nightgauge" || !strings.HasPrefix(parts[n-1], "config") {
		return false
	}
	ext := filepath.Ext(parts[n-1])
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
}

// credentialKey matches a config key whose value is a credential or names
// one: github_auth.token, github_auth.tokens, github_auth.app.private_key and
// private_key_path, platform.license_key, and any key of the same kind added
// later.
var credentialKey = regexp.MustCompile(`(?i)(token|private_key|license_key|api_?key|secret|password|passphrase|credential)`)

// redactedValue replaces a credential field's value.
const redactedValue = "[REDACTED]"

// redactConfig returns a Nightgauge config file with every credential
// field's value replaced, then every remaining credential-shaped string. A
// file that does not parse is refused whole: what cannot be parsed cannot be
// shown to be free of credentials.
func redactConfig(raw []byte) (string, *Error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", fail(CodeSecretPath, "the config file does not parse, so its credential fields cannot be redacted; it is not shown")
	}
	redactNode(&doc)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return "", fail(CodeSecretPath, "the config file cannot be shown with its credential fields redacted")
	}
	return credshape.RedactEgress(string(out)), nil
}

func redactNode(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			redactNode(c)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if credentialKey.MatchString(key.Value) {
				// In place, so an anchor on the value now names the
				// redaction and an alias to it shows nothing more.
				val.Kind, val.Tag, val.Value, val.Content, val.Alias, val.Style =
					yaml.ScalarNode, "!!str", redactedValue, nil, nil, 0
				continue
			}
			redactNode(val)
		}
	}
}
