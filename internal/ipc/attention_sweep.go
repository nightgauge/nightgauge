package ipc

// attention.sweep / attention.mute / attention.unmute — the repo-scoped half of
// the Action Center surface contract (issues #89 / #92 / #93).
//
// The sweep itself is deterministic Go (internal/attention/sweep); this file is
// only the binding that lets the extension ask for one. It exists rather than
// having the extension shell out to `nightgauge attention sweep` because the
// daemon holds the SAME *attention.Store instance the `attention.event` push is
// subscribed to: reconciling in-process means a card raised or auto-resolved by
// a sweep reaches every surface through the existing push, with no second event
// emitter and no polling. A separate CLI process would write the same files and
// nobody would be told.
//
// NOT A DAEMON (sweep.go's design commitment): there is no timer here. The
// extension decides when to sweep — activation, view refresh, its configured
// interval, and after a run terminates — and this handler makes each of those
// calls cheap and idempotent.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/forge"
)

// AttentionSweepRepoResult is one repo's outcome. Every failure mode is a field
// rather than an error: a sweep must never be fatal to whatever invoked it, and
// a surface that triggers one on activation cannot be allowed to surface a
// modal because a token expired.
type AttentionSweepRepoResult struct {
	Repo string `json:"repo"`
	// Skipped is true when the sweep declined to reconcile at all (auth or
	// rate-limit failure). No cards were created, updated, or retracted.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
	// Error is set when the repo could not be swept at all — an unresolvable
	// forge client, or a malformed repo spec.
	Error string `json:"error,omitempty"`
	// Evaluated lists the producers that observed this repo successfully.
	Evaluated []string `json:"evaluated,omitempty"`
	// Failed maps a producer name to the error that stopped it. Its cards were
	// deliberately left untouched.
	Failed       map[string]string `json:"failed,omitempty"`
	Created      int               `json:"created"`
	Updated      int               `json:"updated"`
	Refreshed    int               `json:"refreshed"`
	Suppressed   int               `json:"suppressed"`
	AutoResolved int               `json:"autoResolved"`
}

// AttentionSweepResult is the attention.sweep response.
type AttentionSweepResult struct {
	Repos []AttentionSweepRepoResult `json:"repos"`
	// Created / Updated / AutoResolved are the totals across every repo, so a
	// caller can decide whether anything changed without walking the list.
	Created      int `json:"created"`
	Updated      int `json:"updated"`
	AutoResolved int `json:"autoResolved"`
	// Unavailable reports that nothing was evaluated because the daemon has no
	// attention store or no forge factory attached. A workspace configured
	// without them is a legitimate state, not a failure — the caller shows
	// nothing rather than an error.
	Unavailable bool `json:"unavailable,omitempty"`
	// Busy reports that another sweep was already in flight and this call was
	// declined. Sweeps are idempotent, so the honest answer to a redundant
	// request is "the one already running covers you".
	Busy bool `json:"busy,omitempty"`
	// Throttled reports that a sweep completed within SweepMinGap and this call
	// was declined without issuing any forge traffic (#848).
	//
	// Deliberately NOT folded into Busy: Busy means "something is running right
	// now", and reporting a finished-moments-ago sweep as in-flight would make
	// a caller wait for a completion event that is never coming.
	Throttled bool `json:"throttled,omitempty"`
	// ThrottledForMs is how long until a sweep would be accepted again. Lets a
	// caller schedule rather than poll.
	ThrottledForMs int64 `json:"throttledForMs,omitempty"`
	// Reason echoes the trigger label the caller sent.
	Reason string `json:"reason,omitempty"`
	// SweptRepos is how many repos this sweep covered (#260). Surfaces so a
	// caller can say "no cards across N repos" instead of just "no cards" —
	// an empty Action Center is only reassuring if you know what was looked at.
	SweptRepos int `json:"sweptRepos"`
	// WorkspaceEvaluated / WorkspaceFailed report the workspace-scoped
	// producers, which run once per sweep rather than per repo.
	WorkspaceEvaluated []string          `json:"workspaceEvaluated,omitempty"`
	WorkspaceFailed    map[string]string `json:"workspaceFailed,omitempty"`
}

// sweepMu serialises sweeps within one daemon. Four independent triggers can
// fire inside the same second (a run terminating during activation, say); the
// second caller is told Busy rather than made to duplicate the first one's
// forge traffic.
var sweepMu sync.Mutex

// SweepMinGap is the shortest interval between two daemon sweeps (#848).
//
// A mutex is not a gap. sweepMu declines a sweep that overlaps one already
// running, which is why back-to-back triggers still produced two FULL sweeps
// as soon as the first had finished:
//
//	2026/08/23 12:58:36  sweep
//	2026/08/23 12:58:50  sweep     <- 14s later
//	2026/08/23 21:57:48  sweep
//	2026/08/23 21:58:07  sweep     <- 19s later
//
// Both are well inside the extension's own SWEEP_MIN_GAP_MS of 60s. That guard
// lives in AttentionSweepService and therefore only covers the triggers that
// pass through it; the daemon is where the timer, activation, view-refresh,
// run-terminated and manual paths all converge, so it is the only place a gap
// can bind on all of them.
//
// Matched to the extension's 60s deliberately: the extension-side guard already
// throttles every trigger it owns to this interval, so enforcing the same value
// here changes nothing for those paths and catches only the ones that slip
// underneath it.
const SweepMinGap = 60 * time.Second

// sweepNow is time.Now behind a variable so a test can cross the gap without
// sleeping through it.
var sweepNow = time.Now

// sweepJitterRand supplies the uniform variate for the per-daemon gap jitter;
// a test pins it (0.5 → exactly SweepMinGap).
var sweepJitterRand = rand.Float64

// jitteredSweepGap is SweepMinGap ±20%, drawn once per daemon at construction
// so the daemons on one machine do not all become eligible to sweep — and
// re-read every board — in the same second after a shared quota reset.
func jitteredSweepGap() time.Duration {
	return time.Duration(float64(SweepMinGap) * (0.8 + 0.4*sweepJitterRand()))
}

// sweepGap is this daemon's min gap; a Server built as a literal (tests)
// has none and falls back to the unjittered constant.
func (s *Server) sweepGap() time.Duration {
	if s.sweepMinGap > 0 {
		return s.sweepMinGap
	}
	return SweepMinGap
}

// handleAttentionSweep evaluates the registered repo-scoped producers against
// each requested repo and reconciles the results into the shared store.
func (s *Server) handleAttentionSweep(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionSweepParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("attention.sweep: parse params: %w", err)
		}
	}
	res := AttentionSweepResult{Repos: []AttentionSweepRepoResult{}, Reason: p.Reason}

	store := s.attentionStore()
	if store == nil || s.forgeClientFn == nil {
		res.Unavailable = true
		return res, nil
	}
	repos := normalizeRepoSpecs(p.Repos)
	if len(repos) == 0 {
		return res, nil
	}

	if !sweepMu.TryLock() {
		res.Busy = true
		return res, nil
	}
	defer sweepMu.Unlock()

	// The gap is checked INSIDE the lock so two triggers racing for it cannot
	// both read a stale lastSweepAt and both proceed — the exact shape of the
	// 14-seconds-apart pair in #848.
	now := sweepNow()
	if gap := s.sweepGap(); !s.lastSweepAt.IsZero() {
		if elapsed := now.Sub(s.lastSweepAt); elapsed < gap {
			// Not Busy: nothing is running. A sweep finished moments ago and
			// its results already cover this caller, so re-reading every board
			// would spend the most expensive call this product makes to
			// re-derive an answer we hold.
			res.Throttled = true
			res.ThrottledForMs = (gap - elapsed).Milliseconds()
			return res, nil
		}
	}

	for _, repo := range repos {
		res.Repos = append(res.Repos, s.sweepOneRepo(ctx, store, repo))
	}
	for _, r := range res.Repos {
		res.Created += r.Created
		res.Updated += r.Updated
		res.AutoResolved += r.AutoResolved
	}
	res.SweptRepos = len(repos)

	// Workspace-scoped producers run ONCE, after the per-repo loop, and are
	// given the same repo list (#260). They exist to reason about that list as
	// an object — most importantly, about what is missing from it, which no
	// per-repo producer can observe.
	s.sweepWorkspace(ctx, store, repos, &res)

	// Stamped only after a sweep that actually looked. An Unavailable or
	// empty-repo-list call returns earlier and must not start the gap, or a
	// daemon that briefly had no forge factory would suppress the first real
	// sweep after it recovered.
	s.lastSweepAt = sweepNow()
	return res, nil
}

// sweepWorkspace evaluates the workspace-scoped producers. It never fails the
// call: a workspace producer erroring must not invalidate the per-repo results
// already collected.
func (s *Server) sweepWorkspace(ctx context.Context, store *attention.Store, repos []string, res *AttentionSweepResult) {
	// Any repo's client will do — workspace producers use it only for
	// board-level discovery, and every repo in a workspace shares the board.
	// Without one, local-checkout discovery still runs.
	var client forge.ForgeClient
	if len(repos) > 0 && s.forgeClientFn != nil {
		if c, err := s.forgeClientFn(repos[0]); err == nil {
			client = c
		}
	}
	sweeper := &sweep.Sweeper{
		Store:         store,
		Registry:      sweep.Default,
		Forge:         client,
		WorkspaceRoot: s.workspaceRootPath(),
	}
	wres, err := sweeper.SweepWorkspace(ctx, repos)
	if err != nil {
		log.Printf("attention.sweep: workspace sweep could not run: %v", err)
		return
	}
	res.Created += wres.Created
	res.AutoResolved += wres.AutoResolved
	res.WorkspaceEvaluated = wres.Evaluated
	res.WorkspaceFailed = wres.Failed
}

// sweepOneRepo runs the registry against a single repo. It never returns an
// error: everything is folded into the per-repo result.
func (s *Server) sweepOneRepo(ctx context.Context, store *attention.Store, repo string) AttentionSweepRepoResult {
	out := AttentionSweepRepoResult{Repo: repo}
	client, err := s.forgeClientFn(repo)
	if err != nil {
		out.Error = err.Error()
		log.Printf("attention.sweep: no forge client for %s (skipped): %v", repo, err)
		return out
	}
	sweeper := &sweep.Sweeper{
		Store:         store,
		Registry:      sweep.Default,
		Forge:         client,
		WorkspaceRoot: s.workspaceRootPath(),
	}
	result, err := sweeper.Sweep(ctx, repo)
	if err != nil {
		// Sweep only errors for a caller mistake (a malformed repo spec, a
		// producer returning an invalid request). Report it on the repo rather
		// than failing the whole call, so one bad spec cannot hide the others.
		out.Error = err.Error()
		log.Printf("attention.sweep: %s could not be swept: %v", repo, err)
		return out
	}
	out.Skipped = result.Skipped
	out.SkipReason = result.SkipReason
	out.Evaluated = result.Evaluated
	out.Failed = result.Failed
	out.Created = result.Reconciled.Created
	out.Updated = result.Reconciled.Updated
	out.Refreshed = result.Reconciled.Refreshed
	out.Suppressed = result.Reconciled.Suppressed
	out.AutoResolved = result.Reconciled.AutoResolved
	return out
}

// normalizeRepoSpecs trims, drops blanks, and de-duplicates while preserving
// order — the same repo listed twice costs a second round of forge traffic for
// an outcome the first pass already reconciled.
func normalizeRepoSpecs(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, r := range in {
		spec := strings.TrimSpace(r)
		if spec == "" || seen[spec] {
			continue
		}
		seen[spec] = true
		out = append(out, spec)
	}
	return out
}

// handleAttentionMute silences alerting on a request until its condition
// changes. The card stays in the inbox at its severity — muting is not
// resolving, and a surface that hides a muted card is misreporting the state of
// the repo.
func (s *Server) handleAttentionMute(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionMuteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("attention.mute: parse params: %w", err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("attention.mute: id is required")
	}
	store := s.attentionStore()
	if store == nil {
		return nil, fmt.Errorf("attention.mute: attention store not configured")
	}
	req, err := store.Mute(p.ID, p.Actor)
	if err != nil {
		return nil, fmt.Errorf("attention.mute: %w", err)
	}
	return AttentionMuteResult{Ok: true, Muted: req.IsMuted()}, nil
}

// handleAttentionUnmute restores alerting on a muted request.
func (s *Server) handleAttentionUnmute(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p AttentionUnmuteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("attention.unmute: parse params: %w", err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("attention.unmute: id is required")
	}
	store := s.attentionStore()
	if store == nil {
		return nil, fmt.Errorf("attention.unmute: attention store not configured")
	}
	req, err := store.Unmute(p.ID, p.Actor)
	if err != nil {
		return nil, fmt.Errorf("attention.unmute: %w", err)
	}
	return AttentionMuteResult{Ok: true, Muted: req.IsMuted()}, nil
}

// AttentionMuteResult is the response for both mute and unmute. Muted reports
// the resulting state, which is NOT always what was asked for: muting an
// already-terminal request is a no-op the caller should see rather than a
// silent success.
type AttentionMuteResult struct {
	Ok    bool `json:"ok"`
	Muted bool `json:"muted"`
}
