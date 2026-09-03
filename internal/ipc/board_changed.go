package ipc

// board.changed — the one-point question the extension asks before it spends
// the sixty-four-point answer.
//
// The repo-scoped attention sweep (attention_sweep.go) reads every board in
// the workspace. The extension used to fire it from five places — a timer,
// window focus regained, a tree refresh, activation, and a run terminating —
// with a sixty-second floor between them, so an operator alt-tabbing or
// refreshing the tree could sweep sixty times an hour against an intended
// four. Each sweep is ~64 GraphQL points plus 18 REST calls across six repos
// (the ledger's post-#847 figure).
//
// This verb lets the event-driven triggers ask "has any bound board moved
// since the last sweep?" through the change probe (#847): one point per board,
// memoised for boardcache.ProbeTTL, so a burst of triggers costs one point
// total. The extension sweeps on a yes and re-renders the last sweep's cards on
// a no; the timer alone carries the unconditional cadence.
//
// FAIL OPEN, the same commitment the cache makes: every path that is not a
// confident "nothing moved" answers Changed. A repo whose board cannot be
// probed — no capability (GitLab), a probe error, no forge factory at all —
// makes the whole answer "changed", because the probe may only ever save a
// sweep, never suppress one. The one exception is a repo whose forge client
// does not resolve: the sweep would skip it at zero cost too, so it says
// nothing about whether to sweep.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
)

// BoardChangedRepoResult is one repo's probe outcome.
type BoardChangedRepoResult struct {
	Repo string `json:"repo"`
	// Changed is true when this repo's board moved after Since.
	Changed bool `json:"changed"`
	// UpdatedAt is the board's last-change time as the probe reported it,
	// RFC 3339. Empty when the board was not probed.
	UpdatedAt string `json:"updatedAt,omitempty"`
	// Skipped is true when no forge client resolves for the repo. The sweep
	// skips such a repo at zero cost, so it does not vote on the answer.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
	// Unprobeable is true when the repo resolved but its board could not be
	// probed — no ChangeProbe capability, or the probe errored. Counts as
	// changed.
	Unprobeable bool   `json:"unprobeable,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// BoardChangedResult is the board.changed response.
type BoardChangedResult struct {
	// Changed is the verdict: sweep, or serve the last sweep's cards.
	Changed bool `json:"changed"`
	// Since is the instant the boards were compared against, RFC 3339 —
	// the caller's timestamp, or the daemon's last sweep when none was sent.
	// Empty when there was nothing to compare against, which reads as changed.
	Since string                   `json:"since,omitempty"`
	Repos []BoardChangedRepoResult `json:"repos"`
	// Unavailable reports that the daemon has no forge factory, so no board
	// could be probed. Changed is true alongside it: fail open.
	Unavailable bool `json:"unavailable,omitempty"`
	// Probed / Unprobeable are the counts behind the verdict, so a log line
	// can say "6 boards probed, none moved" instead of just "false".
	Probed      int `json:"probed"`
	Unprobeable int `json:"unprobeable"`
}

// handleBoardChanged probes every requested repo's board and reports whether
// any moved after Since.
func (s *Server) handleBoardChanged(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p BoardChangedParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("board.changed: parse params: %w", err)
		}
	}
	res := BoardChangedResult{Repos: []BoardChangedRepoResult{}}

	if s.forgeClientFn == nil {
		res.Unavailable = true
		res.Changed = true
		return res, nil
	}

	since, err := s.boardChangedSince(p.Since)
	if err != nil {
		return nil, fmt.Errorf("board.changed: %w", err)
	}
	if since.IsZero() {
		// Nothing has ever been swept in this daemon and the caller offered
		// no baseline — or a sweep is running right now and will stamp a new
		// one. Either way there are no "last sweep's cards" to vouch for, so
		// the only honest answer is "go look"; a caller that does so during
		// the in-flight sweep is told Busy at no cost.
		res.Changed = true
		return res, nil
	}
	res.Since = since.UTC().Format(time.RFC3339)

	for _, repo := range normalizeRepoSpecs(p.Repos) {
		r := s.probeOneBoard(ctx, repo, since)
		res.Repos = append(res.Repos, r)
		switch {
		case r.Skipped:
		case r.Unprobeable:
			res.Unprobeable++
			res.Changed = true
		default:
			res.Probed++
			if r.Changed {
				res.Changed = true
			}
		}
	}
	return res, nil
}

// boardChangedSince resolves the comparison instant: the caller's timestamp
// when sent, else this daemon's last sweep. lastSweepAt is guarded by sweepMu,
// which a running sweep holds for its whole duration — so this must not wait
// on it, or a one-point probe would stall behind a sixty-four-point sweep. A
// zero time is returned instead, which the caller reads as "changed".
func (s *Server) boardChangedSince(raw string) (time.Time, error) {
	if raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("since %q is not RFC 3339: %w", raw, err)
		}
		return ts, nil
	}
	if !sweepMu.TryLock() {
		return time.Time{}, nil
	}
	defer sweepMu.Unlock()
	return s.lastSweepAt, nil
}

// probeOneBoard asks one repo's board when it last changed. Never returns an
// error: every outcome is folded into the result, and every outcome that is
// not a confident "unchanged" reads as changed.
func (s *Server) probeOneBoard(ctx context.Context, repo string, since time.Time) BoardChangedRepoResult {
	out := BoardChangedRepoResult{Repo: repo}
	client, err := s.forgeClientFn(repo)
	if err != nil {
		out.Skipped = true
		out.SkipReason = err.Error()
		return out
	}
	board := client.Board()
	cp, ok := board.(boardcache.ChangeProbe)
	if board == nil || !ok {
		out.Unprobeable = true
		out.Reason = "board offers no change probe"
		return out
	}
	updatedAt, err := cp.ProjectUpdatedAt(ctx)
	if err != nil {
		out.Unprobeable = true
		out.Reason = err.Error()
		log.Printf("board.changed: %s could not be probed (treated as changed): %v", repo, err)
		return out
	}
	out.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	out.Changed = updatedAt.After(since)
	return out
}
