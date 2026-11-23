package workspacemanifest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/configpath"
)

// A sibling's manifest is committed content: any repository cloned beside a
// checkout can list `../victim` and so move the victim's workspace root (and
// with it the Action Center store key, #3495). RootOf therefore honours a
// sibling claim only on evidence the claiming repository cannot write:
//
//  1. both checkouts' git `origin` remotes name the same forge host and owner;
//  2. the manifest root is listed in the machine config's
//     `workspace.trusted_roots`.
//
// Both come from places a clone's committed files never reach: each
// checkout's own `.git/config` (read with --local, the inherited git
// environment scrubbed) and the machine-tier config file. A remote URL form
// parseRemoteOwner does not recognise (a local path, file://, git://) counts
// as no origin, so it never matches.

// trustedRootsKey is the machine-tier key; internal/config.MachineTierKeys
// registers the same dotted path.
const trustedRootsKey = "workspace.trusted_roots"

// refusalLog receives the once-per-pair refusal notice; tests swap it.
var refusalLog io.Writer = os.Stderr

// refusedClaims dedupes refusal notices per (root, checkout) per process.
var refusedClaims sync.Map

// siblingClaimTrusted reports whether the canonical sibling root may claim
// the canonical checkout.
// trustedRoots is the machine config's trusted roots, and checkoutOwner
// the checkout's origin, both resolved once per RootOf call.
func siblingClaimTrusted(root string, trustedRoots []string, checkoutOwner remoteOwner) bool {
	if slices.Contains(trustedRoots, root) {
		return true
	}
	if !checkoutOwner.ok {
		return false
	}
	return originOwner(root) == checkoutOwner
}

// remoteOwner is a lowercased forge host and owner; ok is false when the
// origin is absent, unreadable or unrecognised.
type remoteOwner struct {
	host, owner string
	ok          bool
}

// logRefusedClaim writes the refusal once per (root, checkout) pair.
func logRefusedClaim(root, checkout string) {
	if _, seen := refusedClaims.LoadOrStore([2]string{root, checkout}, struct{}{}); seen {
		return
	}
	fmt.Fprintf(refusalLog,
		"workspacemanifest: ignoring %q's manifest claim on %q: their git origins do not name the same "+
			"host and owner and the root is not in %s\n", root, checkout, trustedRootsKey)
}

// originOwner returns the owner of dir's own `origin` remote. It reads only
// dir's repository config (--local), so it runs no hooks, and it drops the
// git environment variables that would redirect it to another repository or
// inject a value (GIT_DIR from a hook, GIT_CONFIG_* pairs), either of which
// would make every directory report the same origin.
func originOwner(dir string) remoteOwner {
	cmd := exec.Command("git", "-C", dir, "config", "--local", "--get", "remote.origin.url")
	cmd.Env = scrubGitEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return remoteOwner{}
	}
	h, o, ok := parseRemoteOwner(strings.TrimSpace(string(out)))
	return remoteOwner{h, o, ok}
}

func scrubGitEnv(env []string) []string {
	kept := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "GIT_DIR", k == "GIT_WORK_TREE", k == "GIT_COMMON_DIR",
			strings.HasPrefix(k, "GIT_CONFIG_"):
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// parseRemoteOwner extracts host and owner from https://host/owner/repo,
// http://, ssh://[user@]host[:port]/owner/repo and user@host:owner/repo.
func parseRemoteOwner(url string) (host, owner string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"),
		strings.HasPrefix(url, "ssh://"):
		_, rest, _ = strings.Cut(url, "://")
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			return "", "", false
		}
		host, rest = rest[:slash], rest[slash+1:]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		if strings.HasPrefix(host, "[") {
			return "", "", false // IPv6 literal: not a forge owner we can compare
		}
		if colon := strings.IndexByte(host, ':'); colon >= 0 {
			host = host[:colon]
		}
	case !strings.HasPrefix(url, "/") && strings.Contains(url, "@") &&
		strings.Contains(url, ":") && !strings.Contains(url, "://"):
		at := strings.IndexByte(url, '@')
		i := strings.IndexByte(url[at:], ':')
		if i < 0 {
			return "", "", false
		}
		colon := at + i
		host, rest = url[at+1:colon], url[colon+1:]
	default:
		return "", "", false
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if host == "" || len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return strings.ToLower(host), strings.ToLower(parts[0]), true
}

// machineTrustedRoots reads workspace.trusted_roots from the machine config
// file alone: a merged config would let a committed project config trust
// itself. Relative entries are skipped; a missing or unparseable file yields
// none.
func machineTrustedRoots() []string {
	path, err := configpath.MachineConfigPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		Workspace struct {
			TrustedRoots []string `yaml:"trusted_roots"`
		} `yaml:"workspace"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return nil
	}
	roots := make([]string, 0, len(cfg.Workspace.TrustedRoots))
	for _, r := range cfg.Workspace.TrustedRoots {
		if filepath.IsAbs(r) {
			roots = append(roots, canonicalPath(r))
		}
	}
	return roots
}
