package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// The platform's workspace throttle, as the Go side follows it (#2352).
//
// An owner or admin caps how many runs a workspace executes at once,
// optionally until a time. The platform keeps the throttle on the workspace
// and publishes a `throttle` command to the agent the workspace is linked to.
// That command names no workspace, and the registration response's `throttle`
// is the strictest across every workspace linked to the agent, so neither is
// the throttle of the workspace a process serves. The VS Code extension reads
// its own workspace's throttle, by slug, from the workspace list (#2337); the
// daemon does the same here. The list is behind the platform's user
// authentication, so it is read only while a signed-in session exists.

// WorkspaceThrottle caps a workspace at MaxConcurrent runs at once, until
// ResumeAt, or until it is cleared when ResumeAt is nil.
type WorkspaceThrottle struct {
	MaxConcurrent int        `json:"maxConcurrent"`
	ResumeAt      *time.Time `json:"resumeAt"`
}

// InForce reports whether the throttle still caps dispatch at now.
func (t WorkspaceThrottle) InForce(now time.Time) bool {
	return t.ResumeAt == nil || now.Before(*t.ResumeAt)
}

// ReadWorkspaceThrottle reads the throttle of the workspace with this slug
// from the platform's workspace list (GET /v1/workspaces). It returns nil when
// the workspace has none, or when no workspace of the account's teams has the
// slug. It returns an error when the list cannot be read or the throttle is
// malformed, so a failure is never taken for "no throttle". The route needs a
// signed-in user's session: a client holding only a license key is refused
// with ErrCredentialInsufficient before anything is sent.
//
// The read is not skipped while the client's health poll reports it offline:
// it is asked for when the platform has just answered (a registration, a
// command, a stream that opened), and a read that fails changes nothing.
func (c *Client) ReadWorkspaceThrottle(ctx context.Context, slug string) (*WorkspaceThrottle, error) {
	req, err := c.newRequest(ctx, requestSpec{
		Op:      api.OpWorkspacesList,
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return nil, fmt.Errorf("workspace throttle: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workspace throttle: GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("workspace throttle: the platform returned %d", resp.StatusCode)
	}
	var body struct {
		Workspaces []struct {
			Slug     string          `json:"slug"`
			Throttle json.RawMessage `json:"throttle"`
		} `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("workspace throttle: decode the workspace list: %w", err)
	}
	if body.Workspaces == nil {
		return nil, errors.New("workspace throttle: the workspace list is malformed")
	}
	for _, w := range body.Workspaces {
		if w.Slug == slug {
			return parseWorkspaceThrottle(w.Throttle)
		}
	}
	return nil, nil
}

// parseWorkspaceThrottle reads a workspace's `throttle` value: null or absent
// is none, anything else must be a valid throttle.
func parseWorkspaceThrottle(raw json.RawMessage) (*WorkspaceThrottle, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wire struct {
		MaxConcurrent *int    `json:"maxConcurrent"`
		ResumeAt      *string `json:"resumeAt"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("workspace throttle: malformed throttle: %w", err)
	}
	if wire.MaxConcurrent == nil || *wire.MaxConcurrent < 0 {
		return nil, errors.New("workspace throttle: maxConcurrent must be a non-negative integer")
	}
	t := &WorkspaceThrottle{MaxConcurrent: *wire.MaxConcurrent}
	if wire.ResumeAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *wire.ResumeAt)
		if err != nil {
			return nil, fmt.Errorf("workspace throttle: resumeAt is not an ISO-8601 time: %w", err)
		}
		t.ResumeAt = &at
	}
	return t, nil
}

// WorkspaceThrottleFollower keeps a dispatch throttle on the platform throttle
// of the workspace a process serves (#2352). Refresh reads it whenever it may
// have changed: after a registration, on a `throttle` command, when the
// command stream connects, and when a session arrives.
//
// Reads run one at a time, and a Refresh asked for while one is in flight
// runs again after it, so the value applied last always comes from a read
// that started after the last change was signalled. A read that fails changes
// nothing. The throttle is followed only while a signed-in session exists, as
// the extension follows it: without one the cap is lifted and unknown.
type WorkspaceThrottleFollower struct {
	read    func(ctx context.Context, slug string) (*WorkspaceThrottle, error)
	slug    func() (string, bool, error)
	session func() bool
	apply   func(throttle *WorkspaceThrottle, known bool)

	mu      sync.Mutex
	running bool
	again   bool
}

// NewWorkspaceThrottleFollower follows the throttle through read. slug names
// the served workspace (false when the workspace config names none, which has
// no throttle; an error when the config cannot be read, which changes
// nothing), session reports whether a signed-in session exists, and apply
// receives each result: a throttle or nil, known, or unknown with no session.
func NewWorkspaceThrottleFollower(
	read func(ctx context.Context, slug string) (*WorkspaceThrottle, error),
	slug func() (string, bool, error),
	session func() bool,
	apply func(throttle *WorkspaceThrottle, known bool),
) *WorkspaceThrottleFollower {
	return &WorkspaceThrottleFollower{read: read, slug: slug, session: session, apply: apply}
}

// Refresh reads the throttle and applies it. It returns once a read that
// started after the call has been applied or has failed; a call made while
// another is reading returns at once, and the reading call reads again.
func (f *WorkspaceThrottleFollower) Refresh(ctx context.Context) {
	f.mu.Lock()
	if f.running {
		f.again = true
		f.mu.Unlock()
		return
	}
	f.running = true
	f.mu.Unlock()
	for {
		f.readAndApply(ctx)
		f.mu.Lock()
		if !f.again || ctx.Err() != nil {
			f.running = false
			f.again = false
			f.mu.Unlock()
			return
		}
		f.again = false
		f.mu.Unlock()
	}
}

func (f *WorkspaceThrottleFollower) readAndApply(ctx context.Context) {
	if !f.session() {
		f.apply(nil, false)
		return
	}
	slug, ok, err := f.slug()
	if err != nil {
		log.Printf("[nightgauge] workspace throttle: could not resolve the served workspace (keeping the last throttle): %v", err)
		return
	}
	if !ok {
		f.apply(nil, true)
		return
	}
	throttle, err := f.read(ctx, slug)
	if err != nil {
		log.Printf("[nightgauge] workspace throttle: could not read the throttle of workspace %q (keeping the last one): %v", slug, err)
		return
	}
	f.apply(throttle, true)
}
