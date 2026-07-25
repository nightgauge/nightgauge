package attention

// Standing-condition semantics (issue #92).
//
// Every producer wired into the orchestrator's run loop fires on a TRANSITION
// inside a run: the condition is observed once, a request is raised, the run
// moves on. Repo-scoped conditions are different in kind — a red `main` is
// still red on the next sweep, and the one after that. Applying event
// semantics to a standing condition produces either a duplicate per sweep or a
// card that outlives the problem it described, and both end the same way:
// operators stop reading the inbox.
//
// The rules, implemented once here so producer three cannot re-derive them
// badly:
//
//   - STICKY IDENTITY. A condition maps to a stable (producer,
//     idempotency_key) derived from what the condition IS — the check name,
//     the PR number — never from when it was observed. Re-observation updates
//     the existing request in place; it never creates a second one.
//
//   - AUTO-RESOLUTION. When a sweep evaluates a producer successfully and no
//     longer observes one of its conditions, that request transitions to
//     StateAutoResolved — a terminal state distinguishable in the audit trail
//     from a human's StateResolved.
//
//   - UPDATE WITHOUT RE-ALERTING. Content (durations, check details, bodies)
//     changes on every sweep. A refresh whose Fingerprint is unchanged emits
//     ActionRefreshed, which ShouldNotify reports as non-alerting. Only a
//     changed fingerprint emits ActionUpdated.
//
//   - MUTE UNTIL CHANGED. A mute is pinned to the fingerprint it was applied
//     at, not to a timer. It survives every re-observation of the same
//     condition and is dropped the moment the condition itself changes.
//
//   - EXPIRY. A standing request's expiry is refreshed on every observation,
//     so it cannot go stale while the condition holds; StandingExpiry is the
//     declared bounded safety net for a producer that stops being evaluated
//     entirely.
//
// Reconcile, not append: ReconcileStanding is a diff against the open
// requests — raise what is newly true, leave untouched what is still true,
// resolve what is no longer true.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StandingExpiry is the declared default expiry for a repo-scoped standing
// request. It is deliberately long — the same "effectively none, but bounded"
// rule the fleet-deadlock producers already follow (ADR 015 §C) — because a
// standing request must not expire out from under a condition that is still
// true. Every observation pushes expires_at forward by this window, so expiry
// only ever fires for a producer that stopped being evaluated at all (its repo
// left the workspace, its registration was removed), which is exactly the
// stale-card case expiry exists to catch.
const StandingExpiry = 30 * 24 * time.Hour

// ReasonConditionCleared is the machine-stable auto-resolution reason: the
// producer was evaluated successfully and did not observe the condition.
const ReasonConditionCleared = "condition_cleared"

// Additional journal actions for standing conditions.
const (
	// ActionRefreshed — the request's content was updated but its material
	// condition did not change. Never alerts.
	ActionRefreshed = "refreshed"
	// ActionAutoResolved — a sweep retracted the card because the condition
	// cleared.
	ActionAutoResolved = "auto_resolved"
	// ActionMuted / ActionUnmuted — alerting suppression toggled by a human.
	ActionMuted   = "muted"
	ActionUnmuted = "unmuted"
)

// ShouldNotify reports whether this transition is a genuine state change an
// operator should be ALERTED about, as opposed to a bookkeeping transition a
// surface should merely re-render.
//
// This is the single place the "one condition, one notification" rule lives:
// a standing condition observed ten times produces one ActionCreated and nine
// ActionRefreshed, so exactly one of the ten transitions notifies. Surfaces
// still receive every transition as an event — ShouldNotify governs alerting,
// not rendering.
func (e JournalEntry) ShouldNotify() bool {
	if e.Muted {
		return false
	}
	switch e.Action {
	case ActionCreated, ActionUpdated:
		return true
	default:
		return false
	}
}

// StandingSweep is one reconciliation input: everything a single sweep of one
// repo observed, plus which producers actually got to look.
type StandingSweep struct {
	// Repo is the owner/name scope. Required — reconciliation never touches a
	// request belonging to another repo.
	Repo string

	// Producers lists the producers that were evaluated SUCCESSFULLY this
	// sweep. Only their unobserved requests are auto-resolved. A producer that
	// errored, was rate-limited, or was skipped must not appear here: "I could
	// not look" is not evidence that a condition cleared, and retracting a real
	// card on a transient API failure is the exact failure mode that makes an
	// inbox untrustworthy.
	Producers []string

	// Observed is one request per condition currently true. Each must carry a
	// stable IdempotencyKey and a Fingerprint.
	Observed []DecisionRequest
}

// StandingOutcome records what reconciliation did with one condition.
type StandingOutcome struct {
	Key      string `json:"idempotency_key"`
	ID       string `json:"id"`
	Producer string `json:"producer"`
	// Action is one of ActionCreated, ActionUpdated, ActionRefreshed,
	// ActionAutoResolved, or OutcomeSuppressed.
	Action string `json:"action"`
}

// OutcomeSuppressed marks a condition that was NOT raised because a human
// already resolved this exact condition and it has not changed since.
const OutcomeSuppressed = "suppressed"

// StandingResult summarises a reconciliation.
type StandingResult struct {
	Repo         string            `json:"repo"`
	Created      int               `json:"created"`
	Updated      int               `json:"updated"`
	Refreshed    int               `json:"refreshed"`
	Suppressed   int               `json:"suppressed"`
	AutoResolved int               `json:"auto_resolved"`
	Outcomes     []StandingOutcome `json:"outcomes,omitempty"`
}

// Changed reports whether reconciliation mutated anything. A sweep over an
// unchanged repo is expected to report false for created/updated/auto-resolved
// — refreshes are content-only.
func (r StandingResult) Changed() bool {
	return r.Created > 0 || r.Updated > 0 || r.AutoResolved > 0
}

// ReconcileStanding diffs a sweep's observations against the store's open
// standing requests for the same repo. It is the single writer for standing
// conditions and the only producer of StateAutoResolved.
//
// Returns an error only for a caller mistake (missing repo scope, a malformed
// or fingerprint-less observation, two observations sharing one key). Per-file
// I/O failures are skipped so one unreadable record cannot abort a sweep.
func (s *Store) ReconcileStanding(sw StandingSweep) (StandingResult, error) {
	res := StandingResult{Repo: sw.Repo}
	if strings.TrimSpace(sw.Repo) == "" {
		return res, fmt.Errorf("attention: reconcile requires a repo scope")
	}

	// Normalise and validate EVERY observation before taking the lock or
	// touching disk: a sweep is all-or-nothing on caller errors, so a producer
	// bug can never leave the store half-reconciled.
	observed, err := s.prepareObservations(sw)
	if err != nil {
		return res, err
	}
	byKey := make(map[string]int, len(observed))
	for i, o := range observed {
		byKey[o.IdempotencyKey] = i
	}
	evaluated := make(map[string]bool, len(sw.Producers))
	for _, p := range sw.Producers {
		evaluated[p] = true
	}

	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	stored, err := s.scanLocked()
	if err != nil {
		return res, err
	}

	for i := range observed {
		outcome, err := s.reconcileOneLocked(&observed[i], stored)
		if err != nil {
			return res, err
		}
		switch outcome.Action {
		case ActionCreated:
			res.Created++
		case ActionUpdated:
			res.Updated++
		case ActionRefreshed:
			res.Refreshed++
		case OutcomeSuppressed:
			res.Suppressed++
		}
		res.Outcomes = append(res.Outcomes, outcome)
	}

	// Auto-resolve: an open standing request in this repo, from a producer that
	// DID look this sweep, whose condition was not among the observations.
	for _, rec := range stored {
		req := rec.req
		if !req.Standing || req.Lifecycle.State.IsTerminal() {
			continue
		}
		if req.Context.Repo != sw.Repo || !evaluated[req.Producer] {
			continue
		}
		if _, stillTrue := byKey[req.IdempotencyKey]; stillTrue {
			continue
		}
		at := s.nowUTC().Format(tsLayout)
		req.Lifecycle.State = StateAutoResolved
		req.Lifecycle.AutoResolved = &AutoResolvedRecord{
			At:       at,
			Producer: req.Producer,
			Reason:   ReasonConditionCleared,
		}
		if err := s.writeMaterializedLocked(rec.path, req); err != nil {
			continue
		}
		s.emitLocked(JournalEntry{
			Action:         ActionAutoResolved,
			ID:             req.ID,
			IdempotencyKey: req.IdempotencyKey,
			Producer:       req.Producer,
			State:          req.Lifecycle.State,
			Fingerprint:    req.Fingerprint,
			At:             at,
		}, req)
		res.AutoResolved++
		res.Outcomes = append(res.Outcomes, StandingOutcome{
			Key: req.IdempotencyKey, ID: req.ID, Producer: req.Producer, Action: ActionAutoResolved,
		})
	}

	return res, nil
}

// prepareObservations normalises each observation into a fully-defaulted,
// validated standing request.
func (s *Store) prepareObservations(sw StandingSweep) ([]DecisionRequest, error) {
	out := make([]DecisionRequest, 0, len(sw.Observed))
	seen := make(map[string]bool, len(sw.Observed))
	for i := range sw.Observed {
		r := sw.Observed[i]
		r.Standing = true
		if r.Context.Repo == "" {
			r.Context.Repo = sw.Repo
		}
		if r.Context.Repo != sw.Repo {
			return nil, fmt.Errorf("attention: observation %q is scoped to %q, not the swept repo %q",
				r.IdempotencyKey, r.Context.Repo, sw.Repo)
		}
		if strings.TrimSpace(r.Fingerprint) == "" {
			// Without a fingerprint there is no way to tell a re-observation
			// from a state change, so every sweep would re-alert. Reject at the
			// boundary rather than degrade into the spam-folder failure mode.
			return nil, fmt.Errorf("attention: standing request %q requires a fingerprint", r.IdempotencyKey)
		}
		if seen[r.IdempotencyKey] {
			return nil, fmt.Errorf("attention: duplicate idempotency_key %q in one sweep", r.IdempotencyKey)
		}
		seen[r.IdempotencyKey] = true
		if r.DefaultAction == "" {
			r.DefaultAction = ExpireNoop
		}
		// Refresh expiry on every observation: a condition that is still true
		// must never age out.
		r.ExpiresAt = s.nowUTC().Add(StandingExpiry).Format(tsLayout)
		if r.ID == "" {
			id, err := NewID()
			if err != nil {
				return nil, err
			}
			r.ID = id
		}
		if err := validateForRaise(&r); err != nil {
			return nil, err
		}
		s.applyRaiseDefaults(&r)
		out = append(out, r)
	}
	return out, nil
}

// reconcileOneLocked applies one observation against the scanned store.
func (s *Store) reconcileOneLocked(o *DecisionRequest, stored []storedRequest) (StandingOutcome, error) {
	outcome := StandingOutcome{Key: o.IdempotencyKey, Producer: o.Producer}

	if existing, path, ok := findOpenByKey(stored, o.IdempotencyKey); ok {
		// Sticky identity: durable id, creation time and lifecycle survive;
		// the payload is replaced with what the sweep just saw.
		merged := *o
		merged.ID = existing.ID
		merged.CreatedAt = existing.CreatedAt
		merged.Lifecycle = existing.Lifecycle

		action := ActionRefreshed
		if existing.Fingerprint != o.Fingerprint {
			// A genuine state transition: the condition itself moved. Drop the
			// mute (mute lasts until the condition CHANGES) and re-open an
			// acknowledgement, which was scoped to the condition the operator
			// actually saw.
			action = ActionUpdated
			merged.Lifecycle.Muted = nil
			if merged.Lifecycle.State == StateAcknowledged {
				merged.Lifecycle.State = StateOpen
				merged.Lifecycle.Acknowledged = nil
			}
		}
		if err := s.writeMaterializedLocked(path, &merged); err != nil {
			return outcome, err
		}
		s.emitLocked(JournalEntry{
			Action:         action,
			ID:             merged.ID,
			IdempotencyKey: merged.IdempotencyKey,
			Producer:       merged.Producer,
			State:          merged.Lifecycle.State,
			Fingerprint:    merged.Fingerprint,
			Muted:          merged.IsMuted(),
			At:             s.nowUTC().Format(tsLayout),
		}, &merged)
		outcome.ID = merged.ID
		outcome.Action = action
		return outcome, nil
	}

	// No open request. A human who already resolved THIS EXACT condition is not
	// told about it again until it changes — the mute-until-changed rule applied
	// to a resolution. An auto-resolved predecessor never suppresses (the
	// condition genuinely cleared and has come back, which is news), and neither
	// does an expired one (the card went stale on the safety net, not by a
	// decision).
	if prior, ok := latestResolvedByKey(stored, o.IdempotencyKey); ok && prior.Fingerprint == o.Fingerprint {
		outcome.ID = prior.ID
		outcome.Action = OutcomeSuppressed
		return outcome, nil
	}

	path, err := s.pathFor(o.ID)
	if err != nil {
		return outcome, err
	}
	if err := s.writeMaterializedLocked(path, o); err != nil {
		return outcome, err
	}
	s.emitLocked(JournalEntry{
		Action:         ActionCreated,
		ID:             o.ID,
		IdempotencyKey: o.IdempotencyKey,
		Producer:       o.Producer,
		State:          o.Lifecycle.State,
		Fingerprint:    o.Fingerprint,
		At:             s.nowUTC().Format(tsLayout),
	}, o)
	outcome.ID = o.ID
	outcome.Action = ActionCreated
	return outcome, nil
}

// storedRequest pairs a parsed request with the path it came from, so one
// directory scan serves the whole reconciliation.
type storedRequest struct {
	path string
	req  *DecisionRequest
}

// scanLocked reads every parseable request in the store exactly once. Malformed
// files are skipped, matching the reader tolerance List already applies.
func (s *Store) scanLocked() ([]storedRequest, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("attention: read dir: %w", err)
	}
	out := make([]storedRequest, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		req, rerr := readRequest(path)
		if rerr != nil {
			continue
		}
		out = append(out, storedRequest{path: path, req: req})
	}
	return out, nil
}

func findOpenByKey(stored []storedRequest, key string) (*DecisionRequest, string, bool) {
	for _, rec := range stored {
		if rec.req.IdempotencyKey == key && !rec.req.Lifecycle.State.IsTerminal() {
			return rec.req, rec.path, true
		}
	}
	return nil, "", false
}

// latestResolvedByKey returns the most recently HUMAN-resolved request for a
// key, by resolution timestamp.
func latestResolvedByKey(stored []storedRequest, key string) (*DecisionRequest, bool) {
	var best *DecisionRequest
	for _, rec := range stored {
		r := rec.req
		if r.IdempotencyKey != key || r.Lifecycle.State != StateResolved || r.Lifecycle.Resolved == nil {
			continue
		}
		if best == nil || r.Lifecycle.Resolved.At > best.Lifecycle.Resolved.At {
			best = r
		}
	}
	return best, best != nil
}

// Mute suppresses alerting on a request until its condition changes, pinning
// the current fingerprint as the thing being silenced. The card stays in the
// inbox at its severity — muting is not resolving. Terminal requests are a
// no-op, and re-muting an already-muted request re-pins to the current
// fingerprint rather than erroring.
func (s *Store) Mute(id, actor string) (*DecisionRequest, error) {
	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	path, req, err := s.loadLocked(id)
	if err != nil {
		return nil, err
	}
	if req.Lifecycle.State.IsTerminal() {
		return req, nil
	}
	at := s.nowUTC().Format(tsLayout)
	req.Lifecycle.Muted = &MuteRecord{Actor: actor, At: at, Fingerprint: req.Fingerprint}
	if err := s.writeMaterializedLocked(path, req); err != nil {
		return nil, err
	}
	s.emitLocked(JournalEntry{
		Action:         ActionMuted,
		ID:             req.ID,
		IdempotencyKey: req.IdempotencyKey,
		Producer:       req.Producer,
		State:          req.Lifecycle.State,
		Fingerprint:    req.Fingerprint,
		Muted:          true,
		Actor:          actor,
		At:             at,
	}, req)
	return req, nil
}

// Unmute restores alerting. Unmuting an unmuted request is a no-op.
func (s *Store) Unmute(id, actor string) (*DecisionRequest, error) {
	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	path, req, err := s.loadLocked(id)
	if err != nil {
		return nil, err
	}
	if req.Lifecycle.Muted == nil {
		return req, nil
	}
	at := s.nowUTC().Format(tsLayout)
	req.Lifecycle.Muted = nil
	if err := s.writeMaterializedLocked(path, req); err != nil {
		return nil, err
	}
	s.emitLocked(JournalEntry{
		Action:         ActionUnmuted,
		ID:             req.ID,
		IdempotencyKey: req.IdempotencyKey,
		Producer:       req.Producer,
		State:          req.Lifecycle.State,
		Fingerprint:    req.Fingerprint,
		Actor:          actor,
		At:             at,
	}, req)
	return req, nil
}
