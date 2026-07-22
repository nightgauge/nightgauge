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

// Raise creates a new request, or UPDATES the existing open request with the
// same idempotency_key in place (at most one open per key — ADR-015 §C/§D). It
// rejects identity-less records (empty id/idempotency_key/producer) — the #316
// lesson encoded. Returns the id of the live (created or updated) request.
func (s *Store) Raise(req DecisionRequest) (string, error) {
	if err := validateForRaise(&req); err != nil {
		return "", err
	}
	s.applyRaiseDefaults(&req)

	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	// Dedup: an open (non-terminal) request with the same key is updated in
	// place rather than duplicated.
	if existing, path, ok, err := s.findOpenByKeyLocked(req.IdempotencyKey); err != nil {
		return "", err
	} else if ok {
		// Preserve durable identity + creation + lifecycle; refresh the payload.
		merged := req
		merged.ID = existing.ID
		merged.CreatedAt = existing.CreatedAt
		merged.Lifecycle = existing.Lifecycle
		if err := s.writeMaterializedLocked(path, &merged); err != nil {
			return "", err
		}
		s.emitLocked(JournalEntry{
			Action:         ActionUpdated,
			ID:             merged.ID,
			IdempotencyKey: merged.IdempotencyKey,
			Producer:       merged.Producer,
			State:          merged.Lifecycle.State,
			At:             s.nowUTC().Format(tsLayout),
		}, &merged)
		return merged.ID, nil
	}

	path, err := s.pathFor(req.ID)
	if err != nil {
		return "", err
	}
	if err := s.writeMaterializedLocked(path, &req); err != nil {
		return "", err
	}
	s.emitLocked(JournalEntry{
		Action:         ActionCreated,
		ID:             req.ID,
		IdempotencyKey: req.IdempotencyKey,
		Producer:       req.Producer,
		State:          req.Lifecycle.State,
		At:             s.nowUTC().Format(tsLayout),
	}, &req)
	return req.ID, nil
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

// ResolveResult carries the outcome of a Resolve so the caller can surface verb
// and steer errors without the store importing those subsystems.
type ResolveResult struct {
	Request         *DecisionRequest
	Option          Option
	AlreadyResolved bool // the request was already terminal — resolve was a no-op
	VerbErr         error
	SteerErr        error
}

// Resolve applies a resolution once (terminal-state CAS), persists it, then
// executes the option's registered verb OUTSIDE the store lock (ADR-015 §D). A
// replayed resolve on an already-terminal request is a safe no-op. An unknown
// option or unregistered verb is rejected WITHOUT transitioning (ADR-015 §J).
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
	opt, err := ValidateOption(req, optionID)
	if err != nil {
		mu.Unlock()
		return ResolveResult{}, err
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

	// Side effects run OUTSIDE the lock: verb execution and the steer write may
	// touch GitHub / the scheduler / a different store, and must not hold the
	// per-dir mutex.
	res := ResolveResult{Request: req, Option: opt}
	s.listenerMu.Lock()
	steer := s.steerWriter
	s.listenerMu.Unlock()
	if steerText != "" && steer != nil {
		res.SteerErr = steer(req, steerText)
	}
	if exec != nil {
		res.VerbErr = exec.ExecuteVerb(ctx, req, opt)
	}
	return res, nil
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

func (s *Store) findOpenByKeyLocked(key string) (*DecisionRequest, string, bool, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("attention: read dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		req, rerr := readRequest(path)
		if rerr != nil {
			continue
		}
		if req.IdempotencyKey == key && !req.Lifecycle.State.IsTerminal() {
			return req, path, true, nil
		}
	}
	return nil, "", false, nil
}

// writeMaterializedLocked persists the request via write-temp + rename so a
// reader never observes a half-written record (ADR-015 §C).
func (s *Store) writeMaterializedLocked(path string, req *DecisionRequest) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("attention: create store dir: %w", err)
	}
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
