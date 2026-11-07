package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordWorktreeRemoval_AppendsLine(t *testing.T) {
	base := t.TempDir()
	old := removalNow
	removalNow = func() time.Time { return time.Unix(1700000000, 0) }
	t.Cleanup(func() { removalNow = old })

	for _, n := range []string{"repo-issue-1", "repo-issue-2"} {
		if err := RecordWorktreeRemoval(filepath.Join(base, n)); err != nil {
			t.Fatal(err)
		}
	}
	ledger := filepath.Join(base, RemovalLedgerName)
	got, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	want := "1700000000\trepo-issue-1\n1700000000\trepo-issue-2\n"
	if string(got) != want {
		t.Fatalf("ledger = %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(ledger)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("ledger mode = %o, want 600", perm)
		}
	}
}

func TestRecordWorktreeRemoval_RejectsInvalidPath(t *testing.T) {
	for _, p := range []string{"/", ".", filepath.Join(t.TempDir(), RemovalLedgerName)} {
		if err := RecordWorktreeRemoval(p); err == nil {
			t.Errorf("RecordWorktreeRemoval(%q) = nil, want error", p)
		}
	}
}

func TestRecordWorktreeRemoval_ConcurrentLinesStayWhole(t *testing.T) {
	base := t.TempDir()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := RecordWorktreeRemoval(filepath.Join(base, fmt.Sprintf("repo-issue-%d", i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, err := os.ReadFile(filepath.Join(base, RemovalLedgerName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d", len(lines), n)
	}
	names := make([]string, 0, n)
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) != 2 || !strings.HasPrefix(f[1], "repo-issue-") {
			t.Fatalf("malformed line %q", l)
		}
		names = append(names, f[1])
	}
	sort.Strings(names)
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Fatalf("duplicate line for %s", names[i])
		}
	}
}
