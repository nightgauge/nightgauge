package sweep

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// SharedReads collapses reads that several producers make of the same
// repository inside ONE sweep pass.
//
// Three producers read a repository's Dependabot alerts — dependabot-alerts
// and dependabot-stale per repository, and dependabot-coverage for every
// repository from the workspace pass — and each used to issue its own read.
// With SharedReads the first caller for a repository reads and every other
// caller in the same pass waits for and shares that answer, including its
// error: "could not look" is shared too, never turned into a second attempt
// that might disagree with the first inside one pass.
//
// It lives for one pass and no longer — create one per sweep. Answers are
// keyed by the board's token identity as well as the repository, so a pass
// that reads through two identities never hands one's answer to the other.
type SharedReads struct {
	mu     sync.Mutex
	alerts map[string]*sharedAlerts
}

type sharedAlerts struct {
	done chan struct{}
	res  *forgetypes.SecurityAlerts
	err  error
}

// NewSharedReads returns an empty per-pass memo.
func NewSharedReads() *SharedReads {
	return &SharedReads{alerts: map[string]*sharedAlerts{}}
}

// Wrap returns c with its Security() answering through this pass's memo. A
// nil memo or client returns c unchanged.
func (s *SharedReads) Wrap(c forge.ForgeClient) forge.ForgeClient {
	if s == nil || c == nil {
		return c
	}
	return &sharedClient{ForgeClient: c, reads: s}
}

type sharedClient struct {
	forge.ForgeClient
	reads *SharedReads
}

// Security keeps the inner client's nil — producers test for it to report
// "no security service" rather than read through a wrapper of nothing.
func (c *sharedClient) Security() forge.SecurityService {
	inner := c.ForgeClient.Security()
	if inner == nil {
		return nil
	}
	identity := ""
	if r, ok := c.ForgeClient.Board().(boardcache.IdentityReporter); ok {
		identity = r.CacheIdentity()
	}
	return &sharedSecurity{inner: inner, reads: c.reads, identity: identity}
}

type sharedSecurity struct {
	inner    forge.SecurityService
	reads    *SharedReads
	identity string
}

func (s *sharedSecurity) ListOpenAlerts(ctx context.Context, owner, repo string) (*forgetypes.SecurityAlerts, error) {
	key := s.identity + "|" + strings.ToLower(owner+"/"+repo)
	for {
		s.reads.mu.Lock()
		call, ok := s.reads.alerts[key]
		if !ok {
			call = &sharedAlerts{done: make(chan struct{})}
			s.reads.alerts[key] = call
		}
		s.reads.mu.Unlock()
		if !ok {
			call.res, call.err = s.inner.ListOpenAlerts(ctx, owner, repo)
			if isContextErr(call.err) {
				// The leader ran out of ITS time (each repo has its own
				// deadline). That says nothing about the repository, so it is
				// not the pass's answer: forget it, and let each waiter read
				// again under its own deadline.
				s.reads.mu.Lock()
				delete(s.reads.alerts, key)
				s.reads.mu.Unlock()
			}
			close(call.done)
			return call.res, call.err
		}
		select {
		case <-call.done:
			if isContextErr(call.err) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				continue
			}
			return call.res, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
