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

// signedInClient is an online client holding a user session, as the daemon is
// once the extension pushed its session token.
func signedInClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c := onlineClient(t, baseURL)
	c.SetSessionToken("header.payload.signature")
	return c
}

func workspaceListServer(t *testing.T, status int, body string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/workspaces" {
			t.Errorf("request = %s %s, want GET /v1/workspaces", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer header.payload.signature" {
			t.Errorf("Authorization = %q, want the session token", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The daemon reads its own workspace's throttle by slug from the workspace
// list (#2352), as the extension does: a set throttle, none, and a slug the
// account's teams do not have.
func TestReadWorkspaceThrottle(t *testing.T) {
	const list = `{"workspaces":[` +
		`{"slug":"other","throttle":{"maxConcurrent":0,"resumeAt":null}},` +
		`{"slug":"acme-platform","throttle":{"maxConcurrent":1,"resumeAt":"2026-10-02T13:00:00.000Z"}},` +
		`{"slug":"calm","throttle":null}]}`
	srv, _ := workspaceListServer(t, http.StatusOK, list)
	c := signedInClient(t, srv.URL)

	got, err := c.ReadWorkspaceThrottle(context.Background(), "acme-platform")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	if got == nil || got.MaxConcurrent != 1 || got.ResumeAt == nil || !got.ResumeAt.Equal(want) {
		t.Fatalf("acme-platform throttle = %+v, want 1 run until %s", got, want)
	}
	for _, slug := range []string{"calm", "absent"} {
		got, err := c.ReadWorkspaceThrottle(context.Background(), slug)
		if err != nil || got != nil {
			t.Errorf("%s: throttle = %+v, err = %v; want none", slug, got, err)
		}
	}
}

// A list that cannot be read is an error, never "no throttle".
func TestReadWorkspaceThrottle_FailuresAreErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"server error":       {http.StatusInternalServerError, `{}`},
		"not a list":         {http.StatusOK, `{}`},
		"not json":           {http.StatusOK, `<html>`},
		"negative cap":       {http.StatusOK, `{"workspaces":[{"slug":"w","throttle":{"maxConcurrent":-1}}]}`},
		"missing cap":        {http.StatusOK, `{"workspaces":[{"slug":"w","throttle":{"resumeAt":null}}]}`},
		"malformed resumeAt": {http.StatusOK, `{"workspaces":[{"slug":"w","throttle":{"maxConcurrent":1,"resumeAt":"soon"}}]}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := workspaceListServer(t, tc.status, tc.body)
			got, err := signedInClient(t, srv.URL).ReadWorkspaceThrottle(context.Background(), "w")
			if err == nil {
				t.Fatalf("throttle = %+v, err = nil; want an error", got)
			}
		})
	}
}

// The workspace list answers only a signed-in user: a client holding only a
// license key is refused before anything is sent, so the caller can tell
// "cannot read" from "no throttle".
func TestReadWorkspaceThrottle_NeedsASession(t *testing.T) {
	srv, hits := workspaceListServer(t, http.StatusOK, `{"workspaces":[]}`)
	c, err := NewClient(Config{BaseURL: srv.URL, LicenseKey: conformanceLicenseKey, AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	if _, err := c.ReadWorkspaceThrottle(context.Background(), "w"); !errors.Is(err, ErrCredentialInsufficient) {
		t.Fatalf("err = %v, want ErrCredentialInsufficient", err)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("a license-key request reached the platform")
	}
}

type throttleApplied struct {
	throttle *WorkspaceThrottle
	known    bool
}

// followerFor builds a follower over fakes and records what it applies.
func followerFor(read func(context.Context, string) (*WorkspaceThrottle, error), slug func() (string, bool, error), session func() bool) (*WorkspaceThrottleFollower, func() []throttleApplied) {
	var mu sync.Mutex
	var applied []throttleApplied
	f := NewWorkspaceThrottleFollower(read, slug, session, func(t *WorkspaceThrottle, known bool) {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, throttleApplied{t, known})
	})
	return f, func() []throttleApplied {
		mu.Lock()
		defer mu.Unlock()
		return append([]throttleApplied(nil), applied...)
	}
}

// The follower applies the served workspace's throttle while a session
// exists, lifts it (unknown) without one, applies none for a workspace config
// that names no workspace, and changes nothing when a read fails.
func TestWorkspaceThrottleFollower(t *testing.T) {
	ctx := context.Background()
	one := &WorkspaceThrottle{MaxConcurrent: 1}
	session := true
	slugOK, slugErr := true, error(nil)
	readErr := error(nil)
	var reads []string
	f, applied := followerFor(
		func(_ context.Context, slug string) (*WorkspaceThrottle, error) {
			reads = append(reads, slug)
			return one, readErr
		},
		func() (string, bool, error) { return "acme-platform", slugOK, slugErr },
		func() bool { return session },
	)

	f.Refresh(ctx)
	if got := applied(); len(got) != 1 || got[0].throttle != one || !got[0].known {
		t.Fatalf("applied = %+v, want the workspace's throttle, known", got)
	}
	if len(reads) != 1 || reads[0] != "acme-platform" {
		t.Fatalf("reads = %v, want one read by the served slug", reads)
	}

	readErr = errors.New("the platform returned 503")
	f.Refresh(ctx)
	if got := applied(); len(got) != 1 {
		t.Fatalf("a failed read applied something: %+v", got)
	}
	readErr = nil

	slugErr = errors.New("manifest unreadable")
	f.Refresh(ctx)
	if got := applied(); len(got) != 1 {
		t.Fatalf("an unresolvable workspace applied something: %+v", got)
	}
	slugErr = nil

	slugOK = false
	f.Refresh(ctx)
	if got := applied(); len(got) != 2 || got[1].throttle != nil || !got[1].known {
		t.Fatalf("a config naming no workspace: applied = %+v, want none, known", got)
	}

	session = false
	f.Refresh(ctx)
	if got := applied(); len(got) != 3 || got[2].throttle != nil || got[2].known {
		t.Fatalf("no session: applied = %+v, want unknown", got)
	}
	if len(reads) != 2 {
		t.Fatalf("reads = %v: only a session with a named workspace reads", reads)
	}
}

// A refresh asked for while a read is in flight reads once more after it, so
// the throttle applied last comes from a read that started after the change.
func TestWorkspaceThrottleFollower_ReadsAgainAfterAReadInFlight(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var reads int32
	f, applied := followerFor(
		func(context.Context, string) (*WorkspaceThrottle, error) {
			n := atomic.AddInt32(&reads, 1)
			started <- struct{}{}
			if n == 1 {
				<-release
				return &WorkspaceThrottle{MaxConcurrent: 1}, nil
			}
			return &WorkspaceThrottle{MaxConcurrent: 2}, nil
		},
		func() (string, bool, error) { return "w", true, nil },
		func() bool { return true },
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
	got := applied()
	if len(got) != 2 || got[1].throttle.MaxConcurrent != 2 {
		t.Fatalf("applied = %+v, want the newer read last", got)
	}
}
