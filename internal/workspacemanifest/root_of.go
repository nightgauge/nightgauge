package workspacemanifest

import (
	"os"
	"path/filepath"
	"slices"
)

// RootOf returns the root of the workspace the checkout at mainCheckout belongs
// to (#3453): the directory holding the workspace manifest that claims it, or
// the checkout itself when no manifest does (single-repository mode). The
// result is canonical (absolute, symlink-evaluated, cleaned), so every
// checkout of one workspace maps to byte-identical text.
//
// mainCheckout must be a main checkout, not a linked worktree: a worktree's
// path (often under the machine-state root) says nothing about the workspace,
// so callers map a worktree to its main checkout first.
//
// A workspace's manifest lists its repositories by paths relative to the
// manifest root, and a sibling repository (`path: ../platform`) is not a
// descendant of that root, so a parent walk alone cannot find it. Resolution is
// therefore, first match wins:
//
//  1. the nearest ancestor of the checkout (the checkout included) whose
//     manifest parses and either sits in the checkout or lists it;
//  2. a sibling directory of the checkout, in sorted name order, whose manifest
//     parses and lists it;
//  3. the checkout itself.
//
// A manifest that does not parse is ignored. A repository listed by two
// workspaces' manifests resolves deterministically by that order.
func RootOf(mainCheckout string) string {
	c := canonicalPath(mainCheckout)
	for dir := c; ; dir = filepath.Dir(dir) {
		if m := loadQuiet(dir); m != nil && (dir == c || lists(m, c)) {
			return dir
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	parent := filepath.Dir(c)
	if parent == c {
		return c
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return c
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names {
		s := filepath.Join(parent, name)
		if s == c {
			continue
		}
		if m := loadQuiet(s); m != nil && lists(m, c) {
			return canonicalPath(s)
		}
	}
	return c
}

// loadQuiet parses dir's manifest, or returns nil when it is absent, not a
// regular file, or does not parse.
func loadQuiet(dir string) *Manifest {
	path := ManifestPath(dir)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	m, err := Load(path)
	if err != nil {
		return nil
	}
	return m
}

// lists reports whether one of m's repositories resolves to the canonical
// checkout c.
func lists(m *Manifest, c string) bool {
	for _, e := range m.Entries {
		if e.Path == "" {
			continue
		}
		if canonicalPath(m.ResolveEntryPath(e.Path)) == c {
			return true
		}
	}
	return false
}

// canonicalPath makes p absolute, evaluates symlinks when it exists, and
// cleans it.
func canonicalPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Clean(p)
}
