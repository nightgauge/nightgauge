package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalDirectoryGateAllowsReadInsideWorktree(t *testing.T) {
	worktree := t.TempDir()
	file := filepath.Join(worktree, "notes.md")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"tool_name":"Read","cwd":%q,"tool_input":{"file_path":%q}}`, worktree, file)
	if got := EvaluateExternalDirectoryGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("a read inside the worktree must be allowed, got %q (%s)", got.Decision, got.Reason)
	}
}

// TestExternalDirectoryGateAllowsInWorktreeSymlink is AC3: a symlink that
// stays inside the worktree must keep working exactly like today.
func TestExternalDirectoryGateAllowsInWorktreeSymlink(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, "real.md")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"tool_name":"Read","cwd":%q,"tool_input":{"file_path":%q}}`, worktree, link)
	if got := EvaluateExternalDirectoryGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("an in-worktree symlink to an in-worktree file must be allowed, got %q (%s)", got.Decision, got.Reason)
	}
}

func TestExternalDirectoryGateAllowsSkillDir(t *testing.T) {
	worktree := t.TempDir()
	skillDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_SKILL_DIR", skillDir)
	t.Setenv("NIGHTGAUGE_CONTEXT_FILE", "")
	t.Setenv("NIGHTGAUGE_OUTPUT_FILE", "")

	file := filepath.Join(skillDir, "_includes", "note.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	input := fmt.Sprintf(`{"tool_name":"Read","cwd":%q,"tool_input":{"file_path":%q}}`, worktree, file)
	if got := EvaluateExternalDirectoryGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("a read under NIGHTGAUGE_SKILL_DIR must be allowed, got %q (%s)", got.Decision, got.Reason)
	}
}

// TestExternalDirectoryGateBlocksSymlinkPlantedInSkillDir is the issue's own
// verification shape: a symlink planted INSIDE an allow-listed directory,
// pointing outside every allow-listed directory, must be refused — the
// lexical permission map matches the link's own path and lets the read
// through; this gate resolves the symlink first.
func TestExternalDirectoryGateBlocksSymlinkPlantedInSkillDir(t *testing.T) {
	worktree := t.TempDir()
	skillDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_SKILL_DIR", skillDir)
	t.Setenv("NIGHTGAUGE_CONTEXT_FILE", "")
	t.Setenv("NIGHTGAUGE_OUTPUT_FILE", "")

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("shh"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillDir, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	input := fmt.Sprintf(`{"tool_name":"Read","cwd":%q,"tool_input":{"file_path":%q}}`, worktree, link)
	got := EvaluateExternalDirectoryGate([]byte(input))
	if got.Decision != "block" {
		t.Fatalf("a symlink planted inside an allow-listed dir pointing outside every allow-listed "+
			"root must be refused, got %q", got.Decision)
	}
	if got.Reason == "" {
		t.Fatal("a block must carry a reason")
	}
}

func TestExternalDirectoryGateBlocksSymlinkPlantedUnderTmp(t *testing.T) {
	worktree := t.TempDir()
	t.Setenv("NIGHTGAUGE_SKILL_DIR", "")
	t.Setenv("NIGHTGAUGE_CONTEXT_FILE", "")
	t.Setenv("NIGHTGAUGE_OUTPUT_FILE", "")

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("shh"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join("/tmp", fmt.Sprintf("nightgauge-1816-test-link-%d", os.Getpid()))
	_ = os.Remove(link)
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	input := fmt.Sprintf(`{"tool_name":"Read","cwd":%q,"tool_input":{"file_path":%q}}`, worktree, link)
	got := EvaluateExternalDirectoryGate([]byte(input))
	if got.Decision != "block" {
		t.Fatalf("a symlink under /tmp pointing outside every allow-listed root must be refused, got %q", got.Decision)
	}
}

func TestExternalDirectoryGateFailsOpenOnMalformedJSON(t *testing.T) {
	if got := EvaluateExternalDirectoryGate([]byte(`{not json`)); got.Decision != "allow" {
		t.Fatalf("malformed JSON must fail open, got %q", got.Decision)
	}
}

func TestExternalDirectoryGateFailsOpenOnNonReadTool(t *testing.T) {
	input := `{"tool_name":"Edit","cwd":"/tmp","tool_input":{"file_path":"/etc/passwd"}}`
	if got := EvaluateExternalDirectoryGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("a non-Read tool must be a no-op for this gate, got %q", got.Decision)
	}
}

func TestExternalDirectoryGateFailsOpenOnEmptyFilePath(t *testing.T) {
	input := `{"tool_name":"Read","cwd":"/tmp","tool_input":{"file_path":""}}`
	if got := EvaluateExternalDirectoryGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("an empty file_path must fail open, got %q", got.Decision)
	}
}

// TestExternalDirectoryGateNonexistentPathStillBlocked is the EvalSymlinks
// failure path (#1816 AC2): a path that cannot be resolved must fall back to
// its unresolved form, not to an implicit allow — the containment decision
// that follows must still run and refuse a path outside every allow-listed
// root.
func TestExternalDirectoryGateNonexistentPathStillBlocked(t *testing.T) {
	worktree := t.TempDir()
	t.Setenv("NIGHTGAUGE_SKILL_DIR", "")
	t.Setenv("NIGHTGAUGE_CONTEXT_FILE", "")
	t.Setenv("NIGHTGAUGE_OUTPUT_FILE", "")

	missing := filepath.Join(t.TempDir(), "does", "not", "exist.txt")
	input := fmt.Sprintf(`{"tool_name":"Read","cwd":%q,"tool_input":{"file_path":%q}}`, worktree, missing)
	got := EvaluateExternalDirectoryGate([]byte(input))
	if got.Decision != "block" {
		t.Fatalf("a nonexistent path outside every allow-listed root must still be refused, got %q", got.Decision)
	}
}
