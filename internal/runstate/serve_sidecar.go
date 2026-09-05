package runstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The serve daemon's heartbeat PID sidecar (#388).
//
// `nightgauge serve` is the IPC server the VSCode extension host owns — the one
// long-lived verb that wrote no PID record at all. `doctor`'s orphaned-process
// carrier (#341) therefore had to except it BY ARGV, and an argv exception is
// invisibility rather than ownership: a serve daemon that outlived its
// extension host — the exact "everything looks stopped but something is still
// running" symptom the carrier exists to surface — was excepted right along
// with the healthy ones.
//
// This file is the marker that replaces that exception. Two properties are what
// make it a claim rather than a filename, and each of them is load-bearing:
//
// WHERE IT LIVES — machine-global, one file per workspace, under
// <home>/.nightgauge/serve/. The obvious home, `.nightgauge/serve.json` inside
// the workspace, does not work, because doctor's two halves have different
// reach: it enumerates processes with `ps -axo`, which lists the WHOLE machine,
// but it walks sidecars only under the workspace it was invoked from. A daemon
// serving any OTHER workspace on the box — or the primary workspace, when
// doctor runs from a sibling repo whose upward walk never reaches the workspace
// marker — would then be a live claim doctor cannot see, and would be reported
// as an orphan on every single run past the 1h age floor. That is precisely how
// a check teaches operators to stop reading its output. A machine-wide scanner
// needs a machine-wide claim store, so this directory is read unconditionally.
//
// WHAT KEEPS IT ALIVE — ownership is a PROGRESS test, never a presence test
// (#334, #389). A write-once pid+started_at record would stop making progress
// the moment it was written and read as stale after 24h, converting every
// daemon older than a day into a reported orphan. So the daemon heartbeats. But
// a BARE ticker is a presence test wearing a timestamp: it refreshes for any
// process still alive enough to schedule a goroutine, which is exactly what the
// motivating leak does — `serve` blocks on a stdin whose write end outlives the
// extension host, so an abandoned daemon sits there vouching for itself
// forever. The progress signal is therefore host attachment, not liveness; see
// serveClaim.tick.
//
// A SIGKILL'd daemon leaves the file behind, and that needs no special
// handling: an abandoned claim simply stops making progress and expires under
// the standard doctrine, at which point any surviving process is reported like
// every other unclaimed one.

// serveSidecarDirName is the per-user, machine-global claim directory under
// ~/.nightgauge. One file per workspace — see ServeSidecarName.
const serveSidecarDirName = "serve"

// ServeHeartbeatInterval is how often a live, host-attached daemon rewrites
// LastHeartbeatAt.
//
// Far inside doctor's 24h claim window, so a missed tick — a suspended laptop,
// a slow disk, a write that failed once — cannot expire a healthy daemon; and
// far enough apart that a process whose job is mostly to sit idle is not
// rewriting a file every minute.
const ServeHeartbeatInterval = 15 * time.Minute

// ServeSidecar is the on-disk record of a running serve daemon. snake_case to
// match its peers (orchestrator.CurrentRunSidecar, run-state.json).
type ServeSidecar struct {
	PID             int       `json:"pid"`
	StartedAt       time.Time `json:"started_at"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	// WorkspaceRoot is the workspace this daemon serves. The file name now
	// encodes the same root reversibly (#1426), but this field remains the
	// authority: it survives a record that was copied, moved, or written
	// under a root too long to fit in one path segment. doctor names it when
	// it reports the daemon, and the prune trusts it over the name.
	WorkspaceRoot string `json:"workspace_root"`
}

// ServeSidecarDir is <home>/.nightgauge/serve — the one directory every doctor
// run reads, whatever workspace it was invoked from.
//
// The home directory comes from os.UserHomeDir (i.e. $HOME), the same resolver
// the machine-tier config root uses, so the hermetic-HOME seam the test suites
// already rely on isolates this too.
func ServeSidecarDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("resolve home directory: empty")
	}
	return filepath.Join(home, ".nightgauge", serveSidecarDirName), nil
}

// ServeSidecarName is the file a workspace's claim occupies: the workspace
// root encoded as one path segment, plus `.json`.
//
// It used to be the first 16 hex digits of sha256(root), on the reasoning that
// the name has to be one path segment while a root is an arbitrary absolute
// path, and that nothing ever has to reverse it because the record carries
// workspace_root. The first half still holds. The second holds for the record
// and FAILS for its `.lock` sibling, which carries nothing at all (#1426): an
// orphaned lock named a workspace that nothing on the machine could recover,
// so it could be neither attributed nor explained. The key is reversible now
// — see encodeServeRegistryKey — which also makes a directory listing legible
// to the operator who has to act on it.
func ServeSidecarName(workspaceRoot string) string {
	return encodeServeRegistryKey(normalizeWorkspaceRoot(workspaceRoot)) + serveRecordSuffix
}

// ServeSidecarPath is where this workspace's claim is written and read.
func ServeSidecarPath(workspaceRoot string) (string, error) {
	dir, err := ServeSidecarDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ServeSidecarName(workspaceRoot)), nil
}

// normalizeWorkspaceRoot collapses the spellings of one workspace root to a
// single key input, so a relative path and its absolute form do not claim two
// different files for the same workspace.
func normalizeWorkspaceRoot(workspaceRoot string) string {
	if abs, err := filepath.Abs(workspaceRoot); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(workspaceRoot)
}

// WriteServeSidecar persists sc through the same write-temp → fsync → rename
// contract as every other sidecar. Atomicity is not decoration here: `doctor`
// reads this file on an unrelated schedule, and a reader that catches a
// half-written record sees an unparsable sidecar, which fails toward reporting
// a healthy daemon as an orphan.
//
// The record's WorkspaceRoot is stamped here rather than taken from the caller,
// so the file's contents can never disagree with the workspace its name encodes.
func WriteServeSidecar(workspaceRoot string, sc ServeSidecar) error {
	path, err := ServeSidecarPath(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve serve sidecar path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create serve sidecar dir: %w", err)
	}
	sc.WorkspaceRoot = normalizeWorkspaceRoot(workspaceRoot)
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal serve sidecar: %w", err)
	}
	return AtomicWriteFile(path, append(data, '\n'), 0644)
}

// ReadServeSidecar returns the recorded daemon for a workspace, and whether one
// was readable. A missing or unparsable file is (zero, false) — never an error
// to act on.
func ReadServeSidecar(workspaceRoot string) (ServeSidecar, bool) {
	path, err := ServeSidecarPath(workspaceRoot)
	if err != nil {
		return ServeSidecar{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ServeSidecar{}, false
	}
	var sc ServeSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return ServeSidecar{}, false
	}
	return sc, true
}

// RemoveServeSidecar deletes this workspace's claim, but ONLY while the record
// still names pid.
//
// Ownership runs in both directions. Two daemons can briefly overlap on one
// workspace (an extension-host restart), and the loser's clean exit must not
// delete the record the WINNER is depending on — that would leave a perfectly
// healthy daemon unclaimed and reported as an orphan. A record naming anyone
// else, an unreadable one, or no record at all is left exactly as it is;
// shutdown never fails on bookkeeping.
//
// The read and the unlink are not one operation, so a takeover landing between
// them loses the winner's claim. That window is microseconds inside an already
// rare restart overlap, and it self-heals on the winner's next heartbeat —
// unlike an unconditional remove, which loses it every time.
func RemoveServeSidecar(workspaceRoot string, pid int) error {
	if sc, ok := ReadServeSidecar(workspaceRoot); !ok || sc.PID != pid {
		return nil
	}
	path, err := ServeSidecarPath(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve serve sidecar path: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove serve sidecar: %w", err)
	}
	return nil
}

// ClaimServeSidecar writes the startup record for pid, applying the collision
// rule for a claim that is already there:
//
//   - the recorded PID is dead (or is this process) — the previous daemon was
//     killed without unwinding its defer, or this is a restart. Overwrite
//     silently; a stale marker for a dead process is exactly what this file is
//     designed to expire, and saying so on every start would train operators to
//     ignore the message that matters.
//   - the recorded PID is a DIFFERENT live process — two serve daemons are
//     running against one workspace. Warn naming both PIDs, then take the file
//     (last writer wins). The alternative, refusing to start, would let a
//     wedged daemon lock a workspace out of serving; the alternative of staying
//     silent would leave the older daemon unclaimed and reported as an orphan
//     with nothing explaining why. The loser does not fight back — see
//     serveClaim.tick and RemoveServeSidecar.
func ClaimServeSidecar(workspaceRoot string, pid int, now time.Time, logf func(string, ...any)) (ServeSidecar, error) {
	if prior, ok := ReadServeSidecar(workspaceRoot); ok && prior.PID != pid && ProcessAlive(prior.PID) {
		serveLogf(logf)("WARN: the serve sidecar for %s is held by live pid %d; pid %d is taking it over — two serve daemons appear to be running against this workspace",
			normalizeWorkspaceRoot(workspaceRoot), prior.PID, pid)
	}
	sc := ServeSidecar{PID: pid, StartedAt: now, LastHeartbeatAt: now}
	return sc, WriteServeSidecar(workspaceRoot, sc)
}

// StartServeSidecar claims this workspace's serve record for this process and
// keeps it fresh, returning the stop that ends the heartbeat and releases the
// claim — the clean-shutdown path.
//
// stop is a func rather than a context so that the caller can `defer` it beside
// the log closer, on the first line of the command: every early return (config
// failure, a port already bound) then unwinds it too. The serve command's own
// cancellation context is not constructed until several hundred lines later, by
// which point a sidecar written at startup would already have exits that skip
// it. stop is idempotent.
//
// Never fatal. A daemon that cannot write its own sidecar still serves; it just
// reads as unclaimed in `doctor`, which is a warning, not an outage — and dying
// for a bookkeeping file would be a far worse trade.
func StartServeSidecar(workspaceRoot string, logf func(string, ...any)) (stop func()) {
	return startServeSidecar(workspaceRoot, os.Getpid(), os.Getppid, ServeHeartbeatInterval, logf)
}

// startServeSidecar is StartServeSidecar with its three ambient inputs — this
// process's pid, the parent probe, and the tick interval — passed in, so the
// reparent path can be exercised without building a real process tree and
// killing its middle.
func startServeSidecar(workspaceRoot string, pid int, ppid func() int, every time.Duration, logf func(string, ...any)) (stop func()) {
	log := serveLogf(logf)
	if workspaceRoot == "" {
		// The one path that guarantees permanent unclaimed status must not
		// also be the one path that says nothing about it (#302).
		log("WARN: no workspace root resolved; serve is running without its PID sidecar and will read as unclaimed in `nightgauge doctor`")
		return func() {}
	}
	c := &serveClaim{
		root:      workspaceRoot,
		pid:       pid,
		parentPID: ppid(),
		ppid:      ppid,
		log:       log,
		done:      make(chan struct{}),
	}
	// Sweep the registry this daemon is about to write into (#1426).
	//
	// A daemon start is the right moment for it because it is the one event
	// guaranteed to happen on a machine that uses this directory at all, and
	// because the alternative — `doctor` — is a reporter, and a reporter that
	// deletes what it reports on is a different tool. Best-effort by
	// construction: the sweep returns no error, and a file it could not
	// remove is simply left for the next start.
	//
	// It runs BEFORE the claim, which is safe in both directions. This
	// workspace's own prior record is either live (kept) or dead (removed,
	// and about to be rewritten by ClaimServeSidecar anyway); and its lock
	// file cannot be swept, because by the time serve starts the sidecar the
	// scheduler lease is either held by THIS process or refused to it by a
	// live one — and PruneServeRegistry only unlinks a lock it can take.
	if swept := PruneServeRegistry(time.Now()); swept.Removed() > 0 {
		log("serve: pruned the machine-global claim registry — %d dead record(s), %d unheld lock file(s)",
			swept.Records, swept.Locks)
	}

	sc, err := ClaimServeSidecar(workspaceRoot, pid, time.Now(), log)
	if err != nil {
		log("WARN: could not write serve sidecar: %v — this daemon will read as unclaimed in `nightgauge doctor`", err)
	}
	c.sc = sc
	go c.run(every)
	return c.stop
}

// serveClaim owns one daemon's record for the life of the process.
//
// Every write and the removal go through its mutex, and the stopped flag is
// read under that same lock, which fixes the order: a tick that has not taken
// the lock by the time stop() takes it never writes at all, so removal is
// strictly last. Without that, a tick in flight at shutdown re-creates the file
// AFTER the removal — a fresh claim naming a pid that no longer exists, which
// then vouches for whatever recycles that pid for the next 24 hours.
type serveClaim struct {
	root      string
	pid       int
	parentPID int
	ppid      func() int
	log       func(string, ...any)
	done      chan struct{}

	mu      sync.Mutex
	sc      ServeSidecar
	stopped bool
}

// run rewrites the claim on every tick until it is stopped or retired.
func (c *serveClaim) run(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case now := <-ticker.C:
			if !c.tick(now) {
				return
			}
		}
	}
}

// tick refreshes the claim, reporting whether the heartbeat should continue.
//
// Two conditions retire it permanently, and both are the point of the design:
//
//   - THE HOST IS GONE. A bare ticker proves only that a goroutine still gets
//     scheduled, which the daemon this issue is about does perfectly well:
//     `serve` blocks on a stdin whose write end outlives the extension host, so
//     an abandoned daemon refreshes its own claim forever and doctor never sees
//     it. The extension host always spawns serve as its DIRECT child, and an
//     operator running it from a terminal has the shell as parent; in both
//     cases losing that parent (macOS and Linux reparent the orphan to
//     init/launchd) IS the abandoned shape. So the parent is the progress
//     signal: when it changes, this daemon stops vouching for itself, the claim
//     expires under doctor's staleSidecarClaim window, and the process gets
//     reported — the visibility #388 exists for. The rejected alternative, an
//     IPC self-ping, is self-attestation wearing a protocol (a wedged daemon
//     answers pings) and doctor frequently runs with no host attached at all.
//   - SOMEONE ELSE HOLDS THE FILE. If the record now names a different LIVE
//     pid, a newer daemon took over (an extension-host restart overlapping this
//     one). Taking it back would set the two of them alternating ownership
//     every interval, each reading as an orphan for the half of the time the
//     other holds the file. Log once and stand down.
//
// A failed write is neither: a transient disk error must not end the heartbeat
// (that would expire the claim and report a healthy daemon), and it must
// certainly not end the daemon.
func (c *serveClaim) tick(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return false
	}
	if parent := c.ppid(); parent != c.parentPID {
		c.log("WARN: serve's parent process changed (%d → %d): this daemon has outlived the host that started it, so it stops refreshing its PID sidecar and `nightgauge doctor` will report it once the claim expires",
			c.parentPID, parent)
		return false
	}
	if prior, ok := ReadServeSidecar(c.root); ok && prior.PID != c.pid && ProcessAlive(prior.PID) {
		c.log("WARN: the serve sidecar for %s now names live pid %d; pid %d will not take it back and stops heartbeating",
			normalizeWorkspaceRoot(c.root), prior.PID, c.pid)
		return false
	}
	c.sc.LastHeartbeatAt = now
	if err := WriteServeSidecar(c.root, c.sc); err != nil {
		c.log("WARN: serve sidecar heartbeat write failed: %v", err)
	}
	return true
}

// stop ends the heartbeat and releases the claim, in that order, under the same
// lock every write takes. Idempotent: the defer may run after an explicit stop.
func (c *serveClaim) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.stopped = true
	close(c.done)
	if err := RemoveServeSidecar(c.root, c.pid); err != nil {
		c.log("WARN: could not remove serve sidecar on shutdown: %v", err)
	}
}

// serveLogf makes a nil logger safe so no call site has to nil-check one.
func serveLogf(logf func(string, ...any)) func(string, ...any) {
	if logf == nil {
		return func(string, ...any) {}
	}
	return logf
}
