package github

import (
	"os"
	"path/filepath"
	"testing"
)

// resetLedgerResolution clears every latch openAPILedger consults, so each
// case resolves the path from scratch the way a fresh process would.
func resetLedgerResolution(t *testing.T) {
	t.Helper()
	ledgerWorkspaceRoot.Store("")
	ledgerConfigOff.Store(false)
	t.Cleanup(func() { ledgerWorkspaceRoot.Store("") })
}

// The daemon bug in one assertion: the process cwd is NOT a workspace, the
// explicitly-named root is, and the ledger must follow the root.
func TestSetAPILedgerWorkspaceRoot_WinsOverCwd(t *testing.T) {
	resetLedgerResolution(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Chdir(t.TempDir()) // a directory that is not a workspace
	t.Setenv(apiLedgerEnv, "")

	SetAPILedgerWorkspaceRoot(root)
	l := openAPILedger()
	if l == nil {
		t.Fatal("openAPILedger returned nil — the named workspace has a .nightgauge/")
	}
	defer l.close()
	want := filepath.Join(root, ".nightgauge", "logs", "github-api.jsonl")
	if l.path != want {
		t.Errorf("ledger path = %q, want %q", l.path, want)
	}
}

// Without an explicit root the cwd still decides, which is what every
// non-daemon invocation relies on.
func TestSetAPILedgerWorkspaceRoot_UnsetFallsBackToCwd(t *testing.T) {
	resetLedgerResolution(t)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Chdir(cwd)
	t.Setenv(apiLedgerEnv, "")

	l := openAPILedger()
	if l == nil {
		t.Fatal("openAPILedger returned nil in a real workspace")
	}
	defer l.close()
	if want := filepath.Join(cwd, ".nightgauge", "logs", "github-api.jsonl"); l.path != want {
		t.Errorf("ledger path = %q, want %q", l.path, want)
	}
}

// An empty root is not an instruction — it must not erase a root already set,
// and must not be joined into a path rooted at "".
func TestSetAPILedgerWorkspaceRoot_EmptyIsIgnored(t *testing.T) {
	resetLedgerResolution(t)
	SetAPILedgerWorkspaceRoot("/tmp/some-root")
	SetAPILedgerWorkspaceRoot("   ")
	base, err := ledgerRelativeBase()
	if err != nil {
		t.Fatalf("ledgerRelativeBase: %v", err)
	}
	if base != "/tmp/some-root" {
		t.Errorf("base = %q, want the previously set root", base)
	}
}

// The env var names a file outright, so it outranks the workspace root for
// exactly the same reason it outranks config: NIGHTGAUGE_* is top precedence.
func TestSetAPILedgerWorkspaceRoot_EnvPathWins(t *testing.T) {
	resetLedgerResolution(t)
	explicit := filepath.Join(t.TempDir(), "explicit.jsonl")
	t.Setenv(apiLedgerEnv, explicit)
	SetAPILedgerWorkspaceRoot(t.TempDir())

	l := openAPILedger()
	if l == nil {
		t.Fatal("openAPILedger returned nil for an explicitly named file")
	}
	defer l.close()
	if l.path != explicit {
		t.Errorf("ledger path = %q, want %q", l.path, explicit)
	}
}

// The env opt-out still outranks everything, root or no root.
func TestSetAPILedgerWorkspaceRoot_EnvOffStillWins(t *testing.T) {
	resetLedgerResolution(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Setenv(apiLedgerEnv, "0")
	SetAPILedgerWorkspaceRoot(root)

	if l := openAPILedger(); l != nil {
		l.close()
		t.Fatal("ledger opened despite NIGHTGAUGE_GITHUB_API_LOG=0")
	}
}

// A root with no .nightgauge/ is not a workspace, and the always-on ledger
// must not conjure one there either.
func TestSetAPILedgerWorkspaceRoot_GreenfieldRootOpensNothing(t *testing.T) {
	resetLedgerResolution(t)
	t.Setenv(apiLedgerEnv, "")
	SetAPILedgerWorkspaceRoot(t.TempDir())

	if l := openAPILedger(); l != nil {
		l.close()
		t.Fatal("ledger opened in a directory with no .nightgauge/")
	}
}
