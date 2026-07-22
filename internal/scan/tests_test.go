package scan

import (
	"context"
	"testing"
)

func runTestsScan(t *testing.T, dir string) *TestsScanResult {
	t.Helper()
	res, err := RunTestsScan(context.Background(), TestsOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunTestsScan: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

// TestRunTestsScan_EmptyWorkdir asserts schema contract: ratio resolves to a
// number (zero) on empty workdir, never null.
func TestRunTestsScan_EmptyWorkdir(t *testing.T) {
	dir := t.TempDir()
	res := runTestsScan(t, dir)
	if res.SourceFiles != 0 || res.TestFiles != 0 {
		t.Errorf("counts should be zero: %+v", res)
	}
	if res.TestToSourceRatio != 0.0 {
		t.Errorf("ratio = %f on empty workdir, want 0", res.TestToSourceRatio)
	}
}

// Zero source files → ratio is 0 (explicit guard, not NaN).
func TestRunTestsScan_OnlyTestsZeroSources(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "foo.test.ts", "")
	writeFile(t, dir, "bar.spec.js", "")
	res := runTestsScan(t, dir)
	if res.TestFiles != 2 {
		t.Errorf("test_files = %d, want 2", res.TestFiles)
	}
	if res.SourceFiles != 0 {
		t.Errorf("source_files = %d, want 0", res.SourceFiles)
	}
	if res.TestToSourceRatio != 0.0 {
		t.Errorf("ratio = %f, want 0 (zero-source guard)", res.TestToSourceRatio)
	}
}

// 1:1 mix → ratio = 1.0.
func TestRunTestsScan_OneToOneRatio(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "foo.test.ts", "")
	writeFile(t, dir, "bar.ts", "")
	res := runTestsScan(t, dir)
	if res.TestFiles != 1 || res.SourceFiles != 1 {
		t.Errorf("counts = test=%d source=%d, want 1/1", res.TestFiles, res.SourceFiles)
	}
	if res.TestToSourceRatio != 1.0 {
		t.Errorf("ratio = %f, want 1.0", res.TestToSourceRatio)
	}
}

// Each of the four test-name patterns is detected.
func TestRunTestsScan_AllFourTestPatternsDetected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.test.ts", "")  // *.test.*
	writeFile(t, dir, "b.spec.js", "")  // *.spec.*
	writeFile(t, dir, "c_test.go", "")  // *_test.*
	writeFile(t, dir, "test_d.py", "")  // test_*
	writeFile(t, dir, "regular.go", "") // source
	res := runTestsScan(t, dir)
	if res.TestFiles != 4 {
		t.Errorf("test_files = %d, want 4 (one of each pattern)", res.TestFiles)
	}
	if res.SourceFiles != 1 {
		t.Errorf("source_files = %d, want 1", res.SourceFiles)
	}
}

// A file matching a test pattern must NOT also count toward source_files.
func TestRunTestsScan_TestFilesNotCountedAsSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.test.ts", "")
	writeFile(t, dir, "a.spec.ts", "")
	writeFile(t, dir, "a_test.ts", "")
	res := runTestsScan(t, dir)
	if res.SourceFiles != 0 {
		t.Errorf("source_files = %d, want 0 (test files must not double-count)", res.SourceFiles)
	}
	if res.TestFiles != 3 {
		t.Errorf("test_files = %d, want 3", res.TestFiles)
	}
}

// Walk pruning skips excluded directories.
func TestRunTestsScan_ExcludedDirsPruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/lib.ts", "")
	writeFile(t, dir, "vendor/lib.go", "")
	writeFile(t, dir, "dist/bundle.js", "")
	writeFile(t, dir, "src/main.ts", "")
	res := runTestsScan(t, dir)
	if res.SourceFiles != 1 {
		t.Errorf("source_files = %d, want 1 (only src/main.ts; excluded dirs pruned)", res.SourceFiles)
	}
}

// Files outside the source-extension allowlist are ignored entirely.
func TestRunTestsScan_NonAllowlistExtensionIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "")
	writeFile(t, dir, "config.json", "")
	writeFile(t, dir, "main.go", "")
	res := runTestsScan(t, dir)
	if res.SourceFiles != 1 {
		t.Errorf("source_files = %d, want 1 (only main.go is allowlisted)", res.SourceFiles)
	}
	if res.TestFiles != 0 {
		t.Errorf("test_files = %d, want 0", res.TestFiles)
	}
}

// Empty workdir defaults to CWD (no error).
func TestRunTestsScan_EmptyWorkdirFallsToCWD(t *testing.T) {
	_, err := RunTestsScan(context.Background(), TestsOptions{Workdir: ""})
	if err != nil {
		t.Fatalf("RunTestsScan with empty workdir should fall back to CWD, got error: %v", err)
	}
}

// Direct classifier — guards against drift in the four-pattern matcher.
func TestIsTestFile_PatternMatching(t *testing.T) {
	tests := map[string]bool{
		"foo.test.ts":     true,
		"foo.spec.js":     true,
		"foo_test.go":     true,
		"test_foo.py":     true,
		"foo.ts":          false,
		"main.go":         false,
		"testdata.go":     false, // does not start with "test_" — that's a prefix match with underscore
		"untestable.go":   false,
		"foo.testharness": false, // no .test. (no dot after)
	}
	for name, want := range tests {
		if got := isTestFile(name); got != want {
			t.Errorf("isTestFile(%q) = %v, want %v", name, got, want)
		}
	}
}
