package layout

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestCheckoutDir pins CHECKOUT to <absolute-git-dir>/nightgauge-worktree
// (ADR-024 § 7): the main checkout's sits in the git common dir beside CLONE,
// a linked worktree's in its own git dir, so the two never share one, while
// both resolve the same CLONE.
func TestCheckoutDir(t *testing.T) {
	root := gitInit(t)
	gittest.Run(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, root, "worktree", "add", "-q", wt)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(evalRoot(t, root), ".git")

	for from, want := range map[string]string{
		root: filepath.Join(common, "nightgauge-worktree"),
		sub:  filepath.Join(common, "nightgauge-worktree"),
		wt:   filepath.Join(common, "worktrees", "wt", "nightgauge-worktree"),
	} {
		got, err := CheckoutDir(from)
		if err != nil || got != want {
			t.Errorf("CheckoutDir(%s) = %q, %v; want %q", from, got, err, want)
			continue
		}
		info, err := os.Stat(got)
		if err != nil || !info.IsDir() {
			t.Errorf("CHECKOUT %s not created: %v", got, err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Errorf("CHECKOUT %s mode = %#o, want 0700", got, info.Mode().Perm())
		}
		if clone, err := CloneDir(from); err != nil || clone != filepath.Join(common, "nightgauge") {
			t.Errorf("CloneDir(%s) = %q, %v; want the shared CLONE", from, clone, err)
		}
	}

	for _, bad := range []string{"", "relative"} {
		if _, err := CheckoutDir(bad); !errors.Is(err, ErrRootNotAbsolute) {
			t.Errorf("CheckoutDir(%q) err = %v, want ErrRootNotAbsolute", bad, err)
		}
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	if _, err := CheckoutDir(t.TempDir()); !errors.Is(err, ErrNotGitRepository) {
		t.Errorf("CheckoutDir(outside git) err = %v, want ErrNotGitRepository", err)
	}
}

// TestCheckoutDirIgnoresInheritedGitDir: a GIT_DIR a hook exported never
// redirects the resolver to another repository.
func TestCheckoutDirIgnoresInheritedGitDir(t *testing.T) {
	root, other := gitInit(t), gitInit(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	got, err := CheckoutDir(root)
	if err != nil || got != filepath.Join(evalRoot(t, root), ".git", "nightgauge-worktree") {
		t.Errorf("CheckoutDir with GIT_DIR set = %q, %v", got, err)
	}
}

// TestCheckoutSymlinksRefused: a CHECKOUT, or an entry in it, that is a
// symlink is refused, and a confined write never follows a link out.
func TestCheckoutSymlinksRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := gitInit(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".git", "nightgauge-worktree")); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckoutDir(root); !errors.Is(err, ErrUnsafeCloneDir) {
		t.Errorf("CheckoutDir over a symlinked CHECKOUT err = %v, want ErrUnsafeCloneDir", err)
	}

	root2 := gitInit(t)
	dir, err := CheckoutDir(root2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, CheckoutAttention)); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckoutPath(root2, CheckoutAttention); !errors.Is(err, ErrUnsafeCloneDir) {
		t.Errorf("CheckoutPath(symlinked entry) err = %v, want ErrUnsafeCloneDir", err)
	}
	if _, err := WriteCheckoutFile(root2, "attention/card.json", strings.NewReader("{}")); err == nil {
		t.Error("WriteCheckoutFile followed a symlinked directory out of CHECKOUT")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("a write escaped CHECKOUT into %s: %v", outside, entries)
	}
	for _, bad := range []string{"", "../x", "/etc/passwd", "a/../../x"} {
		if _, err := CheckoutPath(root2, bad); !errors.Is(err, ErrUnsafeClassFileName) {
			t.Errorf("CheckoutPath(%q) err = %v, want ErrUnsafeClassFileName", bad, err)
		}
	}
}

func TestCheckoutWriteAppendAndSubdir(t *testing.T) {
	root := gitInit(t)
	p, err := WriteCheckoutFile(root, CheckoutRunState, strings.NewReader(`{"s":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(evalRoot(t, root), ".git", "nightgauge-worktree", "run-state.json"); p != want {
		t.Errorf("WriteCheckoutFile = %q, want %q", p, want)
	}
	for _, line := range []string{"a\n", "b\n"} {
		if _, err := AppendCheckoutFile(root, "health/trends.jsonl", strings.NewReader(line)); err != nil {
			t.Fatal(err)
		}
	}
	trends, _ := CheckoutPath(root, "health/trends.jsonl")
	if got, _ := os.ReadFile(trends); string(got) != "a\nb\n" {
		t.Errorf("appended = %q", got)
	}
	dir, err := CheckoutSubdir(root, CheckoutAttention)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Errorf("CheckoutSubdir = %s: %v %v", dir, info, err)
	}
	if out := gittest.Run(t, root, "status", "--porcelain", "--ignored"); out != "" {
		t.Errorf("git status after CHECKOUT writes = %q, want clean", out)
	}
}

func TestCheckoutDisplay(t *testing.T) {
	if got := CheckoutDisplay(); got != ".git/nightgauge-worktree" {
		t.Errorf("CheckoutDisplay() = %q", got)
	}
	if got := CheckoutDisplay(CheckoutCurrentRun); got != ".git/nightgauge-worktree/current-run.json" {
		t.Errorf("CheckoutDisplay(current-run.json) = %q", got)
	}
}

// TestCheckoutEntriesLegacy: every entry's legacy path is under the old
// working-tree .nightgauge/, and the run-control singletons name the CLONE
// class a layout-v1 build kept them in.
func TestCheckoutEntriesLegacy(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range CheckoutEntries {
		if seen[e.Name] {
			t.Errorf("duplicate entry %s", e.Name)
		}
		seen[e.Name] = true
		if !strings.HasPrefix(e.Legacy, ".nightgauge/") || !strings.HasSuffix(e.Legacy, e.Name) {
			t.Errorf("%s: legacy %q", e.Name, e.Legacy)
		}
		if e.CloneLegacyClass != "" && e.Legacy != ".nightgauge/"+e.CloneLegacyClass+"/"+e.Name {
			t.Errorf("%s: v1 class %s does not match legacy %s", e.Name, e.CloneLegacyClass, e.Legacy)
		}
	}
}

func TestCloneRootOf(t *testing.T) {
	root := gitInit(t)
	clone, err := CloneDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		filepath.Join(clone, "pipeline", "history", "x.jsonl"): clone,
		filepath.Join(clone, "logs", "github-api-2026.jsonl"):  clone,
		filepath.Join(root, "nightgauge", "x.jsonl"):           "",
		filepath.Join(t.TempDir(), "nightgauge", "x"):          "",
		"relative/nightgauge/x":                                "",
	} {
		got, ok := CloneRootOf(p)
		if got != want || ok != (want != "") {
			t.Errorf("CloneRootOf(%s) = %q, %v; want %q", p, got, ok, want)
		}
	}
}
