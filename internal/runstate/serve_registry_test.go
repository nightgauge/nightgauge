package runstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/hometest"
	"github.com/nightgauge/nightgauge/internal/pidtest"
)

// #1426. The machine-global serve registry AS a registry: what is in it, what
// removes what, and what a file name is worth on its own.
//
// The measured state that opened the issue was 150 records of which 143 named
// a workspace root that no longer existed, and 191 lock files of which 174 had
// no record at all. Nothing in the tree removed either kind, so the numbers
// only ever went up — and a registry that is 95% dead invites a feature to be
// built on it that is over-broad in a way no fresh machine can reproduce.

// registryDir is the isolated claim directory these tests operate in.
func registryDir(t *testing.T) string {
	t.Helper()
	dir, err := ServeSidecarDir()
	if err != nil {
		t.Fatalf("ServeSidecarDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir claim dir: %v", err)
	}
	return dir
}

// deadWorkspaceRoot returns a path that HAS existed and no longer does — the
// dominant shape in the issue's measurement (a t.TempDir() from a finished
// test run, or a reclaimed worktree).
func deadWorkspaceRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "reclaimed-workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	return root
}

// AC1, records. A daemon start sweeps the registry it is about to write into.
//
// This is the accumulation half of #1426: RemoveServeSidecar fires only on a
// clean shutdown, and only while the record still names the exiting pid, so
// anything killed, crashed or reparented left its record behind permanently.
func TestStartServeSidecar_PrunesRecordsWhoseWorkspaceIsGone(t *testing.T) {
	isolatedHome(t)
	gone := deadWorkspaceRoot(t)
	dead := pidtest.Reaped(t, ProcessAlive)
	if err := WriteServeSidecar(gone, ServeSidecar{
		PID:             dead,
		StartedAt:       time.Now().Add(-48 * time.Hour),
		LastHeartbeatAt: time.Now().Add(-48 * time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	stop := StartServeSidecar(t.TempDir(), nil)
	defer stop()

	if sc, ok := ReadServeSidecar(gone); ok {
		t.Fatalf("the record for a workspace that no longer exists survived a daemon start: %+v", sc)
	}
}

// AC1, locks. Lock files were never unlinked by any path at all, so they
// accumulated independently of the records that explain them.
func TestStartServeSidecar_RemovesLockFilesNobodyHolds(t *testing.T) {
	isolatedHome(t)
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	orphan := deadWorkspaceRoot(t)
	registryDir(t)
	lockPath, err := ServeLeasePath(orphan)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}

	stop := StartServeSidecar(t.TempDir(), nil)
	defer stop()

	if _, err := os.Stat(lockPath); err == nil {
		t.Fatalf("an orphaned lock file nobody holds survived a daemon start: %s", lockPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", lockPath, err)
	}
}

// A lock a live process holds is not litter, whatever its record says. The
// kernel's answer to "is this held?" is the only one that cannot race, and a
// sweep that stole a lock would produce the two-scheduler state the lease
// exists to prevent.
func TestPruneServeRegistry_LeavesAHeldLockAlone(t *testing.T) {
	isolatedHome(t)
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := t.TempDir()
	lease, err := AcquireServeLease(root)
	if err != nil {
		t.Fatalf("AcquireServeLease: %v", err)
	}
	defer lease.Release()
	lockPath, err := ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}

	// No record at all — exactly the "orphaned lock" shape — but held.
	res := PruneServeRegistry(time.Now())

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("the prune removed a lock a live process holds: %v", err)
	}
	if res.Locks != 0 {
		t.Errorf("Locks = %d, want 0", res.Locks)
	}
}

// A daemon that is alive but has stopped ticking is doctor's finding, not the
// prune's: the report needs the record to name the holder an operator has to
// stop, so a sweep that removed it would erase the evidence.
func TestPruneServeRegistry_KeepsAWedgedDaemonsRecord(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	if err := WriteServeSidecar(root, ServeSidecar{
		PID:             os.Getpid(),
		StartedAt:       time.Now().Add(-72 * time.Hour),
		LastHeartbeatAt: time.Now().Add(-72 * time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	PruneServeRegistry(time.Now())

	if _, ok := ReadServeSidecar(root); !ok {
		t.Fatal("the prune removed the record of a live but wedged daemon — doctor reports that shape and needs the record to attribute it")
	}
}

// A dead pid whose workspace still exists is the other prunable shape: the
// workspace-root test cannot catch it, so staleness has to.
func TestPruneServeRegistry_RemovesADeadPidsStaleRecord(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	dead := pidtest.Reaped(t, ProcessAlive)
	if err := WriteServeSidecar(root, ServeSidecar{
		PID:             dead,
		StartedAt:       time.Now().Add(-72 * time.Hour),
		LastHeartbeatAt: time.Now().Add(-72 * time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	res := PruneServeRegistry(time.Now())

	if _, ok := ReadServeSidecar(root); ok {
		t.Fatal("a stale record naming a reaped pid survived the prune")
	}
	if res.Records != 1 {
		t.Errorf("Records = %d, want 1", res.Records)
	}
}

// A daemon killed moments ago is not yet evidence of anything: the heartbeat
// is still fresh, and doctor may still be attributing a live process to that
// pid. Both conditions, not either.
func TestPruneServeRegistry_KeepsAFreshRecordWhosePidJustDied(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	dead := pidtest.Reaped(t, ProcessAlive)
	if err := WriteServeSidecar(root, ServeSidecar{
		PID:             dead,
		StartedAt:       time.Now().Add(-time.Hour),
		LastHeartbeatAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}

	PruneServeRegistry(time.Now())

	if _, ok := ReadServeSidecar(root); !ok {
		t.Fatal("the prune removed a record whose heartbeat is still fresh")
	}
}

// A record nothing can read can never claim a pid or name a workspace, and
// writes here are atomic, so it is not a torn write either.
func TestPruneServeRegistry_RemovesAnUnparsableRecord(t *testing.T) {
	isolatedHome(t)
	dir := registryDir(t)
	path := filepath.Join(dir, ServeSidecarName(t.TempDir()))
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("plant record: %v", err)
	}

	res := PruneServeRegistry(time.Now())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an unparsable record survived the prune (stat err = %v)", err)
	}
	if res.Records != 1 {
		t.Errorf("Records = %d, want 1", res.Records)
	}
}

// A lock whose record is alive is explained, and must survive even though this
// process could take the lock — the daemon may simply not hold the SCHEDULER
// lease (serve attaches IPC without one when it is refused).
func TestPruneServeRegistry_KeepsALockExplainedByALiveRecord(t *testing.T) {
	isolatedHome(t)
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	root := t.TempDir()
	registryDir(t)
	if err := WriteServeSidecar(root, ServeSidecar{
		PID: os.Getpid(), StartedAt: time.Now(), LastHeartbeatAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	lockPath, err := ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}

	PruneServeRegistry(time.Now())

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("the prune removed an unheld lock whose record is live: %v", err)
	}
}

// AC2. An orphaned lock names a root that can be recovered. The truncated
// sha256 the name used to be is one-way, so a `.lock` with no `.json` sibling
// named a workspace nothing on the machine could recover.
func TestServeSidecarNameDecodesBackToTheWorkspaceRoot(t *testing.T) {
	for _, root := range []string{
		"/srv/example workspace/repo",
		"/srv/exämple~repo%1",
		"/srv/a.b-c_d/e",
		"/srv/repo",
	} {
		name := ServeSidecarName(root)
		got, ok := ServeRegistryWorkspaceRoot(name)
		if !ok {
			t.Fatalf("ServeRegistryWorkspaceRoot(%q) reported the name undecodable; the root %q is unrecoverable", name, root)
		}
		if want := filepath.Clean(root); got != want {
			t.Errorf("ServeRegistryWorkspaceRoot(%q) = %q, want %q", name, got, want)
		}
		if strings.Contains(name, string(filepath.Separator)) {
			t.Errorf("name %q is not one path segment", name)
		}
	}
}

// Two spellings of one root must not produce two claims for one workspace —
// the property the normalizing hash input already had, kept by the encoder.
func TestServeSidecarNameIsOneKeyPerWorkspace(t *testing.T) {
	root := t.TempDir()
	if a, b := ServeSidecarName(root), ServeSidecarName(root+string(filepath.Separator)); a != b {
		t.Errorf("%q and %q are different keys for one workspace", a, b)
	}
	if a, b := ServeSidecarName("/srv/repo"), ServeSidecarName("/srv/./repo"); a != b {
		t.Errorf("%q and %q are different keys for one workspace", a, b)
	}
	if a, b := ServeSidecarName("/srv/repo"), ServeSidecarName("/srv/Repo"); a == b {
		t.Errorf("two different workspaces share the key %q", a)
	}
}

// The lock file must decode too — it is the half with no record to fall back
// on, which is the entire point of AC2.
func TestServeLeaseNameDecodesBackToTheWorkspaceRoot(t *testing.T) {
	root := "/srv/example workspace/repo"
	path, err := ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	got, ok := ServeRegistryWorkspaceRoot(filepath.Base(path))
	if !ok {
		t.Fatalf("the lock file name %q does not decode; an orphan would name nothing", filepath.Base(path))
	}
	if got != root {
		t.Errorf("decoded %q, want %q", got, root)
	}
}

// A name this package did not write decodes to nothing rather than to a
// plausible lie. "Unrecoverable" has to be reported as unrecoverable.
func TestServeRegistryWorkspaceRootRejectsANameThatIsNotAPath(t *testing.T) {
	for _, name := range []string{
		"0123456789abcdef.json", // the pre-#1426 hash
		"0123456789abcdef.lock",
		"notes.txt",         // not a registry file at all
		"~srv~repo%zz.json", // a malformed escape
		"~srv~repo%A.json",  // a truncated escape
		"~srv~repo%2e.json", // lowercase hex: two names for one root if accepted
		"srv~repo.json",     // decodes, but not to an absolute path
	} {
		if root, ok := ServeRegistryWorkspaceRoot(name); ok {
			t.Errorf("ServeRegistryWorkspaceRoot(%q) = %q, true; want it reported undecodable", name, root)
		}
	}
}

// A root too long to fit in one path segment gets a bounded key. Reversibility
// is impossible there — the information does not fit — so it is REPORTED as
// undecodable, and the name still has to be usable as a filename.
func TestServeSidecarNameStaysWithinOnePathSegment(t *testing.T) {
	long := "/srv/" + strings.Repeat("deep-workspace-directory/", 40) + "repo"
	name := ServeSidecarName(long)
	if len(name) > 255-20 {
		t.Errorf("name for a %d-byte root is %d bytes; NAME_MAX plus the atomic writer's temp suffix leaves no room", len(long), len(name))
	}
	if root, ok := ServeRegistryWorkspaceRoot(name); ok {
		t.Errorf("ServeRegistryWorkspaceRoot(%q) = %q, true; a hashed key has nothing to recover and must say so", name, root)
	}
	// Still one key per workspace, and still distinct between workspaces.
	if name != ServeSidecarName(long) {
		t.Error("the hashed key is not stable")
	}
	if name == ServeSidecarName(long+"2") {
		t.Error("two different long roots share one key")
	}
}

// The hashed key must still be a WRITABLE name, or a deep workspace could not
// claim a record at all.
func TestServeSidecarWritesAndReadsALongWorkspaceRoot(t *testing.T) {
	isolatedHome(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("deep-workspace-directory/", 12)+"repo")
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Fatalf("mkdir deep workspace: %v", err)
	}
	if err := WriteServeSidecar(long, ServeSidecar{PID: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatalf("WriteServeSidecar for a %d-byte root: %v", len(long), err)
	}
	sc, ok := ReadServeSidecar(long)
	if !ok {
		t.Fatal("the record for a long workspace root could not be read back")
	}
	if sc.WorkspaceRoot != long {
		t.Errorf("workspace_root = %q, want %q — the record is the only thing that can name this one", sc.WorkspaceRoot, long)
	}
}

// AC4: doctor and the prune walk the registry through ONE enumeration, so the
// suffix filter, the temp-file exclusion and the key decode cannot drift.
func TestEachServeRegistryFile_VisitsRecordsAndLocksOnce(t *testing.T) {
	isolatedHome(t)
	dir := registryDir(t)
	root := t.TempDir()
	if err := WriteServeSidecar(root, ServeSidecar{PID: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	lockPath, err := ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	// Litter the enumeration is contracted to ignore: the atomic writer's own
	// in-flight temp file, and a directory.
	if err := os.WriteFile(filepath.Join(dir, "x.json.1234.tmp"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("plant tmp: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatalf("plant dir: %v", err)
	}

	var records, locks int
	EachServeRegistryFile(func(f ServeRegistryFile) {
		if f.Lock {
			locks++
		} else {
			records++
			if len(f.Data) == 0 {
				t.Errorf("record %s was visited with no bytes", f.Name)
			}
		}
		if f.WorkspaceRoot != root {
			t.Errorf("%s decoded to %q, want %q", f.Name, f.WorkspaceRoot, root)
		}
		if f.Path != filepath.Join(dir, f.Name) {
			t.Errorf("Path = %q, want it under %q", f.Path, dir)
		}
	})
	if records != 1 || locks != 1 {
		t.Errorf("visited %d records and %d locks, want 1 and 1", records, locks)
	}
}

// AC3, pinned. The claim store is per-user and machine-global, so a test that
// resolves it without an isolated HOME writes into — and on stop DELETES from
// — the operator's real registry. The issue's measurement found the bulk of
// the 143 dead records were exactly that: suites writing into the real HOME.
//
// This test deliberately does NOT call isolatedHome: what it pins is that the
// isolation holds for a test that never asked for it.
func TestServeRegistryNeverResolvesUnderTheRealHome(t *testing.T) {
	if hometest.Home == "" {
		t.Fatal("this package's TestMain does not isolate HOME; every test in it that resolves the claim directory writes into the operator's real registry (#1426)")
	}
	dir, err := ServeSidecarDir()
	if err != nil {
		t.Fatalf("ServeSidecarDir: %v", err)
	}
	if !strings.HasPrefix(dir, hometest.Home) {
		t.Fatalf("ServeSidecarDir() = %q, which is not under this binary's isolated HOME %q", dir, hometest.Home)
	}
	if real := hometest.RealPath(".nightgauge"); real != "" && strings.HasPrefix(dir, real+string(filepath.Separator)) {
		t.Fatalf("ServeSidecarDir() = %q resolves inside the real home's %q", dir, real)
	}
}

// A record that could not be READ is not a record that says its daemon is
// gone. The walker hands the prune nil bytes for both an unreadable file and
// an empty one, and reading nil as "dead" deletes the claim of a perfectly
// healthy daemon — here, this test process itself, with a heartbeat one
// instant old.
//
// The blast radius is what makes this more than one file. Descriptor
// exhaustion at sweep time makes every os.ReadFile in the pass fail, so a
// single prune would erase the claims of every live daemon on the machine —
// the exact "fail toward deleting the evidence doctor reports on" outcome each
// rule in PruneServeRegistry is written to avoid, and the opposite of the bias
// servePathExists already takes for a failed stat.
func TestPruneServeRegistry_KeepsARecordItCouldNotRead(t *testing.T) {
	isolatedHome(t)
	live := t.TempDir()
	if err := WriteServeSidecar(live, ServeSidecar{
		PID: os.Getpid(), StartedAt: time.Now(), LastHeartbeatAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	path, err := ServeSidecarPath(live)
	if err != nil {
		t.Fatalf("ServeSidecarPath: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this filesystem or user can read a 0o000 file, so there is no unreadable record to plant")
	}

	res := PruneServeRegistry(time.Now())

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("the prune deleted the record of a LIVE daemon (pid %d, heartbeat now) because the file could not be read; res = %+v",
			os.Getpid(), res)
	}
}

// The same read failure, one level out: deleting the record sets
// recordSurvives = false, so the same pass then takes the live daemon's lock
// file as an orphan nothing explains.
func TestPruneServeRegistry_KeepsTheLockOfARecordItCouldNotRead(t *testing.T) {
	isolatedHome(t)
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	live := t.TempDir()
	if err := WriteServeSidecar(live, ServeSidecar{
		PID: os.Getpid(), StartedAt: time.Now(), LastHeartbeatAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	recordPath, err := ServeSidecarPath(live)
	if err != nil {
		t.Fatalf("ServeSidecarPath: %v", err)
	}
	lockPath, err := ServeLeasePath(live)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	if err := os.Chmod(recordPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(recordPath, 0o644) })
	if _, err := os.ReadFile(recordPath); err == nil {
		t.Skip("this filesystem or user can read a 0o000 file, so there is no unreadable record to plant")
	}

	res := PruneServeRegistry(time.Now())

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatalf("the prune unlinked a live daemon's lock file because its record could not be read; res = %+v", res)
	}
}

// holdRegistryGuard takes the registry mutation guard from a descriptor of
// this test's own, standing in for the other process that would hold it.
//
// flock is per open-file-description, so a second fd in this process contends
// with runstate's exactly as another process would — the same property
// TestPruneServeRegistry_LeavesAHeldLockAlone relies on.
func holdRegistryGuard(t *testing.T) {
	t.Helper()
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	path := filepath.Join(registryDir(t), serveRegistryGuardName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open guard: %v", err)
	}
	if err := flock.Exclusive(f, 0); err != nil {
		t.Fatalf("take guard: %v", err)
	}
	t.Cleanup(func() {
		_ = flock.Unlock(f)
		_ = f.Close()
	})
	// So the assertions below do not sit for the production wait.
	prev := serveRegistryGuardWait
	serveRegistryGuardWait = 50 * time.Millisecond
	t.Cleanup(func() { serveRegistryGuardWait = prev })
}

// The sweep half of the mutual exclusion (#1426).
//
// Unlinking a lock file is the first thing in the tree that ever made a lease
// lock's path stop naming the inode an acquirer had already opened. An
// acquirer sits between `open(path)` and `flock(fd)` — invisible to the sweep,
// which sees only a lock file no record explains — and an unlink landing there
// leaves it holding a lease on an unlinked inode while the next acquirer
// creates a fresh file at the same path and locks that. Two holders of one
// workspace's lease, which is the state #1349 exists to prevent.
//
// So the sweep must not run at all while an acquisition is in flight. The
// guard is what an in-flight acquirer holds; with the sweep unguarded this
// orphan is unlinked and the test goes red.
func TestPruneServeRegistry_RemovesNothingWhileALeaseAcquisitionIsInFlight(t *testing.T) {
	isolatedHome(t)
	orphan := deadWorkspaceRoot(t)
	lockPath, err := ServeLeasePath(orphan)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	holdRegistryGuard(t)
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}

	res := PruneServeRegistry(time.Now())

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatalf("the sweep unlinked a lock file while a lease acquisition held the registry guard; res = %+v", res)
	}
	if res.Removed() != 0 {
		t.Errorf("Removed() = %d, want 0 — a sweep that cannot hold the guard must remove nothing", res.Removed())
	}
}

// The acquisition half of the same exclusion. Resolved the other way round,
// the race produces a spurious refusal rather than two holders: the acquirer's
// flock is a deliberate single try, so a lock momentarily held by a sweep of
// somebody else's lock file reads as a live daemon and the refusal is
// permanent — `serve` then runs its whole life with no scheduler attached, and
// `autonomous run` exits.
//
// The fix is that the acquirer does not reach its open at all while the sweep
// holds the guard. What it must never do is mistake the sweep for a holder.
func TestAcquireServeLease_DoesNotRaceASweepAndDoesNotMistakeItForAHolder(t *testing.T) {
	isolatedHome(t)
	root := t.TempDir()
	lockPath, err := ServeLeasePath(root)
	if err != nil {
		t.Fatalf("ServeLeasePath: %v", err)
	}
	holdRegistryGuard(t)

	lease, err := AcquireServeLease(root)
	if err == nil {
		lease.Release()
		t.Fatal("took the lease while a registry sweep held the guard — the open and the flock can then be split by an unlink, leaving this lease on an inode no longer at its path")
	}
	if errors.Is(err, ErrServeLeaseHeld) {
		t.Fatalf("a sweep was reported as a live scheduler holding the lease, which is a permanent refusal for a momentary condition: %v", err)
	}
	if _, statErr := os.Stat(lockPath); statErr == nil {
		t.Errorf("the lock file at %s was created before the guard was taken, so the sweep could have unlinked it mid-acquisition", lockPath)
	}
}

// The guard is state in the registry directory, so the walkers have to skip it
// structurally — and the sweep above all, since a sweep that reclaimed its own
// guard would unlink the file it is holding.
func TestRegistryGuardIsNotItselfARegistryFile(t *testing.T) {
	isolatedHome(t)
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	guardPath := filepath.Join(registryDir(t), serveRegistryGuardName)
	if strings.HasSuffix(serveRegistryGuardName, serveRecordSuffix) || strings.HasSuffix(serveRegistryGuardName, serveLockSuffix) {
		t.Fatalf("the guard is named %q, which the registry walkers treat as a record or a lock", serveRegistryGuardName)
	}

	lease, err := AcquireServeLease(t.TempDir())
	if err != nil {
		t.Fatalf("AcquireServeLease: %v", err)
	}
	defer lease.Release()
	if _, err := os.Stat(guardPath); err != nil {
		t.Fatalf("acquiring a lease did not create the guard, so nothing serialises it against the sweep: %v", err)
	}

	EachServeRegistryFile(func(f ServeRegistryFile) {
		if f.Name == serveRegistryGuardName {
			t.Errorf("EachServeRegistryFile visited the guard %q; the sweep would try to reclaim the lock it is holding", f.Name)
		}
	})
	PruneServeRegistry(time.Now())
	if _, err := os.Stat(guardPath); err != nil {
		t.Fatalf("the sweep removed its own guard: %v", err)
	}
}
