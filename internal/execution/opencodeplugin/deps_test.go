package opencodeplugin

// Coverage for the embedded, version-pinned @opencode-ai/plugin dependency
// set (#1635 fix round finding 2, and fix round 2's "trim the embedded
// dependency tree"): WriteDependencies must materialise a working
// node_modules tree with no network and no external process, DepsVersion
// must never silently drift from the archive it names, the archive itself
// must stay small, and OperatorInstallSatisfied — the read-only check that
// replaced round 5's operator-directory merge (#1635/A11 round 6, ADR-022
// amendment 2026-09-15, narrowed AC1) — must never write anything.

import (
	"archive/tar"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteDependenciesExtractsThePinnedPackage: a fresh directory gets a
// node_modules/@opencode-ai/plugin/package.json naming exactly DepsVersion,
// package.json and package-lock.json beside it — what opencode 1.18.30's own
// installer would have produced for `@opencode-ai/plugin@1.18.30`, so its
// own install of that package, triggered by any non-empty `plugin` array,
// finds nothing to do.
func TestWriteDependenciesExtractsThePinnedPackage(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDependencies(dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"package.json",
		"package-lock.json",
		filepath.Join("node_modules", "@opencode-ai", "plugin", "package.json"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("WriteDependencies did not write %s: %v", rel, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "node_modules", "@opencode-ai", "plugin", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := parsePackageVersion(data)
	if err != nil {
		t.Fatal(err)
	}
	if v != DepsVersion {
		t.Errorf("extracted @opencode-ai/plugin version = %q, want %q (DepsVersion)", v, DepsVersion)
	}
}

// TestWriteDependenciesIsPureExtraction: no npm, no node and no network are
// reachable (PATH is empty and every proxy variable points at an address
// nothing answers), yet WriteDependencies still succeeds — it is a pure
// archive extraction, never a package manager invocation. This is the
// regression test for the #1635 fix-round review's finding that the
// previous implementation ran a live `npm install`: weakening
// WriteDependencies back to shelling out to npm turns this red, because
// there is no npm on PATH to shell out to.
func TestWriteDependenciesIsPureExtraction(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:1")
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:1")
	t.Setenv("npm_config_registry", "http://192.0.2.1:1/")

	dir := t.TempDir()
	if err := WriteDependencies(dir); err != nil {
		t.Fatalf("WriteDependencies with no PATH and no reachable network: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@opencode-ai", "plugin")); err != nil {
		t.Fatal(err)
	}
}

// TestDepsArchiveMatchesPinnedVersion: DepsVersion and the archive's own
// package.json must name the same @opencode-ai/plugin version. Bumping one
// without the other — the regeneration doc comment on WriteDependencies
// warns about exactly this — turns this red.
func TestDepsArchiveMatchesPinnedVersion(t *testing.T) {
	v, err := DepsPackageVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != DepsVersion {
		t.Errorf("the embedded archive's own package.json names @opencode-ai/plugin %q, DepsVersion says %q", v, DepsVersion)
	}
}

// TestWriteDependenciesRefusesPathEscape: safeJoin, not the archive (which
// is fixed at build time and never attacker-controlled), is what this
// guards; it exists so a future archive format change cannot silently
// reintroduce a path-traversal write.
func TestWriteDependenciesRefusesPathEscape(t *testing.T) {
	dir := t.TempDir()
	if _, err := safeJoin(dir, "../../etc/passwd"); err == nil {
		t.Fatal("want an error joining a path that escapes dir")
	}
	if _, err := safeJoin(dir, "node_modules/@opencode-ai/plugin/package.json"); err != nil {
		t.Errorf("an ordinary relative path must join cleanly: %v", err)
	}
}

// TestParsePackageVersion is parsePackageVersion's own unit coverage,
// independent of the embedded archive.
func TestParsePackageVersion(t *testing.T) {
	cases := []struct {
		json    string
		want    string
		wantErr bool
	}{
		{`{"name":"x","version":"1.18.30","license":"MIT"}`, "1.18.30", false},
		{`{"version": "2.0.0-beta.1"}`, "2.0.0-beta.1", false},
		{`{"name":"x"}`, "", true},
		{`not json`, "", true},
	}
	for _, c := range cases {
		got, err := parsePackageVersion([]byte(c.json))
		if c.wantErr {
			if err == nil {
				t.Errorf("parsePackageVersion(%s): want an error", c.json)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePackageVersion(%s): %v", c.json, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePackageVersion(%s) = %q, want %q", c.json, got, c.want)
		}
	}
}

// TestWriteDependenciesFilePermissions: every file WriteDependencies writes
// is 0600 and every directory 0700, matching Write's plugin-tree
// permissions (opencodeplugin's stated contract for anything it writes into
// a run's config directory).
func TestWriteDependenciesFilePermissions(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDependencies(dir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "node_modules", "@opencode-ai", "plugin", "package.json")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 0600", target, perm)
	}
	dirInfo, err := os.Stat(filepath.Join(dir, "node_modules"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("node_modules mode = %o, want 0700", perm)
	}
}

// TestSafeJoinTrimsLeadingSlash guards safeJoin's normalisation of a tar
// entry name against a leading "/", which tar.Reader itself never produces
// for this archive but a hand-crafted one could.
func TestSafeJoinTrimsLeadingSlash(t *testing.T) {
	dir := t.TempDir()
	got, err := safeJoin(dir, "/package.json")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "package.json")
	if got != want {
		t.Errorf("safeJoin(%q, \"/package.json\") = %q, want %q", dir, got, want)
	}
	if !strings.HasPrefix(got, dir) {
		t.Errorf("safeJoin result %q escaped %q", got, dir)
	}
}

// writeOperatorNpmProject writes dir's package.json (a "dependencies" block
// naming @opencode-ai/plugin at pluginVersion, plus extraPackageJSONDep when
// non-empty — opencode's own documented way to add a custom tool's
// dependency), a package-lock.json whose root ("") package entry lists
// exactly @opencode-ai/plugin (never extraPackageJSONDep) under
// "dependencies" — the real npm lockfile shape captured in
// depsdata/opencode-ai-plugin-1.18.30.tar.gz's own package-lock.json
// (depsdata/README.md) — and node_modules/, so
// OperatorInstallSatisfied's own node_modules existence check passes too.
func writeOperatorNpmProject(t *testing.T, dir, pluginVersion, extraPackageJSONDep string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := `"@opencode-ai/plugin":"` + pluginVersion + `"`
	if extraPackageJSONDep != "" {
		deps += `,"` + extraPackageJSONDep + `":"1.0.0"`
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"dependencies":{`+deps+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := `{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"` + pluginVersion + `"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOperatorInstallSatisfiedMatchesOpenCodesOwnInstallCheck (#1635/A11 fix
// round, correcting round 8): OperatorInstallSatisfied must mirror opencode
// 1.18.30's own Npm.install, pulled from the strings of the pinned binary
// and driven against it directly (the #1635 fix-round review's probe
// scripts). That check:
//  1. is satisfied immediately, without installing anything, if the
//     directory itself is not writable;
//  2. is unsatisfied if node_modules is absent;
//  3. otherwise is satisfied unless some dependency NAME in package.json —
//     dependencies, devDependencies, peerDependencies,
//     optionalDependencies, plus @opencode-ai/plugin itself — is missing
//     from package-lock.json's own root ("") package entry.
//
// It never compares versions and never reads
// node_modules/.package-lock.json or the installed package's own version
// marker. Round 8 required all four files to exist AND the marker to equal
// DepsVersion exactly — the opposite of both directions of this check, and
// wrong in both directions the review measured against the real binary; see
// each subtest below.
func TestOperatorInstallSatisfiedMatchesOpenCodesOwnInstallCheck(t *testing.T) {
	t.Run("satisfied: real npm-shaped project, current pin", func(t *testing.T) {
		dir := t.TempDir()
		writeOperatorNpmProject(t, dir, DepsVersion, "")
		if !OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = false, want true: package.json's only dependency name is in package-lock.json's root package")
		}
	})

	t.Run("satisfied: an earlier opencode's install — version never compared", func(t *testing.T) {
		// opencode 1.18.30 never upgrades an already-satisfied directory and
		// never reads the installed version at all: round 8 required the
		// marker to equal DepsVersion exactly, which the real check does
		// not. This is the shape every operator whose $HOME/.opencode, or
		// inherit_user_config OPENCODE_CONFIG_DIR, was populated by an
		// earlier opencode version hits on every dispatch.
		dir := t.TempDir()
		writeOperatorNpmProject(t, dir, "1.18.29", "")
		if !OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = false, want true: opencode's own check never compares versions, only dependency names")
		}
	})

	t.Run("satisfied: no hidden lockfile (node_modules/.package-lock.json absent)", func(t *testing.T) {
		// Round 8 required this file to exist; opencode's own check never
		// reads it.
		dir := t.TempDir()
		writeOperatorNpmProject(t, dir, DepsVersion, "")
		if _, err := os.Stat(filepath.Join(dir, "node_modules", ".package-lock.json")); err == nil {
			t.Fatal("test fixture bug: the hidden lockfile must not exist for this case")
		}
		if !OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = false, want true: opencode's own check never reads node_modules/.package-lock.json")
		}
	})

	t.Run("unsatisfied: package.json names a dependency the lockfile root lacks", func(t *testing.T) {
		// OpenCode's own documented way to add a custom tool is
		// `import { tool } from "@opencode-ai/plugin"` declared as a
		// package.json dependency; opencode's own check reinstalls when the
		// lockfile has not caught up. Round 8's file-existence-only
		// predicate (and the old "satisfied (full set)" fixture's bare `{}`
		// lockfile) read this shape satisfied instead.
		dir := t.TempDir()
		writeOperatorNpmProject(t, dir, DepsVersion, "a-custom-tool-dependency")
		if OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = true, want false: package.json names a dependency package-lock.json's root package does not")
		}
	})

	t.Run("unsatisfied: node_modules absent", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"),
			[]byte(`{"dependencies":{"@opencode-ai/plugin":"`+DepsVersion+`"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package-lock.json"),
			[]byte(`{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"`+DepsVersion+`"}}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = true, want false: node_modules does not exist, so opencode's own check installs")
		}
	})

	t.Run("unsatisfied: package.json absent", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = true, want false: no package.json to read a dependency list from")
		}
	})

	t.Run("unsatisfied: directory does not exist at all", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "never-created")
		if OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = true, want false: dir does not exist at all")
		}
	})

	t.Run("satisfied: the directory itself is not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory write permission, so this case cannot be constructed")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		if !OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = false, want true: opencode's own Npm.install returns immediately, installing nothing, the moment its directory is not writable")
		}
	})
}

// TestOperatorInstallSatisfiedWritesNothing: OperatorInstallSatisfied must
// never write to dir, on any path through it — checked by making dir itself
// read-only (0500), which would turn any attempted write (even a MkdirAll)
// into an error the test would need to tolerate if this function ever tried
// one. A read-only dir also happens to be the "directory is not writable"
// shape opencode's own check treats as satisfied (deps_test.go's
// TestOperatorInstallSatisfiedMatchesOpenCodesOwnInstallCheck covers that
// return value in detail); this test's own assertion is the no-write
// guarantee, not that value.
func TestOperatorInstallSatisfiedWritesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permission, so no write attempt would fail here regardless")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	OperatorInstallSatisfied(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("OperatorInstallSatisfied wrote into a read-only directory it must never write to: %v", entries)
	}
}

// TestDepsArchiveSizeBudget (#1635 fix round 2, "trim the embedded
// dependency tree"): the embedded archive must stay well under the ~11 MB,
// 3,886-file tree the first version of this fix shipped. 32 KiB is
// justified in depsdata/README.md: the four pinned files run about 26 KB
// uncompressed and gzip to under 5 KB as of opencode 1.18.30, so 32 KiB
// leaves headroom for lockfile growth in a future pinned version while
// still catching a regression back toward embedding whole node_modules
// trees long before it reaches even 1% of the original size.
const depsArchiveSizeBudget = 32 * 1024

func TestDepsArchiveSizeBudget(t *testing.T) {
	stat, err := fs.Stat(depsArchive, depsArchiveName)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() > depsArchiveSizeBudget {
		t.Errorf("%s is %d bytes, over the %d-byte budget (depsdata/README.md § Size budget): "+
			"regenerate it with depsdata/regenerate and prune to what opencode's install check actually reads, "+
			"or raise the budget with a justification", depsArchiveName, stat.Size(), depsArchiveSizeBudget)
	}
}

// TestDepsArchiveHoldsOnlyThePinnedFourFiles pins the trimmed archive's own
// contents: exactly the four files that satisfy opencode 1.18.30's own
// install check for a run's own directory (depsdata/README.md), nothing
// from the full @opencode-ai/plugin dependency closure. The check itself
// reads only two of them, by dependency name (OperatorInstallSatisfied's
// doc comment); the other two are what a real install also produces.
// Re-embedding the whole installed tree (the pre-trim shape) turns this
// red.
func TestDepsArchiveHoldsOnlyThePinnedFourFiles(t *testing.T) {
	want := map[string]bool{
		"package.json":                                  true,
		"package-lock.json":                             true,
		"node_modules/.package-lock.json":               true,
		"node_modules/@opencode-ai/plugin/package.json": true,
	}
	f, err := depsArchive.Open(depsArchiveName)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	got := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeReg {
			got[hdr.Name] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("archive holds %d regular files %v, want exactly %v", len(got), got, want)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("archive is missing %s", name)
		}
	}
}
