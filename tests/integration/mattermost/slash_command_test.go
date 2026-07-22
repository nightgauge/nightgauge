//go:build integration

package mattermost_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/notifications/inbound"
)

// envFixtureToken is the env-var name the synthetic config points the
// fixture channel's signing token at. The inbound TokenStore resolves
// channel tokens through config env-refs (see config.ChannelToken in
// internal/config), so the test threads the fixture-captured
// outgoing-webhook token through the process env the same way
// production config would — no production-code change to expose a
// direct setter is needed.
const envFixtureToken = "MATTERMOST_E2E_FIXTURE_TOKEN"

// captureDispatcher is the integration-package spy implementation of
// inbound.CommandDispatcher. The production captureDispatcher in
// internal/notifications/inbound is unexported, so the integration suite
// carries its own minimal copy.
type captureDispatcher struct {
	mu   sync.Mutex
	cmds []inbound.MattermostCommand
}

func (c *captureDispatcher) Dispatch(_ context.Context, cmd inbound.MattermostCommand) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmds = append(c.cmds, cmd)
	return nil
}

func (c *captureDispatcher) Commands() []inbound.MattermostCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]inbound.MattermostCommand, len(c.cmds))
	copy(out, c.cmds)
	return out
}

// startReceiverForTest builds an inbound handler whose TokenStore is
// loaded with the fixture channel's outgoing-webhook token, wraps it in
// an httptest.Server bound to a random loopback port, and returns the
// receiver URL. t.Cleanup tears the server down.
func startReceiverForTest(t *testing.T, disp inbound.CommandDispatcher) string {
	t.Helper()

	t.Setenv(envFixtureToken, fixtures.OutgoingWebhookToken)
	cfg := &config.Config{
		Notifiers: &config.NotifiersConfig{
			Mattermost: &config.MattermostNotifierConfig{
				Channels: map[string]*config.ChannelToken{
					fixtures.ChannelName: {TokenEnv: envFixtureToken},
				},
			},
		},
	}
	store := inbound.NewTokenStore()
	if err := store.Reload(cfg); err != nil {
		t.Fatalf("token store reload: %v", err)
	}

	handler := inbound.NewHandler("/mattermost", store, disp, time.Now)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL + "/mattermost"
}

// validTriggerID builds a Mattermost trigger_id whose unix-millisecond
// suffix is the current time, so the receiver's replay-window check (see
// internal/notifications/inbound/verify.go) accepts it. Format:
// "<request_id>.<unix_ms>".
func validTriggerID() string {
	return "ci-test." + strconv.FormatInt(time.Now().UnixMilli(), 10)
}

// TestMattermost_SlashCommand_Signed_DispatchesStatus POSTs a slash
// command form carrying the real fixture token to the in-process
// receiver and asserts the command is verified, dispatched, and
// acknowledged with a 200 + JSON body.
func TestMattermost_SlashCommand_Signed_DispatchesStatus(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	spy := &captureDispatcher{}
	receiverURL := startReceiverForTest(t, spy)

	form := url.Values{
		"token":        {fixtures.OutgoingWebhookToken},
		"team_id":      {fixtures.TeamID},
		"channel_id":   {fixtures.ChannelID},
		"channel_name": {fixtures.ChannelName},
		"user_id":      {"ci-test-user"},
		"user_name":    {"ci-test"},
		"command":      {"/nightgauge"},
		"text":         {"status"},
		"trigger_id":   {validTriggerID()},
	}
	resp, err := http.PostForm(receiverURL, form)
	if err != nil {
		t.Fatalf("post slash command: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", resp.StatusCode, body)
	}
	var ack map[string]any
	if err := json.Unmarshal(body, &ack); err != nil {
		t.Errorf("response body is not valid JSON: %v (body=%q)", err, body)
	}

	cmds := spy.Commands()
	if len(cmds) != 1 {
		t.Fatalf("dispatcher saw %d commands, want 1", len(cmds))
	}
	if cmds[0].Text != "status" {
		t.Errorf("dispatched text = %q, want %q", cmds[0].Text, "status")
	}
	if cmds[0].ChannelName != fixtures.ChannelName {
		t.Errorf("dispatched channel = %q, want %q", cmds[0].ChannelName, fixtures.ChannelName)
	}
}

// TestMattermost_SlashCommand_Unsigned_Returns401 confirms the receiver
// rejects a request carrying the wrong token with a 401 and never
// reaches the dispatcher.
func TestMattermost_SlashCommand_Unsigned_Returns401(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	spy := &captureDispatcher{}
	receiverURL := startReceiverForTest(t, spy)

	form := url.Values{
		"token":        {"wrong-token-the-receiver-must-reject"},
		"team_id":      {fixtures.TeamID},
		"channel_id":   {fixtures.ChannelID},
		"channel_name": {fixtures.ChannelName},
		"user_id":      {"ci-test-user"},
		"user_name":    {"ci-test"},
		"command":      {"/nightgauge"},
		"text":         {"status"},
		"trigger_id":   {validTriggerID()},
	}
	resp, err := http.PostForm(receiverURL, form)
	if err != nil {
		t.Fatalf("post slash command: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if n := len(spy.Commands()); n != 0 {
		t.Errorf("dispatcher saw %d commands on a rejected request, want 0", n)
	}
}
