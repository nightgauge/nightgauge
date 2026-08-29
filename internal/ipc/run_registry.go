package ipc

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Run-identity keying for the IPC runtime registry — ADR-017 (#370), step 4.
//
// `activeRuntimes` is keyed by the caller-minted RUN IDENTITY, not by the
// issue number. The issue number is a derived index (Decision 6): two
// dispatches of one issue are two runs, two entries, two snapshots and two
// records, and neither can mutate the other's accumulators. Everything in this
// file is the machinery that makes that true — the entry wrapper, the per-id
// adoption singleflight, the verb-class-first resolution rule, the terminal
// latch's registry half, and the derived index scan.

// --- Verb classes (Decision 3) --------------------------------------------
//
// The class is resolved BEFORE any registry is consulted. The first draft
// branched by class only after the scheduler-registry step of Decision 11's
// resolution order — a step that never reached the class check when the
// scheduler happened to hold the id, so a terminal verb could be served from a
// registry that has no runEntry, no latch and no compare-and-delete target
// (F29).

type verbClass int

const (
	// verbRunProgress is a caller describing its OWN run: the transition,
	// phase and progress verbs. Adoption is safe for this class because the
	// identity came from the caller — an adopting zombie re-creates its own
	// run under its own key, where every write it makes lands on its own
	// record.
	verbRunProgress verbClass = iota
	// verbTerminal is the claim: pipeline.notifyComplete, and only it.
	verbTerminal
	// verbAdministrative is a caller asserting something ABOUT a run:
	// pipeline.setPaused today, pipeline.abandonRun from step 6. It may
	// install a run's state (adopting a snapshot that is already on disk) and
	// may NEVER adopt-empty, because a claim that invents its own target can
	// assert something about a run nobody has ever seen.
	verbAdministrative
	// verbLookup is pipeline.getState: not a run message at all.
	verbLookup
)

func (c verbClass) String() string {
	switch c {
	case verbRunProgress:
		return "run-progress"
	case verbTerminal:
		return "terminal"
	case verbAdministrative:
		return "administrative"
	case verbLookup:
		return "lookup"
	}
	return "unknown"
}

// --- Rejection codes (Decision 3) -----------------------------------------
//
// A rejection is a JSON-RPC ERROR carrying a machine-readable code, never a
// success response with a status field: nothing in packages/ reads a
// non-error status field, which is how the earlier design's rejections became
// invisible.

const (
	// codeRunIDRequired is what a version-skewed client produces. It is a
	// COMPATIBILITY signal, and the ProtocolVersion hard-fail exists to make
	// it unreachable.
	codeRunIDRequired = "run_id_required"
	// codeRunIDInvalid is what a malformed or hostile value produces. It is a
	// SECURITY check, not a compatibility one: the socket is unauthenticated
	// (ADR-015) and the value becomes a map key and a filename component.
	codeRunIDInvalid = "run_id_invalid"
	// codeRunClosed is set by exactly one thing — a run's own notifyComplete
	// terminal claim — which is why a live run's own progress can never be
	// rejected.
	codeRunClosed = "run_closed"
	// codeRunNotFound is reachable only on administrative verbs, which carry
	// no run content.
	codeRunNotFound = "run_not_found"
	// codeRunWrongOwner is a refusal to DEGRADE rather than a capability
	// check: the scheduler owns that run's terminal bookkeeping through
	// OnPipelineComplete, and the only correct answer to "close this
	// scheduler run over IPC" is "that is not this socket's run to close".
	codeRunWrongOwner = "run_wrong_owner"
)

// runIdentityError is the rejection shape for every identity refusal. The
// machine-readable code leads the message because the transport collapses a
// handler error into RPCError{Code: ErrRunIdentity, Message: err.Error()} —
// the string is the only place a code can survive that trip, and every reader
// (test, log grep, TS catch) finds it at the front.
type runIdentityError struct {
	Code   string
	Method string
	RunID  string
	Issue  int
	Detail string
}

func (e *runIdentityError) Error() string {
	msg := fmt.Sprintf("%s: %s refused for run %q (issue #%d)", e.Code, e.Method, e.RunID, e.Issue)
	if e.Detail != "" {
		msg += " — " + e.Detail
	}
	return msg
}

// rejectRun builds the rejection and logs it. Every rejection is observed in
// GO, because no design property in ADR-017 may depend on the TypeScript side
// noticing one: five of the call sites are bare, non-logging `catch {}` blocks
// that fabricate local state and show a healthy run while the server refused
// everything.
func (s *Server) rejectRun(method, code, runID string, issue int, detail string) error {
	err := &runIdentityError{Code: code, Method: method, RunID: runID, Issue: issue, Detail: detail}
	s.logRunRejection(method, code, runID, issue, err)
	return err
}

// rejectionLimitWindow bounds the rejection log to one line per key per
// minute. The high-frequency notifyStageProgress path (>= 1 call per 5s per
// run) is why it exists.
const rejectionLimitWindow = time.Minute

// rejectionKeyCap bounds the limiter's own memory. A hostile peer can mint
// unbounded distinct ids, so the table is dropped wholesale rather than
// evicted per key — losing a limiter's memory costs at most one extra log
// line per key, which is the safe direction.
const rejectionKeyCap = 4096

// logRunRejection logs one line per (method, runId) per minute. A call
// rejected for a MISSING or MALFORMED identity has no id to key on, so its
// limiter key falls back to (method, issueNumber) — an id-less flood still has
// a key. (The ADR names a peer component too; this transport has no per-peer
// identity at the handler seam — one stdio pipe or one socket connection per
// extension host — so there is nothing to add.)
func (s *Server) logRunRejection(method, code, runID string, issue int, err error) {
	key := method + "|" + runID
	if runID == "" || !runstate.IsIdentity(runID) {
		key = fmt.Sprintf("%s|issue:%d", method, issue)
	}
	now := time.Now()
	s.rejectLogMu.Lock()
	if s.rejectLogSeen == nil {
		s.rejectLogSeen = make(map[string]time.Time)
	}
	if len(s.rejectLogSeen) > rejectionKeyCap {
		s.rejectLogSeen = make(map[string]time.Time)
	}
	last, seen := s.rejectLogSeen[key]
	if seen && now.Sub(last) < rejectionLimitWindow {
		s.rejectLogMu.Unlock()
		return
	}
	s.rejectLogSeen[key] = now
	s.rejectLogMu.Unlock()
	log.Printf("ipc: REFUSED %s for #%d — %s (ADR-017 Decision 3): %v", method, issue, code, err)
}

// --- The registry entry (Decision 12) --------------------------------------

// runEntry is the registry's BOOKKEEPING for one run; RuntimeState is the
// run's CONTENT. The two are not copies of one fact: this half is admission
// control for NEW resolutions and dies with the process, while the
// RuntimeState half travels with the object and is marshalled into every byte
// the run writes.
//
// EVERY field here is owned by runtimesMu, and runEntry fields are NEVER read
// under rs.mu — the rule that keeps the lock order (runtimesMu → rs.mu, or
// schedulerMu → rs.mu, never both registry locks together) acyclic.
type runEntry struct {
	rs *state.RuntimeState

	// repo and issue mirror the run's index key so the derived issue-index
	// scan (Decision 6) is a pure runtimesMu operation: reading them off the
	// RuntimeState would mean taking rs.mu inside the scan, holding a
	// server-global lock across N run mutexes.
	repo  string
	issue int

	// terminal is the REGISTRY half of the terminal latch (claim step 1c),
	// set in the same runtimesMu hold that stamps the durable half onto the
	// RuntimeState. It gates resolution of new calls; the durable half gates
	// everything a fresh process, the gate CLI and the reconciler can see.
	terminal bool

	// abandoned mirrors RuntimeState.Abandoned for the index ranking. NOTHING
	// SETS IT YET — pipeline.abandonRun is ADR-017 step 6. The field exists
	// because the ranking below reads it, and a ranking written to ignore
	// abandonment would have to be rewritten rather than extended.
	abandoned   bool
	abandonedAt time.Time

	// firstSeen is stamped once at entry creation; lastSeen is the
	// SERVER-OBSERVED lease stamp that every accepted run-progress or
	// terminal call refreshes, and it is what the index ranks on. Ranking on
	// firstSeen was the first draft's rule and it is not self-correcting:
	// firstSeen never moves, so a wedged run that adopts before a live one
	// stays "current" for the rest of the live run (F12 through the index).
	firstSeen time.Time
	lastSeen  time.Time
}

// touchLocked stamps the lease. Caller holds runtimesMu.
//
// An ADMINISTRATIVE resolution installs an entry whose lastSeen is the ZERO
// time and never moves it: an administrative verb may install the run's state,
// and may never make the run look alive. Without that split, abandonRun —
// whose population is by definition runs the operator has given up on — would
// hand every one of them a fresh lease and suppress the reconciliation #44
// exists to do.
func (e *runEntry) touchLocked(class verbClass) {
	if class == verbAdministrative || class == verbLookup {
		return
	}
	e.lastSeen = time.Now()
}

// newRunEntry builds an entry around a runtime. now is passed so firstSeen and
// the first lease stamp agree.
func newRunEntry(rs *state.RuntimeState, repo string, issue int) *runEntry {
	if repo == "" {
		repo = rs.Repo
	}
	if issue == 0 {
		issue = rs.IssueNumber
	}
	return &runEntry{
		rs:        rs,
		repo:      repo,
		issue:     issue,
		terminal:  rs.Terminal,
		abandoned: rs.Abandoned,
		firstSeen: time.Now(),
	}
}

// --- closedRuns: a cache, not the authority (Decision 4) -------------------

// closedRunsCap bounds the ring. 1024 × a 36-byte id is ~40 KB and covers
// weeks of runs at any plausible dispatch rate.
const closedRunsCap = 1024

// closedRunRing is an in-memory FIFO ring of run ids whose terminal claim has
// run. It is NEVER persisted and never loaded. Ids are inserted once and never
// re-touched, so an LRU would degenerate to a FIFO anyway; stating it as a
// FIFO keeps the eviction order decidable by a reader.
//
// THE AUTHORITY IS THE DURABLE `terminal` MARKER stamped into
// runtime-{issue}-{runId}.json before removal. While that file exists, "closed"
// is decidable by any process across any restart, and adoption reading a
// terminal snapshot re-populates this ring — the ring's only refill path. Once
// the file is gone there is no durable authority, and that is deliberate: a
// persistent closed-run journal is a second writer over run state whose
// retention has to be managed.
//
// Eviction therefore cannot produce a wrong record, a doubled outcome or a
// reopened run. It can only downgrade a cheap in-memory refusal into one
// spurious pipeline_done (R-4), because the skeleton record a late duplicate
// builds is dropped by the richer-upgrade-only rule and its learning row by
// the corpus dedup.
//
// The zero value is usable. Guarded by the server's runtimesMu — every method
// here assumes the caller holds it.
type closedRunRing struct {
	ids   map[string]struct{}
	order []string
}

func (r *closedRunRing) addLocked(id string) {
	if id == "" {
		return
	}
	if r.ids == nil {
		r.ids = make(map[string]struct{}, closedRunsCap)
	}
	if _, dup := r.ids[id]; dup {
		return
	}
	r.ids[id] = struct{}{}
	r.order = append(r.order, id)
	for len(r.order) > closedRunsCap {
		delete(r.ids, r.order[0])
		r.order = r.order[1:]
	}
}

func (r *closedRunRing) hasLocked(id string) bool {
	_, ok := r.ids[id]
	return ok
}

// --- Adoption: the per-id singleflight (Decision 4) ------------------------

// adoptFlight is one goroutine's disk read for one identity. Adoption is a
// check → disk read → insert and every request runs in its own goroutine, so
// the post-restart case the ADR names as the ORDINARY one is concurrent by
// default: the extension's notifyStageProgress and its next
// notifyStageTransition arrive for the same unknown id within milliseconds. If
// both miss, both load and both construct a *RuntimeState, one wins the map
// and the loser keeps mutating an orphan whose Persist targets the same
// filename — same-run field loss inside one process (F30).
type adoptFlight struct {
	done           chan struct{}
	rs             *state.RuntimeState
	terminalOnDisk bool
	err            error
}

// loadRunSnapshot is the flight's I/O, performed with NO lock held.
//
// It distinguishes three outcomes, and the third is the one that matters:
//   - nothing on disk (rs == nil, err == nil) — the ordinary miss;
//   - a snapshot carrying the durable terminal marker (terminalOnDisk) — the
//     run is closed and adoption must REFUSE rather than rehydrate, which is
//     what makes closedRuns durable across a restart for every case where the
//     file survives its own removal;
//   - a snapshot that EXISTS but cannot be read (err != nil) — a refusal, not
//     an empty adoption. Adopting empty there would install a runtime that
//     believes the run has no history and would let its next Persist overwrite
//     the unreadable-but-present file with a thinner one. Refusing costs that
//     one call's content and the next message retries the load.
//
// A rehydrated snapshot also settles OWNERSHIP before it is installed (#557).
// A snapshot carries the pid of the process driving the run, and adoption is
// where this process finds out whether that is still someone: a GONE owner's
// run becomes this process's to seal, while a LIVE owner's run keeps naming its
// owner, so this server's terminal claim will decline to SealAndRemove a file
// the other process is still writing. That is the cross-process half of the
// terminal latch — the in-memory `sealed` flag can only stop the process that
// set it, and `run_wrong_owner` (Decision 3) previously stopped only the
// scheduler runs THIS process could see.
func loadRunSnapshot(stateDir, runID string) (rs *state.RuntimeState, terminalOnDisk bool, err error) {
	if stateDir == "" {
		return nil, false, nil
	}
	loaded, loadErr := state.LoadPersistedState(stateDir, runID)
	if loadErr != nil {
		if errors.Is(loadErr, fs.ErrNotExist) || os.IsNotExist(loadErr) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("adopt run %s: snapshot is present but unreadable: %w", runID, loadErr)
	}
	if loaded == nil {
		return nil, false, nil
	}
	if loaded.Terminal {
		return nil, true, nil
	}
	switch owner, moved := loaded.AdoptOwnership(); {
	case moved:
		log.Printf("ipc: adopted run %s whose owner (pid %d) is gone — this process now owns its seal (#557)",
			runID, owner)
	case !loaded.OwnedByThisProcess():
		log.Printf("ipc: adopted run %s but pid %d still owns it — this server serves the run's progress and will NOT seal its snapshot (#557)",
			runID, owner)
	}
	return loaded, false, nil
}

// resolveOrAdopt is Decision 4's pseudocode, verbatim in semantics.
//
// THE CALLER HOLDS NEITHER LOCK, and this takes runtimesMu up to three times —
// which is exactly why the terminal claim calls it as step 0, BEFORE its own
// critical section opens (C16/F36): containing it would deadlock.
//
// The flight covers the I/O, not the DECISION. An empty adoption does no I/O,
// so it needs no flight — a check-and-insert under runtimesMu is already
// exactly-once. That is why the class rule is applied per caller in the settle
// phase rather than baked into the flight: an administrative call and a
// run-progress call may race for one unknown id, and each must get its own
// class's answer from the one shared load.
func (s *Server) resolveOrAdopt(method string, class verbClass, runID, repo string, issue int) (*runEntry, error) {
	s.runtimesMu.Lock()
	if s.activeRuntimes == nil {
		s.activeRuntimes = make(map[string]*runEntry)
	}
	if e := s.activeRuntimes[runID]; e != nil {
		// The terminal re-check is NOT redundant with resolveRun's. Claim step
		// 2 runs unlocked between the latch (step 1c) and the compare-and-delete
		// (step 3), and this fast path is reachable inside that window by a
		// run-progress call that passed resolveRun before the latch was stamped.
		// Serving it there would refresh the lease of a CLOSED run and make it
		// "current" for its repo#issue in the derived index.
		if e.terminal {
			s.runtimesMu.Unlock()
			return nil, s.rejectRun(method, codeRunClosed, runID, issue,
				"the registry entry is terminal-latched")
		}
		detail, refused := corroborateLocked(class, e, repo, issue)
		if refused {
			s.runtimesMu.Unlock()
			return nil, s.rejectRun(method, codeRunNotFound, runID, issue, detail)
		}
		e.touchLocked(class)
		s.runtimesMu.Unlock()
		return e, nil
	}
	if f := s.adopting[runID]; f != nil {
		// Wait; do NOT load. Exactly one goroutine per id performs the disk
		// read and every other caller settles against the same result, so two
		// *RuntimeState objects for one identity cannot exist.
		s.runtimesMu.Unlock()
		<-f.done
		return s.settleAdoption(f, method, class, runID, repo, issue)
	}
	f := &adoptFlight{done: make(chan struct{})}
	if s.adopting == nil {
		s.adopting = make(map[string]*adoptFlight)
	}
	s.adopting[runID] = f
	s.runtimesMu.Unlock()

	// BOTH halves of the flight's teardown are in ONE defer, so a load that
	// panics (the handler's recover would otherwise swallow it) strands
	// neither. Leaving the `adopting` delete on the success path was the
	// asymmetric form: a panic inside the load closed the flight but left the
	// map entry forever, and every later call for that id then waited on an
	// already-closed flight and settled as an ordinary MISS — adopt-empty over
	// a real snapshot, whose next Persist replaces the rich file with a thin
	// one. Taking runtimesMu inside the defer is safe because the insert below
	// has already released it, and a double delete would be harmless anyway.
	func() {
		defer func() {
			s.runtimesMu.Lock()
			delete(s.adopting, runID)
			s.runtimesMu.Unlock()
			close(f.done)
		}()
		f.rs, f.terminalOnDisk, f.err = loadRunSnapshot(s.pipelineStateDir(repo), runID)

		s.runtimesMu.Lock()
		switch {
		case f.terminalOnDisk:
			s.closedRuns.addLocked(runID)
		case f.rs != nil:
			e := newRunEntry(f.rs, repo, issue)
			s.activeRuntimes[runID] = e
			e.touchLocked(class)
		}
		s.runtimesMu.Unlock()
	}()

	return s.settleAdoption(f, method, class, runID, repo, issue)
}

// settleAdoption is the SETTLE label of Decision 4's pseudocode: every caller
// of this id runs it, including the one that performed the load.
func (s *Server) settleAdoption(f *adoptFlight, method string, class verbClass, runID, repo string, issue int) (*runEntry, error) {
	if f.err != nil {
		// Not one of the five codes: this is an I/O failure, not a statement
		// about the run. The bias matches the rest of the ADR — never destroy
		// a record to serve a call.
		log.Printf("ipc: %s could not adopt run %s for #%d: %v", method, runID, issue, f.err)
		return nil, f.err
	}
	if f.terminalOnDisk {
		return nil, s.rejectRun(method, codeRunClosed, runID, issue,
			"its snapshot carries the durable terminal marker")
	}

	// EVERY EXIT BELOW UNLOCKS BEFORE IT LOGS. `defer s.runtimesMu.Unlock()`
	// would put rejectRun's rate-limiter and its log.Printf inside the
	// server-global registry hold, inverting the property the limiter exists
	// for: logging a refusal must never queue behind the registry. Decide under
	// the lock, log after it — the shape resolveRun's closedRuns and terminal
	// arms already use.
	s.runtimesMu.Lock()
	if e := s.activeRuntimes[runID]; e != nil {
		// The loaded entry, or a peer's empty one.
		//
		// Terminal-checked for the same reason the fast path is: a claim's
		// unlocked step-2 window can latch this entry while this call was
		// waiting on the flight, and serving it would refresh a closed run's
		// lease.
		if e.terminal {
			s.runtimesMu.Unlock()
			return nil, s.rejectRun(method, codeRunClosed, runID, issue,
				"the registry entry is terminal-latched")
		}
		detail, refused := corroborateLocked(class, e, repo, issue)
		if refused {
			s.runtimesMu.Unlock()
			return nil, s.rejectRun(method, codeRunNotFound, runID, issue, detail)
		}
		e.touchLocked(class)
		s.runtimesMu.Unlock()
		return e, nil
	}
	if class == verbAdministrative {
		// NEVER ADOPT-EMPTY. An administrative verb is a caller making an
		// assertion ABOUT a run, and a claim that invents its own target can
		// assert something about a run nobody has ever seen — the force-clear
		// arm that fires for a dispatch which wedged inside `git worktree add`
		// and never reached Go at all. Adopting an EXISTING snapshot is not
		// that: the snapshot is the evidence.
		s.runtimesMu.Unlock()
		return nil, s.rejectRun(method, codeRunNotFound, runID, issue,
			"no live entry and no snapshot on disk; an administrative verb never invents a run")
	}
	// No I/O — lock-guarded check-and-insert is already exactly-once.
	e := newRunEntry(state.NewRuntimeState(repo, issue, "", runID), repo, issue)
	s.activeRuntimes[runID] = e
	e.touchLocked(class)
	s.runtimesMu.Unlock()
	return e, nil
}

// corroborateLocked applies Decision 3's administrative rule: an administrative
// verb resolving a live entry must have named the same run the entry describes.
// A mismatched repo or issueNumber yields run_not_found and mutates nothing, so
// no pipeline_done with IssueNumber: 0 can ever be emitted (C8).
//
// Empty caller values are "not asserted" rather than "asserted empty": the
// pause wire carries `repo` with omitempty and the operator surfaces do not
// always name one. Caller holds runtimesMu.
//
// IT DECIDES; IT DOES NOT LOG. The refusal detail is returned so the caller can
// release runtimesMu and only then call rejectRun — the rate limiter and its
// log.Printf must never run inside the server-global registry hold. That is
// also why this is a plain function rather than a *Server method: it has no
// business reaching anything that logs.
func corroborateLocked(class verbClass, e *runEntry, repo string, issue int) (detail string, refused bool) {
	if class != verbAdministrative {
		return "", false
	}
	if issue != 0 && e.issue != 0 && e.issue != issue {
		return fmt.Sprintf("run belongs to issue #%d", e.issue), true
	}
	if repo != "" && e.repo != "" && e.repo != repo {
		return fmt.Sprintf("run belongs to repo %q", e.repo), true
	}
	return "", false
}

// --- The six-line server rule (Decision 3 + Decision 11) -------------------

// runResolution is what a resolved call records onto. Exactly one of the two
// registries answered:
//   - entry != nil — the IPC registry owns the run, and the entry is the
//     terminal claim's compare-and-delete target;
//   - schedulerOwned — the GO SCHEDULER owns it, and a run-progress call is
//     served READ-THROUGH onto the scheduler's own runtime and NEVER adopted.
//     Without that arm, PipelineBridge's calls would carry the scheduler's
//     RunID into adoption and manufacture a second in-memory entry for a run
//     the scheduler registry already owns: it would hold a lease, would never
//     be terminal-claimed (the scheduler path never calls notifyComplete),
//     would leak for the life of the server, and would become the "current"
//     run for its repo#issue in the derived index.
type runResolution struct {
	entry          *runEntry
	rs             *state.RuntimeState
	schedulerOwned bool
}

// runID answers with the identity actually resolved.
func (r runResolution) runID() string {
	if r.rs == nil {
		return ""
	}
	return r.rs.RunID
}

// resolveRun is the server's rule, and the verb class is decided on line 3 —
// before any registry is consulted:
//
//	1  runId == ""                  → run_id_required
//	2  !IsIdentity(runId)           → run_id_invalid   (BEFORE any use)
//	3  class                        → decided by the caller, per verb
//	4  closedRuns.has(runId)        → run_closed       (cache; the durable
//	                                                    marker is the authority)
//	5  IPC registry                 → serve
//	   scheduler registry           → run-progress: serve read-through;
//	                                  terminal/administrative: run_wrong_owner
//	6  unresolved                   → adopt (Decision 4)
//
// runtimesMu and the scheduler's activeRuntimesMu are NEVER held at the same
// time: this releases its own lock before consulting the scheduler at step 5.
func (s *Server) resolveRun(method string, class verbClass, runID, repo string, issue int) (runResolution, error) {
	if runID == "" {
		return runResolution{}, s.rejectRun(method, codeRunIDRequired, runID, issue,
			"every pipeline.* run message carries its run identity")
	}
	if !runstate.IsIdentity(runID) {
		// Checked BEFORE any use. This value becomes a map key and a filename
		// component on a socket ADR-015 documents as unauthenticated, so the
		// validation belongs at the sink and ahead of every consumer.
		return runResolution{}, s.rejectRun(method, codeRunIDInvalid, runID, issue,
			"not a canonical run identity")
	}

	s.runtimesMu.Lock()
	if s.closedRuns.hasLocked(runID) {
		s.runtimesMu.Unlock()
		return runResolution{}, s.rejectRun(method, codeRunClosed, runID, issue,
			"its terminal claim has already run")
	}
	if e := s.activeRuntimes[runID]; e != nil {
		if e.terminal {
			s.runtimesMu.Unlock()
			return runResolution{}, s.rejectRun(method, codeRunClosed, runID, issue,
				"the registry entry is terminal-latched")
		}
		detail, refused := corroborateLocked(class, e, repo, issue)
		if refused {
			s.runtimesMu.Unlock()
			return runResolution{}, s.rejectRun(method, codeRunNotFound, runID, issue, detail)
		}
		e.touchLocked(class)
		s.runtimesMu.Unlock()
		return runResolution{entry: e, rs: e.rs}, nil
	}
	s.runtimesMu.Unlock()

	if s.schedulerRuns != nil {
		if rt := s.schedulerRuns.LookupRunByID(runID); rt != nil {
			// RUN-PROGRESS and ADMINISTRATIVE are both served from the
			// scheduler's own live runtime (#379).
			//
			// Administrative was refused here until the scheduler registry was
			// keyed on identity. The refusal was never about the verb being
			// unsafe — it was that an issue-number-keyed registry could not
			// prove which run a caller meant. It can now, and serving is
			// strictly better than refusing: the alternative was ADOPTING a
			// snapshot of the same run into the IPC registry, which produced a
			// SECOND *RuntimeState for one identity, with the operator's pause
			// landing on the copy the scheduler does not read.
			//
			// The write goes through the scheduler's live object, so rs.mu
			// serialises it against every Persist that run makes — the same
			// single-object argument F33 makes for the adoption path.
			if class == verbRunProgress || class == verbAdministrative {
				return runResolution{rs: rt, schedulerOwned: true}, nil
			}
			// TERMINAL still stops here, and this arm is why the distinction
			// matters. The scheduler books that run's record itself through
			// OnPipelineComplete; serving a terminal verb from a registry with
			// no latch, no lease and no compare-and-delete target would write a
			// SECOND authoritative record under one run id (F29). Logged loudly
			// with the resolved registry named, because on today's tree this
			// should be unreachable and a non-zero count is a real signal.
			// "The scheduler path never calls notifyComplete" is a statement
			// about today's TypeScript, not an invariant of the socket.
			return runResolution{}, s.rejectRun(method, codeRunWrongOwner, runID, issue,
				"the Go scheduler owns this run; its terminal bookkeeping is not this socket's to do")
		}
	}

	e, err := s.resolveOrAdopt(method, class, runID, repo, issue)
	if err != nil {
		return runResolution{}, err
	}
	return runResolution{entry: e, rs: e.rs}, nil
}

// --- The derived issue index (Decision 6) ----------------------------------

// THERE IS NO SECOND MAP. The issue index is a derived scan of activeRuntimes
// under runtimesMu, keyed conceptually by repo#issue. The registry is small by
// construction — entries are evicted at the terminal claim — so a scan is
// cheaper than a second map's synchronisation invariant, and a derived index
// cannot drift from its source.
//
// "Current" = the non-abandoned, non-terminal entry for repo#issue with the
// newest lastSeen — the SERVER-OBSERVED lease stamp that every accepted call
// refreshes, so no caller clock is trusted.
//
// repo is a component of the index KEY, not of the identity: the identity is
// globally unique on its own, which is what actually fixes the cross-repo
// same-issue-number collision (F8). An empty repo means the caller named none
// and matches any — the degenerate case, and still better than the confidently
// wrong answer an issue-only key gave.
func (s *Server) currentRunForIssue(repo string, issue int) (current *runEntry, others []string) {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	return s.currentRunForIssueLocked(repo, issue)
}

func (s *Server) currentRunForIssueLocked(repo string, issue int) (current *runEntry, others []string) {
	var all []*runEntry
	for _, e := range s.activeRuntimes {
		if e == nil || e.issue != issue {
			continue
		}
		if repo != "" && e.repo != "" && e.repo != repo {
			continue
		}
		all = append(all, e)
	}
	// Deterministic order for the "other run ids" list, newest lease first.
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].lastSeen.Equal(all[j].lastSeen) {
			return all[i].lastSeen.After(all[j].lastSeen)
		}
		return all[i].rs.RunID > all[j].rs.RunID
	})
	for _, e := range all {
		if current == nil && !e.terminal && !e.abandoned {
			current = e
			continue
		}
		others = append(others, e.rs.RunID)
	}
	return current, others
}

// runLeaseIsFresh is ARM 1 of the reconciler's liveness ladder (ADR-017 7.2):
// this server's registry holds the run, it is not terminal-latched, and its
// lease was refreshed inside livenessWindow.
//
// It is keyed on the RUN, which is the whole correction. The issue-keyed
// predicate it replaces let any non-terminal entry of an issue pin every
// snapshot of that issue — including an adopt-empty entry whose lastSeen had not
// moved in hours, and including the other concurrent dispatch's file.
//
// lastSeen is the SERVER-OBSERVED stamp that only run-progress and terminal
// verbs refresh (touchLocked): an entry installed by an administrative
// resolution carries the zero time and can never satisfy this arm. abandonRun is
// not evidence that a run is alive; it is the opposite claim.
//
// THE ZERO TIME IS LOAD-BEARING HERE, not merely absent (§7.3, "only run
// traffic counts as re-assertion"). now.Sub(time.Time{}) overflows int64
// nanoseconds and saturates at the maximum duration, so the comparison below
// answers false for an administrative install — that is the mechanism by which
// installing a run's STATE does not rescue its snapshot from the post-grace
// re-evaluation. Anything that special-cases the zero time into "fresh" (e.g.
// "any non-zero lastSeen pins") reintroduces F9 wearing a run id: touchLocked
// stamps lastSeen on every accepted run-progress verb INCLUDING adopt-empty
// adoption, so such a predicate would make organic and adopt-empty entries alike
// unreapable. Pinned by
// TestRunIdentity_AdministrativeInstallDoesNotRescueItsSnapshot.
func (s *Server) runLeaseIsFresh(runID string, now time.Time) bool {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	e := s.activeRuntimes[runID]
	return e != nil && !e.terminal && now.Sub(e.lastSeen) < livenessWindow
}

// PipelineGetStateResult is pipeline.getState's answer once one issue can name
// several runs (ADR-017 Decision 6).
//
// The runtime snapshot is EMBEDDED, so every field the response carried before
// the re-key is still at the same place on the wire — this is a superset, not a
// reshape. What it adds is disambiguation: the identity of the run that
// answered, and, when the issue has more than one, the other run ids, so a
// caller can tell that it is looking at one of several rather than at "the"
// run.
//
// RunID shadows the embedded snapshot's `runId` at depth 0, which encoding/json
// resolves in favour of the shallower field — the same value either way, stated
// explicitly here because it is the contract.
type PipelineGetStateResult struct {
	*state.RuntimeState
	RunID       string   `json:"runId"`
	OtherRunIDs []string `json:"otherRunIds,omitempty"`
}

func newPipelineGetStateResult(snap *state.RuntimeState, others []string) *PipelineGetStateResult {
	if snap == nil {
		return nil
	}
	return &PipelineGetStateResult{RuntimeState: snap, RunID: snap.RunID, OtherRunIDs: others}
}

// --- The terminal claim's registry half (Decision 5) -----------------------

// errStaleClaim reports that the entry resolved at step 0 is no longer the one
// in the registry — it was compare-and-deleted and possibly re-adopted between
// the unlocked resolve and the claim. The caller retries once from step 0; a
// second mismatch is treated as an assertion.
var errStaleClaim = errors.New("terminal claim: registry entry went stale between resolve and claim")

// claimTerminalLocked is claim step 1: the re-check that makes the unlocked
// step 0 safe, then 1b–1d under rs.mu nested inside runtimesMu, then both
// halves of the latch.
//
// It holds BOTH locks rather than latching admission and taking rs.mu
// separately. The cheaper form is safe against a second CLAIM (admission is
// already latched) but not against an ordinary transition, which can take rs.mu
// in the gap and append a stage AFTER the sequence declares the replay to be
// the run's last accepted mutation. Holding both is microseconds of a
// server-global lock for two map writes and one struct copy; the disk read
// Decision 4 refused to put under this lock is three orders of magnitude more
// expensive.
func (s *Server) claimTerminal(entry *runEntry, runID string, executionPaths, puntReasons map[string]string, outcomeFor func(state.PipelineStage) string) (*state.RuntimeState, error) {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()

	cur := s.activeRuntimes[runID]
	if cur != entry {
		return nil, errStaleClaim
	}
	if entry.terminal {
		return nil, errAlreadyClaimed
	}
	// 1b + 1c(durable) + 1d, as one critical section under rs.mu. Lock order
	// runtimesMu → rs.mu, never the reverse.
	snap := entry.rs.ClaimTerminal(executionPaths, puntReasons, outcomeFor)
	// 1c, registry half.
	entry.terminal = true
	return snap, nil
}

// runTerminalClaim is claim steps 0 and 1 together, including 1a's ONE RETRY:
// if the entry resolved at step 0 went stale, the resolution is redone from
// scratch and the claim re-attempted; a second mismatch returns run_closed and
// logs loudly, the same assertion posture step 3's failed compare gets.
func (s *Server) runTerminalClaim(method string, res runResolution, runID, repo string, issue int,
	executionPaths, puntReasons map[string]string, outcomeFor func(state.PipelineStage) string,
) (*runEntry, *state.RuntimeState, error) {
	entry := res.entry
	for attempt := 0; attempt < 2; attempt++ {
		snap, err := s.claimTerminal(entry, runID, executionPaths, puntReasons, outcomeFor)
		switch {
		case err == nil:
			return entry, snap, nil
		case errors.Is(err, errAlreadyClaimed):
			return nil, nil, s.rejectRun(method, codeRunClosed, runID, issue,
				"another terminal claim latched this run first")
		case errors.Is(err, errStaleClaim) && attempt == 0:
			log.Printf("ipc: terminal claim for run %s found a stale resolution — retrying once from step 0 (ADR-017 Decision 5 step 1a)", runID)
			retried, rerr := s.resolveRun(method, verbTerminal, runID, repo, issue)
			if rerr != nil {
				return nil, nil, rerr
			}
			if retried.entry == nil {
				return nil, nil, s.rejectRun(method, codeRunWrongOwner, runID, issue,
					"the run moved to the scheduler registry between resolve and claim")
			}
			entry = retried.entry
		default:
			log.Printf("ipc: terminal claim for run %s failed its re-check TWICE — refusing rather than claiming an entry nobody resolved (ADR-017 Decision 5 step 1a)", runID)
			return nil, nil, s.rejectRun(method, codeRunClosed, runID, issue,
				"the registry entry changed underneath two consecutive claims")
		}
	}
	return nil, nil, s.rejectRun(method, codeRunClosed, runID, issue, "terminal claim exhausted its retry")
}

// errAlreadyClaimed reports that another claim won the latch first.
var errAlreadyClaimed = errors.New("terminal claim: entry is already terminal-latched")

// compareAndDeleteRun is claim step 3: delete activeRuntimes[runID] ONLY if the
// entry stored there is the same pointer that was claimed, and record the id in
// closedRuns.
//
// The unlocked step-2 window can only produce another call for the SAME runId —
// refused by the latch — or calls for other run ids, which are on different
// keys. A failed compare is not expected to be reachable and is treated as an
// assertion: log loudly, KEEP the record and the outcome (they were written
// under a valid claim at the time), and do not delete.
func (s *Server) compareAndDeleteRun(entry *runEntry, runID string) {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	if cur := s.activeRuntimes[runID]; cur == entry {
		delete(s.activeRuntimes, runID)
	} else {
		log.Printf("ipc: terminal claim for run %s found a DIFFERENT registry entry at compare-and-delete — keeping the record and leaving the entry in place (ADR-017 Decision 5 step 3 assertion)",
			runID)
	}
	s.closedRuns.addLocked(runID)
}
