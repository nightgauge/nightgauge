// Package sweep evaluates repo-scoped attention producers with no pipeline run
// in flight (issue #89).
//
// Every DecisionRequest producer Nightgauge had before this package fired from
// the orchestrator's run loop, so a repo could only report a blocker while
// something was already running — which is precisely when an operator is most
// likely to notice on their own. The conditions that matter most (a red
// required check on the default branch, a green PR nobody can merge) exist
// when nothing is running at all.
//
// A sweep is the missing evaluation loop: it asks each registered producer "is
// this repo blocked?", then RECONCILES the answers against the store —
// raising what is newly true, leaving untouched what is still true, and
// auto-resolving what is no longer true (see attention.ReconcileStanding).
//
// Design commitments:
//
//   - NOT A DAEMON. A sweep is cheap, idempotent, and safe to run redundantly.
//     Callers invoke it on extension activation, on a repository-view refresh,
//     on a conservative timer, and after a run terminates. A long-lived process
//     is a bigger commitment than the problem warrants and complicates the
//     local-first story.
//   - LOCAL-FIRST. No hosted platform, no scheduler, no run required.
//   - FORGE-NEUTRAL. Producers receive a forge.ForgeClient, never a GitHub
//     client, so GitLab is not designed out.
//   - NEVER FATAL. A producer that errors is logged and excluded from
//     reconciliation, so its existing cards are left alone rather than
//     retracted on a transient failure. An auth or rate-limit failure skips the
//     whole sweep. Worst case: no cards this cycle.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/trace"
)

// DefaultTimeout bounds the API cost of one sweep. Producers run sequentially
// under a single deadline, so the worst case for a repo is one timeout's worth
// of forge traffic no matter how many producers are registered.
const DefaultTimeout = 30 * time.Second

// Input is what a producer gets to evaluate a repo. It carries the resolved
// repo identity in both forms so producers never re-parse owner/name.
type Input struct {
	// Repo is the canonical "owner/name" spec.
	Repo string
	// Owner and Name are Repo split, for the forge service signatures.
	Owner string
	Name  string
	// Forge is the forge-abstraction client for this repo. Never nil when a
	// producer is called.
	Forge forge.ForgeClient

	// Existing is every non-terminal request already open for this repo, from
	// every producer including the run-scoped ones.
	//
	// It exists so a sweep producer can decline to raise a condition another
	// producer is already carding. A PR blocked by branch protection is one
	// fact; a run that punted on it and a sweep that scanned for it are two
	// vantage points on that one fact, and the operator should get one card,
	// not two. Producers must treat this as read-only advisory context — the
	// store is the sweeper's to write.
	Existing []attention.DecisionRequest
}

// OpenRequestForPR returns the first non-terminal request from the named
// producer that is about the given PR. Producers use it to dedupe; it is a
// method on Input so the "one condition, one card" rule has one implementation
// rather than one per producer.
func (in Input) OpenRequestForPR(producer string, pr int) (attention.DecisionRequest, bool) {
	for _, r := range in.Existing {
		if r.Producer == producer && r.Context.PR == pr && !r.Lifecycle.State.IsTerminal() {
			return r, true
		}
	}
	return attention.DecisionRequest{}, false
}

// Producer evaluates one class of repo-scoped standing condition.
//
// Registration is an interface precisely so the producers in this epic — and
// every future one — need no change to the sweep itself: a producer is
// anything that can answer "what is true about this repo right now?".
type Producer interface {
	// Name is the stable producer id stamped on every request it raises. It is
	// half of the (producer, idempotency_key) sticky identity, so it must never
	// change once shipped.
	Name() string

	// Evaluate returns one DecisionRequest per condition that is CURRENTLY
	// true. Each must carry a stable IdempotencyKey and a Fingerprint derived
	// from what the condition is — never from when it was observed.
	//
	// An empty slice is a positive assertion that no condition holds, and
	// causes this producer's open requests to auto-resolve. An error means "I
	// could not look", and leaves them untouched. Producers must never return
	// an empty slice to signal a failure.
	Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error)
}

// Registry is the set of producers a sweep evaluates. Producers register into
// Default from an init() so adding one is a single self-contained file.
type Registry struct {
	mu        sync.RWMutex
	producers []Producer
	// workspaceProducers are evaluated once per sweep against the whole
	// configured repo list rather than per-repo (#260). See workspace.go.
	workspaceProducers []WorkspaceProducer
}

// NewRegistry returns an empty registry (tests build their own).
func NewRegistry() *Registry { return &Registry{} }

// Default is the process-wide registry the CLI sweeps.
var Default = NewRegistry()

// Register adds a producer. A duplicate Name replaces the earlier
// registration rather than double-evaluating the same condition.
func (r *Registry) Register(p Producer) {
	if r == nil || p == nil || strings.TrimSpace(p.Name()) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.producers {
		if existing.Name() == p.Name() {
			r.producers[i] = p
			return
		}
	}
	r.producers = append(r.producers, p)
}

// Producers returns the registered producers in a stable name order, so a
// sweep's forge traffic and its trace are reproducible.
func (r *Registry) Producers() []Producer {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Producer, len(r.producers))
	copy(out, r.producers)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Result reports what one sweep did. It is the CLI's --json payload.
type Result struct {
	Repo string `json:"repo"`
	// SweepID identifies this sweep in the decision trace.
	SweepID string `json:"sweep_id,omitempty"`
	// Skipped is true when the sweep declined to reconcile at all — the
	// degraded path for auth and rate-limit failures. No cards were created,
	// updated, or retracted.
	Skipped bool `json:"skipped,omitempty"`
	// SkipReason explains a skip in one line.
	SkipReason string `json:"skip_reason,omitempty"`
	// Evaluated lists the producers that observed the repo successfully.
	Evaluated []string `json:"evaluated,omitempty"`
	// Failed maps a producer name to the error that stopped it. Its cards were
	// deliberately left untouched.
	Failed map[string]string `json:"failed,omitempty"`
	// Reconciled carries the store diff.
	Reconciled attention.StandingResult `json:"reconciled"`
}

// OK reports whether every registered producer observed the repo and the sweep
// reconciled. --strict turns a false here into a non-zero exit.
func (r Result) OK() bool { return !r.Skipped && len(r.Failed) == 0 }

// Sweeper evaluates a registry against one repo and reconciles the store.
type Sweeper struct {
	// Store is the DecisionRequest store. Required.
	Store *attention.Store
	// Registry defaults to Default when nil.
	Registry *Registry
	// Forge is the client handed to every producer. Required.
	Forge forge.ForgeClient
	// WorkspaceRoot enables the decision-trace leg; empty disables it.
	WorkspaceRoot string
	// Timeout bounds one sweep. Zero uses DefaultTimeout.
	Timeout time.Duration
	// Logf receives degradation messages. Nil uses the standard logger.
	Logf func(format string, args ...any)
}

func (s *Sweeper) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Sweep evaluates every registered producer against repo ("owner/name") and
// reconciles the results.
//
// It returns an error only for a caller mistake (no store, no forge client, a
// malformed repo spec, a producer returning an invalid request). Everything
// environmental — a network failure, an expired token, a rate limit — is a
// logged degradation reported on Result, never an error: a sweep failure must
// not be fatal to whatever invoked it.
func (s *Sweeper) Sweep(ctx context.Context, repo string) (Result, error) {
	res := Result{Repo: repo}
	if s == nil || s.Store == nil {
		return res, fmt.Errorf("sweep: store is required")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return res, err
	}
	if s.Forge == nil {
		return res, fmt.Errorf("sweep: forge client is required")
	}

	reg := s.Registry
	if reg == nil {
		reg = Default
	}
	producers := reg.Producers()
	if len(producers) == 0 {
		// Nothing registered is a legitimate steady state (a build with no
		// repo-scoped producers), not a failure. Reconciling with an empty
		// producer list would be a no-op anyway; returning early keeps it free.
		return res, nil
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Read the repo's open cards ONCE, before any producer runs, so every
	// producer dedupes against the same snapshot. Reading per-producer would
	// let the first producer's own writes influence the second's decisions
	// within a single sweep, which makes the outcome depend on registration
	// order. A read failure is not fatal: producers lose their dedupe context
	// and may double-card, which is strictly better than skipping the sweep.
	existing, err := s.Store.List(attention.ListFilter{Repo: repo})
	if err != nil {
		s.logf("attention sweep: could not read open requests for %s (producers lose dedupe context): %v", repo, err)
		existing = nil
	}

	in := Input{Repo: repo, Owner: owner, Name: name, Forge: s.Forge, Existing: existing}
	var observed []attention.DecisionRequest
	for _, p := range producers {
		reqs, perr := p.Evaluate(ctx, in)
		if perr != nil {
			if isSweepFatal(perr) {
				// A rate limit or a bad token means no producer can be trusted
				// to have observed anything. Reconciling a partial view would
				// retract live cards, so skip the whole sweep instead.
				res.Skipped = true
				res.SkipReason = fmt.Sprintf("%s: %v", p.Name(), perr)
				res.Evaluated = nil
				s.logf("attention sweep: skipped %s — %s", repo, res.SkipReason)
				return res, nil
			}
			if res.Failed == nil {
				res.Failed = map[string]string{}
			}
			res.Failed[p.Name()] = perr.Error()
			s.logf("attention sweep: producer %q failed on %s (its cards left untouched): %v", p.Name(), repo, perr)
			continue
		}
		for i := range reqs {
			reqs[i].Producer = p.Name()
		}
		observed = append(observed, reqs...)
		res.Evaluated = append(res.Evaluated, p.Name())
	}

	reconciled, err := s.Store.ReconcileStanding(attention.StandingSweep{
		Repo:      repo,
		Producers: res.Evaluated,
		Observed:  observed,
	})
	if err != nil {
		return res, err
	}
	res.Reconciled = reconciled
	res.SweepID = s.emitTrace(repo, reconciled)
	return res, nil
}

// isSweepFatal reports whether a producer error invalidates the whole sweep
// rather than just that producer's observation. Auth and rate-limit failures
// are repo-wide: no other producer would fare better, and a partial view must
// never drive an auto-resolve.
func isSweepFatal(err error) bool {
	return errors.Is(err, forge.ErrRateLimited) ||
		errors.Is(err, forge.ErrUnauthorized) ||
		errors.Is(err, forge.ErrPermissionDenied) ||
		errors.Is(err, context.DeadlineExceeded)
}

// emitTrace writes the sweep's decision-request legs into an ADR-013 trace
// keyed by a synthetic sweep id, so an auto-resolution is replayable in the
// Lifecycle Explorer exactly like a human resolution inside a run. Refreshes
// and suppressions are omitted: nothing changed, so there is nothing to
// replay. Returns the sweep id, or "" when nothing was written.
func (s *Sweeper) emitTrace(repo string, res attention.StandingResult) string {
	if s.WorkspaceRoot == "" || !res.Changed() {
		return ""
	}
	uid, err := runstate.NewRunID()
	if err != nil {
		return ""
	}
	sweepID := "sweep_" + uid
	w := trace.NewWriter(s.WorkspaceRoot, sweepID, repo, 0)
	if w == nil {
		return ""
	}
	for _, o := range res.Outcomes {
		switch o.Action {
		case attention.ActionCreated, attention.ActionUpdated, attention.ActionAutoResolved:
		default:
			continue
		}
		payload := trace.DecisionRequestPayload{
			ID:         o.ID,
			Producer:   o.Producer,
			Transition: o.Action,
		}
		if o.Action == attention.ActionAutoResolved {
			payload.Note = attention.ReasonConditionCleared
		}
		w.Emit(trace.KindDecisionRequest, "attention-sweep", payload)
	}
	return sweepID
}

// splitRepo validates and splits an "owner/name" spec.
func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("sweep: repo must be owner/name, got %q", repo)
	}
	return parts[0], parts[1], nil
}
