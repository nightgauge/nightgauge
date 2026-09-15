package opencodeplugin

// Embedded, version-pinned @opencode-ai/plugin dependency set for a run's OWN
// OpenCode config directory (#1635 fix round, ADR-022 amendment 2026-09-14,
// findings 2 and "trim the embedded dependency tree"; #1635/A11 round 6,
// ADR-022 amendment 2026-09-15, "operator directories are never merged
// into").
//
// opencode 1.18.30 installs the npm package @opencode-ai/plugin into any
// OpenCode config directory whose resolved config carries a non-empty
// `plugin` array — independent of whether a plugin file imports that
// package — and every invocation that resolves such a config (`debug
// config`, `run`, ...) waits for that install before doing anything else.
// Observed directly against the pinned binary: with `npm install` run for
// it live, an unreachable registry does not fail fast; it blocks past a
// stage's own timeout.
//
// depsArchive (opencode-ai-plugin-1.18.30.tar.gz) holds exactly the four
// files driving the real 1.18.30 binary's own "is @opencode-ai/plugin
// already installed" check and nothing else — package.json,
// package-lock.json, node_modules/.package-lock.json and
// node_modules/@opencode-ai/plugin/package.json. WriteDependencies extracts
// it into a run's own OpenCode config directory — a directory this run just
// created and owns outright, where only the embedded Nightgauge plugin ever
// loads, and that plugin never imports @opencode-ai/plugin or anything in
// its dependency tree. A stub with no dist/ is safe there and NOWHERE ELSE.
//
// Nightgauge never seeds or merges anything into an operator-owned OpenCode
// config directory ($HOME/.opencode, an inherited OPENCODE_CONFIG_DIR): an
// earlier round did (MergeDependencies, removed here, and the ~10.4 MB
// depsOperatorArchive it extracted), and a review found the trimmed stub it
// fell back to for a directory the operator's own tool files import
// `@opencode-ai/plugin` from left that import permanently unresolvable,
// while the real installed tree it was widened to instead re-admitted a
// large, opaque binary blob into a public repository for a directory
// Nightgauge does not own. OpenCode's own install into its own config
// directories is the operator's environment, exactly as in the operator's
// own OpenCode runs — narrowed AC1 (ADR-022 amendment 2026-09-15). This
// package's OperatorInstallSatisfied is read-only: it tells a caller whether
// a dispatch touching such a directory risks that install waiting on the
// registry, never writes anything there.
//
// The archive is captured from a real `npm install --omit=optional
// --no-audit --no-fund --ignore-scripts` of exactly
// `@opencode-ai/plugin@1.18.30` (DepsVersion, the tested opencode version —
// ADR-022 § 20) and embedded read-only: no npm binary, no lifecycle script
// and no network round trip runs to produce it at dispatch time, ever. See
// depsdata/README.md for the full provenance, the empirical method that
// found the four-file set sufficient for the run's own directory, and how to
// regenerate it for a new pinned opencode version (depsdata/regenerate).
import (
	"archive/tar"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed depsdata/opencode-ai-plugin-1.18.30.tar.gz
var depsArchive embed.FS

// depsArchiveName is depsArchive's one entry.
const depsArchiveName = "depsdata/opencode-ai-plugin-1.18.30.tar.gz"

// DepsVersion is the exact @opencode-ai/plugin version depsArchive holds —
// the tested opencode version (ADR-022 § 20). It MUST match the "version"
// field of the archive's own node_modules/@opencode-ai/plugin/package.json;
// TestDepsArchiveMatchesPinnedVersion compares them.
const DepsVersion = "1.18.30"

// WriteDependencies extracts depsArchive into dir — a run's own OpenCode
// config directory, the exact directory 1.18.30 would otherwise install
// @opencode-ai/plugin into — creating node_modules/, package.json and
// package-lock.json there. Every directory is created 0700, every file
// 0600, matching Write's plugin-tree permissions. Best-effort by contract
// with the caller (adapters.InstallNightgaugePlugin): an error here must
// never fail a dispatch, since a target directory 1.18.30 finds unsatisfied
// still falls back to its own install.
//
// dir must be this run's own, freshly-created directory — never an
// operator-owned one. See the package doc comment for why: depsArchive's
// node_modules/@opencode-ai/plugin holds only a version marker, no dist/,
// which is safe only where nothing but the embedded Nightgauge plugin ever
// imports it. Nightgauge never writes into an operator-owned directory at
// all (OperatorInstallSatisfied is read-only).
//
// A path in the archive that would escape dir is refused rather than
// written — the archive is embedded and fixed at build time, so this can
// only ever catch a corrupt build, never anything a run's own environment
// or a target repository controls.
func WriteDependencies(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("opencodeplugin: creating %s: %w", dir, err)
	}
	return extractArchive(dir)
}

// pluginPackageName is the npm package depsArchive exists to satisfy, and
// OperatorInstallSatisfied checks the version of.
const pluginPackageName = "@opencode-ai/plugin"

// extractArchive walks depsArchive's one entry into dir, truncating and
// writing every regular file it finds unconditionally. dir must already
// exist (WriteDependencies creates it fresh). extractArchive has exactly one
// caller and one write policy now: an earlier round's merge policy for
// operator-owned directories (a same-signature writer that special-cased a
// file by name — never overwriting an existing lockfile, never truncating an
// existing node_modules entry) is removed along with the merge machinery
// itself (#1635/A11 round 6, ADR-022 amendment 2026-09-15, "operator
// directories are never merged into"); nothing in this package still needs a
// pluggable write policy. A path in the archive that would escape dir is
// refused rather than written — the archive is embedded and fixed at build
// time, so this can only ever catch a corrupt build, never anything a run's
// own environment or a target repository controls.
func extractArchive(dir string) error {
	f, err := depsArchive.Open(depsArchiveName)
	if err != nil {
		return fmt.Errorf("opencodeplugin: opening the embedded dependency archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("opencodeplugin: the embedded dependency archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("opencodeplugin: reading the embedded dependency archive: %w", err)
		}
		target, err := safeJoin(dir, hdr.Name)
		if err != nil {
			return fmt.Errorf("opencodeplugin: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("opencodeplugin: creating %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("opencodeplugin: creating %s: %w", filepath.Dir(target), err)
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("opencodeplugin: reading %s from the embedded dependency archive: %w", hdr.Name, err)
			}
			if err := os.WriteFile(target, data, 0o600); err != nil {
				return fmt.Errorf("opencodeplugin: writing %s: %w", target, err)
			}
		default:
			// A symlink or other special entry: the embedded archive carries
			// none (only four plain files); skip rather than write anything
			// unexpected.
		}
	}
}

// OperatorInstallSatisfied reports, READ-ONLY, whether dir — an
// operator-owned OpenCode config directory ($HOME/.opencode, or an inherited
// OPENCODE_CONFIG_DIR under opencode.inherit_user_config) — already holds the
// full set opencode 1.18.30's own "is @opencode-ai/plugin already installed"
// check reads (depsdata/README.md's table: package.json, package-lock.json,
// node_modules/.package-lock.json, and node_modules/@opencode-ai/plugin's own
// package.json naming DepsVersion), not only the version marker. Nightgauge
// never seeds or merges anything into such a directory (#1635/A11 round 6,
// ADR-022 amendment 2026-09-15, narrowed AC1 — an earlier round did, and the
// archive that made that safe for a directory holding the operator's own
// tool/plugin files is removed); this exists only for
// adapters.InstallNightgaugePlugin, never to write there.
//
// #1635/A11 round 8 (ADR-022 amendment 2026-09-15, correcting round 7):
// checking only the version marker made this report "satisfied" for a
// directory that, on the pinned binary, still took opencode's own ~71s
// unsatisfied-marker wait — round 7's own re-measurement conflated "the
// marker names the right version" with "opencode's own check passes," which
// reads the other three files too. Driven against the real binary with the
// full set this function now checks, a satisfied operator directory DOES get
// opencode's local, instant fast path (~1s), matching a run's own
// XDG-resolved config directory (WriteDependencies' target, checked the same
// way this function checks an operator directory). operatorInstallRisk
// (adapters/opencode_plugin_deps.go) arms the watchdog only for a directory
// this reports unsatisfied.
func OperatorInstallSatisfied(dir string) bool {
	for _, rel := range operatorInstallCheckedFiles {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			return false
		}
	}
	v, err := installedPluginVersion(dir)
	return err == nil && v == DepsVersion
}

// operatorInstallCheckedFiles are the three files, beside the version marker
// installedPluginVersion itself reads, that opencode 1.18.30's own "is
// @opencode-ai/plugin already installed" check reads (depsdata/README.md's
// table) — the same four files WriteDependencies extracts for a run's own
// directory.
var operatorInstallCheckedFiles = []string{
	"package.json",
	"package-lock.json",
	"node_modules/.package-lock.json",
}

// installedPluginVersion reads dir's own
// node_modules/@opencode-ai/plugin/package.json, if any, and returns the
// version it names.
func installedPluginVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "node_modules", "@opencode-ai", "plugin", "package.json"))
	if err != nil {
		return "", err
	}
	return parsePackageVersion(data)
}

// safeJoin joins dir and name (a tar entry path, always "/"-separated),
// refusing a name that would resolve outside dir.
func safeJoin(dir, name string) (string, error) {
	clean := filepath.FromSlash(strings.TrimPrefix(name, "/"))
	target := filepath.Join(dir, clean)
	if target != dir && !strings.HasPrefix(target, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return target, nil
}

// DepsPackageVersion reads the "version" field of depsArchive's own
// node_modules/@opencode-ai/plugin/package.json without extracting anything
// to disk — what TestDepsArchiveMatchesPinnedVersion compares DepsVersion
// against, so the two can never drift apart unnoticed.
func DepsPackageVersion() (string, error) {
	f, err := depsArchive.Open(depsArchiveName)
	if err != nil {
		return "", fmt.Errorf("opencodeplugin: opening the embedded dependency archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("opencodeplugin: the embedded dependency archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	const want = "node_modules/@opencode-ai/plugin/package.json"
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("opencodeplugin: the embedded dependency archive has no %s", want)
		}
		if err != nil {
			return "", fmt.Errorf("opencodeplugin: reading the embedded dependency archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Name != want {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return "", fmt.Errorf("opencodeplugin: reading %s from the embedded dependency archive: %w", want, err)
		}
		return parsePackageVersion(data)
	}
}

// parsePackageVersion pulls the top-level "version" string out of a
// package.json without a full JSON dependency: this package otherwise
// imports nothing but the standard library.
func parsePackageVersion(data []byte) (string, error) {
	const key = `"version"`
	i := strings.Index(string(data), key)
	if i < 0 {
		return "", fmt.Errorf(`no "version" key found`)
	}
	rest := data[i+len(key):]
	q1 := strings.IndexByte(string(rest), '"')
	if q1 < 0 {
		return "", fmt.Errorf(`no "version" value found`)
	}
	rest = rest[q1+1:]
	q2 := strings.IndexByte(string(rest), '"')
	if q2 < 0 {
		return "", fmt.Errorf(`unterminated "version" value`)
	}
	return string(rest[:q2]), nil
}
