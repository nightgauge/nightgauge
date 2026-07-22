// Tests for persistMaxConcurrent's pipeline-block targeting. See Issue #3195.
//
// The previous implementation did a naive first-match on any
// `max_concurrent:` line, which silently updated `autonomous.max_concurrent`
// whenever it appeared in the YAML before the `pipeline:` block. These tests
// pin the new pipeline-targeted behavior across the four shapes a user's
// config might be in.
package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupConfig provisions a Server pointing at a fresh temp workspace whose
// .nightgauge/config.yaml has the supplied body. persistMaxConcurrent
// only reads workspaceRoot.
func setupConfig(t *testing.T, body string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, ".nightgauge", "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return &Server{workspaceRoot: dir}, path
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(b)
}

// Regression: with autonomous.max_concurrent declared above pipeline.max_concurrent,
// the previous implementation would clobber the autonomous value and leave
// pipeline at its old value — producing the exact bug described in #3195.
func TestPersistMaxConcurrent_UpdatesPipelineEvenWhenAutonomousAppearsFirst(t *testing.T) {
	srv, path := setupConfig(t, `autonomous:
  max_concurrent: 1
  scan_interval: 30s
pipeline:
  max_concurrent: 3
  worktree_base: .worktrees
`)

	if err := srv.persistMaxConcurrent(5); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, path)
	if !strings.Contains(out, "  max_concurrent: 5") {
		t.Errorf("expected pipeline.max_concurrent: 5 in output:\n%s", out)
	}
	// Autonomous block must remain untouched.
	if !strings.Contains(out, "autonomous:\n  max_concurrent: 1") {
		t.Errorf("autonomous.max_concurrent must be preserved unchanged:\n%s", out)
	}
}

func TestPersistMaxConcurrent_UpdatesExistingPipelineKey(t *testing.T) {
	srv, path := setupConfig(t, `pipeline:
  max_concurrent: 3
  worktree_base: .worktrees
`)

	if err := srv.persistMaxConcurrent(7); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, path)
	if !strings.Contains(out, "  max_concurrent: 7") {
		t.Errorf("expected max_concurrent: 7, got:\n%s", out)
	}
}

func TestPersistMaxConcurrent_InsertsKeyWhenPipelineBlockExistsButKeyDoesNot(t *testing.T) {
	srv, path := setupConfig(t, `pipeline:
  worktree_base: .worktrees
`)

	if err := srv.persistMaxConcurrent(2); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, path)
	if !strings.Contains(out, "  max_concurrent: 2") {
		t.Errorf("expected inserted max_concurrent: 2, got:\n%s", out)
	}
	if !strings.Contains(out, "worktree_base: .worktrees") {
		t.Errorf("existing pipeline keys must be preserved, got:\n%s", out)
	}
}

func TestPersistMaxConcurrent_AppendsBlockWhenAbsent(t *testing.T) {
	srv, path := setupConfig(t, `project:
  number: 1
`)

	if err := srv.persistMaxConcurrent(4); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, path)
	if !strings.Contains(out, "pipeline:\n  max_concurrent: 4") {
		t.Errorf("expected appended pipeline block, got:\n%s", out)
	}
	if !strings.Contains(out, "project:\n  number: 1") {
		t.Errorf("existing project block must be preserved, got:\n%s", out)
	}
}

// Defense against indented-child false matches: nested keys like
// `context_schema_repair: { max_attempts: 1 }` previously leaked into the
// first-line replacement path.
func TestPersistMaxConcurrent_IgnoresNestedMaxAttempts(t *testing.T) {
	srv, path := setupConfig(t, `pipeline:
  context_schema_repair:
    max_attempts: 1
  max_concurrent: 3
`)

	if err := srv.persistMaxConcurrent(8); err != nil {
		t.Fatalf("persistMaxConcurrent: %v", err)
	}

	out := readConfig(t, path)
	if !strings.Contains(out, "  max_concurrent: 8") {
		t.Errorf("expected pipeline.max_concurrent: 8, got:\n%s", out)
	}
	if !strings.Contains(out, "    max_attempts: 1") {
		t.Errorf("nested max_attempts: 1 must remain unchanged, got:\n%s", out)
	}
}
