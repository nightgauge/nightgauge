package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempFile creates a temp file with the given content and returns its path.
// The file is removed when t.Cleanup runs.
func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// TestDetectPatterns_EachSlugMatches verifies that each of the 7 slugs can be
// triggered by a file containing one of its keywords.
func TestDetectPatterns_EachSlugMatches(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		slug    string
		content string
	}{
		{"event-system", "const emitter = new EventEmitter();\n"},
		{"auth-security", "function authenticate(token) {}\n"},
		{"service-pattern", "class UserService {\n}\n"},
		{"repo-storage", "class UserRepository {\n}\n"},
		{"config-system", "const config = loadConfig();\n"},
		{"pipeline-workflow", "const pipeline = new PipelineOrchestrator();\n"},
		{"ipc-transport", "const proc = spawn('node', []);\n"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.slug, func(t *testing.T) {
			path := writeTempFile(t, dir, tc.slug+".ts", tc.content)
			result, err := DetectPatterns(PatternDetectOptions{FilesGlob: path})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			found := false
			for _, p := range result.Patterns {
				if p.Slug == tc.slug {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("slug %q not found in result; got %v", tc.slug, result.Patterns)
			}
		})
	}
}

// TestDetectPatterns_NoMatch verifies that a file containing no keywords
// produces an empty Patterns slice.
func TestDetectPatterns_NoMatch(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "empty.ts", "const x = 42;\n")
	result, err := DetectPatterns(PatternDetectOptions{FilesGlob: path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Patterns) != 0 {
		t.Errorf("expected 0 patterns, got %d: %v", len(result.Patterns), result.Patterns)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %v", result.Warnings)
	}
}

// TestDetectPatterns_MultipleSlugsSameFile verifies that a file triggering
// multiple slugs has all matching slugs returned.
func TestDetectPatterns_MultipleSlugsSameFile(t *testing.T) {
	dir := t.TempDir()
	content := "class AuthService {\n  authenticate(token: string) {}\n}\n"
	path := writeTempFile(t, dir, "multi.ts", content)
	result, err := DetectPatterns(PatternDetectOptions{FilesGlob: path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	slugSet := make(map[string]bool)
	for _, p := range result.Patterns {
		slugSet[p.Slug] = true
	}
	if !slugSet["service-pattern"] {
		t.Error("expected service-pattern slug")
	}
	if !slugSet["auth-security"] {
		t.Error("expected auth-security slug")
	}
}

// TestDetectPatterns_ZeroFiles verifies that a glob matching no files returns
// empty Patterns and no warnings.
func TestDetectPatterns_ZeroFiles(t *testing.T) {
	result, err := DetectPatterns(PatternDetectOptions{FilesGlob: "/nonexistent/path/*.ts"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Patterns) != 0 {
		t.Errorf("expected 0 patterns, got %d", len(result.Patterns))
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %v", result.Warnings)
	}
}

// TestDetectPatterns_InvalidGlob verifies that a malformed glob returns an error.
func TestDetectPatterns_InvalidGlob(t *testing.T) {
	_, err := DetectPatterns(PatternDetectOptions{FilesGlob: "["})
	if err == nil {
		t.Fatal("expected error for invalid glob, got nil")
	}
}

// TestDetectPatterns_UnreadableFile verifies that an unreadable file adds a
// warning and does not cause a hard error (exit 0 semantics).
func TestDetectPatterns_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any file — chmod 000 test not meaningful")
	}
	dir := t.TempDir()
	path := writeTempFile(t, dir, "secret.ts", "const x = 1;\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	result, err := DetectPatterns(PatternDetectOptions{FilesGlob: path})
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected at least one warning for unreadable file")
	}
}

// TestDetectPatterns_EmptyFilesGlob verifies that an empty FilesGlob returns
// a hard error.
func TestDetectPatterns_EmptyFilesGlob(t *testing.T) {
	_, err := DetectPatterns(PatternDetectOptions{FilesGlob: ""})
	if err == nil {
		t.Fatal("expected error for empty FilesGlob, got nil")
	}
}

// TestDetectPatterns_OutputOrder verifies that the Patterns slice follows the
// fixed slugOrder (deterministic output).
func TestDetectPatterns_OutputOrder(t *testing.T) {
	dir := t.TempDir()
	// Content that triggers all 7 slugs.
	content := strings.Join([]string{
		"const emitter = new EventEmitter();",
		"function authenticate(token) {}",
		"class UserService {}",
		"class UserRepository {}",
		"const config = {};",
		"const pipeline = new PipelineOrchestrator();",
		"const proc = spawn('node', []);",
	}, "\n")
	path := writeTempFile(t, dir, "all.ts", content)
	result, err := DetectPatterns(PatternDetectOptions{FilesGlob: path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Patterns) != 7 {
		t.Fatalf("expected 7 patterns, got %d: %v", len(result.Patterns), result.Patterns)
	}
	for i, p := range result.Patterns {
		if p.Slug != slugOrder[i] {
			t.Errorf("position %d: expected slug %q, got %q", i, slugOrder[i], p.Slug)
		}
	}
}

// TestDetectPatterns_SchemaVersion verifies that the V field is always 1.
func TestDetectPatterns_SchemaVersion(t *testing.T) {
	result, err := DetectPatterns(PatternDetectOptions{FilesGlob: "/nonexistent/*.ts"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.V != 1 {
		t.Errorf("expected V=1, got %d", result.V)
	}
}
