package skillrender

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BundleExcluded reports whether an entry of a skill directory stays out of
// every bundle and install. rel is the entry's path inside the skill
// directory, slash-separated. The rule: a top-level CHANGELOG.md, any
// directory named tests or __tests__, and any file ending in .test.ts or
// .test.js, at any depth. Test material never ships, so .vscodeignore's
// **/*.test.ts cannot drop a file the bundle carries (#2948).
//
// It is the Go half of the rsync excludes in
// packages/nightgauge/scripts/bundle-marketplace.sh, is_excluded in
// scripts/lib/install_skill_tree.py and isSkillExcluded in the extension's
// bundledSkills.ts. TestBundleShipsEverySkillFile runs the script against it.
func BundleExcluded(rel string, isDir bool) bool {
	if rel == "CHANGELOG.md" {
		return true
	}
	name := path.Base(rel)
	if isDir {
		return name == "tests" || name == "__tests__"
	}
	return strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".test.js")
}

// sharedDirName is the sibling of the skill directories that every skill
// includes from. The bundle carries all of it, without excludes.
const sharedDirName = "_shared"

// BundleFileDiff is one file on which a skills bundle and its source disagree.
type BundleFileDiff struct {
	// Path is relative to the skills root, with forward slashes.
	Path string
	// Missing is true when source holds the file and the bundle does not, and
	// false when the bundle holds a file source does not.
	Missing bool
}

// DiffBundleFiles compares a skills bundle with its source tree: _shared, every
// skill directory in the bundle, and every StageSkillDirs skill whether the
// bundle holds it or not. Skills the bundle does not carry and no stage names
// are out of scope, because the bundle ships a subset by design.
func DiffBundleFiles(sourceRoot, bundleRoot string) ([]BundleFileDiff, error) {
	dirs := map[string]bool{sharedDirName: true}
	for _, dir := range StageSkillDirs {
		dirs[dir] = true
	}
	entries, err := os.ReadDir(bundleRoot)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = true
		}
	}
	names := make([]string, 0, len(dirs))
	for dir := range dirs {
		names = append(names, dir)
	}
	sort.Strings(names)

	var diffs []BundleFileDiff
	for _, dir := range names {
		src, err := shippedFiles(filepath.Join(sourceRoot, dir), dir != sharedDirName)
		if err != nil {
			return nil, err
		}
		dst, err := shippedFiles(filepath.Join(bundleRoot, dir), dir != sharedDirName)
		if err != nil {
			return nil, err
		}
		for file := range src {
			if !dst[file] {
				diffs = append(diffs, BundleFileDiff{Path: dir + "/" + file, Missing: true})
			}
		}
		for file := range dst {
			if !src[file] {
				diffs = append(diffs, BundleFileDiff{Path: dir + "/" + file})
			}
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return diffs, nil
}

// shippedFiles returns the files under dir, relative to it. A directory that
// does not exist holds no files; the caller's other side then reports them all.
func shippedFiles(dir string, applyExcludes bool) (map[string]bool, error) {
	files := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if applyExcludes && rel != "." && BundleExcluded(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			files[rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return files, nil
}
