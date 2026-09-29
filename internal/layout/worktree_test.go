package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return gittest.InitRepo(t, dir, "-q")
}

func TestRepoKey_ShapeAndStability(t *testing.T) {
	k := RepoKey("/a/b/.git")
	if len(k) != 12 || strings.Trim(k, "0123456789abcdef") != "" {
		t.Fatalf("RepoKey = %q, want 12 lowercase hex characters", k)
	}
	if RepoKey("/a/b/.git/") != k {
		t.Fatal("RepoKey must not depend on a trailing separator")
	}
	if RepoKey("/a/c/.git") == k {
		t.Fatal("two clones share a key")
	}
}

func TestWorktreeBase_DefaultIsStateKeyedOnCommonDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(EnvStateHome, state)
	root := gitInit(t)

	got, err := WorktreeBase(root, WorktreeBaseSetting{})
	if err != nil {
		t.Fatalf("WorktreeBase: %v", err)
	}
	common, err := GitCommonDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(state, "worktrees", RepoKey(common)); got != want {
		t.Fatalf("WorktreeBase = %q, want %q", got, want)
	}
	// A linked worktree of the same clone resolves to the same base.
	linked := filepath.Join(t.TempDir(), "linked")
	gittest.Run(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x")
	gittest.Run(t, root, "worktree", "add", "-q", "--detach", linked)
	if fromLinked, err := WorktreeBase(linked, WorktreeBaseSetting{}); err != nil || fromLinked != got {
		t.Fatalf("WorktreeBase from a linked worktree = (%q, %v), want %q", fromLinked, err, got)
	}
}

func TestWorktreeBase_Configured(t *testing.T) {
	root := gitInit(t)
	home, _ := os.UserHomeDir()
	abs := filepath.Join(t.TempDir(), "wt")
	src := WorktreeBaseSetting{Source: "/cfg/config.local.yaml", Line: 4}

	cases := []struct {
		value   string
		want    string
		wantErr string
	}{
		{value: abs, want: abs},
		{value: "~/ng-wt", want: filepath.Join(home, "ng-wt")},
		{value: ".worktrees", wantErr: "relative path"},
		{value: "~other/x", wantErr: "only a leading ~"},
		{value: filepath.Join(root, "in-tree"), wantErr: "inside the working tree"},
		{value: root, wantErr: "inside the working tree"},
	}
	for _, tc := range cases {
		s := src
		s.Value = tc.value
		got, err := WorktreeBase(root, s)
		if tc.wantErr != "" {
			if !errors.Is(err, ErrWorktreeBase) || !strings.Contains(err.Error(), tc.wantErr) ||
				!strings.Contains(err.Error(), "/cfg/config.local.yaml:4") {
				t.Errorf("%q: error = %v, want ErrWorktreeBase naming %q and the file:line", tc.value, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: WorktreeBase = (%q, %v), want %q", tc.value, got, err, tc.want)
		}
	}
}

func TestWorktreeBase_RefusesRelativeRoot(t *testing.T) {
	if _, err := WorktreeBase("relative", WorktreeBaseSetting{}); !errors.Is(err, ErrRootNotAbsolute) {
		t.Fatalf("error = %v, want ErrRootNotAbsolute", err)
	}
}

func TestWorktreePath_Containment(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	got, err := WorktreePath(base, "owner/repo", 12)
	if err != nil || got != filepath.Join(base, "repo-issue-12") {
		t.Fatalf("WorktreePath = (%q, %v)", got, err)
	}
	for _, repo := range []string{"..", "owner/..", ".", "a/b c", "a/-x", "a/x..y"} {
		if _, err := WorktreePath(base, repo, 1); !errors.Is(err, ErrWorktreeEscape) {
			t.Errorf("repo %q: error = %v, want ErrWorktreeEscape", repo, err)
		}
	}
	if _, err := WorktreePath("rel/base", "repo", 1); !errors.Is(err, ErrWorktreeEscape) {
		t.Errorf("relative base: error = %v, want ErrWorktreeEscape", err)
	}

	// A leaf planted as a symlink out of the base is refused after evaluation.
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "repo-issue-13")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := WorktreePath(base, "repo", 13); !errors.Is(err, ErrWorktreeEscape) {
		t.Fatalf("symlinked leaf: error = %v, want ErrWorktreeEscape", err)
	}
	// A symlinked base itself is fine: containment is judged against its target.
	linkBase := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(base, linkBase); err != nil {
		t.Fatal(err)
	}
	if _, err := WorktreePath(linkBase, "repo", 14); err != nil {
		t.Fatalf("symlinked base: %v", err)
	}
}

func TestEvalExisting_ResolvesParentsOfMissingTail(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := EvalExisting(filepath.Join(link, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, _ := filepath.EvalSymlinks(real)
	if want := filepath.Join(wantRoot, "a", "b"); got != want {
		t.Fatalf("EvalExisting = %q, want %q", got, want)
	}
}
