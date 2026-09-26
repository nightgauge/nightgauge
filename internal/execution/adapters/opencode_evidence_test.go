package adapters

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// TestPreserveOpenCodeRunEvidence is #2171 item 3: a failed run keeps its
// session database and logs, owner-only, and nothing else from the root.
func TestPreserveOpenCodeRunEvidence(t *testing.T) {
	home := t.TempDir()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenCodeRunRoot(home, id)
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("data/opencode/opencode.db", "db")
	write("data/opencode/opencode.db-wal", "wal")
	write("data/opencode/log/2026-09-26.log", "log")
	write("data/opencode/auth.json", "secret")
	write("home/.netrc", "secret")
	write("config/opencode/opencode.json", "{}")
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "data/opencode/log/link.log")); err != nil {
		t.Fatal(err)
	}

	dst, err := PreserveOpenCodeRunEvidence(home, id, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(OpenCodeEvidenceDir(home), id); dst != want {
		t.Fatalf("dst = %q, want %q", dst, want)
	}
	var got []string
	_ = filepath.WalkDir(dst, func(p string, d os.DirEntry, _ error) error {
		fi, _ := os.Lstat(p)
		if d.IsDir() {
			if fi.Mode().Perm() != 0o700 {
				t.Errorf("%s mode %v, want 0700", p, fi.Mode().Perm())
			}
			return nil
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, fi.Mode().Perm())
		}
		rel, _ := filepath.Rel(dst, p)
		got = append(got, rel)
		return nil
	})
	want := []string{"log/2026-09-26.log", "opencode.db", "opencode.db-wal"}
	if len(got) != len(want) {
		t.Fatalf("preserved %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("preserved %v, want %v", got, want)
		}
	}
	for _, d := range []string{OpenCodeEvidenceDir(home), filepath.Dir(OpenCodeEvidenceDir(home))} {
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", d, fi.Mode().Perm())
		}
	}

	// The root can now go; the evidence stays.
	if err := RemoveOpenCodeRunRoot(home, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "opencode.db")); err != nil {
		t.Fatalf("evidence gone with the root: %v", err)
	}

	// Swept by age like the run roots.
	old := time.Now().Add(-OpenCodeOrphanMaxAge - time.Hour)
	if err := os.Chtimes(dst, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := SweepOpenCodeRunEvidence(home, OpenCodeOrphanMaxAge, time.Now())
	if err != nil || len(removed) != 1 || removed[0] != id {
		t.Fatalf("sweep removed %v, %v; want [%s]", removed, err, id)
	}
}

func TestPreserveOpenCodeRunEvidence_NothingToKeep(t *testing.T) {
	home := t.TempDir()
	id, _ := runstate.NewRunID()
	dst, err := PreserveOpenCodeRunEvidence(home, id, time.Now())
	if err != nil || dst != "" {
		t.Fatalf("missing root: dst=%q err=%v, want none", dst, err)
	}
	if _, err := os.Stat(OpenCodeEvidenceDir(home)); !os.IsNotExist(err) {
		t.Fatalf("evidence dir created for nothing: %v", err)
	}
}
