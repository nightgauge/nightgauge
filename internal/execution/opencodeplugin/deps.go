package opencodeplugin

// Read-only check of an operator-owned OpenCode config directory's npm state.
//
// Nightgauge's OpenCode plugin is a self-contained directory (plugin.go): it
// imports only node:* built-ins and each other, so a run's own config
// directory needs no npm package at all. The vendored @opencode-ai/plugin
// 1.18.30 seed that once stubbed opencode 1.x's own install there is removed
// (#3306). What remains is OperatorInstallSatisfied, which tells
// adapters.operatorInstallRisk whether a dispatch inheriting an operator's
// OpenCode config directory risks waiting on opencode's own install there.
// It never writes anything.
import (
	"encoding/json"
	"os"
	"path/filepath"
)

// pluginPackageName is the npm package opencode's own install check always
// requires, by name only.
const pluginPackageName = "@opencode-ai/plugin"

// OperatorInstallSatisfied reports, READ-ONLY, whether dir — an
// operator-owned OpenCode config directory ($HOME/.opencode, or an inherited
// OPENCODE_CONFIG_DIR under opencode.inherit_user_config) — already satisfies
// opencode 1.18.30's own "is @opencode-ai/plugin already installed" check
// (`Npm.install`, pulled from the strings of the pinned binary and driven
// against it directly — #1635/A11 fix round, correcting round 8). That
// check:
//
//  1. is satisfied immediately, without installing anything, if dir itself
//     is not writable (dirUnwritable);
//  2. is unsatisfied if dir/node_modules is absent;
//  3. otherwise is satisfied unless some dependency NAME in dir/package.json
//     — the "dependencies", "devDependencies", "peerDependencies" and
//     "optionalDependencies" blocks, plus @opencode-ai/plugin itself — is
//     missing from dir/package-lock.json's own root ("") package entry.
//
// It never compares versions, and never reads
// dir/node_modules/.package-lock.json or the installed package's own version
// marker at all: round 8's predicate — all four files exist AND the marker
// equals the pinned version exactly — checked files this reads nothing like, and
// read both directions wrong (deps_test.go's
// TestOperatorInstallSatisfiedMatchesOpenCodesOwnInstallCheck holds the
// measured cases). Nightgauge never seeds or merges anything into such a
// directory (#1635/A11 round 6, ADR-022 amendment 2026-09-15, narrowed AC1 —
// an earlier round did, and the archive that made that safe for a directory
// holding the operator's own tool/plugin files is removed); this exists only
// for adapters.InstallNightgaugePlugin (via operatorInstallRisk), never to
// write there.
func OperatorInstallSatisfied(dir string) bool {
	if dirUnwritable(dir) {
		return true
	}
	if fi, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil || !fi.IsDir() {
		return false
	}
	pkgData, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg npmDependencyBlock
	if err := json.Unmarshal(pkgData, &pkg); err != nil {
		return false
	}
	want := pkg.names()
	want[pluginPackageName] = struct{}{}

	lockData, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return false
	}
	var lock npmLockFile
	if err := json.Unmarshal(lockData, &lock); err != nil {
		return false
	}
	have := lock.Packages[""].names()
	for name := range want {
		if _, ok := have[name]; !ok {
			return false
		}
	}
	return true
}

// npmDependencyBlock is the shape opencode 1.18.30's own install check reads
// dependency NAMES out of twice: once from a root package.json, once from a
// package-lock.json's own root ("") entry in "packages". Both look
// identical to that check, so one type decodes either.
type npmDependencyBlock struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// names returns the union of every dependency NAME (never a version) this
// block declares, across all four dependency kinds.
func (b npmDependencyBlock) names() map[string]struct{} {
	out := map[string]struct{}{}
	for _, block := range [...]map[string]string{b.Dependencies, b.DevDependencies, b.PeerDependencies, b.OptionalDependencies} {
		for name := range block {
			out[name] = struct{}{}
		}
	}
	return out
}

// npmLockFile is the one field of a package-lock.json this package reads:
// its root ("") package entry, the only one opencode's own install check
// consults.
type npmLockFile struct {
	Packages map[string]npmDependencyBlock `json:"packages"`
}
