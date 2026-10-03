package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
)

func workspaceThrottleResult(t *testing.T, s *Server) PlatformWorkspaceThrottleResult {
	t.Helper()
	raw, err := s.methods["platform.workspaceThrottle"](context.Background(), nil)
	if err != nil {
		t.Fatalf("platform.workspaceThrottle: %v", err)
	}
	encoded, _ := json.Marshal(raw)
	var result PlatformWorkspaceThrottleResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

// The daemon reports the workspace throttle it follows (#2352) to a headless
// scheduler on the socket: unknown until it follows one, then the throttle in
// force or none.
func TestPlatformWorkspaceThrottle_ReportsWhatTheDaemonFollows(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	if got := workspaceThrottleResult(t, s); got.Known || got.Throttle != nil {
		t.Fatalf("no throttle followed: %+v, want unknown", got)
	}

	d := orchestrator.NewDispatchThrottle()
	s.SetDispatchThrottle(d)
	if got := workspaceThrottleResult(t, s); got.Known || got.Unread {
		t.Fatalf("followed nothing yet: %+v, want unknown, not unread", got)
	}

	// A session exists, but no read has succeeded yet: unread, which tells a
	// headless scheduler to keep the throttle it learned before.
	d.MarkUnread()
	if got := workspaceThrottleResult(t, s); got.Known || !got.Unread || got.Throttle != nil {
		t.Fatalf("followed, not read yet: %+v, want unread", got)
	}

	resumeAt := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	d.Set(&platform.WorkspaceThrottle{MaxConcurrent: 1, ResumeAt: &resumeAt}, true)
	got := workspaceThrottleResult(t, s)
	if !got.Known || got.Throttle == nil || got.Throttle.MaxConcurrent != 1 || !got.Throttle.ResumeAt.Equal(resumeAt) {
		t.Fatalf("throttled: %+v, want 1 run until %s", got, resumeAt)
	}

	d.Set(nil, true)
	if got := workspaceThrottleResult(t, s); !got.Known || got.Unread || got.Throttle != nil {
		t.Fatalf("cleared: %+v, want known, none", got)
	}

	d.Set(nil, false)
	if got := workspaceThrottleResult(t, s); got.Known || got.Unread {
		t.Fatalf("no session: %+v, want unknown, not unread", got)
	}
}

// Installing or clearing the session tells the daemon to read the throttle
// again (#2352).
func TestSetSessionToken_NotifiesTheThrottleFollower(t *testing.T) {
	s := NewServer(nil, WithPlatformClient(newTestPlatformClientFor(t, "http://127.0.0.1:1", "test-key")))
	s.writer = &bytes.Buffer{}
	called := make(chan struct{}, 2)
	s.OnSessionToken(func() { called <- struct{}{} })

	raw, _ := json.Marshal(PlatformSetSessionTokenParams{Token: "header.payload.signature"})
	if _, err := s.methods["platform.setSessionToken"](context.Background(), raw); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the session listener was not called")
	}
}
