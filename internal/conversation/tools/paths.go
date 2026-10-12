package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
	yaml "gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/credshape"
)

// workspaceFS is the workspace root as the read tools see it. A path is
// resolved once, lexically and then through its symlinks, and checked: it
// must stay inside the root and name no secret. The file opened is then that
// exact resolved path, one component at a time with O_NOFOLLOW from a
// descriptor on the root, so a symlink the check did not see (one swapped in
// after it, or a component the filesystem resolves differently) makes the
// open fail rather than lead somewhere unchecked.
type workspaceFS struct {
	// dir is the root's absolute path with every symlink resolved.
	dir string
	// rootFD is a descriptor on dir, the anchor every open starts from.
	rootFD int
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
	fd, err := unix.Open(real, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &workspaceFS{dir: real, rootFD: fd}, nil
}

func (w *workspaceFS) close() {
	if w != nil && w.rootFD >= 0 {
		_ = unix.Close(w.rootFD)
		w.rootFD = -1
	}
}

var (
	errOutside = fail(CodeOutsideWorkspace, "the path is outside the workspace root; give a path relative to it, with no '..' that leaves it")
	errSecret  = fail(CodeSecretPath, "the path is a secret-bearing file, which a conversation never reads")
	errMissing = fail(CodeNotFound, "nothing exists at that path in the workspace")
	errChanged = fail(CodeOutsideWorkspace, "the path changed while it was being read; it is not read")
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

// open opens a resolved, checked path: each component in turn, relative to
// the one before, never following a symlink, and never blocking on a FIFO.
// Any symlink on the way fails it, so what is opened is the path the checks
// passed.
func (w *workspaceFS) open(rel string) (*os.File, *Error) {
	if rel != "." && (!filepath.IsLocal(rel) || secretPath(rel)) {
		return nil, errOutside // only a resolved, checked path is ever opened
	}
	parts := []string{"."}
	if rel != "." {
		parts = strings.Split(filepath.ToSlash(rel), "/")
	}
	dirfd, owned := w.rootFD, false
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(dirfd, part, flags, 0)
		if owned {
			_ = unix.Close(dirfd)
		}
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				return nil, errMissing
			}
			return nil, errChanged // ELOOP or ENOTDIR: a symlink where the check saw none
		}
		dirfd, owned = fd, true
	}
	return os.NewFile(uintptr(dirfd), w.abs(rel)), nil
}

// secretExtensions and secretNames are the secret-bearing files the read
// tools refuse anywhere in the workspace. They are compared folded
// (foldName), because a case-insensitive filesystem opens .ENV, and on APFS
// also .git-credentialſ (long s), as the file the deny list names.
var (
	secretExtensions = foldAll(".pem", ".key", ".p12", ".pfx", ".jks", ".keystore")
	secretNames      = foldSet(".netrc", ".npmrc", ".pypirc", ".git-credentials",
		"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519")
	foldedGit        = foldName(".git")
	foldedEnvPrefix  = foldName(".env")
	foldedNightgauge = foldName(".nightgauge")
	foldedConfig     = foldName("config")
)

// foldName is a path component as a filesystem that ignores case, and some
// that ignore invisible characters, compares it: every character replaced by
// the smallest of its Unicode simple case-folding orbit (so s, S and ſ, or
// k, K and the Kelvin sign, are one), invisible format characters and
// variation selectors dropped, and trailing dots and spaces trimmed. Two
// names a filesystem may treat as one fold to the same string, so the deny
// list cannot be stepped around by spelling.
func foldName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) || r == '\u034f' {
			continue
		}
		low := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < low {
				low = f
			}
		}
		b.WriteRune(low)
	}
	return strings.TrimRight(b.String(), ". ")
}

func foldAll(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = foldName(n)
	}
	return out
}

func foldSet(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, n := range foldAll(names...) {
		out[n] = true
	}
	return out
}

// foldedParts is a workspace-relative path's components, folded.
func foldedParts(rel string) []string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		parts[i] = foldName(p)
	}
	return parts
}

// secretPath reports whether a workspace-relative path names a
// secret-bearing file: anything inside .git (its config, its hooks, its
// objects), an environment file (.env*), a key or certificate store
// (*.pem, *.key and the like), or a credential dotfile, compared folded in
// every component. The credential fields of the .nightgauge config files
// are not refused but redacted (redactConfig).
func secretPath(rel string) bool {
	parts := foldedParts(rel)
	for _, part := range parts {
		if part == foldedGit {
			return true
		}
	}
	base := parts[len(parts)-1]
	if strings.HasPrefix(base, foldedEnvPrefix) || secretNames[base] {
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
	parts := foldedParts(rel)
	n := len(parts)
	if n < 2 || parts[n-2] != foldedNightgauge || !strings.HasPrefix(parts[n-1], foldedConfig) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(parts[n-1]))
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
