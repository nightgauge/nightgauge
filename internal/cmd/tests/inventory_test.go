package tests

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func runInventory(t *testing.T, dir string) *InventoryResult {
	t.Helper()
	res, err := RunInventory(context.Background(), InventoryOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunInventory: %v", err)
	}
	if res.V != SchemaVersion {
		t.Errorf("V = %d, want %d", res.V, SchemaVersion)
	}
	if res.Warnings == nil {
		t.Errorf("warnings should never be nil (must be empty array, never null)")
	}
	return res
}

func TestRunInventory_EmptyWorkdir(t *testing.T) {
	dir := t.TempDir()
	res := runInventory(t, dir)
	if res.Counts.SourceFiles != 0 || res.Counts.TestFiles != 0 || res.Counts.UntestedFiles != 0 {
		t.Errorf("counts should all be zero on empty dir: %+v", res.Counts)
	}
}

func TestRunInventory_AllFourTestPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.test.ts", "")
	writeFile(t, dir, "a.ts", "")
	writeFile(t, dir, "b.spec.js", "")
	writeFile(t, dir, "b.js", "")
	writeFile(t, dir, "c_test.go", "")
	writeFile(t, dir, "c.go", "")
	writeFile(t, dir, "test_d.py", "")
	writeFile(t, dir, "d.py", "")
	res := runInventory(t, dir)
	if res.Counts.TestFiles != 4 {
		t.Errorf("test_files = %d, want 4 — got: %v", res.Counts.TestFiles, res.TestFiles)
	}
	if res.Counts.SourceFiles != 4 {
		t.Errorf("source_files = %d, want 4 — got: %v", res.Counts.SourceFiles, res.SourceFiles)
	}
	if res.Counts.UntestedFiles != 0 {
		t.Errorf("untested_files = %d, want 0 — got: %v", res.Counts.UntestedFiles, res.UntestedFiles)
	}
	wantMap := map[string]string{
		"a.test.ts": "a.ts",
		"b.spec.js": "b.js",
		"c_test.go": "c.go",
		"test_d.py": "d.py",
	}
	if !reflect.DeepEqual(res.TestToSourceMapping, wantMap) {
		t.Errorf("mapping = %v, want %v", res.TestToSourceMapping, wantMap)
	}
}

func TestRunInventory_UntestedDetection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "covered.ts", "")
	writeFile(t, dir, "covered.test.ts", "")
	writeFile(t, dir, "uncovered.ts", "")
	res := runInventory(t, dir)
	if res.Counts.UntestedFiles != 1 {
		t.Fatalf("untested_files count = %d, want 1 — got list: %v", res.Counts.UntestedFiles, res.UntestedFiles)
	}
	if res.UntestedFiles[0] != "uncovered.ts" {
		t.Errorf("untested[0] = %q, want %q", res.UntestedFiles[0], "uncovered.ts")
	}
}

func TestRunInventory_BasenameMapsAcrossDirectories(t *testing.T) {
	// A test in tests/ covering a source in src/ — basenames match, so the
	// source counts as covered per the SKILL.md Phase 1.4 prose.
	dir := t.TempDir()
	writeFile(t, dir, "src/widget.ts", "")
	writeFile(t, dir, "tests/widget.test.ts", "")
	res := runInventory(t, dir)
	if len(res.UntestedFiles) != 0 {
		t.Errorf("expected widget.ts to be covered by tests/widget.test.ts, got untested: %v", res.UntestedFiles)
	}
	mapped, ok := res.TestToSourceMapping["tests/widget.test.ts"]
	if !ok {
		t.Errorf("mapping missing key tests/widget.test.ts, got: %v", res.TestToSourceMapping)
	}
	// Mapping prefers the actual source file when the basename is unique
	// in sourceBases.
	if mapped != "src/widget.ts" {
		t.Errorf("mapping = %q, want src/widget.ts", mapped)
	}
}

func TestRunInventory_BasenameCollisionPrefersSameDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pkgA/user.go", "")
	writeFile(t, dir, "pkgB/user.go", "")
	writeFile(t, dir, "pkgB/user_test.go", "")
	res := runInventory(t, dir)
	mapped, ok := res.TestToSourceMapping["pkgB/user_test.go"]
	if !ok {
		t.Fatalf("mapping missing pkgB/user_test.go: %v", res.TestToSourceMapping)
	}
	if mapped != "pkgB/user.go" {
		t.Errorf("collision resolution = %q, want pkgB/user.go (same-dir preference)", mapped)
	}
}

func TestRunInventory_ExcludedDirsPruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/lib.ts", "")
	writeFile(t, dir, "vendor/lib.go", "")
	writeFile(t, dir, "dist/bundle.js", "")
	writeFile(t, dir, "build/x.go", "")
	writeFile(t, dir, ".git/config.go", "")
	writeFile(t, dir, "coverage/c.go", "")
	writeFile(t, dir, "src/main.ts", "")
	res := runInventory(t, dir)
	if res.Counts.SourceFiles != 1 {
		t.Errorf("source_files = %d, want 1 (only src/main.ts) — got: %v", res.Counts.SourceFiles, res.SourceFiles)
	}
}

func TestRunInventory_NonAllowlistExtensionsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "")
	writeFile(t, dir, "config.json", "")
	writeFile(t, dir, "main.go", "")
	res := runInventory(t, dir)
	if res.Counts.SourceFiles != 1 {
		t.Errorf("source_files = %d, want 1", res.Counts.SourceFiles)
	}
}

func TestRunInventory_SortedDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "z/y.ts", "")
	writeFile(t, dir, "a/b.ts", "")
	writeFile(t, dir, "m/n.ts", "")
	res := runInventory(t, dir)

	got := append([]string(nil), res.SourceFiles...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, res.SourceFiles) {
		t.Errorf("source_files not sorted: %v", res.SourceFiles)
	}
}

func TestSourceStem(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"a.test.ts":  {"a.ts", true},
		"a.spec.js":  {"a.js", true},
		"a_test.go":  {"a.go", true},
		"test_a.py":  {"a.py", true},
		"foo.ts":     {"", false},
		"main.go":    {"", false},
		"testdata":   {"", false}, // no extension, no test_ prefix
		"test_a":     {"a", true},
		"a.test.tsx": {"a.tsx", true},
	}
	for in, c := range cases {
		got, ok := sourceStem(in)
		if got != c.want || ok != c.ok {
			t.Errorf("sourceStem(%q) = (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}
