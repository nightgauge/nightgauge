package inbound

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// captureDispatcher records every dispatched command in-memory so tests
// can assert what the handler forwarded after token + replay checks
// passed.
type captureDispatcher struct {
	mu   sync.Mutex
	cmds []MattermostCommand
}

func (c *captureDispatcher) Dispatch(_ context.Context, cmd MattermostCommand) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmds = append(c.cmds, cmd)
	return nil
}

func (c *captureDispatcher) Commands() []MattermostCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]MattermostCommand, len(c.cmds))
	copy(out, c.cmds)
	return out
}

// startServerForTest boots a real *Server bound to 127.0.0.1:0, returns
// the receiver and registers a t.Cleanup for graceful shutdown.
func startServerForTest(t *testing.T, cfg *config.InboundConfig, store *TokenStore, disp CommandDispatcher) *Server {
	t.Helper()

	srv := New(cfg, store, disp)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	started := make(chan struct{})
	go func() {
		// Signal readiness on the next scheduling tick once Start has
		// bound the listener (Start binds synchronously before serving).
		// We poll Addr() rather than using a sentinel channel so the
		// test does not race against the goroutine's scheduling.
		_ = srv.Start(ctx)
	}()

	// Spin until Addr is populated. Bounded so a regression that
	// prevents Start from binding the listener fails fast.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Addr() != "" {
			close(started)
			return srv
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server did not bind listener within deadline")
	return nil
}

// configWithLoopbackPort returns an InboundConfig pinned to 127.0.0.1:0
// (port-zero) so each test gets an independent listener.
func configWithLoopbackPort(path string) *config.InboundConfig {
	return &config.InboundConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    0,
		Path:    path,
	}
}

func mattermostFormBody(token, channel, triggerID string) string {
	form := url.Values{}
	form.Set("token", token)
	form.Set("channel_name", channel)
	form.Set("channel_id", "channel-id-1")
	form.Set("team_id", "team-1")
	form.Set("user_id", "user-1")
	form.Set("user_name", "alice")
	form.Set("command", "/inc")
	form.Set("text", "status")
	form.Set("trigger_word", "/inc")
	form.Set("trigger_id", triggerID)
	return form.Encode()
}

func nowMs() int64 { return time.Now().UnixMilli() }

func TestServer_PostSignedReturns200AndDispatches(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	if err := store.Reload(cfg); err != nil {
		t.Fatalf("token reload: %v", err)
	}
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	body := mattermostFormBody("dev-secret", "dev", fmt.Sprintf("req.%d", nowMs()))
	resp, err := http.Post(
		"http://"+srv.Addr()+srv.Path(),
		"application/x-www-form-urlencoded",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body=%q)", resp.StatusCode, got)
	}

	cmds := disp.Commands()
	if len(cmds) != 1 {
		t.Fatalf("dispatcher saw %d commands, want 1", len(cmds))
	}
	if cmds[0].ChannelName != "dev" || cmds[0].UserName != "alice" || cmds[0].Text != "status" {
		t.Fatalf("dispatched cmd payload mismatch: %+v", cmds[0])
	}
}

func TestServer_PostUnsignedReturns401(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	if err := store.Reload(cfg); err != nil {
		t.Fatalf("token reload: %v", err)
	}
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	body := mattermostFormBody("WRONG-TOKEN", "dev", fmt.Sprintf("req.%d", nowMs()))
	resp, err := http.Post(
		"http://"+srv.Addr()+srv.Path(),
		"application/x-www-form-urlencoded",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := disp.Commands(); len(got) != 0 {
		t.Fatalf("dispatcher saw %d commands on 401, want 0", len(got))
	}
}

func TestServer_PostUnknownChannelReturns401(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	_ = store.Reload(cfg)
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	body := mattermostFormBody("dev-secret", "OTHER-CHANNEL", fmt.Sprintf("req.%d", nowMs()))
	resp, err := http.Post(
		"http://"+srv.Addr()+srv.Path(),
		"application/x-www-form-urlencoded",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for unknown channel", resp.StatusCode)
	}
}

func TestServer_PostStaleTriggerReturns408(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	_ = store.Reload(cfg)
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	// 10 minutes ago — outside the 5-minute window.
	staleMs := time.Now().Add(-10 * time.Minute).UnixMilli()
	body := mattermostFormBody("dev-secret", "dev", fmt.Sprintf("req.%d", staleMs))
	resp, err := http.Post(
		"http://"+srv.Addr()+srv.Path(),
		"application/x-www-form-urlencoded",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408", resp.StatusCode)
	}
}

func TestServer_PostMissingTimestampReturns408(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	_ = store.Reload(cfg)
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	body := mattermostFormBody("dev-secret", "dev", "no-timestamp-here")
	resp, err := http.Post(
		"http://"+srv.Addr()+srv.Path(),
		"application/x-www-form-urlencoded",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408 for missing timestamp", resp.StatusCode)
	}
}

func TestServer_HealthzReturns200(t *testing.T) {
	store := NewTokenStore()
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	resp, err := http.Get("http://" + srv.Addr() + srv.Path() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

func TestServer_WrongContentTypeReturns415(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	_ = store.Reload(cfg)
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	resp, err := http.Post(
		"http://"+srv.Addr()+srv.Path(),
		"application/json",
		strings.NewReader(`{"token":"dev-secret"}`),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
}

// TestHandler_HealthzNonLoopbackReturns404 exercises the defense-in-depth
// non-loopback rejection on the health endpoint without spinning up a real
// listener — httptest.NewRecorder lets us synthesize a non-loopback
// RemoteAddr the live server can not produce.
func TestHandler_HealthzNonLoopbackReturns404(t *testing.T) {
	store := NewTokenStore()
	disp := &captureDispatcher{}
	h := NewHandler("/mattermost", store, disp, time.Now)

	req := httptest.NewRequest(http.MethodGet, "/mattermost/healthz", nil)
	req.RemoteAddr = "8.8.8.8:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for non-loopback healthz probe", rec.Code)
	}
}

// TestServer_XRequestTimestampOverridesTriggerID confirms the header takes
// priority over a stale trigger_id, which lets reverse proxies pin the
// timestamp when they normalize the request.
func TestServer_XRequestTimestampOverridesTriggerID(t *testing.T) {
	t.Setenv("TEST_DEV_TOKEN", "dev-secret")
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					"dev": {TokenEnv: "TEST_DEV_TOKEN"},
				},
			},
		},
	}
	store := NewTokenStore()
	_ = store.Reload(cfg)
	disp := &captureDispatcher{}
	srv := startServerForTest(t, configWithLoopbackPort("/mattermost"), store, disp)

	staleTrigger := fmt.Sprintf("req.%d", time.Now().Add(-10*time.Minute).UnixMilli())
	body := mattermostFormBody("dev-secret", "dev", staleTrigger)

	req, err := http.NewRequest(http.MethodPost,
		"http://"+srv.Addr()+srv.Path(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Request-Timestamp", fmt.Sprintf("%d", nowMs()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (header should override stale trigger)", resp.StatusCode)
	}
}
