package layout

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteClassFile(t *testing.T) {
	root := gitInit(t)
	p, err := WriteClassFile(root, ClassPipeline, "issue-42.json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(evalRoot(t, root), ".git", "nightgauge", "pipeline", "issue-42.json")
	if p != want {
		t.Fatalf("WriteClassFile path = %q, want %q", p, want)
	}
	if got, _ := os.ReadFile(p); string(got) != `{"a":1}` {
		t.Fatalf("content = %q", got)
	}
	// A rewrite replaces the file and leaves no temporary behind.
	if _, err := WriteClassFile(root, ClassPipeline, "issue-42.json", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("pipeline dir holds %d entries, want 1 (no temp file left)", len(entries))
	}
	// Subdirectories are created.
	if _, err := WriteClassFile(root, ClassPlans, "sub/42-x.md", strings.NewReader("plan")); err != nil {
		t.Fatal(err)
	}
}

// TestCreateClassFile: a create never replaces a file already present, so a
// check-then-write race cannot overwrite another writer's file, and it leaves
// no temporary behind either way.
func TestCreateClassFile(t *testing.T) {
	root := gitInit(t)
	p, err := CreateClassFile(root, ClassPipeline, "seed.yaml", strings.NewReader("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateClassFile(root, ClassPipeline, "seed.yaml", strings.NewReader("second")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create err = %v, want fs.ErrExist", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "first" {
		t.Fatalf("content = %q, want the first writer's", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("pipeline dir holds %d entries, want 1 (no temp file left)", len(entries))
	}
	if _, err := CreateCheckoutFile(root, "complexity-model.yaml", strings.NewReader("m")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCheckoutFile(root, "complexity-model.yaml", strings.NewReader("n")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second checkout create err = %v, want fs.ErrExist", err)
	}
}

func TestAppendClassFile(t *testing.T) {
	root := gitInit(t)
	for _, line := range []string{"a\n", "b\n"} {
		if _, err := AppendClassFile(root, ClassPipeline, "history/x.jsonl", strings.NewReader(line)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := ClassFilePath(root, ClassPipeline, "history/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "a\nb\n" {
		t.Fatalf("appended content = %q", got)
	}
}

func TestClassFileConfinement(t *testing.T) {
	root := gitInit(t)
	for _, bad := range []string{"", "../x", "a/../../x", "/etc/passwd", "a//b", "./a"} {
		if _, err := WriteClassFile(root, ClassPlans, bad, strings.NewReader("x")); !errors.Is(err, ErrUnsafeClassFileName) {
			t.Errorf("WriteClassFile(%q) err = %v, want ErrUnsafeClassFileName", bad, err)
		}
	}
	if _, err := WriteClassFile(root, "worktrees", "x", strings.NewReader("x")); err == nil {
		t.Error("WriteClassFile(unknown class) = nil error")
	}

	// A symlink inside the class directory that points outside is not
	// followed, neither as a parent nor as the target.
	dir, err := PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := WriteClassFile(root, ClassPipeline, "out/x.json", strings.NewReader("x")); err == nil {
		t.Error("write through a symlinked parent succeeded")
	}
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendClassFile(root, ClassPipeline, "link.json", strings.NewReader("x")); err == nil {
		t.Error("append to a symlinked target succeeded")
	}
	if _, err := WriteClassFile(root, ClassPipeline, "link.json", strings.NewReader("x")); err == nil {
		t.Error("write to a symlinked target succeeded")
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("file outside CLONE changed: %q", got)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("files written outside CLONE: %v", entries)
	}
}

func TestClassFileOutsideGit(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	dir := t.TempDir()
	if _, err := WriteClassFile(dir, ClassPlans, "x.md", strings.NewReader("x")); !errors.Is(err, ErrNotGitRepository) {
		t.Fatalf("err = %v, want ErrNotGitRepository", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote into a non-repository: %v", entries)
	}
}
