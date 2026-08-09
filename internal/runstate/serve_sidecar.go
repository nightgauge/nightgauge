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
// This file is the marker that replaces that exception, and it is claimed by
// the same doctrine as every other carrier (#334, #389): ownership is a
// PROGRESS test, never a presence test. A write-once pid+started_at record
// would stop making progress the moment it was written and read as stale after
// 24h, converting every daemon older than a day into a reported orphan — the
// opposite failure. So the daemon HEARTBEATS: LastHeartbeatAt is rewritten
// every ServeHeartbeatInterval, far inside doctor's 24h claim window.
//
// A SIGKILL'd daemon leaves the file behind, and that needs no special
// handling: an abandoned sidecar simply stops making progress and expires under
// the standard doctrine, at which point any surviving process is reported like
// every other unclaimed one.

// ServeSidecarFile is the sidecar's name under `.nightgauge/`. It sits at the
// resolved WORKSPACE root rather than a repo root because `serve` is
// workspace-scoped: one daemon serves every repo in the workspace, and the
// workspace root is a directory doctor's sidecar scan always visits (the repo
// root list, by itself, does not include it).
const ServeSidecarFile = "serve.json"

// ServeHeartbeatInterval is how often a live daemon rewrites LastHeartbeatAt.
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
}

// ServeSidecarPath is the sidecar's path for a workspace root.
func ServeSidecarPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", ServeSidecarFile)
}

// WriteServeSidecar persists sc through the same write-temp → fsync → rename
// contract as every other sidecar. Atomicity is not decoration here: `doctor`
// reads this file on an unrelated schedule, and a reader that catches a
// half-written record sees an unparsable sidecar, which fails toward reporting
// a healthy daemon as an orphan.
func WriteServeSidecar(workspaceRoot string, sc ServeSidecar) error {
	path := ServeSidecarPath(workspaceRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create serve sidecar dir: %w", err)
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal serve sidecar: %w", err)
	}
	return AtomicWriteFile(path, append(data, '\n'), 0644)
}

// ReadServeSidecar returns the recorded daemon, and whether one was readable.
// A missing or unparsable file is (zero, false) — never an error to act on.
func ReadServeSidecar(workspaceRoot string) (ServeSidecar, bool) {
	data, err := os.ReadFile(ServeSidecarPath(workspaceRoot))
	if err != nil {
		return ServeSidecar{}, false
	}
	var sc ServeSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return ServeSidecar{}, false
	}
	return sc, true
}

// RemoveServeSidecar deletes the sidecar. A file that is already gone is
// success: clean shutdown must not fail on bookkeeping.
func RemoveServeSidecar(workspaceRoot string) error {
	if err := os.Remove(ServeSidecarPath(workspaceRoot)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove serve sidecar: %w", err)
	}
	return nil
}

// ClaimServeSidecar writes the startup record for pid, applying the collision
// rule for a sidecar that is already there:
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
//     with nothing explaining why.
func ClaimServeSidecar(workspaceRoot string, pid int, now time.Time, logf func(string, ...any)) (ServeSidecar, error) {
	if prior, ok := ReadServeSidecar(workspaceRoot); ok && prior.PID != pid && ProcessAlive(prior.PID) {
		serveLogf(logf)("WARN: serve sidecar %s is held by live pid %d; pid %d is taking it over — two serve daemons appear to be running against this workspace",
			ServeSidecarPath(workspaceRoot), prior.PID, pid)
	}
	sc := ServeSidecar{PID: pid, StartedAt: now, LastHeartbeatAt: now}
	return sc, WriteServeSidecar(workspaceRoot, sc)
}

// StartServeSidecar claims the workspace's serve sidecar for this process and
// keeps it fresh, returning the stop that ends the heartbeat and removes the
// file — the clean-shutdown path.
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
	log := serveLogf(logf)
	if workspaceRoot == "" {
		return func() {}
	}
	sc, err := ClaimServeSidecar(workspaceRoot, os.Getpid(), time.Now(), log)
	if err != nil {
		log("WARN: could not write serve sidecar: %v — this daemon will read as unclaimed in `nightgauge doctor`", err)
	}
	done := make(chan struct{})
	go heartbeatServeSidecar(done, workspaceRoot, sc, ServeHeartbeatInterval, log)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			if err := RemoveServeSidecar(workspaceRoot); err != nil {
				log("WARN: could not remove serve sidecar on shutdown: %v", err)
			}
		})
	}
}

// heartbeatServeSidecar rewrites sc's LastHeartbeatAt on every tick until done
// closes. A failed write is logged and the loop continues: a transient disk
// error must not silently end the heartbeat (which would expire the claim and
// report a healthy daemon), and it must certainly not end the daemon.
func heartbeatServeSidecar(done <-chan struct{}, workspaceRoot string, sc ServeSidecar, every time.Duration, logf func(string, ...any)) {
	log := serveLogf(logf)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			sc.LastHeartbeatAt = now
			if err := WriteServeSidecar(workspaceRoot, sc); err != nil {
				log("WARN: serve sidecar heartbeat write failed: %v", err)
			}
		}
	}
}

// serveLogf makes a nil logger safe so no call site has to nil-check one.
func serveLogf(logf func(string, ...any)) func(string, ...any) {
	if logf == nil {
		return func(string, ...any) {}
	}
	return logf
}
