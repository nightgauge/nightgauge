package attention

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// The store-holder subprocess contract. The test binary re-execs itself under
// these variables and becomes a second nightgauge process writing the same
// attention directory.
const (
	holderRootEnv = "NIGHTGAUGE_TEST_ATTENTION_HOLDER_ROOT"
	holderIDEnv   = "NIGHTGAUGE_TEST_ATTENTION_HOLDER_ID"
	holderLogEnv  = "NIGHTGAUGE_TEST_ATTENTION_HOLDER_LOG"
	holderHoldEnv = "NIGHTGAUGE_TEST_ATTENTION_HOLDER_HOLD"
)

// TestMain doubles as the second-process store writer.
//
// An in-process test of this contract passes vacuously: two goroutines
// contend on the same `dirLocks` mutex, so they are serialised no matter what
// the on-disk write does. The defect #1425 names is specifically
// CROSS-process — the daemon's `attention.resolve` against an operator's
// `nightgauge attention raise`, two `attention.New(root)` values in two
// address spaces, neither able to see the other's mutex — so the contract is
// exercised against a real second process here, following
// internal/runstate/serve_lease_crossprocess_test.go.
func TestMain(m *testing.M) {
	if root := os.Getenv(holderRootEnv); root != "" {
		holdStoreAndBlock(root)
		return
	}
	os.Exit(m.Run())
}

// holdStoreAndBlock resolves one card with a verb that deliberately dawdles.
// Resolve runs the verb INSIDE the store's critical section (ADR-015 §D), so
// for the length of that verb this process is mid-write against the store
// directory — exactly the window a second process must not write into.
func holdStoreAndBlock(root string) {
	hold, err := time.ParseDuration(os.Getenv(holderHoldEnv))
	if err != nil {
		_, _ = os.Stderr.WriteString("holder: bad hold duration: " + err.Error() + "\n")
		os.Exit(1)
	}
	s := New(root)
	verb := &announcingExecutor{log: os.Getenv(holderLogEnv), hold: hold}
	if _, err := s.Resolve(context.Background(), os.Getenv(holderIDEnv), "go", "holder", "", "", verb); err != nil {
		_, _ = os.Stderr.WriteString("holder: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

// announcingExecutor marks the store critical section's boundaries in a shared
// ordering log and announces on stdout that the section is entered, so the
// parent can time its own mutation to land inside it.
type announcingExecutor struct {
	log  string
	hold time.Duration
}

func (e *announcingExecutor) ExecuteVerb(context.Context, *DecisionRequest, Option) error {
	appendMarker(e.log, "holder-enter")
	_, _ = os.Stdout.WriteString("HELD\n")
	_ = os.Stdout.Sync()
	time.Sleep(e.hold)
	appendMarker(e.log, "holder-exit")
	return nil
}

// appendMarker records one ordering observation. O_APPEND makes the write
// atomic per call across processes on POSIX, so the log records a true
// ordering rather than an interleaving of its own.
func appendMarker(path, marker string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(marker + "\n")
}

func readMarkers(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open ordering log: %v", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// TestStoreSerialisesMutationsAcrossProcesses is the #1425 regression.
//
// Before the fix the store's only cross-process claim was "atomic temp+rename
// plus the terminal-state CAS". Neither serialises anything: the CAS runs
// before the write and guards the lifecycle transition, not the bytes, and the
// rename is atomic per rename while every writer of a given card opened the
// SAME `<id>.json.tmp`. So a second process walked straight into another's
// half-finished write.
func TestStoreSerialisesMutationsAcrossProcesses(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := t.TempDir()
	s := New(root)

	held := mustID(t)
	other := mustID(t)
	if _, _, err := s.Raise(validRequest(held, "cond:held")); err != nil {
		t.Fatalf("Raise held card: %v", err)
	}
	if _, _, err := s.Raise(validRequest(other, "cond:other")); err != nil {
		t.Fatalf("Raise other card: %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "ordering.log")
	const hold = 2 * time.Second

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		holderRootEnv+"="+root,
		holderIDEnv+"="+held,
		holderLogEnv+"="+logPath,
		holderHoldEnv+"="+hold.String(),
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	// Reaped by PID and verified dead: a backgrounded holder that outlives its
	// test is a leak this workspace has been bitten by.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if runstate.ProcessAlive(cmd.Process.Pid) {
			t.Errorf("holder pid %d survived the test", cmd.Process.Pid)
		}
	})

	buf := make([]byte, 32)
	n, rerr := stdout.Read(buf)
	if rerr != nil || !strings.HasPrefix(string(buf[:n]), "HELD") {
		t.Fatalf("holder never reported entering the store's critical section (read %q, err %v)", string(buf[:n]), rerr)
	}

	// The holder is now provably mid-write against this store directory. A
	// mutation from THIS process must queue behind it.
	if _, err := s.Acknowledge(other, "operator"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	appendMarker(logPath, "second-process-wrote")

	if err := cmd.Wait(); err != nil {
		t.Fatalf("holder exited with %v", err)
	}

	got := readMarkers(t, logPath)
	want := []string{"holder-enter", "holder-exit", "second-process-wrote"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("store mutations interleaved across processes: order %v, want %v — "+
			"a second process wrote the store directory while another was mid-write (#1425)", got, want)
	}
}

// TestMaterializedTempPathIsUniquePerWriter pins the second half of the fix.
//
// The advisory lock is deliberately fail-open (an unsupported platform, an
// unwritable store dir, a wedged holder that outlasts the bounded wait), and
// on every one of those paths a temp filename shared by all writers is the
// live corruption vector again. A per-writer name removes it unconditionally.
func TestMaterializedTempPathIsUniquePerWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dr_0192abcd-0000-7000-8000-000000000001.json")

	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		tmp, err := materializedTempPath(path)
		if err != nil {
			t.Fatalf("materializedTempPath: %v", err)
		}
		if seen[tmp] {
			t.Fatalf("two writers would open the same temp path %q — the fixed-name interleave (#1425) is back", tmp)
		}
		seen[tmp] = true
		if got := filepath.Dir(tmp); got != dir {
			t.Fatalf("temp path %q lives in %q, not beside its target in %q — the rename would cross a filesystem and stop being atomic", tmp, got, dir)
		}
		if filepath.Ext(tmp) == ".json" {
			t.Fatalf("temp path %q ends in .json — scanLocked parses every *.json entry, so an in-flight write would surface as a phantom card", tmp)
		}
	}
}

// TestWritesLeaveNoTempResidue guards the other end of the unique name: a
// per-writer temp file that is never renamed or removed accumulates one
// carcass per write in a directory the sweep walks on every tick.
func TestWritesLeaveNoTempResidue(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	id := mustID(t)
	if _, _, err := s.Raise(validRequest(id, "cond:residue")); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Acknowledge(id, "operator"); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if _, err := s.IncrementStreak("cond:residue"); err != nil {
		t.Fatalf("IncrementStreak: %v", err)
	}
	for _, d := range []string{s.Dir(), filepath.Join(s.Dir(), streakSubdir)} {
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatalf("read %s: %v", d, err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("write left temp residue %s in %s", e.Name(), d)
			}
		}
	}
}
