package runstate

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
)

// leaseHolderEnv makes the test binary re-exec itself as a lease holder.
const (
	leaseHolderEnv     = "NIGHTGAUGE_TEST_HOLD_SERVE_LEASE"
	leaseHolderHomeEnv = "NIGHTGAUGE_TEST_HOLD_SERVE_LEASE_HOME"
)

// TestMain doubles as the holder subprocess.
//
// The same-process tests prove the mechanism, but the bug #1349 fixes is
// specifically CROSS-process: two independent `nightgauge` invocations, each
// with its own activeRuntimes map, neither able to see the other. A guard that
// only ever refuses a second call inside one process would pass those tests and
// still let the real failure through, so the contract is exercised against a
// real second process here.
func TestMain(m *testing.M) {
	if root := os.Getenv(leaseHolderEnv); root != "" {
		holdLeaseAndBlock(root)
		return
	}
	os.Exit(m.Run())
}

func holdLeaseAndBlock(root string) {
	if home := os.Getenv(leaseHolderHomeEnv); home != "" {
		// The claim directory is machine-global under the home directory; the
		// child must resolve the same isolated one the parent test created.
		_ = os.Setenv("HOME", home)
	}
	lease, err := AcquireServeLease(root)
	if err != nil {
		_, _ = os.Stderr.WriteString("holder: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer lease.Release()
	// Announce that the lease is held, then wait to be killed.
	_, _ = os.Stdout.WriteString("HELD " + strconv.Itoa(os.Getpid()) + "\n")
	_ = os.Stdout.Sync()
	time.Sleep(60 * time.Second)
}

func TestLeaseRefusesASecondProcessAndNamesItsPID(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		leaseHolderEnv+"="+root,
		leaseHolderHomeEnv+"="+home,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	// Reaped explicitly by PID and verified dead: a backgrounded holder that
	// outlives its test is exactly the leak this workspace has been bitten by.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if ProcessAlive(cmd.Process.Pid) {
			t.Errorf("holder pid %d survived the test", cmd.Process.Pid)
		}
	})

	buf := make([]byte, 64)
	n, err := stdout.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "HELD ") {
		t.Fatalf("holder never reported taking the lease (read %q, err %v)", string(buf[:n]), err)
	}
	holderPID := cmd.Process.Pid

	// The sidecar the real daemon would have written beside its lock.
	now := time.Now()
	if werr := WriteServeSidecar(root, ServeSidecar{
		PID: holderPID, StartedAt: now, LastHeartbeatAt: now,
	}); werr != nil {
		t.Fatalf("WriteServeSidecar: %v", werr)
	}

	_, err = AcquireServeLease(root)
	var held *ServeLeaseError
	if !errors.As(err, &held) {
		t.Fatalf("a second PROCESS took the lease (err = %v) — this is the two-scheduler bug itself", err)
	}
	if held.Holder.PID != holderPID {
		t.Errorf("refusal named pid %d, want the live holder %d", held.Holder.PID, holderPID)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(holderPID)) {
		t.Errorf("refusal text %q does not carry the holder's PID, so an operator cannot act on it", err.Error())
	}

	// Once the holder dies the workspace must be serviceable again, with
	// nobody cleaning up by hand.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for {
		lease, aerr := AcquireServeLease(root)
		if aerr == nil {
			lease.Release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lease was still refused 3s after the holder died: %v", aerr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
