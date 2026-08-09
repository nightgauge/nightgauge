package runstate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// #388. The serve daemon's PID marker. These tests drive the PRODUCTION writer
// — the same function the daemon calls and the same one doctor's fixtures use —
// because the defect this file closes was a reader and a writer that never
// agreed on a shape at all.

// deadPID returns a PID that has certainly exited: a real child, reaped.
// Invented "large number" PIDs are not reliably dead — the kernel recycles.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run throwaway child: %v", err)
	}
	pid := cmd.Process.Pid
	if ProcessAlive(pid) {
		t.Skipf("pid %d was recycled before the test could use it as a dead pid", pid)
	}
	return pid
}

// capturingLog records what the writer told the operator.
type capturingLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturingLog) logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (c *capturingLog) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func TestClaimServeSidecar_WritesTheRecordTheReaderExpects(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 8, 20, 0, 0, 0, time.UTC)

	sc, err := ClaimServeSidecar(root, 4156, now, nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	if sc.PID != 4156 || !sc.StartedAt.Equal(now) || !sc.LastHeartbeatAt.Equal(now) {
		t.Fatalf("claimed record is wrong: %+v", sc)
	}
	// The on-disk keys are the contract doctor reads by name — snake_case, and
	// RFC3339 stamps, exactly like current-run.json.
	raw, err := os.ReadFile(ServeSidecarPath(root))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("the sidecar this writer produced is not readable JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"pid", "started_at", "last_heartbeat_at"} {
		if _, ok := onDisk[key]; !ok {
			t.Errorf("the record has no %q key — doctor reads this file by field name: %s", key, raw)
		}
	}
	if stamp, _ := onDisk["last_heartbeat_at"].(string); stamp != now.Format(time.RFC3339) {
		t.Errorf("last_heartbeat_at = %q, want RFC3339 %q", stamp, now.Format(time.RFC3339))
	}
}

func TestClaimServeSidecar_ADeadPredecessorIsOverwrittenSilently(t *testing.T) {
	// The SIGKILL shape: the previous daemon never unwound its defer. Its
	// marker is exactly the thing this design expects to expire, so saying so
	// on every restart would train operators past the message that matters.
	root := t.TempDir()
	dead := deadPID(t)
	if _, err := ClaimServeSidecar(root, dead, time.Now().Add(-time.Hour), nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	log := &capturingLog{}

	if _, err := ClaimServeSidecar(root, os.Getpid(), time.Now(), log.logf); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if log.all() != "" {
		t.Errorf("a dead predecessor produced operator noise: %q", log.all())
	}
	sc, ok := ReadServeSidecar(root)
	if !ok || sc.PID != os.Getpid() {
		t.Errorf("the dead predecessor's claim survived: %+v (ok=%v)", sc, ok)
	}
}

func TestClaimServeSidecar_ALivePredecessorWarnsNamingBothPIDs(t *testing.T) {
	// Two serve daemons against one workspace is a real operator state and the
	// operator cannot see it any other way — the second daemon takes the file
	// (last writer wins) but must not do it quietly.
	root := t.TempDir()
	other := os.Getppid() // a live process that is not this one
	if !ProcessAlive(other) {
		t.Skipf("pid %d is not alive; no live foreign pid available", other)
	}
	if _, err := ClaimServeSidecar(root, other, time.Now(), nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	log := &capturingLog{}

	if _, err := ClaimServeSidecar(root, os.Getpid(), time.Now(), log.logf); err != nil {
		t.Fatalf("claim: %v", err)
	}

	warning := log.all()
	if warning == "" {
		t.Fatal("a second daemon took a live daemon's sidecar without a word")
	}
	for _, want := range []string{"WARN", strconv.Itoa(other), strconv.Itoa(os.Getpid())} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning does not name %q: %q", want, warning)
		}
	}
	if sc, _ := ReadServeSidecar(root); sc.PID != os.Getpid() {
		t.Errorf("last-writer-wins did not hold: %+v", sc)
	}
}

func TestServeSidecar_RemoveIsTheCleanShutdownPathAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if _, err := ClaimServeSidecar(root, os.Getpid(), time.Now(), nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := RemoveServeSidecar(root); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := ReadServeSidecar(root); ok {
		t.Error("the sidecar survived a clean shutdown")
	}
	// A daemon that already removed it (or never wrote one) must not fail here.
	if err := RemoveServeSidecar(root); err != nil {
		t.Errorf("removing an absent sidecar reported an error: %v", err)
	}
}

func TestStartServeSidecar_ClaimsOnStartAndRemovesOnStop(t *testing.T) {
	root := t.TempDir()

	stop := StartServeSidecar(root, nil)

	sc, ok := ReadServeSidecar(root)
	if !ok {
		t.Fatal("serve started without writing its sidecar — doctor would report this daemon")
	}
	if sc.PID != os.Getpid() {
		t.Errorf("the sidecar names pid %d, want this process (%d)", sc.PID, os.Getpid())
	}
	stop()
	if _, ok := ReadServeSidecar(root); ok {
		t.Error("stop() left the sidecar behind")
	}
	stop() // idempotent: the defer may run after an explicit stop
}

func TestHeartbeatServeSidecar_RefreshesTheProgressStamp(t *testing.T) {
	// The whole reason this file heartbeats: the claim is a progress test, so a
	// record that is never rewritten expires and the daemon reads as an orphan.
	root := t.TempDir()
	start := time.Now().Add(-2 * time.Hour)
	sc := ServeSidecar{PID: os.Getpid(), StartedAt: start, LastHeartbeatAt: start}
	if err := WriteServeSidecar(root, sc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	done := make(chan struct{})
	go heartbeatServeSidecar(done, root, sc, time.Millisecond, nil)
	defer close(done)

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, ok := ReadServeSidecar(root)
		if ok && got.LastHeartbeatAt.After(start) {
			if !got.StartedAt.Equal(start) {
				t.Errorf("the heartbeat rewrote started_at (%v), which must stay the daemon's real start", got.StartedAt)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("last_heartbeat_at never moved past the seeded stamp: %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHeartbeatServeSidecar_AFailedWriteLogsAndKeepsTicking(t *testing.T) {
	// The daemon must never die — nor quietly stop heartbeating — for its own
	// bookkeeping. A loop that returned on the first write error would expire
	// the claim and report a perfectly healthy daemon on the next doctor run.
	root := t.TempDir()
	// .nightgauge is a FILE here, so every MkdirAll under it fails.
	if err := os.WriteFile(filepath.Join(root, ".nightgauge"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	log := &capturingLog{}
	done := make(chan struct{})
	go heartbeatServeSidecar(done, root, ServeSidecar{PID: os.Getpid()}, time.Millisecond, log.logf)
	defer close(done)

	deadline := time.Now().Add(2 * time.Second)
	for {
		log.mu.Lock()
		n := len(log.lines)
		log.mu.Unlock()
		if n >= 2 {
			break // it survived the first failure and tried again
		}
		if time.Now().After(deadline) {
			t.Fatalf("the heartbeat stopped after %d failed write(s): %q", n, log.all())
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(log.all(), "WARN") {
		t.Errorf("a failed heartbeat write was not surfaced: %q", log.all())
	}
}

func TestWriteServeSidecar_IsAtomicSoAReaderNeverSeesAPartialRecord(t *testing.T) {
	// doctor reads this file on an unrelated schedule. A truncate-then-write
	// would hand it a half-written record, which parses as nothing, which fails
	// toward reporting a healthy daemon as an orphan. The temp+rename contract
	// (AtomicWriteFile) is what makes the read path all-or-nothing — a reader
	// holds either the old record or the new one, never a prefix of either.
	root := t.TempDir()
	sc := ServeSidecar{PID: 4156, StartedAt: time.Now(), LastHeartbeatAt: time.Now()}
	if err := WriteServeSidecar(root, sc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			sc.LastHeartbeatAt = time.Now()
			sc.PID = 4156 + i%7
			if err := WriteServeSidecar(root, sc); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
	}()

	for i := 0; i < 3000; i++ {
		data, err := os.ReadFile(ServeSidecarPath(root))
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("the sidecar path vanished mid-write (read %d): %v", i, err)
		}
		var got ServeSidecar
		if err := json.Unmarshal(data, &got); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("a reader caught a partial record (read %d): %v\n%s", i, err, data)
		}
	}
	close(stop)
	wg.Wait()

	// …and the temp file the rename went through is never left behind for the
	// reader to trip over.
	residue, err := filepath.Glob(ServeSidecarPath(root) + ".tmp")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(residue) != 0 {
		t.Errorf("the write left temp residue beside the sidecar: %v", residue)
	}
}
