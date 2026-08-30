package runstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/pidtest"
)

// #388. The serve daemon's PID marker. These tests drive the PRODUCTION writer
// — the same function the daemon calls and the same one doctor's fixtures use —
// because the defect this file closes was a reader and a writer that never
// agreed on a shape at all.

// isolatedHome points the machine-global claim directory (os.UserHomeDir →
// $HOME) at a temp dir and returns it.
//
// Mandatory, not hygiene: since the claim store is per-user and machine-global,
// a test without this would write into the developer's real
// ~/.nightgauge/serve — where it could take over, and on stop DELETE, the claim
// of the serve daemon actually running this editor.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
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

// count returns how many recorded lines contain substr — the difference
// between "it warned" and "it warned once", which is the whole contract for a
// condition that can recur on every tick.
func (c *capturingLog) count(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, l := range c.lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// waitFor polls cond until it holds, failing with why if it never does. Every
// heartbeat assertion here is about a goroutine's next tick, so a poll is the
// honest wait; a fixed sleep would either be slow or flaky.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", why)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestServeSidecarPath_IsMachineGlobalAndKeyedPerWorkspace(t *testing.T) {
	// The location the #388 review forced. doctor enumerates processes for the
	// WHOLE machine but walks sidecars only under the workspace it was invoked
	// from, so a claim living inside the workspace is invisible to every doctor
	// run started anywhere else — and an invisible claim means a healthy daemon
	// reported as an orphan on every run.
	home := isolatedHome(t)
	workspace := "/Users/operator/Repositories/nightgauge"

	path, err := ServeSidecarPath(workspace)
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}

	if want := filepath.Join(home, ".nightgauge", "serve"); filepath.Dir(path) != want {
		t.Errorf("the claim lives in %q, want the machine-global dir %q", filepath.Dir(path), want)
	}
	if strings.HasPrefix(path, workspace) {
		t.Errorf("the claim is inside the workspace it describes (%q) — the location that made it unreadable", path)
	}
	// One file per workspace: two workspaces on one machine must not fight
	// over a single record.
	other, err := ServeSidecarPath("/Users/operator/Repositories/something-else")
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	if other == path {
		t.Errorf("two workspaces resolved to the same claim file: %q", path)
	}
	// …and one workspace is one file however its path was spelled, or a daemon
	// and a doctor run would disagree about which record is current.
	for _, spelling := range []string{workspace + "/", workspace + "/.", filepath.Join(workspace, "sub", "..")} {
		same, err := ServeSidecarPath(spelling)
		if err != nil {
			t.Fatalf("resolve path: %v", err)
		}
		if same != path {
			t.Errorf("%q resolved to %q, want the same claim as %q", spelling, same, path)
		}
	}
}

func TestClaimServeSidecar_WritesTheRecordTheReaderExpects(t *testing.T) {
	isolatedHome(t)
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
	path, err := ServeSidecarPath(root)
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("the sidecar this writer produced is not readable JSON: %v\n%s", err, raw)
	}
	for _, key := range []string{"pid", "started_at", "last_heartbeat_at", "workspace_root"} {
		if _, ok := onDisk[key]; !ok {
			t.Errorf("the record has no %q key — doctor reads this file by field name: %s", key, raw)
		}
	}
	if stamp, _ := onDisk["last_heartbeat_at"].(string); stamp != now.Format(time.RFC3339) {
		t.Errorf("last_heartbeat_at = %q, want RFC3339 %q", stamp, now.Format(time.RFC3339))
	}
	// The file name is a hash, so the record is the only thing that can tell an
	// operator — or doctor's report — which workspace a claim belongs to.
	if got, _ := onDisk["workspace_root"].(string); got != filepath.Clean(root) {
		t.Errorf("workspace_root = %q, want %q", got, filepath.Clean(root))
	}
}

func TestClaimServeSidecar_ADeadPredecessorIsOverwrittenSilently(t *testing.T) {
	// The SIGKILL shape: the previous daemon never unwound its defer. Its
	// marker is exactly the thing this design expects to expire, so saying so
	// on every restart would train operators past the message that matters.
	isolatedHome(t)
	root := t.TempDir()
	dead := pidtest.Reaped(t, ProcessAlive)
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
	isolatedHome(t)
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

func TestRemoveServeSidecar_ReleasesThisClaimAndNeverAnotherDaemonsClaim(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	if _, err := ClaimServeSidecar(root, os.Getpid(), time.Now(), nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := RemoveServeSidecar(root, os.Getpid()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := ReadServeSidecar(root); ok {
		t.Error("the sidecar survived a clean shutdown")
	}
	// A daemon that already removed it (or never wrote one) must not fail here.
	if err := RemoveServeSidecar(root, os.Getpid()); err != nil {
		t.Errorf("removing an absent sidecar reported an error: %v", err)
	}

	// The loser's clean exit. Daemon B took the workspace over while A was
	// still shutting down; A's defer must not delete the record B depends on,
	// or B — a perfectly healthy daemon — reads as an orphan for the next 24h.
	winner := os.Getppid()
	if _, err := ClaimServeSidecar(root, winner, time.Now(), nil); err != nil {
		t.Fatalf("seed the winner: %v", err)
	}

	if err := RemoveServeSidecar(root, os.Getpid()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	sc, ok := ReadServeSidecar(root)
	if !ok || sc.PID != winner {
		t.Errorf("the losing daemon deleted the winner's claim: %+v (ok=%v)", sc, ok)
	}
}

func TestStartServeSidecar_ClaimsOnStartAndReleasesOnStop(t *testing.T) {
	isolatedHome(t)
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

func TestStartServeSidecar_NoWorkspaceRootIsLoudAboutWhatItCosts(t *testing.T) {
	// The guard is right — the claim is keyed by the workspace root, so there
	// is nothing to write without one — but a silent return is the exact class
	// #302 eliminated: the one path that guarantees permanent unclaimed status
	// was also the one path that said nothing about it.
	log := &capturingLog{}

	stop := StartServeSidecar("", log.logf)
	defer stop()

	if log.all() == "" {
		t.Fatal("serve started with no workspace root, wrote no sidecar, and said nothing")
	}
	for _, want := range []string{"WARN", "workspace root", "doctor"} {
		if !strings.Contains(log.all(), want) {
			t.Errorf("the warning does not carry %q: %q", want, log.all())
		}
	}
}

func TestServeHeartbeat_RefreshesWhileTheHostIsStillTheParent(t *testing.T) {
	// The whole reason this file heartbeats: the claim is a progress test, so a
	// record that is never rewritten expires and the daemon reads as an orphan.
	isolatedHome(t)
	root := t.TempDir()
	log := &capturingLog{}
	host := 4242

	stop := startServeSidecar(root, os.Getpid(), func() int { return host }, time.Millisecond, log.logf)
	defer stop()

	claimed, ok := ReadServeSidecar(root)
	if !ok {
		t.Fatal("the claim was never written")
	}
	waitFor(t, "last_heartbeat_at to move past the claim stamp", func() bool {
		sc, ok := ReadServeSidecar(root)
		return ok && sc.LastHeartbeatAt.After(claimed.LastHeartbeatAt)
	})

	sc, _ := ReadServeSidecar(root)
	if !sc.StartedAt.Equal(claimed.StartedAt) {
		t.Errorf("the heartbeat rewrote started_at (%v), which must stay the daemon's real start", sc.StartedAt)
	}
	if log.all() != "" {
		t.Errorf("a healthy heartbeat produced operator noise: %q", log.all())
	}
}

func TestServeHeartbeat_StopsForeverOnceTheDaemonIsReparented(t *testing.T) {
	// The discriminator, and the reason a bare ticker is not one. A ticker
	// refreshes for any process still alive enough to schedule a goroutine —
	// including the daemon this whole issue is about, which blocks on a stdin
	// whose write end outlived the extension host and would otherwise vouch
	// for itself forever. Losing the parent IS the abandoned shape, so the
	// claim must freeze there, say so ONCE, and let doctor report the process
	// when the claim expires.
	isolatedHome(t)
	root := t.TempDir()
	log := &capturingLog{}
	var mu sync.Mutex
	host := 4242
	ppid := func() int {
		mu.Lock()
		defer mu.Unlock()
		return host
	}

	stop := startServeSidecar(root, os.Getpid(), ppid, time.Millisecond, log.logf)
	defer stop()
	claimed, ok := ReadServeSidecar(root)
	if !ok {
		t.Fatal("the claim was never written")
	}
	waitFor(t, "a heartbeat while the host is still attached", func() bool {
		sc, ok := ReadServeSidecar(root)
		return ok && sc.LastHeartbeatAt.After(claimed.LastHeartbeatAt)
	})

	mu.Lock()
	host = 1 // the host died; launchd/init adopted this daemon
	mu.Unlock()

	waitFor(t, "the stand-down warning", func() bool { return log.count("parent process changed") > 0 })
	frozen, _ := ReadServeSidecar(root)
	// Many tick intervals later, the stamp must not have moved a nanosecond.
	time.Sleep(100 * time.Millisecond)
	after, ok := ReadServeSidecar(root)
	if !ok {
		t.Fatal("the claim vanished; it must stay and EXPIRE, so doctor can report the daemon")
	}
	if !after.LastHeartbeatAt.Equal(frozen.LastHeartbeatAt) {
		t.Errorf("the claim kept refreshing after the host was gone (%v → %v) — an abandoned daemon still vouching for itself",
			frozen.LastHeartbeatAt, after.LastHeartbeatAt)
	}
	if n := log.count("parent process changed"); n != 1 {
		t.Errorf("the reparent was reported %d times, want exactly 1: %q", n, log.all())
	}
	for _, want := range []string{"4242", "1", "doctor"} {
		if !strings.Contains(log.all(), want) {
			t.Errorf("the warning does not carry %q: %q", want, log.all())
		}
	}
}

// newTestClaim builds exactly what startServeSidecar builds, so the ownership
// rules can be driven one tick at a time. A ticker cannot express this
// interleaving: the takeover has to land BETWEEN two ticks, and racing a 1ms
// ticker against a write means an in-flight tick can clobber the newer daemon's
// record before the rule under test ever sees it.
func newTestClaim(t *testing.T, root string, log *capturingLog) *serveClaim {
	t.Helper()
	sc, err := ClaimServeSidecar(root, os.Getpid(), time.Now(), log.logf)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return &serveClaim{
		root:      root,
		pid:       os.Getpid(),
		parentPID: 4242,
		ppid:      func() int { return 4242 },
		log:       log.logf,
		done:      make(chan struct{}),
		sc:        sc,
	}
}

func TestServeHeartbeat_NeverTakesTheClaimBackFromALiveDaemon(t *testing.T) {
	// Extension-host restart overlap. Both daemons heartbeat, so without a
	// re-read before each write they alternate ownership every interval and
	// each one reads as an orphan for the half of the time the other holds the
	// file. The older daemon stands down instead — and its eventual exit must
	// leave the winner's record alone.
	isolatedHome(t)
	root := t.TempDir()
	log := &capturingLog{}
	winner := os.Getppid()
	if !ProcessAlive(winner) {
		t.Skipf("pid %d is not alive; no live foreign pid available", winner)
	}
	c := newTestClaim(t, root, log)

	if !c.tick(time.Now()) {
		t.Fatalf("a healthy claim stopped heartbeating: %q", log.all())
	}
	now := time.Now()
	if err := WriteServeSidecar(root, ServeSidecar{PID: winner, StartedAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatalf("the newer daemon could not take the file: %v", err)
	}

	if c.tick(time.Now().Add(time.Minute)) {
		t.Error("the older daemon kept heartbeating over a live successor — the two would alternate ownership every interval")
	}

	if sc, ok := ReadServeSidecar(root); !ok || sc.PID != winner {
		t.Fatalf("the older daemon stole the claim back: %+v (ok=%v)", sc, ok)
	}
	if n := log.count("will not take it back"); n != 1 {
		t.Errorf("standing down was reported %d times, want exactly 1: %q", n, log.all())
	}

	c.stop()

	if sc, ok := ReadServeSidecar(root); !ok || sc.PID != winner {
		t.Errorf("the older daemon's exit deleted the winner's claim: %+v (ok=%v)", sc, ok)
	}
}

func TestServeHeartbeat_AFailedWriteLogsAndKeepsTicking(t *testing.T) {
	// The daemon must never die — nor quietly stop heartbeating — for its own
	// bookkeeping. A loop that returned on the first write error would expire
	// the claim and report a perfectly healthy daemon on the next doctor run.
	home := isolatedHome(t)
	root := t.TempDir()
	// The claim directory is a FILE here, so every MkdirAll under it fails.
	if err := os.MkdirAll(filepath.Join(home, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".nightgauge", "serve"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	log := &capturingLog{}

	stop := startServeSidecar(root, os.Getpid(), func() int { return 4242 }, time.Millisecond, log.logf)
	defer stop()

	waitFor(t, "the heartbeat to survive its first failed write and try again", func() bool {
		return log.count("heartbeat write failed") >= 2
	})
	if !strings.Contains(log.all(), "WARN") {
		t.Errorf("a failed heartbeat write was not surfaced: %q", log.all())
	}
}

func TestServeSidecar_StopIsStrictlyLastSoATickCannotResurrectTheClaim(t *testing.T) {
	// Shutdown racing an in-flight tick. Ordered by the claim's mutex, so a
	// tick either wrote before the removal or never writes at all. Unordered,
	// the tick re-creates the file AFTER the removal — a fresh claim naming a
	// pid that no longer exists, which then vouches for whatever recycles that
	// pid for the next 24 hours. Run under -race for the interleaving; the
	// assertion is the outcome.
	isolatedHome(t)
	for i := 0; i < 50; i++ {
		root := t.TempDir()
		stop := startServeSidecar(root, os.Getpid(), func() int { return 4242 }, 10*time.Microsecond, nil)
		time.Sleep(time.Duration(i%7) * 10 * time.Microsecond)
		stop()

		if sc, ok := ReadServeSidecar(root); ok {
			t.Fatalf("iteration %d: a tick resurrected the claim after shutdown: %+v", i, sc)
		}
	}
}

func TestWriteServeSidecar_IsAtomicUnderConcurrentWritersSoNoReaderSeesAMix(t *testing.T) {
	// doctor reads this file on an unrelated schedule. A truncate-then-write
	// would hand it a half-written record, which parses as nothing, which fails
	// toward reporting a healthy daemon as an orphan — so the write goes
	// through temp+fsync+rename.
	//
	// The temp name has to be unique per write for that to mean anything. Two
	// daemons overlapping on one workspace both write this exact path; with a
	// fixed `<target>.tmp` they open the SAME temp file, interleave their bytes
	// into it, and each rename it into place, so the reader can observe a
	// record that neither of them wrote.
	home := isolatedHome(t)
	root := t.TempDir()
	path, err := ServeSidecarPath(root)
	if err != nil {
		t.Fatalf("resolve path: %v", err)
	}
	pids := []int{4156, 60001, 700002}
	if err := WriteServeSidecar(root, ServeSidecar{PID: pids[0], StartedAt: time.Now(), LastHeartbeatAt: time.Now()}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, pid := range pids {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				now := time.Now()
				if err := WriteServeSidecar(root, ServeSidecar{PID: pid, StartedAt: now, LastHeartbeatAt: now}); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(pid)
	}

	for i := 0; i < 2000; i++ {
		data, err := os.ReadFile(path)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("the claim path vanished mid-write (read %d): %v", i, err)
		}
		var got ServeSidecar
		if err := json.Unmarshal(data, &got); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("a reader caught a partial record (read %d): %v\n%s", i, err, data)
		}
		// Every field must come from ONE writer. A pid that is not one of the
		// three is bytes from two records spliced together.
		if got.PID != pids[0] && got.PID != pids[1] && got.PID != pids[2] {
			close(stop)
			wg.Wait()
			t.Fatalf("a reader caught a spliced record (read %d): pid %d belongs to no writer\n%s", i, got.PID, data)
		}
	}
	close(stop)
	wg.Wait()

	// …and no temp file the renames went through is left behind for doctor's
	// directory scan to trip over.
	residue, err := filepath.Glob(filepath.Join(home, ".nightgauge", "serve", "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(residue) != 0 {
		t.Errorf("the writes left temp residue in the claim directory: %v", residue)
	}
}
