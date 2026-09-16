// Command regenerate rebuilds
// internal/execution/opencodeplugin/depsdata/opencode-ai-plugin-<version>.tar.gz,
// the embedded, version-pinned dependency archive opencodeplugin.WriteDependencies
// extracts (#1635, ADR-022 amendment 2026-09-14, round 2 "trim the embedded
// dependency tree"; #1635/A11 round 6, ADR-022 amendment 2026-09-15,
// "operator directories are never merged into").
//
// # Why this archive exists
//
// opencode 1.18.30 installs the npm package @opencode-ai/plugin into any
// OpenCode config directory whose resolved config carries a non-empty
// `plugin` array — independent of whether the plugin file itself imports
// that package — and every invocation that resolves such a config waits for
// that install before doing anything else. Nightgauge pre-seeds the RUN'S
// OWN config directory with a captured copy of that package so opencode's
// own install always finds nothing to do there.
//
// The first version of that captured copy was the entire `npm install`
// output for @opencode-ai/plugin@1.18.30 written into every directory alike:
// ~11 MB gzipped, 3,886 files, checked into this public repository. Driving
// the real 1.18.30 binary against progressively smaller trees (see the
// #1635 fix round 2 notes) established empirically that opencode's own "is
// this already installed" check reads exactly four files and nothing else:
//
//   - package.json                                   (the resolved dependency declaration)
//   - package-lock.json                               (root lockfile)
//   - node_modules/.package-lock.json                  (npm's own hidden lockfile)
//   - node_modules/@opencode-ai/plugin/package.json    (the version marker)
//
// Neither opencode's plugin loader nor the Nightgauge plugin itself
// (plugin/nightgauge.js, plugin/nightgauge/gates.js) ever requires anything
// else in the tree: both import only node:* built-ins and each other, never
// @opencode-ai/plugin or any of its transitive dependencies. So for a run's
// own OpenCode config directory — a directory this run just created and
// owns outright, where only the Nightgauge plugin ever loads — the other
// files are dead weight.
//
// A fourth-round fix seeded the SAME four-file stub into an operator-owned
// directory ($HOME/.opencode, an inherited OPENCODE_CONFIG_DIR) too, which a
// review found permanently broke an operator's own tool or plugin file that
// imports `@opencode-ai/plugin` — the stub has no dist/ and, once it
// satisfies opencode's own install check, opencode never re-installs to fix
// that. The fix for THAT re-embedded the complete, real installed tree
// (~10.4 MB) as a second, operator-only archive — which round 6 removes
// instead: Nightgauge no longer writes into an operator-owned OpenCode
// directory AT ALL (ADR-022 amendment 2026-09-15, narrowed AC1). OpenCode's
// own install into its own config directories is the operator's
// environment, exactly as in the operator's own OpenCode runs. This program
// now builds only the one archive, for the run's own directory.
//
// # What this program does
//
//  1. Runs a real `npm install --omit=optional --no-audit --no-fund
//     --ignore-scripts` of exactly `@opencode-ai/plugin@<version>` in a
//     scratch directory — the authentic upstream output, not hand-written —
//     so package.json, package-lock.json and node_modules/.package-lock.json
//     are real, consistent npm artifacts, not fabricated.
//  2. Builds the archive from exactly the four files listed above; the root
//     package.json is replaced with the canonical minimal form regardless of
//     what the scratch install's own package.json said (npm needs a "name"
//     to install into a directory cleanly; opencode's own check does not
//     read one).
//  3. Tars and gzips it with a fixed modification time, fixed owner and
//     stable (sorted) entry order, so re-running this on the same npm
//     registry state reproduces byte-identical output — no build timestamp,
//     host name or file-system order leaks in.
//  4. Writes it to internal/execution/opencodeplugin/depsdata/.
//
// --ignore-scripts disables npm lifecycle scripts for every package in the
// tree, including transitive ones: nothing in this pinned dependency set may
// run arbitrary code as a side effect of regenerating it.
//
// # Usage
//
//	go run ./internal/execution/opencodeplugin/depsdata/regenerate [--version 1.18.30]
//
// Requires network access and a real npm on PATH; never run by a test or by
// CI. After regenerating for a new version, update
// opencodeplugin.DepsVersion to match — TestDepsArchiveMatchesPinnedVersion
// fails until both agree — and re-run the #1635 real-binary integration
// suite (`-tags opencode_integration`) to reconfirm the four-file set is
// still everything opencode's install check reads on the new version.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// pinnedFiles is the empirically-determined minimal set the archive holds
// (see the package doc comment): every file opencode 1.18.30's own "is
// @opencode-ai/plugin already installed" check reads, and nothing else.
// Order here does not matter — the archive is written in sorted order
// regardless — but this is also the list a human reviewing a diff of this
// program should check against the fix round's notes before adding or
// removing an entry.
var pinnedFiles = []string{
	"package.json",
	"package-lock.json",
	filepath.Join("node_modules", ".package-lock.json"),
	filepath.Join("node_modules", "@opencode-ai", "plugin", "package.json"),
}

// archiveEpoch is the fixed modification time every tar entry and the gzip
// header itself carry, so regenerating the archive from identical npm
// output reproduces identical bytes regardless of when or where it runs.
var archiveEpoch = time.Unix(0, 0).UTC()

func main() {
	version := flag.String("version", "1.18.30", "the exact @opencode-ai/plugin version to pin")
	out := flag.String("out", "", "output path (default: depsdata/opencode-ai-plugin-<version>.tar.gz next to this program)")
	flag.Parse()

	self, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	depsDataDir := filepath.Join(self, "internal", "execution", "opencodeplugin", "depsdata")

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(depsDataDir, fmt.Sprintf("opencode-ai-plugin-%s.tar.gz", *version))
	}

	scratch, err := os.MkdirTemp("", "nightgauge-opencode-plugin-deps-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(scratch)

	if err := npmInstall(scratch, *version); err != nil {
		fatal(err)
	}
	if err := verifyPinnedVersion(scratch, *version); err != nil {
		fatal(err)
	}
	if err := writeArchive(scratch, outPath, pinnedFiles, *version); err != nil {
		fatal(err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", outPath, info.Size())
	fmt.Println("update opencodeplugin.DepsVersion if the version changed, then run:")
	fmt.Println("  go test ./internal/execution/opencodeplugin/...")
}

// npmInstall runs a real, authentic npm install of exactly
// @opencode-ai/plugin@version into dir: the source of truth for the
// archive. Real network access is required and expected — this program is
// never run by a test or by CI.
func npmInstall(dir, version string) error {
	// "name" is set explicitly, not left for npm to infer from the scratch
	// directory's own (randomised) basename: an inferred name would leak
	// into package-lock.json and node_modules/.package-lock.json and make
	// the archive non-reproducible between runs.
	pkg := fmt.Sprintf(`{"name":"nightgauge-opencode-plugin-deps","private":true,"dependencies":{"@opencode-ai/plugin":%q}}`, version)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		return fmt.Errorf("writing the scratch package.json: %w", err)
	}
	cmd := exec.Command("npm", "install",
		"--omit=optional", "--no-audit", "--no-fund", "--ignore-scripts", "--loglevel=error")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install: %w", err)
	}
	return nil
}

// verifyPinnedVersion reads back node_modules/@opencode-ai/plugin's own
// package.json and refuses to produce an archive that does not actually
// hold the requested version — npm silently satisfying a different version
// from a stale local cache would otherwise go unnoticed.
func verifyPinnedVersion(dir, version string) error {
	data, err := os.ReadFile(filepath.Join(dir, "node_modules", "@opencode-ai", "plugin", "package.json"))
	if err != nil {
		return fmt.Errorf("reading the installed @opencode-ai/plugin package.json: %w", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("decoding the installed @opencode-ai/plugin package.json: %w", err)
	}
	if pkg.Version != version {
		return fmt.Errorf("npm install produced @opencode-ai/plugin@%s, want %s", pkg.Version, version)
	}
	return nil
}

// writeArchive tars and gzips exactly names from src into outPath, in sorted
// order, every entry stamped with archiveEpoch and uid/gid 0: the same npm
// output always produces byte-identical output. Directory entries are
// omitted entirely — WriteDependencies creates any parent directory a
// regular file needs, so an archive need not carry one.
func writeArchive(src, outPath string, names []string, version string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	// gz.Header.ModTime stays the zero value (never set) so the gzip header
	// itself carries no timestamp, matching every tar entry below.
	tw := tar.NewWriter(gz)

	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for _, name := range sorted {
		if err := addFile(tw, src, name, version); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

// canonicalRootPackageJSON is what the archive's own top-level package.json
// holds, regardless of what npm install's scratch package.json said: exactly
// the one dependency this whole archive exists to satisfy, and nothing else
// — no "name" or "private" key leaking in from the scratch directory
// npmInstall gave a fixed name only so package-lock.json and
// node_modules/.package-lock.json regenerate byte-identically.
// WriteDependencies and every doc comment describing the embedded archive
// assume exactly this shape.
func canonicalRootPackageJSON(version string) []byte {
	return []byte(fmt.Sprintf(`{"dependencies":{"@opencode-ai/plugin":%q}}`, version))
}

// addFile adds one file to tw as a tar entry named with forward slashes (the
// archive format WriteDependencies expects), stamped with archiveEpoch and
// uid/gid/uname/gname all zeroed, so the entry's bytes depend only on the
// file's own content. The top-level package.json is replaced with
// canonicalRootPackageJSON rather than read from the scratch install; every
// other file is npm's own output, unmodified.
func addFile(tw *tar.Writer, src, relOSPath, version string) error {
	var data []byte
	if relOSPath == "package.json" {
		data = canonicalRootPackageJSON(version)
	} else {
		var err error
		data, err = os.ReadFile(filepath.Join(src, relOSPath))
		if err != nil {
			return fmt.Errorf("reading %s from the npm install: %w", relOSPath, err)
		}
	}
	hdr := &tar.Header{
		Name:     filepath.ToSlash(relOSPath),
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(data)),
		ModTime:  archiveEpoch,
		Uid:      0,
		Gid:      0,
		Uname:    "",
		Gname:    "",
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing the tar header for %s: %w", relOSPath, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("writing %s into the archive: %w", relOSPath, err)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "regenerate:", err)
	os.Exit(1)
}
