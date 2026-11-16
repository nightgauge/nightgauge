package opencodeplugin

// Coverage for OperatorInstallSatisfied, the read-only check of an
// operator-owned OpenCode config directory (#1635/A11, ADR-022 amendment
// 2026-09-15). It must mirror opencode's own install check and never write.

import (
	"os"
	"path/filepath"
	"testing"
)

// pinnedPluginVersion is the @opencode-ai/plugin version the fixtures name.
// OperatorInstallSatisfied never compares versions; any string works.
const pinnedPluginVersion = "1.18.30"

// writeOperatorNpmProject writes dir's package.json (a "dependencies" block
// naming @opencode-ai/plugin at pluginVersion, plus extraPackageJSONDep when
// non-empty — opencode's own documented way to add a custom tool's
// dependency), a package-lock.json whose root ("") package entry lists
// exactly @opencode-ai/plugin (never extraPackageJSONDep) under
// "dependencies" — the real npm lockfile shape — and node_modules/, so
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
// the pinned version exactly — the opposite of both directions of this check, and
// wrong in both directions the review measured against the real binary; see
// each subtest below.
func TestOperatorInstallSatisfiedMatchesOpenCodesOwnInstallCheck(t *testing.T) {
	t.Run("satisfied: real npm-shaped project, current pin", func(t *testing.T) {
		dir := t.TempDir()
		writeOperatorNpmProject(t, dir, pinnedPluginVersion, "")
		if !OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = false, want true: package.json's only dependency name is in package-lock.json's root package")
		}
	})

	t.Run("satisfied: an earlier opencode's install — version never compared", func(t *testing.T) {
		// opencode 1.18.30 never upgrades an already-satisfied directory and
		// never reads the installed version at all: round 8 required the
		// marker to equal the pinned version exactly, which the real check does
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
		writeOperatorNpmProject(t, dir, pinnedPluginVersion, "")
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
		writeOperatorNpmProject(t, dir, pinnedPluginVersion, "a-custom-tool-dependency")
		if OperatorInstallSatisfied(dir) {
			t.Error("OperatorInstallSatisfied(dir) = true, want false: package.json names a dependency package-lock.json's root package does not")
		}
	})

	t.Run("unsatisfied: node_modules absent", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"),
			[]byte(`{"dependencies":{"@opencode-ai/plugin":"`+pinnedPluginVersion+`"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package-lock.json"),
			[]byte(`{"packages":{"":{"dependencies":{"@opencode-ai/plugin":"`+pinnedPluginVersion+`"}}}}`), 0o644); err != nil {
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
