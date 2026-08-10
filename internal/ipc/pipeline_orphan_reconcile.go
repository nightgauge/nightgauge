package ipc

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Orphaned-run reconciliation (#44), keyed on the RUN (ADR-017 7.2–7.4).
//
// The extension/HeadlessOrchestrator path emits its terminal pipeline_done only
// via pipeline.notifyComplete. When the extension host dies mid-run (window
// closed, crash, sleep) that event never fires and the platform's pipeline_runs
// row stays 'running' forever — the "phantom in-flight run" symptom. The
// persisted snapshot carries the run's identity across the crash, so this
// reconciler can close the row from a process that has no registry at all.
//
// Three things make that safe, and each is a defect this replaces:
//
//   - the skip predicate is the FIVE-ARM LIVENESS LADDER below, not "does this
//     issue have a registry entry". An issue-keyed skip is blind to Go-scheduler
//     runs, which are never in activeRuntimes and whose snapshots sit in the same
//     directories — so every workspace.setRoot closed live scheduler runs (F21);
//   - the startup sweep DEFERS. The extension client restarts the Go backend on
//     process exit while the extension host and all its in-flight runs survive,
//     so an inline sweep at Server.Run reconciled live runs into skeleton records
//     (F26). It now re-evaluates from scratch at startupGrace expiry;
//   - emission and removal are SPLIT. Gating the whole pass on a platform client
//     made retention dead code on a local-only workspace while the scheme moved
//     from one file per issue to one file per run (F24).
//
// Discovery is `runtime-{issue}-{runId}.json` (Decision 8) plus its one sibling,
// the pause-restore claim artifact `resuming-{issue}-{runId}.{claimToken}.json`
// (Decision 9), both parsed from the shared identity constant. The claim rows are
// production-INERT until step 8 mints the first artifact — inert, not wrong.

// The ladder's and the retention table's constants. Each is a threshold the ADR
// derives rather than picks, so the derivation lives next to the value.
const (
	// livenessWindow bounds arms 1 and 4 (ADR-017 7.2). 30 minutes: the lease is
	// a coarse backstop for a lost abandonRun, not the primary mechanism — it can
	// only fire for a run that lost its abandon call, crossed a 30-minute stage
	// boundary gap, and has no live process.
	livenessWindow = 30 * time.Minute

	// startupGrace is ladder arm 5 and the claim-release threshold (7.3, 9).
	// Derived, not chosen by feel: the client's five backend-restart attempts sum
	// to 2+4+8+16+32 = 62s of backoff, plus process start, plus the first
	// notifyStageProgress at a >= 5s cadence. 120s clears that ladder with margin.
	startupGrace = 120 * time.Second

	// claimSkewTolerance is one-sided on purpose (C17): tolerance for a claim
	// token minted slightly AHEAD of this reader's clock, never a grace period
	// added to startupGrace. There is exactly one release threshold.
	claimSkewTolerance = 60 * time.Second

	// snapshotAgeCap is 7.4's last row: anything with an identity that nothing
	// has collected in two weeks is debris, INCLUDING a paused snapshot. A pause
	// is never reconciled while it is fresh (C5 — it powers the restore prompt at
	// the next activation, possibly days later), but a pause nobody resumed in two
	// weeks is not a pending decision.
	snapshotAgeCap = 14 * 24 * time.Hour
)

// --- The liveness ladder (ADR-017 7.2) -------------------------------------

// runEvidence is the ladder's evidence, injected so each arm is independently
// fakeable: a test makes exactly one arm true and asserts the candidate
// survives. Production wiring is serverEvidence below — the IPC registry lease,
// the scheduler's own registry, the #341 process probe, and the startup grace.
//
// Arm 4 (the disk-side lease) is not here: it is the candidate's own file mtime,
// which the scan already holds.
type runEvidence struct {
	leaseFresh    func(runID string) bool
	schedulerLive func(runID string) bool
	processAlive  func(pid int) bool
	withinGrace   func() bool
}

// serverEvidence binds the ladder to the real registries.
//
// KNOWN LIMITATION, stated rather than papered over (ADR-017 7.2 per-population
// table): the extension path's Persist is gated on a non-empty repo
// (notifyStageTransition), so a run whose first transitions carry no repo has
// written no snapshot at all — arms 3 and 4 are structurally false for it until
// its first repo-carrying transition. It has no file to reconcile either, so the
// gap costs nothing here; it means the per-population coverage claim holds from
// the first repo-carrying transition, not from dispatch.
func (s *Server) serverEvidence(now time.Time) runEvidence {
	return runEvidence{
		leaseFresh: func(runID string) bool { return s.runLeaseIsFresh(runID, now) },
		schedulerLive: func(runID string) bool {
			// Guarded exactly as every other schedulerRuns call site: a nil
			// *Scheduler in an interface field is a NON-nil interface.
			if s.schedulerRuns == nil {
				return false
			}
			// Called with NO lock held — runtimesMu and the scheduler's
			// activeRuntimesMu are never held at the same time.
			return s.schedulerRuns.IsRunLive(runID)
		},
		processAlive: runstate.ProcessAlive,
		withinGrace:  s.withinStartupGrace,
	}
}

// skipRun is the ladder, evaluated in ADR-017 7.2's order and short-circuiting.
//
// EVERY ARM CAN ONLY PRODUCE A SKIP, NEVER A CLOSE, and the asymmetry is the
// design (C13): a false positive — a recycled pid, a snapshot touched by
// something else — costs one deferred sweep, collected on the next activation
// and unconditionally by the 14-day cap; a false negative costs a live run its
// entire record, its learning row and its telemetry, silently.
//
// `abandoned` is deliberately NOT on this ladder. The first draft's `&&
// !abandoned` made a fresh, actively-streaming abandoned run ineligible for the
// skip and handed its crash snapshot to the remover — abandonment describes a
// dispatch, not a run (7.1).
func skipRun(ev runEvidence, runID string, snap *state.RuntimeState, modTime, now time.Time) bool {
	switch {
	case ev.leaseFresh != nil && ev.leaseFresh(runID):
		return true // 1. this server's registry, lease inside the window
	case ev.schedulerLive != nil && ev.schedulerLive(runID):
		return true // 2. the Go scheduler's registry (Decision 11)
	case snap != nil && ev.processAlive != nil && ev.processAlive(snap.PID):
		return true // 3. the run's own stage child
	case now.Sub(modTime) < livenessWindow:
		return true // 4. the disk-side lease; a future mtime skips too, by design
	case ev.withinGrace != nil && ev.withinGrace():
		return true // 5. the reconnect window (7.3)
	}
	return false
}

// --- The disposition table (ADR-017 7.4) -----------------------------------

// disposition is what the retention table says to do with one file. The table
// is a PURE classifier: no I/O, no server, no analytics — so the rows are
// table-testable, and "does the platform exist" cannot reach the removal rules.
type disposition int

const (
	// dispositionKeep leaves the file untouched.
	dispositionKeep disposition = iota
	// dispositionRemove removes without emitting: the terminal claim already
	// emitted, or abandonRun did.
	dispositionRemove
	// dispositionEmitAndRemove is the ordinary orphan: one terminal
	// pipeline_done(success=false), then removal. The removal happens whether or
	// not the emission does (F24).
	dispositionEmitAndRemove
	// dispositionReleaseClaim releases a stale pause-restore claim (Decision 9).
	// The executor decides rename-back vs remove from whether the canonical name
	// is occupied — it NEVER overwrites an occupied one.
	dispositionReleaseClaim
)

func (d disposition) String() string {
	switch d {
	case dispositionRemove:
		return "remove"
	case dispositionEmitAndRemove:
		return "emit+remove"
	case dispositionReleaseClaim:
		return "release-claim"
	default:
		return "keep"
	}
}

// reconcileCandidate is one discovered file, resolved far enough to classify.
type reconcileCandidate struct {
	name    string
	issue   int
	runID   string
	modTime time.Time

	// snap is the parsed canonical snapshot. Nil for a claim artifact, which is
	// classified by its NAME alone — its body is another host's working state.
	snap *state.RuntimeState

	// claim marks a resuming-{issue}-{runId}.{claimToken}.json artifact.
	// claimAge is decoded from the TOKEN, never from the file's mtime, which
	// rename(2) does not update (C17/F34); claimAgeKnown is false when the token
	// does not parse or names an instant beyond claimSkewTolerance in the future,
	// and both of those fail safe to "live".
	claim         bool
	claimAge      time.Duration
	claimAgeKnown bool
}

// classifyCandidate resolves one file to exactly one row of 7.4's table,
// evaluated TOP TO BOTTOM. Row order is literal: a `terminal: true` snapshot is
// removed even when a registry entry exists for the run, because the claim
// already emitted and the entry is latched.
func classifyCandidate(c reconcileCandidate, ev runEvidence, now time.Time) disposition {
	// The claim artifact is a DISJOINT file family — a name is either
	// `runtime-…` or `resuming-…`, never both — so its two rows are decided
	// here rather than interleaved with the snapshot rows they cannot compete
	// with. Both fail-safe directions live in claimAgeKnown.
	if c.claim {
		if !c.claimAgeKnown || c.claimAge <= startupGrace {
			return dispositionKeep
		}
		return dispositionReleaseClaim
	}
	// Identity-less files never reach here: ParseSnapshotFilename excludes them
	// from discovery and the Go legacy sweep (Migration, step 9) owns them. So
	// does a file whose body disagrees with its name — resolveCandidate refuses
	// it as corruption rather than giving it a row.
	if c.snap == nil {
		return dispositionKeep
	}

	// terminal: true — no emission (the claim already emitted), removed at any
	// age, grace or no grace.
	if c.snap.Terminal {
		return dispositionRemove
	}

	// skipRun true on any arm, including the startup grace.
	if skipRun(ev, c.runID, c.snap, c.modTime, now) {
		return dispositionKeep
	}

	// abandoned: the two rows the F4 correction split apart. Inside the window
	// the dispatch is abandoned but the run may still be streaming, so its crash
	// snapshot stays; outside it, removal WITHOUT emitting, because abandonRun
	// already emitted the dispatch-terminal event.
	//
	// A nil AbandonedAt cannot be produced by markAbandonedLocked and can only
	// arrive by hand-authored JSON. It is unageable, so it is treated as fresh
	// (C13) — and still collected by the age cap below via its file mtime.
	if c.snap.Abandoned {
		if c.snap.AbandonedAt == nil || now.Sub(*c.snap.AbandonedAt) <= livenessWindow {
			return dispositionKeep
		}
		return dispositionRemove
	}

	// paused, fresh: never reconciled — it powers the restore prompt (C1/C5).
	// "Fresh" here is the AGE CAP, not the liveness window: the prompt is read at
	// the next activation, which may be days later.
	if c.snap.Paused && now.Sub(c.modTime) <= snapshotAgeCap {
		return dispositionKeep
	}

	// Everything left is either an ordinary orphan or something past the 14-day
	// cap that was never reconciled — one pipeline_done(success=false), then the
	// file goes. A paused snapshot past the cap arrives here, which is the row's
	// whole point.
	return dispositionEmitAndRemove
}

// --- The scan ---------------------------------------------------------------

// reconcileAction pairs a file with the disposition the table gave it, and with
// the terminal event that must be emitted BEFORE the removal when there is one.
type reconcileAction struct {
	Path        string
	Disposition disposition
	// Event is set only for dispositionEmitAndRemove. It is built by the pure
	// collector so a test can assert what WOULD be emitted without a platform.
	Event platform.PipelineEvent
	// RunID and Issue name the run for the executor's log line — a claim
	// artifact has no loaded snapshot to name it from.
	RunID string
	Issue int
}

// collectReconcileActions scans stateDir and returns one action per file the
// table did not keep. It is the whole reconciler minus its effects: no removal,
// no rename, no emission, so the rules can be tested against a directory
// without a Server.
func collectReconcileActions(stateDir string, ev runEvidence, now time.Time) []reconcileAction {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil
	}

	var actions []reconcileAction
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		c, ok := resolveCandidate(stateDir, entry, now)
		if !ok {
			continue
		}
		d := classifyCandidate(c, ev, now)
		if d == dispositionKeep {
			continue
		}
		act := reconcileAction{
			Path:        filepath.Join(stateDir, c.name),
			Disposition: d,
			RunID:       c.runID,
			Issue:       c.issue,
		}
		if d == dispositionEmitAndRemove {
			event, built := buildOrphanDoneEvent(c.snap, now)
			if !built {
				// Nothing nameable to close. Leaving the file is the safe
				// direction: the age cap will take it on a later pass.
				continue
			}
			act.Event = event
		}
		actions = append(actions, act)
	}
	return actions
}

// resolveCandidate turns a directory entry into a classifiable candidate. ok is
// false for anything that is not one of the two known families, for an
// unreadable file, and for the corruption case below.
func resolveCandidate(stateDir string, entry os.DirEntry, now time.Time) (reconcileCandidate, bool) {
	name := entry.Name()

	if issueStr, runID, token, ok := runstate.ParseResumingArtifactName(name); ok {
		// ParseResumingArtifactName returns the issue as a STRING (its regex
		// captures text); the reconciler only needs it for the canonical name it
		// may rename back to, which is composed from the same digits.
		issue, err := strconv.Atoi(issueStr)
		if err != nil {
			return reconcileCandidate{}, false
		}
		c := reconcileCandidate{name: name, issue: issue, runID: runID, claim: true}
		c.claimAge, c.claimAgeKnown = claimAgeOf(token, now)
		return c, true
	}

	issue, runID, ok := state.ParseSnapshotFilename(name)
	if !ok {
		return reconcileCandidate{}, false
	}
	info, err := entry.Info()
	if err != nil {
		return reconcileCandidate{}, false
	}
	// Loaded by the EXACT path (issue, runId) already names — LoadPersistedState
	// would re-read the whole directory per candidate.
	rt, err := state.LoadSnapshotByIdentity(stateDir, issue, runID)
	if err != nil || rt == nil {
		return reconcileCandidate{}, false
	}
	// The name promised an identity; this checks the CONTENT delivered THE SAME
	// ONE. The predicate is EQUALITY, not `!= ""`: every in-tree writer composes
	// the filename from the fields it marshals, so the case that actually escapes
	// is a body carrying a DIFFERENT valid identity, which builds a perfectly
	// well-formed pipeline_done and reports the WRONG run terminal to the
	// platform. Such a file gets no row in the table and is never touched here —
	// residual debris classes belong to step 9's sweep.
	if rt.RunID != runID {
		log.Printf("orphan-reconcile: %s carries run %q in its body — leaving it untouched (name/body identity mismatch)",
			name, rt.RunID)
		return reconcileCandidate{}, false
	}
	return reconcileCandidate{
		name:    name,
		issue:   issue,
		runID:   runID,
		modTime: info.ModTime(),
		snap:    rt,
	}, true
}

// claimAgeOf decodes a pause-restore claim's age from its TOKEN (ADR-017 9,
// C17). ok is false — meaning "treat this claim as FRESH, never release it" —
// when the token does not decode, or names an instant more than
// claimSkewTolerance ahead of this reader's clock. A future claim is not an old
// one, and both failure modes bias toward not releasing (C13): the cost is a
// paused run needing one more activation to re-prompt; the cost of the other
// direction is two live dispatches under one identity.
//
// NEVER the file's mtime. rename(2) updates the inode's st_ctime and the two
// directories' st_mtime, but never the renamed file's own st_mtime, so an
// mtime-aged claim inherits the age of the PAUSE and is born releasable — the
// release then renames stale paused content back over a live run's canonical
// snapshot and re-advertises paused: true for a running id (F34).
func claimAgeOf(claimToken string, now time.Time) (time.Duration, bool) {
	ms, err := runstate.UUIDv7Millis(claimToken)
	if err != nil {
		log.Printf("orphan-reconcile: claim token %q does not decode — treating the claim as live (ADR-017 C17): %v",
			claimToken, err)
		return 0, false
	}
	claimedAt := time.UnixMilli(ms)
	if claimedAt.After(now.Add(claimSkewTolerance)) {
		log.Printf("orphan-reconcile: claim token %q names %s, ahead of this clock — treating the claim as live (ADR-017 C17)",
			claimToken, claimedAt.UTC().Format(time.RFC3339))
		return 0, false
	}
	return now.Sub(claimedAt), true
}

// buildOrphanDoneEvent builds the terminal pipeline_done for an interrupted run.
func buildOrphanDoneEvent(snap *state.RuntimeState, now time.Time) (platform.PipelineEvent, bool) {
	if snap == nil {
		return platform.PipelineEvent{}, false
	}
	stagesRun := make([]string, 0, len(snap.CompletedStages))
	var totalDuration time.Duration
	for _, sr := range snap.CompletedStages {
		stagesRun = append(stagesRun, string(sr.Stage))
		totalDuration += sr.Duration
	}
	return buildPipelineDoneEvent(snap.RunID, PipelineNotifyCompleteParams{
		Repo:        snap.Repo,
		IssueNumber: snap.IssueNumber,
		Success:     false,
		// Sum of completed-stage durations, NOT wall clock since start — the run
		// has been dead for an unknowable stretch of that wall time (the
		// 42h-elapsed-timer symptom this reconciler fixes).
		TotalDurationMs: int(totalDuration.Milliseconds()),
		StagesRun:       stagesRun,
	}, now)
}

// pipelineStateScanRoots returns every workspace root whose
// .nightgauge/pipeline dir may hold persisted runtime snapshots: the IPC
// server's launch root plus every repo registered with the client resolver.
// Snapshots are persisted into the run's target repo (#215), so the orphan
// scan must cover all of them or crash recovery misses cross-repo runs.
func (s *Server) pipelineStateScanRoots() []string {
	seen := make(map[string]bool)
	var roots []string
	add := func(root string) {
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	// Through the accessor: workspace.setRoot writes this field from a handler
	// goroutine while the deferred sweep reads it from the timer's (J.5).
	add(s.workspaceRootPath())
	for _, p := range s.resolver.RegisteredPaths() {
		add(p)
	}
	return roots
}

// --- The passes -------------------------------------------------------------

// withinStartupGrace is ladder arm 5. It is a SERVER-level predicate, not a
// local of the startup goroutine, because workspace.setRoot reconciles inline
// and must defer exactly like the startup pass while the window is open.
//
// Zero means no grace was ever armed — a Server that never ran Run (every test
// that is not about the sweep, and the socket-only transport) reconciles
// normally rather than being frozen forever.
func (s *Server) withinStartupGrace() bool {
	until := s.startupGraceUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}

// startDeferredReconcile arms the startup grace and schedules the one-shot
// re-evaluation (ADR-017 7.3). Server.Run must NOT reconcile inline: the client
// restarts the Go backend on process exit (5 attempts, 2000ms · 2^(n-1) backoff)
// WHILE the extension host and all its in-flight runs survive, so an inline
// sweep runs with both registries empty against runs that are alive — it emits a
// terminal pipeline_done for a live run, removes its snapshot, and leaves the
// eventual notifyComplete to write a skeleton record with no measurable routing
// signal (F26).
//
// The candidate set is deliberately NOT carried across the window: at expiry the
// pass re-scans FROM SCRATCH, so a run that reconnected has an entry (arm 1), a
// refreshed file (arm 4) or a live stage child (arm 3), and a snapshot created
// during the window is judged on its own evidence rather than being exempt for
// having arrived late.
//
// ctx is Run's, which is the only cancellation channel the server has — there is
// no Close/Stop/Shutdown — so `nightgauge serve`'s SIGTERM handler cancels the
// timer through the same cancel() that ends the stdio loop.
func (s *Server) startDeferredReconcile(ctx context.Context) {
	s.startDeferredReconcileAfter(ctx, startupGrace)
}

// startDeferredReconcileAfter is startDeferredReconcile with the window named.
// The production caller passes the constant; the parameter exists so the timer
// and its cancellation are observable in a test that does not sleep for two
// minutes — an untested timer is the only part of this that cannot be asserted
// through the pass itself.
func (s *Server) startDeferredReconcileAfter(ctx context.Context, grace time.Duration) {
	s.startupGraceUntil.Store(time.Now().Add(grace).UnixNano())
	go func() {
		log.Printf("orphan-reconcile: deferring the startup sweep %s — %d snapshot(s) present at activation (ADR-017 7.3)",
			grace, s.countReconcileCandidates())
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.reconcileOrphanedRuns()
	}()
}

// countReconcileCandidates reports how many canonical snapshots the scan roots
// hold. Observability only — the sweep re-derives its own set at expiry, so this
// number is a log line and never a decision.
func (s *Server) countReconcileCandidates() int {
	n := 0
	for _, root := range s.pipelineStateScanRoots() {
		entries, err := os.ReadDir(filepath.Join(root, ".nightgauge", "pipeline"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if _, _, ok := state.ParseSnapshotFilename(entry.Name()); ok {
				n++
			}
		}
	}
	return n
}

// reconcileOrphanedRuns runs one reconcile pass over every scan root.
//
// Both call sites — the deferred startup timer and workspace.setRoot — run THIS
// function, and reconcileMu serializes them: two passes over one directory would
// race each other's removals and renames, and a setRoot arriving at grace expiry
// is exactly when that happens.
//
// NOTHING HERE IS GATED ON THE PLATFORM. The scan and every removal rule run
// unconditionally; only the emission is skipped when analyticsSvc is nil, so a
// local-only workspace collects its snapshots on the same schedule as a
// connected one and simply tells nobody (F24).
func (s *Server) reconcileOrphanedRuns() {
	s.reconcilePass(time.Now())
}

func (s *Server) reconcilePass(now time.Time) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	// Reaping is a post-grace act for the same reason the sweep is: during the
	// window an entry that has not been re-asserted yet may belong to a run that
	// is about to reconnect.
	if !s.withinStartupGrace() {
		s.reapStaleRunEntries(now)
	}

	ev := s.serverEvidence(now)
	for _, root := range s.pipelineStateScanRoots() {
		stateDir := filepath.Join(root, ".nightgauge", "pipeline")
		for _, act := range collectReconcileActions(stateDir, ev, now) {
			s.applyReconcileAction(stateDir, act)
		}
	}
}

// applyReconcileAction is the effect half: emission, removal, and the claim
// release. Best-effort throughout — emission is fire-and-forget
// (AnalyticsService buffers offline) and a run whose event is lost anyway is
// caught by the platform-side reaper.
func (s *Server) applyReconcileAction(stateDir string, act reconcileAction) {
	switch act.Disposition {
	case dispositionReleaseClaim:
		s.releaseStaleClaim(stateDir, act)
		return
	case dispositionEmitAndRemove:
		if s.analyticsSvc != nil {
			s.analyticsSvc.EmitPipelineEvent(context.Background(), act.Event)
		}
	case dispositionRemove, dispositionKeep:
	}

	if err := os.Remove(act.Path); err != nil {
		log.Printf("orphan-reconcile: %s for run %s but could not remove %s: %v",
			act.Disposition, act.RunID, act.Path, err)
		return
	}
	log.Printf("orphan-reconcile: %s — run %s (issue #%d) from %s",
		act.Disposition, act.RunID, act.Issue, filepath.Base(act.Path))
}

// releaseStaleClaim applies Decision 9's release table to a claim artifact whose
// token is older than startupGrace.
//
// Canonical ABSENT: rename the artifact back. The claim is released, the pause
// survives, the next activation prompts again — which is also what a claimant
// killed between the rename and its first persist leaves behind.
//
// Canonical PRESENT: NEVER rename back. The claimant won, resumed, and its run
// has already re-persisted; the artifact is superseded debris whose content is
// strictly older by construction. Renaming here would overwrite a live run's
// state with the pre-pause snapshot and re-advertise paused: true for a running
// id (F34's consequence 1) — the exact window between the run's first
// post-resume Persist and the claimant's delete.
func (s *Server) releaseStaleClaim(stateDir string, act reconcileAction) {
	canonical := filepath.Join(stateDir, state.SnapshotFilename(act.Issue, act.RunID))
	if _, err := os.Stat(canonical); err == nil {
		if err := os.Remove(act.Path); err != nil {
			log.Printf("orphan-reconcile: stale claim for run %s is superseded but could not be removed: %v", act.RunID, err)
			return
		}
		log.Printf("orphan-reconcile: removed superseded claim artifact for run %s (issue #%d) — the canonical snapshot is present and newer (ADR-017 Decision 9)",
			act.RunID, act.Issue)
		return
	}
	if err := os.Rename(act.Path, canonical); err != nil {
		log.Printf("orphan-reconcile: could not release stale claim for run %s: %v", act.RunID, err)
		return
	}
	log.Printf("orphan-reconcile: released stale claim for run %s (issue #%d) — the pause survives to the next activation (ADR-017 Decision 9)",
		act.RunID, act.Issue)
}

// reapStaleRunEntries evicts registry entries that no evidence supports.
//
// It exists because adoption has no expiry of its own: an adopt-empty entry
// (Decision 4) holds its key forever, so the registry grows without bound in a
// long-lived server and every one of those entries answers arm 1 for a run
// nobody is running. The predicate is the ladder minus its disk arms — the
// entry's own lease, the scheduler, and the recorded pid.
//
// A reaped id is NOT added to closedRuns. The ring means "a terminal claim has
// run"; a reaped entry's run never reached one, so a run that later proves alive
// must be able to re-adopt from its snapshot and book honestly (C13's bias).
//
// Lock discipline: runtimesMu is taken twice with the evidence gathered in
// between, because the scheduler's activeRuntimesMu may never be held at the
// same time as runtimesMu, and ProcessAlive is a syscall.
func (s *Server) reapStaleRunEntries(now time.Time) {
	type reapCandidate struct {
		id    string
		entry *runEntry
		rs    *state.RuntimeState
	}

	var candidates []reapCandidate
	s.runtimesMu.Lock()
	for id, e := range s.activeRuntimes {
		if e == nil || e.terminal {
			continue
		}
		// max(lastSeen, firstSeen): an administrative install leaves lastSeen at
		// the zero time deliberately (it may state a run's state, never that the
		// run is alive), so firstSeen is what keeps a just-installed entry from
		// being reaped on the very next pass.
		seen := e.lastSeen
		if e.firstSeen.After(seen) {
			seen = e.firstSeen
		}
		if now.Sub(seen) <= livenessWindow {
			continue
		}
		candidates = append(candidates, reapCandidate{id: id, entry: e, rs: e.rs})
	}
	s.runtimesMu.Unlock()
	if len(candidates) == 0 {
		return
	}

	doomed := make([]reapCandidate, 0, len(candidates))
	for _, c := range candidates {
		if s.schedulerRuns != nil && s.schedulerRuns.IsRunLive(c.id) {
			continue
		}
		if c.rs != nil && runstate.ProcessAlive(c.rs.StageChildPID()) {
			continue
		}
		doomed = append(doomed, c)
	}

	evicted := make([]string, 0, len(doomed))
	s.runtimesMu.Lock()
	for _, c := range doomed {
		// Compare-and-delete: the entry may have been claimed and replaced in the
		// unlocked window above, and evicting a successor by key would drop a live
		// run out of its own registry.
		if s.activeRuntimes[c.id] == c.entry {
			delete(s.activeRuntimes, c.id)
			evicted = append(evicted, c.id)
		}
	}
	s.runtimesMu.Unlock()

	for _, id := range evicted {
		log.Printf("orphan-reconcile: reaped registry entry for run %s — no lease, no scheduler, no live process for %s (ADR-017 7.2)",
			id, livenessWindow)
	}
}
