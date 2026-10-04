package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
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
// the throttle of the workspace a process serves, and neither is applied.
// The daemon reads its own workspace's throttle from the platform's agent
// throttle endpoint instead: every workspace the agent covers, each with its
// own throttle, readable with the license key the daemon registers with. It
// finds its workspace there by slug and team, and never takes the strictest
// of several.

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

// AgentWorkspaceThrottle is one workspace the agent covers, as the agent
// throttle read lists it: the workspace, its team and slug, and its own
// throttle, nil when it has none in force.
type AgentWorkspaceThrottle struct {
	WorkspaceID string
	TeamID      string
	Slug        string
	Throttle    *WorkspaceThrottle
}

// The classes of a refused agent throttle read. A ThrottleReadError unwraps
// to one of them, or to none for a status outside the contract.
var (
	// ErrThrottleUnauthorized: the platform refused the credential (401,
	// UNAUTHORIZED or TOKEN_EXPIRED).
	ErrThrottleUnauthorized = errors.New("the platform refused the credential")
	// ErrThrottleInvalidAgent: the agent id is not one the platform accepts
	// (422 VALIDATION_ERROR).
	ErrThrottleInvalidAgent = errors.New("the platform refused the agent id")
	// ErrPlatformUnavailable: the platform could not verify the credential
	// in time (503 AUTH_VERIFY_TIMEOUT or SERVICE_UNAVAILABLE); RetryAfter
	// says when to ask again.
	ErrPlatformUnavailable = errors.New("the platform is unavailable")
)

// ThrottleReadError is an agent throttle read the platform answered with an
// error status. A 404 unwraps to ErrAgentNotFound: the agent is not this
// account's, or no longer exists, and the next registration replaces it.
type ThrottleReadError struct {
	Status int
	// Code is the platform's error code, when its body carried one.
	Code string
	// RetryAfter is the 503's Retry-After, zero when absent.
	RetryAfter time.Duration
	detail     string
	class      error
}

func (e *ThrottleReadError) Error() string {
	if e.class != nil {
		return fmt.Sprintf("agent workspace throttles: %v (%s)", e.class, e.detail)
	}
	return "agent workspace throttles: " + e.detail
}

func (e *ThrottleReadError) Unwrap() error { return e.class }

// ReadAgentWorkspaceThrottles reads the throttle of every workspace agentID
// covers from the platform's agent throttle endpoint, with the client's
// pipeline credential (the session token when one is installed, else the
// license key). The list is empty when the agent covers no workspace. A
// refusal is a *ThrottleReadError, and a malformed answer an error, so a
// failure is never taken for "no throttle".
func (c *Client) ReadAgentWorkspaceThrottles(ctx context.Context, agentID string) ([]AgentWorkspaceThrottle, error) {
	req, err := c.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsListWorkspaceThrottles,
		PathArgs: []string{agentID},
		Headers:  map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return nil, fmt.Errorf("agent workspace throttles: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent workspace throttles: GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, throttleReadError(resp)
	}
	var body struct {
		Workspaces []struct {
			WorkspaceID *string         `json:"workspace_id"`
			TeamID      *string         `json:"team_id"`
			Slug        *string         `json:"slug"`
			Throttle    json.RawMessage `json:"throttle"`
		} `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("agent workspace throttles: decode: %w", err)
	}
	if body.Workspaces == nil {
		return nil, errors.New("agent workspace throttles: the answer lists no workspaces")
	}
	out := make([]AgentWorkspaceThrottle, 0, len(body.Workspaces))
	for _, w := range body.Workspaces {
		if w.WorkspaceID == nil || w.TeamID == nil || w.Slug == nil {
			return nil, errors.New("agent workspace throttles: a workspace lacks its id, team or slug")
		}
		throttle, err := parseWorkspaceThrottle(w.Throttle)
		if err != nil {
			return nil, fmt.Errorf("agent workspace throttles: workspace %q: %w", printableField(*w.Slug), err)
		}
		out = append(out, AgentWorkspaceThrottle{
			WorkspaceID: *w.WorkspaceID, TeamID: *w.TeamID, Slug: *w.Slug, Throttle: throttle,
		})
	}
	return out, nil
}

// throttleReadError classifies a refused agent throttle read. The body is
// consumed.
func throttleReadError(resp *http.Response) *ThrottleReadError {
	e := &ThrottleReadError{Status: resp.StatusCode}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorResponseBodyLimit))
	var parsed platformErrorBody
	if json.Unmarshal(raw, &parsed) == nil {
		e.Code = parsed.Error.Code
	}
	e.detail = fmt.Sprintf("the platform returned %d", resp.StatusCode)
	if e.Code != "" {
		e.detail += " " + printableField(e.Code)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		e.class = ErrThrottleUnauthorized
	case http.StatusNotFound:
		e.class = ErrAgentNotFound
	case http.StatusUnprocessableEntity:
		e.class = ErrThrottleInvalidAgent
	case http.StatusServiceUnavailable:
		e.class = ErrPlatformUnavailable
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return e
}

// parseRetryAfter reads a Retry-After value, delay-seconds or an HTTP date,
// as a wait from now; zero when absent, malformed or past.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// ErrWorkspaceAmbiguous is MatchWorkspaceThrottle's answer when the served
// workspace's team is not known and several of the agent's workspaces, on
// different teams, have its slug.
var ErrWorkspaceAmbiguous = errors.New("several of the agent's workspaces have this slug, on different teams, and the served workspace's team is not known")

// MatchWorkspaceThrottle finds the served workspace in the agent throttle
// read by its slug and team, and returns its own throttle, nil for none. A
// workspace the read does not list has none: the agent covers it no longer,
// or never did. Each workspace carries its own throttle and the strictest of
// several is never taken: with teamID empty (not known) one workspace with
// the slug is the served one, and several are ErrWorkspaceAmbiguous.
func MatchWorkspaceThrottle(list []AgentWorkspaceThrottle, slug, teamID string) (*WorkspaceThrottle, error) {
	var found []AgentWorkspaceThrottle
	for _, w := range list {
		if w.Slug == slug && (teamID == "" || w.TeamID == teamID) {
			found = append(found, w)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0].Throttle, nil
	}
	teams := make([]string, 0, len(found))
	for _, w := range found {
		teams = append(teams, printableField(w.TeamID))
	}
	return nil, fmt.Errorf("%w (workspace %q on teams %s)", ErrWorkspaceAmbiguous, printableField(slug), strings.Join(teams, ", "))
}

// ReadDefaultTeam reads the signed-in user's default team from the platform's
// team list (GET /v1/teams): the team a registration that names none writes
// its workspace to, which is how the core registers. ok is false when the
// user is on no team. The list answers only a signed-in user, so it is sent
// with the session token alone, and refused with ErrNoSession without one.
func (c *Client) ReadDefaultTeam(ctx context.Context) (teamID string, ok bool, err error) {
	req, err := c.newRequest(ctx, requestSpec{
		Op:          api.OpTeamsListMine,
		Headers:     map[string]string{"Accept": "application/json"},
		SessionOnly: true,
	})
	if err != nil {
		return "", false, fmt.Errorf("default team: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("default team: GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false, fmt.Errorf("default team: %s", describeErrorResponse(resp))
	}
	var body struct {
		Teams []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"is_default"`
		} `json:"teams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("default team: decode: %w", err)
	}
	if body.Teams == nil {
		return "", false, errors.New("default team: the answer lists no teams")
	}
	for _, t := range body.Teams {
		if t.IsDefault && t.ID != "" {
			return t.ID, true, nil
		}
	}
	return "", false, nil
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

// workspaceThrottleReadTimeout bounds one read. Reads run one at a time, so
// a read that never answered would hold back every later one: throttle
// commands, stream reconnects and registrations.
const workspaceThrottleReadTimeout = 30 * time.Second

// WorkspaceThrottleFollower keeps a dispatch throttle on the platform throttle
// of the workspace a process serves (#2352). Refresh reads it whenever it may
// have changed: after a registration, on a `throttle` command, when the
// command stream connects, and when the session changes. RetryIfStale, called
// on the heartbeat, reads again after a read failed, no sooner than the
// platform's Retry-After, so a failure is retried at the heartbeat's cadence
// and nothing else polls.
//
// Reads run one at a time, and a Refresh asked for while one is in flight
// runs again after it, so the value applied last always comes from a read
// that started after the last change was signalled. Each read is bounded by
// workspaceThrottleReadTimeout. A read that fails changes nothing and is
// logged: nothing is capped until a read succeeds, and a throttle read
// before is kept until its resumeAt or the next read that succeeds. Until the
// agent is registered the throttle is unread.
type WorkspaceThrottleFollower struct {
	read    func(ctx context.Context, agentID string) ([]AgentWorkspaceThrottle, error)
	agentID func() string
	slug    func() (string, bool, error)
	// team reads the served workspace's team; nil when none can be read.
	team   func(ctx context.Context) (string, bool, error)
	target WorkspaceThrottleTarget
	// readTimeout bounds each read; workspaceThrottleReadTimeout but in tests.
	readTimeout time.Duration
	now         func() time.Time

	mu      sync.Mutex
	running bool
	again   bool
	// stale is set when the last read failed; RetryIfStale reads again
	// once now reaches retryAt.
	stale   bool
	retryAt time.Time
	// teamID is the team read last, kept until ForgetTeam; teamRead says
	// whether it was read.
	teamID   string
	teamRead bool
	// lastLog suppresses a failure line identical to the one before.
	lastLog string
}

// WorkspaceThrottleTarget receives what a WorkspaceThrottleFollower learns
// (#2352); orchestrator.DispatchThrottle is one.
type WorkspaceThrottleTarget interface {
	// Set applies a read's throttle, or nil for none, with known true; with
	// known false it lifts the throttle as not followed.
	Set(throttle *WorkspaceThrottle, known bool)
	// MarkUnread records that the throttle is followed but no read has
	// succeeded yet; it changes a known throttle in nothing.
	MarkUnread()
}

// NewWorkspaceThrottleFollower follows the throttle through read, which
// lists the throttles of the workspaces agentID covers. agentID is the
// registered agent ("" before the first registration); slug names the served
// workspace (false when the workspace config names none, which has no
// throttle; an error when the config cannot be read, which changes nothing);
// target receives each result.
func NewWorkspaceThrottleFollower(
	read func(ctx context.Context, agentID string) ([]AgentWorkspaceThrottle, error),
	agentID func() string,
	slug func() (string, bool, error),
	target WorkspaceThrottleTarget,
) *WorkspaceThrottleFollower {
	return &WorkspaceThrottleFollower{
		read: read, agentID: agentID, slug: slug, target: target,
		readTimeout: workspaceThrottleReadTimeout,
		now:         time.Now,
	}
}

// WithTeam sets how the served workspace's team is read: its id, ok false
// when it cannot be known (no signed-in session), or an error. The team is
// read once and kept until ForgetTeam. Without one, or while it is not known,
// the workspace is matched by slug alone, which is ambiguous only when
// several of the agent's workspaces on different teams share the slug.
func (f *WorkspaceThrottleFollower) WithTeam(team func(ctx context.Context) (string, bool, error)) *WorkspaceThrottleFollower {
	f.team = team
	return f
}

// ForgetTeam drops the team read before, so the next read asks again: call
// it when the session changes.
func (f *WorkspaceThrottleFollower) ForgetTeam() {
	f.mu.Lock()
	f.teamID, f.teamRead = "", false
	f.mu.Unlock()
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

// RetryIfStale refreshes when the last read failed and the platform's
// Retry-After, if it gave one, has passed. The daemon calls it on every
// heartbeat.
func (f *WorkspaceThrottleFollower) RetryIfStale(ctx context.Context) {
	f.mu.Lock()
	due := f.stale && !f.now().Before(f.retryAt)
	f.mu.Unlock()
	if due {
		f.Refresh(ctx)
	}
}

func (f *WorkspaceThrottleFollower) readAndApply(ctx context.Context) {
	f.target.MarkUnread()
	agentID := f.agentID()
	if agentID == "" {
		// Not registered yet: the registration refreshes.
		return
	}
	slug, ok, err := f.slug()
	if err != nil {
		f.failed(true, 0, "could not resolve the served workspace (keeping the last throttle): %v", err)
		return
	}
	if !ok {
		f.succeeded()
		f.target.Set(nil, true)
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, f.readTimeout)
	defer cancel()
	list, err := f.read(readCtx, agentID)
	if err != nil {
		var refused *ThrottleReadError
		var wait time.Duration
		if errors.As(err, &refused) {
			wait = refused.RetryAfter
		}
		f.failed(true, wait, "could not read the throttle of workspace %q, so dispatch is not held to a new one (keeping the last throttle, if any): %v", slug, err)
		return
	}
	throttle, err := MatchWorkspaceThrottle(list, slug, f.servedTeam(readCtx))
	if err != nil {
		// Reading again tells nothing new until the session, which
		// brings the team, changes: not retried on the heartbeat.
		f.failed(false, 0, "could not tell which workspace is served (keeping the last throttle, if any): %v", err)
		return
	}
	f.succeeded()
	f.target.Set(throttle, true)
}

// servedTeam is the served workspace's team, read once per session, or ""
// when it cannot be known.
func (f *WorkspaceThrottleFollower) servedTeam(ctx context.Context) string {
	if f.team == nil {
		return ""
	}
	f.mu.Lock()
	if f.teamRead {
		team := f.teamID
		f.mu.Unlock()
		return team
	}
	f.mu.Unlock()
	team, ok, err := f.team(ctx)
	if err != nil {
		log.Printf("[nightgauge] workspace throttle: could not read the served workspace's team, matching by slug alone: %v", err)
		return ""
	}
	if !ok {
		return ""
	}
	f.mu.Lock()
	f.teamID, f.teamRead = team, true
	f.mu.Unlock()
	return team
}

// failed logs a failure unless it repeats the one before and, with retry,
// marks the throttle stale, to be read again on a heartbeat no sooner than
// wait.
func (f *WorkspaceThrottleFollower) failed(retry bool, wait time.Duration, format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	f.mu.Lock()
	f.stale = retry
	f.retryAt = f.now().Add(wait)
	repeat := line == f.lastLog
	f.lastLog = line
	f.mu.Unlock()
	if !repeat {
		log.Printf("[nightgauge] workspace throttle: %s", line)
	}
}

func (f *WorkspaceThrottleFollower) succeeded() {
	f.mu.Lock()
	f.stale = false
	f.lastLog = ""
	f.mu.Unlock()
}
