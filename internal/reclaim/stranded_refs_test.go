package reclaim

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func strandedRepo(t *testing.T) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "init", "-b", "main")
	gittest.Run(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")
	return dir, strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
}

func TestListStrandedRefs(t *testing.T) {
	dir, sha := strandedRepo(t)
	refs, err := ListStrandedRefs(dir)
	if err != nil || refs == nil || len(refs) != 0 {
		t.Fatalf("empty repo: refs=%v err=%v, want empty slice", refs, err)
	}
	gittest.Run(t, dir, "update-ref", StrandedRefName(42), sha)
	gittest.Run(t, dir, "update-ref", StrandedRefNamespace+"bogus", sha)
	gittest.Run(t, dir, "update-ref", StrandedRefNamespace+"0042", sha)
	refs, err = ListStrandedRefs(dir)
	if err != nil || len(refs) != 3 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
	for _, r := range refs {
		want := 0
		if r.Ref == StrandedRefName(42) {
			want = 42
		}
		if r.Issue != want || r.Commit != sha {
			t.Errorf("%s: issue=%d commit=%s, want issue %d", r.Ref, r.Issue, r.Commit, want)
		}
	}
}

func TestDeleteStrandedRef(t *testing.T) {
	dir, sha := strandedRepo(t)
	ref := StrandedRefName(7)
	if got, err := ResolveStrandedRef(dir, ref); err != nil || got != "" {
		t.Fatalf("absent ref: %q %v", got, err)
	}
	gittest.Run(t, dir, "update-ref", StrandedRefName(77), sha)
	if got, _ := ResolveStrandedRef(dir, ref); got != "" {
		t.Fatalf("resolving %s matched a sibling ref: %q", ref, got)
	}
	gittest.Run(t, dir, "update-ref", ref, sha)
	if err := DeleteStrandedRef(dir, ref, ""); err == nil {
		t.Fatal("delete with no expected SHA must be refused")
	}
	if err := DeleteStrandedRef("", ref, sha); err == nil {
		t.Fatal("delete with no repo root must be refused")
	}

	if err := DeleteStrandedRef(dir, ref, strings.Repeat("0", 39)+"1"); err == nil {
		t.Fatal("delete at a mismatched SHA must be refused")
	}
	if got, _ := ResolveStrandedRef(dir, ref); got != sha {
		t.Fatalf("ref moved after refused delete: %q", got)
	}
	if err := DeleteStrandedRef(dir, "refs/heads/main", sha); err == nil {
		t.Fatal("delete outside the namespace must be refused")
	}
	if err := DeleteStrandedRef(dir, ref, sha); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, err := ResolveStrandedRef(dir, ref); err != nil || got != "" {
		t.Fatalf("after delete: %q %v", got, err)
	}
}
