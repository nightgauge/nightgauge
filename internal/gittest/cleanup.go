package gittest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// maxLeftoverEntries caps the leftover listing so a fixture that left a whole
// object store behind still produces a readable failure.
const maxLeftoverEntries = 50

// ReportWritersOnCleanup removes dir at test cleanup, before t.TempDir's own
// RemoveAll, and fails the test with what is still in it when the removal
// fails (#2786). Call it after the t.TempDir() that created dir: cleanups run
// last-registered first, so this one runs ahead of the TempDir removal.
//
// It does not retry. A background git writer that outlives the test is a real
// leak, and retrying until it stops would hide it (#680, #2759). The failure
// instead lists the entries the writer left in dir — paths, sizes, mtimes
// relative to the start of cleanup, and the contents of gc.pid and gc.log —
// with a hint naming the writer each one points to, so the next recurrence
// names its writer instead of leaving it to be inferred from a trace.
//
// dir must be inside os.TempDir(): this helper deletes it, and a fixture that
// passed a real checkout by mistake fails at registration instead.
func ReportWritersOnCleanup(t testing.TB, dir string) {
	t.Helper()
	if !underTempDir(dir) {
		t.Fatalf("gittest.ReportWritersOnCleanup: %s is not inside %s; refusing to register its removal",
			dir, os.TempDir())
		return
	}
	t.Cleanup(func() {
		began := time.Now()
		err := os.RemoveAll(dir)
		if err == nil {
			return
		}
		t.Errorf("cleanup of %s failed: %v\nstill present (a process kept writing it after the test ended):\n%s",
			dir, err, describeLeftovers(dir, began))
	})
}

// underTempDir reports whether dir is an absolute path strictly inside
// os.TempDir(), comparing both the raw and the symlink-resolved forms (macOS
// resolves /var to /private/var, and fixtures often pass the resolved path).
func underTempDir(dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	clean := filepath.Clean(dir)
	resolved := clean
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		resolved = r
	}
	tmp := filepath.Clean(os.TempDir())
	roots := []string{tmp}
	if r, err := filepath.EvalSymlinks(tmp); err == nil && r != tmp {
		roots = append(roots, r)
	}
	for _, root := range roots {
		for _, p := range []string{clean, resolved} {
			if rel, err := filepath.Rel(root, p); err == nil && rel != "." &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// describeLeftovers lists what remains under root after a failed removal:
// every entry with its size and its mtime relative to since, the contents of
// gc.pid and gc.log, and one hint per kind of writer the entries point to.
// It is pure over the filesystem so it can be tested on a planted tree.
func describeLeftovers(root string, since time.Time) string {
	type entry struct {
		rel  string
		info fs.FileInfo
	}
	var entries []entry
	total := 0
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// A writer still running can remove an entry between the
			// directory read and the lstat; that entry is simply gone.
			if TolerateConcurrentScratchWrite(err) {
				return nil
			}
			return err
		}
		if path == root {
			return nil
		}
		total++
		if len(entries) < maxLeftoverEntries {
			rel, _ := filepath.Rel(root, path)
			entries = append(entries, entry{rel: filepath.ToSlash(rel), info: info})
		}
		return nil
	})

	var b strings.Builder
	if len(entries) == 0 {
		b.WriteString("  (no entries: the directory itself could not be removed)\n")
	}
	for _, e := range entries {
		name := e.rel
		if e.info.IsDir() {
			name += "/"
		}
		fmt.Fprintf(&b, "  %s  %d bytes  mtime %s\n", name, e.info.Size(), relativeMtime(e.info.ModTime(), since))
	}
	if total > len(entries) {
		fmt.Fprintf(&b, "  ... %d more entries not listed\n", total-len(entries))
	}
	if walkErr != nil {
		fmt.Fprintf(&b, "  (listing stopped: %v)\n", walkErr)
	}

	rels := make([]string, len(entries))
	for i, e := range entries {
		rels[i] = e.rel
	}
	hints := writerHints(root, rels)
	if len(hints) > 0 {
		b.WriteString("likely writer:\n")
		for _, h := range hints {
			fmt.Fprintf(&b, "  - %s\n", h)
		}
	}
	return b.String()
}

// relativeMtime renders an mtime as an offset from the start of cleanup, so a
// reader sees at a glance which entries were written after the test ended.
func relativeMtime(mtime, since time.Time) string {
	d := mtime.Sub(since).Round(time.Millisecond)
	if d >= 0 {
		return fmt.Sprintf("+%s after cleanup began", d)
	}
	return fmt.Sprintf("%s before cleanup began", -d)
}

// writerHints names the git process each leftover points to. The hints sit
// beside the raw listing, never in place of it: they are an inference from
// file names, and the listing is the evidence.
func writerHints(root string, rels []string) []string {
	seen := map[string]bool{}
	var hints []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			hints = append(hints, h)
		}
	}
	for _, rel := range rels {
		base := filepath.Base(rel)
		switch {
		case base == "gc.pid":
			add("git gc (detached): gc.pid = " + firstLine(filepath.Join(root, filepath.FromSlash(rel))))
		case base == "gc.log":
			add("an earlier detached git gc logged: " + firstLine(filepath.Join(root, filepath.FromSlash(rel))))
		case strings.HasPrefix(base, "commit-graph") || strings.Contains(rel, "commit-graphs/") ||
			strings.HasPrefix(base, "multi-pack-index"):
			add("git maintenance or gc: commit-graph/multi-pack-index written (" + rel + ")")
		case strings.Contains(rel, "objects/pack/") && (strings.HasPrefix(base, "tmp_") || strings.HasPrefix(base, ".tmp-")):
			add("git gc or repack: temporary pack " + rel)
		case strings.HasSuffix(base, ".lock"):
			add("a running git command holds " + rel)
		}
	}
	sort.Strings(hints)
	return hints
}

// firstLine returns the first line of path, trimmed, or a note that it could
// not be read (the writer may have removed it after the listing).
func firstLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(unreadable: %v)", err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "(empty)"
	}
	return line
}
