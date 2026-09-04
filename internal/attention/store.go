package attention

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/history"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// Subdir is the workspace-relative directory the attention store lives in.
const Subdir = ".nightgauge/attention"

// journalFile is the append-only lifecycle audit within the store directory.
const journalFile = "journal.jsonl"

// tsLayout is RFC3339 with nanosecond precision in UTC, matching the ADR-015
// created_at/expires_at contract.
const tsLayout = time.RFC3339Nano

// idPattern guards a request id against path traversal before it is used as a
// filename: `dr_` followed by uuidv7-ish characters (hex + dashes).
var idPattern = regexp.MustCompile(`^dr_[A-Za-z0-9-]{8,80}$`)

// dirLocks provides per-directory serialization so concurrent goroutines inside
// one process (parallel producers, the sweep, and a resolve) never interleave a
// read-modify-write on the same attention store — the #316 lesson. Cross-process
// safety comes from atomic temp+rename on the materialized file plus the
// terminal-state CAS (a losing writer no-ops).
var dirLocks sync.Map // abs dir -> *sync.Mutex

func lockFor(dir string) *sync.Mutex {
	m, _ := dirLocks.LoadOrStore(dir, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// NewID returns a fresh request id: `dr_<uuidv7>` (time-ordered, ADR-015 §A).
func NewID() (string, error) {
	u, err := runstate.NewRunID()
	if err != nil {
		return "", fmt.Errorf("attention: generate id: %w", err)
	}
	return "dr_" + u, nil
}

// JournalEntry is one append-only lifecycle audit line (ADR-015 §C). Every
// transition — created, updated (idempotent re-raise), acknowledged, resolved,
// expired — is one line, byte-equivalent with every other JSONL store.
type JournalEntry struct {
	SchemaVersion  int    `json:"schema_version"`
	Action         string `json:"action"`
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Producer       string `json:"producer,omitempty"`
	State          State  `json:"state"`
	Actor          string `json:"actor,omitempty"`
	OptionID       string `json:"option_id,omitempty"`
	Applied        string `json:"applied,omitempty"`
	At             string `json:"at"`
	// Fingerprint is the standing condition's material state at this
	// transition (issue #92); empty for event-scoped requests.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Muted records that alerting was suppressed at this transition. Consumed
	// by ShouldNotify.
	Muted bool `json:"muted,omitempty"`
}

// Journal action constants.
const (
	ActionCreated      = "created"
	ActionUpdated      = "updated"
	ActionAcknowledged = "acknowledged"
	ActionResolved     = "resolved"
	ActionExpired      = "expired"
)

// TransitionListener is notified after each transition is durably persisted
// (journal + materialized file). The store fans out to every registered
// listener so multiple concerns subscribe without the store importing them: the
// orchestrator wires the ADR-013 decision_request trace leg + run-history leg,
// and the IPC server wires the `attention.event` surface push.
type TransitionListener func(entry JournalEntry, req *DecisionRequest)

// SteerWriter writes the OPERATOR_STEER feedback signal for a resolve that
// carried steer_text (ADR-015 §G). The caller wires the orchestrator feedback
// path. Best-effort: an error is surfaced to the resolver but never rolls back
// the resolution.
type SteerWriter func(req *DecisionRequest, steerText string) error

// Store is the single authoritative writer for `.nightgauge/attention/`. All
// mutations route through one Store type with its serialization discipline;
// surfaces never write the files directly (ADR-015 §C).
type Store struct {
	rootDir string
	dir     string
	now     func() time.Time // injectable clock for tests

	listenerMu  sync.Mutex
	listeners   []TransitionListener
	steerWriter SteerWriter
}

// New constructs a Store rooted at the workspace root. rootDir is the directory
// that contains `.nightgauge/`.
func New(rootDir string) *Store {
	return &Store{
		rootDir: rootDir,
		dir:     filepath.Join(rootDir, ".nightgauge", "attention"),
		now:     time.Now,
	}
}

// Subscribe registers a transition listener. Safe to call concurrently;
// listeners fire in registration order after each persisted transition.
func (s *Store) Subscribe(l TransitionListener) {
	if s == nil || l == nil {
		return
	}
	s.listenerMu.Lock()
	s.listeners = append(s.listeners, l)
	s.listenerMu.Unlock()
}

// SetSteerWriter wires the OPERATOR_STEER feedback writer.
func (s *Store) SetSteerWriter(w SteerWriter) {
	if s == nil {
		return
	}
	s.listenerMu.Lock()
	s.steerWriter = w
	s.listenerMu.Unlock()
}

// WithClock overrides the clock (tests only).
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

// Dir returns the absolute attention store directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) nowUTC() time.Time { return s.now().UTC() }

// pathFor returns the materialized file path for id, guarding against traversal.
func (s *Store) pathFor(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("attention: invalid request id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// RaiseOutcome names what a Raise actually DID. Raise has four genuine
// results and they are not interchangeable — a caller that only learns "some
// id" cannot tell "a card is now in front of the operator" from "the operator
// already dismissed this exact condition and nothing was shown". The IPC
// raise verb (#305) surfaces this verbatim so a remote producer knows which
// of the four happened, exactly as an in-process Go producer could infer from
// the journal action.
//
// A store that is not configured is an ERROR, not a fifth outcome: "I could
// not write" is never "I wrote nothing on purpose".
type RaiseOutcome string

// The four values are the SAME vocabulary the standing reconciler already
// reports on StandingOutcome.Action — deliberately aliased to the journal
// action constants rather than re-spelled, so "created" means one thing in this
// package and a surface never has to learn a second set of words for the same
// four facts.
const (
	// OutcomeCreated — no live record existed for the key; a new card was
	// materialized (or an EXPIRED predecessor revived under its own id).
	OutcomeCreated RaiseOutcome = ActionCreated
	// OutcomeUpdated — an open record for the key was refreshed with new
	// payload, and the condition itself moved (event-shaped request, or a
	// standing request whose fingerprint changed). Re-alerts.
	OutcomeUpdated RaiseOutcome = ActionUpdated
	// OutcomeRefreshed — the open record for the key was re-observed and kept.
	// Deliberately silent. Two routes reach it:
	//
	//  1. An open STANDING record re-observed with an unchanged fingerprint —
	//     content refreshed, no re-alert (ADR-015 §M).
	//  2. A raise that would have STRIPPED A REMEDY off the open card: the
	//     stored record offers an option bound to a real verb and the incoming
	//     one offers only noops. The observation is recorded; the payload is
	//     not replaced. See the remedy-preservation block in Raise.
	OutcomeRefreshed RaiseOutcome = ActionRefreshed
	// OutcomeSuppressed — a human already RESOLVED this exact standing
	// condition and its fingerprint has not moved, so nothing was written and
	// no card is showing (ADR-015 §M). The returned id is the prior record's.
	//
	// Not a journal action: nothing was persisted, so there is nothing to
	// audit. It is still an OUTCOME, because the caller has to be able to tell
	// it apart from a card that is now on screen.
	OutcomeSuppressed RaiseOutcome = "suppressed"
)

// Raise creates a new request, or folds it into the record that already exists
// for the same idempotency_key (ADR-015 §C/§D). It rejects identity-less
// records (empty id/idempotency_key/producer) — the #316 lesson encoded.
// Returns what it did and the id of the live request.
//
// "One record per key" holds across expiry, not just while a card is open
// (#108). An open record is updated in place; an EXPIRED one is revived under
// its original id, because a producer re-raising the key is asserting the
// condition outlived its own TTL — and minting a fresh id per TTL window turns
// one long-lived condition into an unbounded stream of duplicate cards.
//
// Standing requests additionally follow the §M rules: an unchanged fingerprint
// refreshes the card without re-alerting, and a condition a human already
// resolved is not handed straight back until its fingerprint moves.
func (s *Store) Raise(req DecisionRequest) (RaiseOutcome, string, error) {
	if err := validateForRaise(&req); err != nil {
		return "", "", err
	}
	s.applyRaiseDefaults(&req)

	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	stored, err := s.scanLocked()
	if err != nil {
		return "", "", err
	}

	// Dedup: an open (non-terminal) request with the same key is updated in
	// place rather than duplicated.
	if existing, path, ok := findOpenByKey(stored, req.IdempotencyKey); ok {
		// A RAISE MAY NEVER TAKE A REMEDY OFF AN OPEN CARD (fixed in review).
		//
		// The merge below is last-writer-wins over the whole payload, which is
		// right for content that moves (a spend that grew, a blocker that
		// changed) and wrong for the OPTION SET. Two structurally different
		// offers can share one idempotency key: `budget-ceiling:<repo>#<n>` is
		// raised both with the `budget.raiseCeiling` option (the daemon
		// corroborated the run's spend) and without it (it could not), and the
		// Go scheduler and the IPC verb (#305) dedup onto the same record. So an
		// uncorroborated observation arriving second silently rewrote a card the
		// scheduler had raised WITH its remedy into one offering two noops — the
		// operator lost a one-click fix and got no signal that it had gone.
		//
		// Keep the whole stored payload, not just its options: a card's title and
		// body EXPLAIN its options ("Raise to $112.50 & retry"), so grafting new
		// prose onto old options produces a card that contradicts itself. The
		// observation is still recorded — ActionRefreshed, the existing
		// non-alerting re-observation action, because nothing the operator sees
		// changed.
		//
		// Strictly one-directional: it blocks DOWNGRADES only. A raise that
		// carries a remedy still replaces (in-place escalation keeps working),
		// and a raise that ADDS one to a remedy-free card still replaces.
		if optionsOfferARemedy(existing.Options) && !optionsOfferARemedy(req.Options) {
			kept := *existing
			s.emitLocked(JournalEntry{
				Action:         ActionRefreshed,
				ID:             kept.ID,
				IdempotencyKey: kept.IdempotencyKey,
				Producer:       kept.Producer,
				State:          kept.Lifecycle.State,
				Fingerprint:    kept.Fingerprint,
				Muted:          kept.IsMuted(),
				At:             s.nowUTC().Format(tsLayout),
			}, &kept)
			return OutcomeRefreshed, kept.ID, nil
		}

		// Preserve durable identity + creation + lifecycle; refresh the payload.
		merged := req
		merged.ID = existing.ID
		merged.CreatedAt = existing.CreatedAt
		merged.Lifecycle = existing.Lifecycle
		action := ActionUpdated
		outcome := OutcomeUpdated
		if merged.Standing && merged.Fingerprint == existing.Fingerprint {
			// The same condition re-observed: bodies and titles move on their
			// own, so refresh the content and stay silent.
			action = ActionRefreshed
			outcome = OutcomeRefreshed
		} else if merged.Standing {
			// The condition itself moved. Drop a mute pinned to the fingerprint
			// that no longer applies, and re-open an acknowledgement that was
			// scoped to the condition the operator actually saw.
			merged.Lifecycle.Muted = nil
			if merged.Lifecycle.State == StateAcknowledged {
				merged.Lifecycle.State = StateOpen
				merged.Lifecycle.Acknowledged = nil
			}
		}
		if err := s.writeMaterializedLocked(path, &merged); err != nil {
			return "", "", err
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
		return outcome, merged.ID, nil
	}

	// No open record. A human who already resolved THIS EXACT standing
	// condition is not told about it again until it changes (ADR-015 §M);
	// without this, dismissing a card for a condition that is still true hands
	// it back on the next observation.
	if req.Standing {
		if prior, ok := latestResolvedByKey(stored, req.IdempotencyKey); ok && prior.Fingerprint == req.Fingerprint {
			return OutcomeSuppressed, prior.ID, nil
		}
	}

	// Revive an expired predecessor under its own id rather than writing a
	// second file for the same condition (#108).
	if prior, ok := findExpiredByKey(stored, req.IdempotencyKey); ok {
		req.ID = prior.ID
	}
	path, err := s.pathFor(req.ID)
	if err != nil {
		return "", "", err
	}
	if err := s.writeMaterializedLocked(path, &req); err != nil {
		return "", "", err
	}
	s.emitLocked(JournalEntry{
		Action:         ActionCreated,
		ID:             req.ID,
		IdempotencyKey: req.IdempotencyKey,
		Producer:       req.Producer,
		State:          req.Lifecycle.State,
		Fingerprint:    req.Fingerprint,
		At:             s.nowUTC().Format(tsLayout),
	}, &req)
	return OutcomeCreated, req.ID, nil
}

// optionsOfferARemedy reports whether an option set contains anything that
// DOES something on resolve.
//
// The test is "binds a verb other than noop", not a producer allowlist:
// `noop` is the registry's explicit "resolve and change nothing" choice, so
// every other registered verb is by definition an action the operator would
// lose if the option disappeared. Keeping the rule at the verb level means a
// new producer inherits the protection without registering anywhere.
func optionsOfferARemedy(options []Option) bool {
	for _, opt := range options {
		if Verb(opt.Verb) != VerbNoop {
			return true
		}
	}
	return false
}

// validateForRaise rejects identity-less and malformed records BEFORE any disk
// mutation (ADR-015 §C: reject identity-less records).
func validateForRaise(req *DecisionRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		return fmt.Errorf("attention: request id is required")
	}
	if !idPattern.MatchString(req.ID) {
		return fmt.Errorf("attention: invalid request id %q", req.ID)
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return fmt.Errorf("attention: idempotency_key is required")
	}
	if strings.TrimSpace(req.Producer) == "" {
		return fmt.Errorf("attention: producer is required")
	}
	if !IsValidKind(req.Kind) {
		return fmt.Errorf("attention: invalid kind %q", req.Kind)
	}
	if !IsValidSeverity(req.Severity) {
		return fmt.Errorf("attention: invalid severity %q", req.Severity)
	}
	if strings.TrimSpace(req.Title) == "" {
		return fmt.Errorf("attention: title is required")
	}
	if req.Standing && strings.TrimSpace(req.Fingerprint) == "" {
		// Without a fingerprint there is no way to tell a re-observation from a
		// state change, so every observation would re-alert. Reject at the
		// boundary rather than degrade into the spam-folder failure mode
		// (ADR-015 §M).
		return fmt.Errorf("attention: standing request %q requires a fingerprint", req.IdempotencyKey)
	}
	// Every declared option must bind a registered verb (the security boundary
	// applies at raise time too, so a producer cannot persist a bad option).
	for _, opt := range req.Options {
		if strings.TrimSpace(opt.ID) == "" {
			return fmt.Errorf("attention: option id is required")
		}
		if !IsRegisteredVerb(opt.Verb) {
			return fmt.Errorf("attention: option %q binds unregistered verb %q", opt.ID, opt.Verb)
		}
	}
	// default_action must be expire_noop or a declared option id.
	if req.DefaultAction == "" {
		return fmt.Errorf("attention: default_action is required (use %q for a no-op)", ExpireNoop)
	}
	if req.DefaultAction != ExpireNoop && req.FindOption(req.DefaultAction) == nil {
		return fmt.Errorf("attention: default_action %q is not a declared option", req.DefaultAction)
	}
	return nil
}

func (s *Store) applyRaiseDefaults(req *DecisionRequest) {
	req.SchemaVersion = SchemaVersion
	now := s.nowUTC()
	if req.CreatedAt == "" {
		req.CreatedAt = now.Format(tsLayout)
	}
	if req.ExpiresAt == "" {
		// A raise without an explicit expiry gets a conservative 24h default so
		// nothing lingers forever (ADR-015 §C). Producers always set this
		// explicitly; the default is a safety net.
		req.ExpiresAt = now.Add(24 * time.Hour).Format(tsLayout)
	}
	if req.Lifecycle.State == "" {
		req.Lifecycle.State = StateOpen
	}
}

// Get reads one request by id. Returns (nil, false, nil) when absent.
func (s *Store) Get(id string) (*DecisionRequest, bool, error) {
	path, err := s.pathFor(id)
	if err != nil {
		return nil, false, err
	}
	req, err := readRequest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return req, true, nil
}

// ListFilter narrows a List scan.
type ListFilter struct {
	// IncludeTerminal includes resolved/expired requests (default: open-ish only).
	IncludeTerminal bool
	// Repo, when non-empty, restricts to requests whose context repo matches.
	Repo string
	// ExcludeMuted drops requests an operator muted. Off by default: a muted
	// card is silenced, not hidden — it still belongs in the inbox. Surfaces
	// that render an alert-worthy subset set this.
	ExcludeMuted bool
}

// List returns requests matching the filter, ordered most-severe-then-newest
// (the inbox order — ADR-015 §I). Malformed files are skipped.
func (s *Store) List(filter ListFilter) ([]DecisionRequest, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("attention: read dir: %w", err)
	}
	var out []DecisionRequest
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		req, err := readRequest(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // skip malformed/partial — a reader tolerates them
		}
		if !filter.IncludeTerminal && req.Lifecycle.State.IsTerminal() {
			continue
		}
		if filter.Repo != "" && req.Context.Repo != filter.Repo {
			continue
		}
		if filter.ExcludeMuted && req.IsMuted() {
			continue
		}
		out = append(out, *req)
	}
	sortInbox(out)
	return out, nil
}

// severityRank orders severities most-urgent first.
func severityRank(s Severity) int {
	switch s {
	case SeverityBlockingFleet:
		return 0
	case SeverityBlockingRun:
		return 1
	default:
		return 2
	}
}

func sortInbox(reqs []DecisionRequest) {
	sort.SliceStable(reqs, func(i, j int) bool {
		ri, rj := severityRank(reqs[i].Severity), severityRank(reqs[j].Severity)
		if ri != rj {
			return ri < rj
		}
		// Newest first within a severity band.
		return reqs[i].CreatedAt > reqs[j].CreatedAt
	})
}

// Acknowledge marks a request seen without resolving it (non-blocking — ADR-015
// §A). Terminal or already-acknowledged requests are a no-op.
func (s *Store) Acknowledge(id, actor string) (*DecisionRequest, error) {
	if strings.TrimSpace(actor) == "" {
		// Same contract as Resolve (#1405): the acknowledgement record carries
		// an actor the platform requires to be non-empty.
		return nil, fmt.Errorf("attention: acknowledging %s requires an actor", id)
	}
	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	path, req, err := s.loadLocked(id)
	if err != nil {
		return nil, err
	}
	if req.Lifecycle.State.IsTerminal() || req.Lifecycle.State == StateAcknowledged {
		return req, nil // idempotent no-op
	}
	at := s.nowUTC().Format(tsLayout)
	req.Lifecycle.State = StateAcknowledged
	req.Lifecycle.Acknowledged = &AckRecord{Actor: actor, At: at}
	if err := s.writeMaterializedLocked(path, req); err != nil {
		return nil, err
	}
	s.emitLocked(JournalEntry{
		Action: ActionAcknowledged,
		ID:     req.ID,
		State:  req.Lifecycle.State,
		Actor:  actor,
		At:     at,
	}, req)
	return req, nil
}

// ResolveResult carries the outcome of a Resolve so the caller can surface
// steer errors without the store importing those subsystems.
type ResolveResult struct {
	Request         *DecisionRequest
	Option          Option
	AlreadyResolved bool // the request was already terminal — resolve was a no-op
	SteerErr        error
}

// Resolve applies a resolution once (terminal-state CAS): it executes the
// option's registered verb WHILE STILL HOLDING the store lock, and persists
// the resolved transition only if the verb succeeds (ADR-015 §D). This
// serializes all resolves for one repo's request directory for the duration
// of the verb call — a deliberate trade-off, since resolutions are
// human-paced, not a hot path, and the alternative (a per-request lock, or
// re-validating a torn CAS after re-acquiring the lock post-verb) is
// meaningfully more complex for no observed concurrency problem. It also
// keeps exactly-once verb execution: a losing concurrent resolve never gets
// past the terminal-state check, so it never runs the verb at all.
//
// A replayed resolve on an already-terminal request is a safe no-op. An
// unknown option or unregistered verb is rejected WITHOUT transitioning
// (ADR-015 §J). If the verb fails, Resolve returns the error (typically a
// *VerbExecutionError) and leaves the request untouched on disk — no
// mutation, no persist, no journal entry — so the card stays open and a
// retry after the underlying condition clears hits the same code path fresh.
func (s *Store) Resolve(ctx context.Context, id, optionID, actor, steerText, note string, exec VerbExecutor) (ResolveResult, error) {
	mu := lockFor(s.dir)
	mu.Lock()

	path, req, err := s.loadLocked(id)
	if err != nil {
		mu.Unlock()
		return ResolveResult{}, err
	}
	if req.Lifecycle.State.IsTerminal() {
		mu.Unlock()
		return ResolveResult{Request: req, AlreadyResolved: true}, nil
	}
	if strings.TrimSpace(actor) == "" {
		// The card contract requires a non-empty resolver (the platform's
		// LifecycleSchema is `actor: z.string().min(1)`), and a resolution
		// nobody is named for is not worth recording (#1405). Refused BEFORE
		// the verb runs, so a caller that forgot the actor does not get a
		// half-applied resolution it cannot persist.
		//
		// Refused rather than defaulted: the store cannot know who the operator
		// is, and inventing one puts a false name in an audit record. The
		// callers that DO know supply it — the CLI via attentionActor(), the
		// IPC layer via its own fallback.
		mu.Unlock()
		return ResolveResult{}, fmt.Errorf("attention: resolving %s requires an actor", id)
	}
	opt, err := ValidateOption(req, optionID)
	if err != nil {
		mu.Unlock()
		return ResolveResult{}, err
	}
	// THE STEER MUST BE ON DISK BEFORE THE VERB RUNS (#1410).
	//
	// A verb that re-dispatches the issue starts a run that reads
	// feedback-{N}.json at stage start. Writing the steer after the verb — and
	// after the unlock, as this did — races the run against the note it was
	// supposed to carry, and a steer that silently fails to arrive is
	// indistinguishable from one that worked.
	//
	// Latent until an option re-dispatches, which is exactly why it is fixed
	// now: at that point the bug would be attributed to the new option rather
	// than to the ordering that was always wrong.
	//
	// INSIDE THE LOCK, and that is a deliberate narrowing of the writer's
	// contract. It was outside because an injected writer "may touch GitHub /
	// the scheduler / a different store". The alternative — release, write,
	// re-acquire — reintroduces the compare-and-swap-after-reacquire the design
	// avoids and breaks exactly-once verb execution, which is the stronger
	// property. So the contract is now stated rather than assumed: a steer
	// writer runs under the per-directory mutex and must not block
	// indefinitely. Both writers wired today (autonomous.go and the attention
	// CLI) are WriteOperatorSteer, a local file write.
	var steerErr error
	s.listenerMu.Lock()
	steer := s.steerWriter
	s.listenerMu.Unlock()
	if steerText != "" && steer != nil {
		steerErr = steer(req, steerText)
	}

	if exec != nil {
		if verr := exec.ExecuteVerb(ctx, req, opt); verr != nil {
			mu.Unlock()
			return ResolveResult{}, verr
		}
	}
	at := s.nowUTC().Format(tsLayout)
	req.Lifecycle.State = StateResolved
	req.Lifecycle.Resolved = &ResolvedRecord{
		Actor:     actor,
		At:        at,
		OptionID:  optionID,
		SteerText: steerText,
		Note:      note,
	}
	if err := s.writeMaterializedLocked(path, req); err != nil {
		mu.Unlock()
		return ResolveResult{}, err
	}
	s.emitLocked(JournalEntry{
		Action:   ActionResolved,
		ID:       req.ID,
		State:    req.Lifecycle.State,
		Actor:    actor,
		OptionID: optionID,
		At:       at,
	}, req)
	mu.Unlock()

	// The steer was written above, before the verb (#1410); its error is
	// reported here unchanged, so callers that inspect SteerErr are unaffected.
	return ResolveResult{Request: req, Option: opt, SteerErr: steerErr}, nil
}

// SweepExpired transitions every open-ish request past its expires_at to
// expired, applying default_action (ADR-015 §C). Idempotent and itself a single
// writer, so expiry cannot race a concurrent resolve — a request already
// resolved is skipped. Returns the number expired. Verbs for non-noop defaults
// execute outside the lock.
func (s *Store) SweepExpired(ctx context.Context, exec VerbExecutor) (int, error) {
	now := s.nowUTC()

	mu := lockFor(s.dir)
	mu.Lock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		mu.Unlock()
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("attention: read dir: %w", err)
	}

	type pending struct {
		req *DecisionRequest
		opt Option
	}
	var toExecute []pending
	expired := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		req, rerr := readRequest(path)
		if rerr != nil {
			continue
		}
		if req.Lifecycle.State.IsTerminal() {
			continue
		}
		exp, perr := time.Parse(tsLayout, req.ExpiresAt)
		if perr != nil || now.Before(exp) {
			continue
		}
		at := now.Format(tsLayout)
		req.Lifecycle.State = StateExpired
		req.Lifecycle.Expired = &ExpiredRecord{At: at, Applied: req.DefaultAction}
		if werr := s.writeMaterializedLocked(path, req); werr != nil {
			continue
		}
		s.emitLocked(JournalEntry{
			Action:  ActionExpired,
			ID:      req.ID,
			State:   req.Lifecycle.State,
			Applied: req.DefaultAction,
			At:      at,
		}, req)
		expired++
		if req.DefaultAction != ExpireNoop {
			if opt, verr := ValidateOption(req, req.DefaultAction); verr == nil {
				toExecute = append(toExecute, pending{req: req, opt: opt})
			}
		}
	}
	mu.Unlock()

	if exec != nil {
		for _, p := range toExecute {
			_ = exec.ExecuteVerb(ctx, p.req, p.opt)
		}
	}
	return expired, nil
}

// --- locked helpers (caller holds the per-dir mutex) ---

func (s *Store) loadLocked(id string) (string, *DecisionRequest, error) {
	path, err := s.pathFor(id)
	if err != nil {
		return "", nil, err
	}
	req, err := readRequest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("attention: request %s not found", id)
		}
		return "", nil, err
	}
	return path, req, nil
}

// writeMaterializedLocked persists the request via write-temp + rename so a
// reader never observes a half-written record (ADR-015 §C).
func (s *Store) writeMaterializedLocked(path string, req *DecisionRequest) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("attention: create store dir: %w", err)
	}
	normalizeForWire(req)
	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return fmt.Errorf("attention: marshal request: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("attention: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("attention: rename: %w", err)
	}
	return nil
}

// normalizeForWire fixes up the shapes Go's zero values produce but the card
// contract cannot express (#1405).
//
// THE ONE THAT MATTERED. `Options []Option` marshals a nil slice as
// `"options":null`, and the platform's card schema is
// `options: z.array(OptionSchema).max(20)` — required, not nullable, not
// optional. So a producer that simply never set Options published a card the
// platform rejected on arrival, forever: 27 such cards on this machine, all
// from post-merge-hook, retried on every sweep and visible on no remote
// surface.
//
// `omitempty` would NOT have fixed it. The key is REQUIRED, so dropping it
// turns "expected array, received null" into "expected array, received
// undefined" and the card stays exactly as invisible. An empty array is what
// the schema accepts — `.max(20)` has no lower bound — so the field must be
// present and empty, not absent.
//
// NORMALIZED HERE, AT THE SINGLE PERSIST CHOKEPOINT, rather than at the one
// producer that has the bug today. Every raise, ack and resolve funnels through
// this function, so a producer added tomorrow cannot reintroduce the shape by
// forgetting a field — which is exactly how this one arrived. Raise's own
// option validation is a range loop, and a nil slice iterates zero times, so
// local validation could never see it.
//
// Deliberately NOT a place to invent data: it repairs shapes, never values. An
// empty resolver actor is rejected at the boundary instead (see Resolve),
// because guessing who resolved a card is worse than refusing to record it.
func normalizeForWire(req *DecisionRequest) {
	if req == nil {
		return
	}
	if req.Options == nil {
		req.Options = []Option{}
	}
}

// emitLocked appends the journal line and fires the OnTransition hook. Called
// under the per-dir mutex; the hook must not re-enter the store (it drives
// event push / external audit legs only).
func (s *Store) emitLocked(entry JournalEntry, req *DecisionRequest) {
	entry.SchemaVersion = SchemaVersion
	if entry.At == "" {
		entry.At = s.nowUTC().Format(tsLayout)
	}
	if err := history.AppendJSONL(filepath.Join(s.dir, journalFile), entry); err != nil {
		// Fail-open: the materialized file is already the read model; a journal
		// append failure must not fail the transition.
		fmt.Fprintf(os.Stderr, "attention: journal append failed (fail-open): %v\n", err)
	}
	s.listenerMu.Lock()
	listeners := make([]TransitionListener, len(s.listeners))
	copy(listeners, s.listeners)
	s.listenerMu.Unlock()
	for _, l := range listeners {
		l(entry, req)
	}
}

// ReadJournal reads every journal entry in order (oldest first). Used for audit
// tooling and tests. A missing journal returns (nil, nil).
func (s *Store) ReadJournal() ([]JournalEntry, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, journalFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []JournalEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e JournalEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func readRequest(path string) (*DecisionRequest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var req DecisionRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("attention: parse %s: %w", filepath.Base(path), err)
	}
	return &req, nil
}
