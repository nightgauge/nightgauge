package ipc

// Tests for board.changed — the probe the extension's event-driven sweep
// triggers consult before spending a full sweep.
//
// The property under test is the fail-open contract: the verb may only ever
// SAVE a sweep, never suppress one it cannot vouch against. So every path that
// is not a confident "nothing moved" must answer changed.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
)

// probeFakeBoard is a BoardService whose only modelled capability is the
// change probe. Any other call panics through the nil embedded interface —
// the verb must not read the board, only ask when it moved.
type probeFakeBoard struct {
	forge.BoardService
	updatedAt time.Time
	err       error
	calls     int
}

func (b *probeFakeBoard) ProjectUpdatedAt(context.Context) (time.Time, error) {
	b.calls++
	return b.updatedAt, b.err
}

// probeFakeForge is the sweep fixture with a probe-capable board attached.
type probeFakeForge struct {
	*sweepFakeForge
	board forge.BoardService
}

func (f *probeFakeForge) Board() forge.BoardService { return f.board }

func probeServer(t *testing.T, fn func(repo string) (forge.ForgeClient, error)) *Server {
	t.Helper()
	s := newAttentionTestServer(t)
	s.forgeClientFn = fn
	return s
}

func runBoardChanged(t *testing.T, s *Server, p BoardChangedParams) BoardChangedResult {
	t.Helper()
	raw, _ := json.Marshal(p)
	out, err := s.handleBoardChanged(context.Background(), raw)
	if err != nil {
		t.Fatalf("board.changed: %v", err)
	}
	res, ok := out.(BoardChangedResult)
	if !ok {
		t.Fatalf("board.changed returned %T", out)
	}
	return res
}

var (
	probeSince = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	probeRepos = []string{"octocat/acme-web", "octocat/acme-api"}
)

func TestBoardChangedAnswersUnchangedWhenNoBoardMovedSince(t *testing.T) {
	board := &probeFakeBoard{updatedAt: probeSince.Add(-time.Minute)}
	s := probeServer(t, func(string) (forge.ForgeClient, error) {
		return &probeFakeForge{sweepFakeForge: redMain(), board: board}, nil
	})

	res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos, Since: probeSince.Format(time.RFC3339)})

	if res.Changed {
		t.Fatalf("a board that last moved before Since must read unchanged: %+v", res)
	}
	if res.Probed != 2 || res.Unprobeable != 0 {
		t.Fatalf("want 2 probed / 0 unprobeable, got %d / %d", res.Probed, res.Unprobeable)
	}
	if board.calls != 2 {
		t.Fatalf("one probe per repo, got %d", board.calls)
	}
}

func TestBoardChangedAnswersChangedWhenAnyBoardMovedSince(t *testing.T) {
	stale := &probeFakeBoard{updatedAt: probeSince.Add(-time.Hour)}
	moved := &probeFakeBoard{updatedAt: probeSince.Add(time.Second)}
	s := probeServer(t, func(repo string) (forge.ForgeClient, error) {
		b := forge.BoardService(stale)
		if repo == "octocat/acme-api" {
			b = moved
		}
		return &probeFakeForge{sweepFakeForge: redMain(), board: b}, nil
	})

	res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos, Since: probeSince.Format(time.RFC3339)})

	if !res.Changed {
		t.Fatalf("one moved board must flip the verdict: %+v", res)
	}
	if res.Repos[0].Changed || !res.Repos[1].Changed {
		t.Fatalf("per-repo verdicts must name the board that moved: %+v", res.Repos)
	}
}

// Fail open: a board that cannot vouch for itself is a board that changed.
func TestBoardChangedFailsOpen(t *testing.T) {
	t.Run("probe error", func(t *testing.T) {
		board := &probeFakeBoard{err: errors.New("rate limited")}
		s := probeServer(t, func(string) (forge.ForgeClient, error) {
			return &probeFakeForge{sweepFakeForge: redMain(), board: board}, nil
		})
		res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos[:1], Since: probeSince.Format(time.RFC3339)})
		if !res.Changed || res.Unprobeable != 1 {
			t.Fatalf("a probe that errors must read as changed: %+v", res)
		}
	})

	t.Run("no probe capability", func(t *testing.T) {
		// The sweep fixture's Board() is nil: an adapter with no board probe
		// at all, which is what a GitLab client looks like here.
		s := probeServer(t, func(string) (forge.ForgeClient, error) { return redMain(), nil })
		res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos[:1], Since: probeSince.Format(time.RFC3339)})
		if !res.Changed || res.Unprobeable != 1 {
			t.Fatalf("an adapter with no probe must read as changed: %+v", res)
		}
	})

	t.Run("cached board with no probe underneath", func(t *testing.T) {
		// The production shape: the sweep factory wraps every client in the
		// board cache, so the verb sees a cachedBoard whether or not the
		// adapter underneath can answer. ErrNoChangeProbe must fail open too.
		cache := boardcache.New(0)
		s := probeServer(t, func(string) (forge.ForgeClient, error) {
			return boardcache.WrapClient(cache, &probeFakeForge{sweepFakeForge: redMain(), board: &noProbeBoard{}}, "octocat", 1), nil
		})
		res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos[:1], Since: probeSince.Format(time.RFC3339)})
		if !res.Changed || res.Unprobeable != 1 {
			t.Fatalf("a cached board over a probe-less adapter must read as changed: %+v", res)
		}
	})

	t.Run("no forge factory", func(t *testing.T) {
		s := newAttentionTestServer(t)
		s.forgeClientFn = nil
		res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos, Since: probeSince.Format(time.RFC3339)})
		if !res.Changed || !res.Unavailable {
			t.Fatalf("no factory must read as changed and unavailable: %+v", res)
		}
	})

	t.Run("never swept and no baseline", func(t *testing.T) {
		board := &probeFakeBoard{updatedAt: probeSince.Add(-time.Hour)}
		s := probeServer(t, func(string) (forge.ForgeClient, error) {
			return &probeFakeForge{sweepFakeForge: redMain(), board: board}, nil
		})
		res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos})
		if !res.Changed {
			t.Fatalf("with no Since and no prior sweep there are no cards to serve: %+v", res)
		}
		if board.calls != 0 {
			t.Fatal("no baseline means no comparison — the probe must not be spent")
		}
	})
}

// noProbeBoard is a BoardService with no ChangeProbe, for the cached-wrapper
// case above.
type noProbeBoard struct{ forge.BoardService }

// An unresolvable repo is the sweep's zero-cost skip, not a change: it must
// neither flip the verdict nor hide the other repos' answers.
func TestBoardChangedSkipsAnUnresolvableRepoWithoutVoting(t *testing.T) {
	board := &probeFakeBoard{updatedAt: probeSince.Add(-time.Minute)}
	s := probeServer(t, func(repo string) (forge.ForgeClient, error) {
		if repo == "octocat/unmapped" {
			return nil, errors.New("no project board resolves for octocat/unmapped")
		}
		return &probeFakeForge{sweepFakeForge: redMain(), board: board}, nil
	})

	res := runBoardChanged(t, s, BoardChangedParams{
		Repos: []string{"octocat/unmapped", "octocat/acme-web"},
		Since: probeSince.Format(time.RFC3339),
	})

	if res.Changed {
		t.Fatalf("a skipped repo must not read as changed: %+v", res)
	}
	if !res.Repos[0].Skipped || res.Repos[1].Skipped {
		t.Fatalf("skip must be reported on the unresolvable repo only: %+v", res.Repos)
	}
	if res.Probed != 1 {
		t.Fatalf("the resolvable repo must still be probed, got %d", res.Probed)
	}
}

// With no Since, the daemon's own last sweep is the baseline — the same
// instant attention.sweep stamps, so the two verbs agree on what "since the
// last sweep" means.
func TestBoardChangedDefaultsToTheDaemonsLastSweep(t *testing.T) {
	board := &probeFakeBoard{updatedAt: probeSince.Add(-time.Minute)}
	s := probeServer(t, func(string) (forge.ForgeClient, error) {
		return &probeFakeForge{sweepFakeForge: redMain(), board: board}, nil
	})
	s.lastSweepAt = probeSince

	res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos[:1]})
	if res.Changed {
		t.Fatalf("board older than the daemon's last sweep must read unchanged: %+v", res)
	}
	if res.Since != probeSince.Format(time.RFC3339) {
		t.Fatalf("Since must echo the daemon's last sweep, got %q", res.Since)
	}

	board.updatedAt = probeSince.Add(time.Second)
	if res := runBoardChanged(t, s, BoardChangedParams{Repos: probeRepos[:1]}); !res.Changed {
		t.Fatalf("board newer than the daemon's last sweep must read changed: %+v", res)
	}
}

func TestBoardChangedRejectsAMalformedSince(t *testing.T) {
	s := probeServer(t, func(string) (forge.ForgeClient, error) { return redMain(), nil })
	raw, _ := json.Marshal(BoardChangedParams{Repos: probeRepos, Since: "yesterday"})
	if _, err := s.handleBoardChanged(context.Background(), raw); err == nil {
		t.Fatal("a Since the daemon cannot parse is a caller bug, not a silent 'changed'")
	}
}
