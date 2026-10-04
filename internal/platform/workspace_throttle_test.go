package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const throttleAgentID = "6f1c2a9e-0000-4000-8000-000000002352"

// licensedClient is an online client holding only a license key, as a daemon
// no extension is attached to is.
func licensedClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: baseURL, LicenseKey: conformanceLicenseKey, AgentID: "machine-1"})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return c
}

// agentThrottlesServer answers the agent throttle read with status, body and
// headers, and checks the request.
func agentThrottlesServer(t *testing.T, status int, body string, header map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agents/"+throttleAgentID+"/throttles" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s?%s, want GET /v1/agents/%s/throttles", r.Method, r.URL.Path, r.URL.RawQuery, throttleAgentID)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+conformanceLicenseKey {
			t.Errorf("Authorization = %q, want the license key", got)
		}
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The license key reads every workspace the agent covers, each with its own
// throttle (#2352): a set one with its resumeAt, and none.
func TestReadAgentWorkspaceThrottles(t *testing.T) {
	const body = `{"workspaces":[` +
		`{"workspace_id":"w-1","team_id":"team-a","slug":"backend","throttle":{"maxConcurrent":1,"resumeAt":"2026-10-05T00:00:00.000Z"}},` +
		`{"workspace_id":"w-2","team_id":"team-a","slug":"frontend","throttle":null}]}`
	srv := agentThrottlesServer(t, http.StatusOK, body, nil)
	got, err := licensedClient(t, srv.URL).ReadAgentWorkspaceThrottles(context.Background(), throttleAgentID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resume := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if len(got) != 2 {
		t.Fatalf("workspaces = %+v, want two", got)
	}
	if w := got[0]; w.WorkspaceID != "w-1" || w.TeamID != "team-a" || w.Slug != "backend" ||
		w.Throttle == nil || w.Throttle.MaxConcurrent != 1 || w.Throttle.ResumeAt == nil || !w.Throttle.ResumeAt.Equal(resume) {
		t.Errorf("backend = %+v, want 1 run until %s", w, resume)
	}
	if w := got[1]; w.Slug != "frontend" || w.Throttle != nil {
		t.Errorf("frontend = %+v, want no throttle", w)
	}

	empty := agentThrottlesServer(t, http.StatusOK, `{"workspaces":[]}`, nil)
	got, err = licensedClient(t, empty.URL).ReadAgentWorkspaceThrottles(context.Background(), throttleAgentID)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("an agent covering no workspace: %+v, %v; want an empty list", got, err)
	}
}

// An answer that cannot be read is an error, never "no throttle".
func TestReadAgentWorkspaceThrottles_MalformedIsAnError(t *testing.T) {
	cases := map[string]string{
		"not json":           `<html>`,
		"no list":            `{}`,
		"null list":          `{"workspaces":null}`,
		"no team":            `{"workspaces":[{"workspace_id":"w","slug":"s","throttle":null}]}`,
		"no slug":            `{"workspaces":[{"workspace_id":"w","team_id":"t","throttle":null}]}`,
		"negative cap":       `{"workspaces":[{"workspace_id":"w","team_id":"t","slug":"s","throttle":{"maxConcurrent":-1,"resumeAt":null}}]}`,
		"missing cap":        `{"workspaces":[{"workspace_id":"w","team_id":"t","slug":"s","throttle":{"resumeAt":null}}]}`,
		"malformed resumeAt": `{"workspaces":[{"workspace_id":"w","team_id":"t","slug":"s","throttle":{"maxConcurrent":1,"resumeAt":"soon"}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := agentThrottlesServer(t, http.StatusOK, body, nil)
			got, err := licensedClient(t, srv.URL).ReadAgentWorkspaceThrottles(context.Background(), throttleAgentID)
			if err == nil {
				t.Fatalf("workspaces = %+v, err = nil; want an error", got)
			}
		})
	}
}

// Each refusal the endpoint declares is classified, and a 503 carries its
// Retry-After.
func TestReadAgentWorkspaceThrottles_ErrorClasses(t *testing.T) {
	retryAt := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	cases := []struct {
		name      string
		status    int
		code      string
		header    map[string]string
		class     error
		minWait   time.Duration
		maxWait   time.Duration
		unclassed bool
	}{
		{name: "unauthorized", status: 401, code: "UNAUTHORIZED", class: ErrThrottleUnauthorized},
		{name: "token expired", status: 401, code: "TOKEN_EXPIRED", class: ErrThrottleUnauthorized},
		{name: "another account's agent", status: 404, code: "NOT_FOUND", class: ErrAgentNotFound},
		{name: "not a uuid", status: 422, code: "VALIDATION_ERROR", class: ErrThrottleInvalidAgent},
		{name: "auth verify timeout", status: 503, code: "AUTH_VERIFY_TIMEOUT", header: map[string]string{"Retry-After": "7"},
			class: ErrPlatformUnavailable, minWait: 7 * time.Second, maxWait: 7 * time.Second},
		{name: "service unavailable, http date", status: 503, code: "SERVICE_UNAVAILABLE", header: map[string]string{"Retry-After": retryAt},
			class: ErrPlatformUnavailable, minWait: time.Minute, maxWait: 2 * time.Minute},
		{name: "server error", status: 500, code: "INTERNAL", unclassed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"error":{"code":"` + tc.code + `","message":"refused"}}`
			srv := agentThrottlesServer(t, tc.status, body, tc.header)
			_, err := licensedClient(t, srv.URL).ReadAgentWorkspaceThrottles(context.Background(), throttleAgentID)
			var refused *ThrottleReadError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v, want a *ThrottleReadError", err)
			}
			if refused.Status != tc.status || refused.Code != tc.code {
				t.Errorf("status, code = %d, %q; want %d, %q", refused.Status, refused.Code, tc.status, tc.code)
			}
			if tc.unclassed {
				for _, class := range []error{ErrThrottleUnauthorized, ErrAgentNotFound, ErrThrottleInvalidAgent, ErrPlatformUnavailable} {
					if errors.Is(err, class) {
						t.Errorf("err = %v is classed %v", err, class)
					}
				}
				return
			}
			if !errors.Is(err, tc.class) {
				t.Errorf("err = %v, want %v", err, tc.class)
			}
			if refused.RetryAfter < tc.minWait || refused.RetryAfter > tc.maxWait {
				t.Errorf("RetryAfter = %s, want between %s and %s", refused.RetryAfter, tc.minWait, tc.maxWait)
			}
		})
	}
}

// The served workspace is found by slug and team; each workspace's own
// throttle applies and the strictest of several never does (#2352).
func TestMatchWorkspaceThrottle(t *testing.T) {
	one := &WorkspaceThrottle{MaxConcurrent: 1}
	zero := &WorkspaceThrottle{MaxConcurrent: 0}
	list := []AgentWorkspaceThrottle{
		{WorkspaceID: "w-1", TeamID: "team-a", Slug: "backend", Throttle: one},
		{WorkspaceID: "w-2", TeamID: "team-b", Slug: "backend", Throttle: zero},
		{WorkspaceID: "w-3", TeamID: "team-a", Slug: "frontend", Throttle: nil},
		{WorkspaceID: "w-4", TeamID: "team-b", Slug: "ops", Throttle: zero},
	}
	cases := []struct {
		name, slug, team string
		want             *WorkspaceThrottle
		ambiguous        bool
	}{
		{name: "slug and team: the team's own, not the strictest", slug: "backend", team: "team-a", want: one},
		{name: "slug and the other team", slug: "backend", team: "team-b", want: zero},
		{name: "unthrottled workspace", slug: "frontend", team: "team-a", want: nil},
		{name: "slug on another team only", slug: "ops", team: "team-a", want: nil},
		{name: "slug not listed", slug: "absent", team: "team-a", want: nil},
		{name: "team unknown, one workspace with the slug", slug: "ops", team: "", want: zero},
		{name: "team unknown, several teams share the slug", slug: "backend", team: "", ambiguous: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchWorkspaceThrottle(list, tc.slug, tc.team)
			if tc.ambiguous {
				if !errors.Is(err, ErrWorkspaceAmbiguous) || got != nil {
					t.Fatalf("got %+v, %v; want ErrWorkspaceAmbiguous", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// A throttle stops capping at its resumeAt; one without stays until cleared.
func TestWorkspaceThrottle_InForce(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Second)
	if !(WorkspaceThrottle{MaxConcurrent: 1, ResumeAt: &later}).InForce(now) {
		t.Error("a throttle before its resumeAt is not in force")
	}
	if (WorkspaceThrottle{MaxConcurrent: 1, ResumeAt: &earlier}).InForce(now) {
		t.Error("a throttle past its resumeAt is in force")
	}
	if !(WorkspaceThrottle{MaxConcurrent: 0}).InForce(now) {
		t.Error("a throttle with no resumeAt is not in force")
	}
}

// The default team comes from the signed-in user's team list, and the list
// is never asked with the license key.
func TestReadDefaultTeam(t *testing.T) {
	var hits int32
	body := `{"teams":[{"id":"team-a","name":"A","role":"owner","is_default":true},{"id":"team-b","name":"B","role":"viewer","is_default":false}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/teams" {
			t.Errorf("request = %s %s, want GET /v1/teams", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+conformanceSessionJWT {
			t.Errorf("Authorization = %q, want the session", got)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := licensedClient(t, srv.URL)
	if _, _, err := c.ReadDefaultTeam(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("license key only: err = %v, want ErrNoSession", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("the team list was asked with the license key")
	}
	c.SetSessionToken(conformanceSessionJWT)
	team, ok, err := c.ReadDefaultTeam(context.Background())
	if err != nil || !ok || team != "team-a" {
		t.Fatalf("default team = %q, %v, %v; want team-a", team, ok, err)
	}
	body = `{"teams":[]}`
	if team, ok, err := c.ReadDefaultTeam(context.Background()); err != nil || ok || team != "" {
		t.Fatalf("on no team: %q, %v, %v; want none", team, ok, err)
	}
}

type throttleApplied struct {
	throttle *WorkspaceThrottle
	known    bool
}

// throttleRecorder is a follower's target that records what it is told.
type throttleRecorder struct {
	mu      sync.Mutex
	applied []throttleApplied
	unread  int
}

func (r *throttleRecorder) Set(t *WorkspaceThrottle, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, throttleApplied{t, known})
}

func (r *throttleRecorder) MarkUnread() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unread++
}

func (r *throttleRecorder) sets() []throttleApplied {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]throttleApplied(nil), r.applied...)
}

func (r *throttleRecorder) unreads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unread
}

func listOf(slug, team string, t *WorkspaceThrottle) []AgentWorkspaceThrottle {
	return []AgentWorkspaceThrottle{{WorkspaceID: "w-" + slug, TeamID: team, Slug: slug, Throttle: t}}
}

func throttleAgentRegistered() string { return throttleAgentID }

func servesBackend() (string, bool, error) { return "backend", true, nil }

// Until the agent is throttleAgentRegistered nothing is read and the throttle is unread;
// once it is, the served workspace's throttle is applied, known.
func TestWorkspaceThrottleFollower_UnreadUntilRegistered(t *testing.T) {
	agentID := ""
	var reads []string
	rec := &throttleRecorder{}
	f := NewWorkspaceThrottleFollower(
		func(_ context.Context, id string) ([]AgentWorkspaceThrottle, error) {
			reads = append(reads, id)
			return listOf("backend", "team-a", &WorkspaceThrottle{MaxConcurrent: 1}), nil
		},
		func() string { return agentID },
		servesBackend,
		rec,
	)
	f.Refresh(context.Background())
	if len(reads) != 0 || len(rec.sets()) != 0 || rec.unreads() != 1 {
		t.Fatalf("unregistered: reads %v, sets %+v, unread %d; want nothing read, unread", reads, rec.sets(), rec.unreads())
	}
	agentID = throttleAgentID
	f.Refresh(context.Background())
	if got := rec.sets(); len(reads) != 1 || reads[0] != throttleAgentID || len(got) != 1 || !got[0].known || got[0].throttle.MaxConcurrent != 1 {
		t.Fatalf("registered: reads %v, sets %+v; want one read by the agent id, the throttle applied", reads, got)
	}
}

// The follower applies a set throttle, a clear, and none for a config naming
// no workspace; a read that fails, or a workspace it cannot resolve, changes
// nothing.
func TestWorkspaceThrottleFollower(t *testing.T) {
	ctx := context.Background()
	var list []AgentWorkspaceThrottle
	readErr := error(nil)
	slugOK, slugErr := true, error(nil)
	rec := &throttleRecorder{}
	f := NewWorkspaceThrottleFollower(
		func(context.Context, string) ([]AgentWorkspaceThrottle, error) { return list, readErr },
		throttleAgentRegistered,
		func() (string, bool, error) { return "backend", slugOK, slugErr },
		rec,
	)

	list = listOf("backend", "team-a", &WorkspaceThrottle{MaxConcurrent: 0})
	f.Refresh(ctx)
	if got := rec.sets(); len(got) != 1 || !got[0].known || got[0].throttle.MaxConcurrent != 0 {
		t.Fatalf("set: %+v, want maxConcurrent 0, known", got)
	}
	list = listOf("backend", "team-a", nil)
	f.Refresh(ctx)
	if got := rec.sets(); len(got) != 2 || !got[1].known || got[1].throttle != nil {
		t.Fatalf("cleared: %+v, want none, known", got)
	}

	readErr = &ThrottleReadError{Status: 503, class: ErrPlatformUnavailable}
	f.Refresh(ctx)
	readErr = nil
	slugErr = errors.New("manifest unreadable")
	f.Refresh(ctx)
	slugErr = nil
	if got := rec.sets(); len(got) != 2 {
		t.Fatalf("a failed read or an unresolvable workspace applied something: %+v", got)
	}

	slugOK = false
	f.Refresh(ctx)
	if got := rec.sets(); len(got) != 3 || !got[2].known || got[2].throttle != nil {
		t.Fatalf("a config naming no workspace: %+v, want none, known", got)
	}
}

// With the team known, the follower applies its own team's workspace, never
// another team's of the same slug nor the strictest; the team is read once
// per session, and asked again after ForgetTeam.
func TestWorkspaceThrottleFollower_MatchesByTeam(t *testing.T) {
	ctx := context.Background()
	list := []AgentWorkspaceThrottle{
		{WorkspaceID: "w-1", TeamID: "team-a", Slug: "backend", Throttle: &WorkspaceThrottle{MaxConcurrent: 2}},
		{WorkspaceID: "w-2", TeamID: "team-b", Slug: "backend", Throttle: &WorkspaceThrottle{MaxConcurrent: 0}},
	}
	team, teamOK := "team-a", false
	var teamReads, reads int
	rec := &throttleRecorder{}
	f := NewWorkspaceThrottleFollower(
		func(context.Context, string) ([]AgentWorkspaceThrottle, error) { reads++; return list, nil },
		throttleAgentRegistered, servesBackend, rec,
	).WithTeam(func(context.Context) (string, bool, error) {
		teamReads++
		return team, teamOK, nil
	})

	// No session: the team is unknown, and two teams share the slug.
	f.Refresh(ctx)
	if got := rec.sets(); len(got) != 0 {
		t.Fatalf("ambiguous: applied %+v, want nothing", got)
	}
	f.RetryIfStale(ctx)
	if reads != 1 {
		t.Fatalf("reads = %d: an ambiguous match was retried on the heartbeat", reads)
	}
	teamOK = true
	f.Refresh(ctx)
	f.Refresh(ctx)
	if got := rec.sets(); len(got) != 2 || got[0].throttle.MaxConcurrent != 2 || got[1].throttle.MaxConcurrent != 2 {
		t.Fatalf("team-a: applied %+v, want team-a's own cap of 2 twice", got)
	}
	if teamReads != 2 {
		t.Fatalf("team reads = %d, want 2: unknown once, then read once and kept", teamReads)
	}
	team = "team-b"
	f.ForgetTeam()
	f.Refresh(ctx)
	if got := rec.sets(); len(got) != 3 || got[2].throttle.MaxConcurrent != 0 || teamReads != 3 {
		t.Fatalf("after ForgetTeam: applied %+v, team reads %d; want team-b's cap of 0, read again", got, teamReads)
	}
}

// A failed read is retried on the heartbeat, no sooner than the platform's
// Retry-After, and a read that succeeded is not repeated.
func TestWorkspaceThrottleFollower_RetryIfStale(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var reads int
	readErr := error(&ThrottleReadError{Status: 503, class: ErrPlatformUnavailable, RetryAfter: time.Minute})
	rec := &throttleRecorder{}
	f := NewWorkspaceThrottleFollower(
		func(context.Context, string) ([]AgentWorkspaceThrottle, error) {
			reads++
			return listOf("backend", "team-a", &WorkspaceThrottle{MaxConcurrent: 1}), readErr
		},
		throttleAgentRegistered, servesBackend, rec,
	)
	f.now = func() time.Time { return now }

	f.RetryIfStale(ctx)
	if reads != 0 {
		t.Fatalf("a heartbeat with nothing stale read %d time(s)", reads)
	}
	f.Refresh(ctx)
	now = now.Add(30 * time.Second)
	f.RetryIfStale(ctx)
	if reads != 1 {
		t.Fatalf("reads = %d: a heartbeat before Retry-After read again", reads)
	}
	now = now.Add(30 * time.Second)
	readErr = nil
	f.RetryIfStale(ctx)
	if got := rec.sets(); reads != 2 || len(got) != 1 || got[0].throttle.MaxConcurrent != 1 {
		t.Fatalf("after Retry-After: reads %d, applied %+v; want the retry applied", reads, got)
	}
	f.RetryIfStale(ctx)
	if reads != 2 {
		t.Fatalf("reads = %d: a heartbeat after a successful read read again", reads)
	}
}

// A refresh asked for while a read is in flight reads once more after it, so
// the throttle applied last comes from a read that started after the change.
func TestWorkspaceThrottleFollower_ReadsAgainAfterAReadInFlight(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var reads int32
	rec := &throttleRecorder{}
	f := NewWorkspaceThrottleFollower(
		func(context.Context, string) ([]AgentWorkspaceThrottle, error) {
			n := atomic.AddInt32(&reads, 1)
			started <- struct{}{}
			if n == 1 {
				<-release
				return listOf("backend", "t", &WorkspaceThrottle{MaxConcurrent: 1}), nil
			}
			return listOf("backend", "t", &WorkspaceThrottle{MaxConcurrent: 2}), nil
		},
		throttleAgentRegistered, servesBackend, rec,
	)
	done := make(chan struct{})
	go func() {
		f.Refresh(context.Background())
		close(done)
	}()
	<-started
	f.Refresh(context.Background()) // returns at once; the reader reads again
	f.Refresh(context.Background()) // coalesces with the one above
	close(release)
	<-done

	if n := atomic.LoadInt32(&reads); n != 2 {
		t.Fatalf("reads = %d, want 2: the in-flight one and one more", n)
	}
	got := rec.sets()
	if len(got) != 2 || got[1].throttle.MaxConcurrent != 2 {
		t.Fatalf("applied = %+v, want the newer read last", got)
	}
}

// A read that never answers is cut off at its bound (#2352), so it cannot
// hold back the refreshes after it: the next one reads and applies.
func TestWorkspaceThrottleFollower_BoundsEachRead(t *testing.T) {
	var reads int32
	rec := &throttleRecorder{}
	f := NewWorkspaceThrottleFollower(
		func(ctx context.Context, _ string) ([]AgentWorkspaceThrottle, error) {
			if atomic.AddInt32(&reads, 1) == 1 {
				<-ctx.Done() // the platform never answers
				return nil, ctx.Err()
			}
			return listOf("backend", "t", &WorkspaceThrottle{MaxConcurrent: 1}), nil
		},
		throttleAgentRegistered, servesBackend, rec,
	)
	f.readTimeout = time.Millisecond

	f.Refresh(context.Background())
	if got := rec.sets(); len(got) != 0 {
		t.Fatalf("a read cut off at its bound applied %+v", got)
	}
	f.Refresh(context.Background())
	if got := rec.sets(); len(got) != 1 || got[0].throttle.MaxConcurrent != 1 {
		t.Fatalf("applied = %+v, want the next read's throttle", got)
	}
	if NewWorkspaceThrottleFollower(nil, nil, nil, &throttleRecorder{}).readTimeout != workspaceThrottleReadTimeout {
		t.Fatal("a follower is built without the read bound")
	}
}

// End to end over HTTP with only a license key: the follower reads the agent
// throttle endpoint and applies its own workspace's throttle.
func TestWorkspaceThrottleFollower_LicenseKeyOnly(t *testing.T) {
	srv := agentThrottlesServer(t, http.StatusOK,
		`{"workspaces":[{"workspace_id":"w-1","team_id":"team-a","slug":"backend","throttle":{"maxConcurrent":1,"resumeAt":null}},`+
			`{"workspace_id":"w-2","team_id":"team-a","slug":"other","throttle":{"maxConcurrent":0,"resumeAt":null}}]}`, nil)
	c := licensedClient(t, srv.URL)
	rec := &throttleRecorder{}
	NewWorkspaceThrottleFollower(c.ReadAgentWorkspaceThrottles, throttleAgentRegistered, servesBackend, rec).
		WithTeam(func(ctx context.Context) (string, bool, error) {
			if !c.HasSessionToken() {
				return "", false, nil
			}
			return c.ReadDefaultTeam(ctx)
		}).
		Refresh(context.Background())
	if got := rec.sets(); len(got) != 1 || !got[0].known || got[0].throttle == nil || got[0].throttle.MaxConcurrent != 1 {
		t.Fatalf("applied = %+v, want backend's own cap of 1", got)
	}
}
