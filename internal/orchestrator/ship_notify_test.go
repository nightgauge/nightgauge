package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/notify"
)

func writeWorkspaceConfig(t *testing.T, yaml string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveShipNotify_Defaults(t *testing.T) {
	t.Setenv(config.DefaultShipNotifyWebhookEnv, "https://discord/wh")
	root := t.TempDir() // no config.yaml

	enabled, webhook, deploy, _ := resolveShipNotify(root)
	if !enabled {
		t.Error("default enabled must be true")
	}
	if webhook != "https://discord/wh" {
		t.Errorf("webhook = %q, want the env value", webhook)
	}
	if deploy != config.DefaultDeployCommand {
		t.Errorf("deploy = %q, want default %q", deploy, config.DefaultDeployCommand)
	}
}

func TestResolveShipNotify_ConfigOverrides(t *testing.T) {
	t.Setenv("MY_SHIP_HOOK", "https://discord/custom")
	root := writeWorkspaceConfig(t, `
owner: test-org
project: 1
ready_to_ship:
  enabled: false
  discord_webhook_env: MY_SHIP_HOOK
  deploy_command: "gh workflow run ship.yml -f target=beta"
`)
	enabled, webhook, deploy, _ := resolveShipNotify(root)
	if enabled {
		t.Error("enabled:false must disable")
	}
	if webhook != "https://discord/custom" {
		t.Errorf("webhook = %q, want the configured env var's value", webhook)
	}
	if deploy != "gh workflow run ship.yml -f target=beta" {
		t.Errorf("deploy = %q, want the configured command", deploy)
	}
}

func TestShipNotifyMessage_CarriesDeployCommand(t *testing.T) {
	e := shipNotifyMessage("o/r", 4067, "gh workflow run deploy-stores.yml -f platforms=all")
	if !strings.Contains(e.Title, "#4067") || !strings.Contains(e.Title, "o/r") {
		t.Errorf("title must name the epic, got %q", e.Title)
	}
	if len(e.Fields) != 1 {
		t.Fatalf("want 1 field (deploy command), got %d", len(e.Fields))
	}
	v := e.Fields[0].Value
	if !strings.Contains(v, "gh workflow run deploy-stores.yml -f platforms=all") {
		t.Errorf("deploy command missing from message: %q", v)
	}
	if !strings.Contains(v, "```") {
		t.Error("deploy command must be in a fenced code block (copy-paste ready)")
	}
	if !strings.Contains(strings.ToLower(e.Description), "automatically") {
		t.Error("description must clarify nothing is auto-submitted")
	}
}

func TestEmitReadyToShipAlert_PostsWhenConfigured(t *testing.T) {
	var hits int32
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv(config.DefaultShipNotifyWebhookEnv, srv.URL)

	s := &Scheduler{workspaceRoot: t.TempDir()}
	s.emitReadyToShipAlert(context.Background(), "o/r", 4067)

	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 webhook POST, got %d", hits)
	}
	var p notify.DiscordPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Embeds) != 1 || !strings.Contains(p.Embeds[0].Fields[0].Value, "deploy-stores.yml") {
		t.Errorf("posted embed missing deploy command: %+v", p)
	}
}

func TestEmitReadyToShipAlert_NoOpWhenNoWebhook(t *testing.T) {
	t.Setenv(config.DefaultShipNotifyWebhookEnv, "") // no webhook
	s := &Scheduler{workspaceRoot: t.TempDir()}
	// Must not panic / must be a no-op (epicNumber 0 and missing webhook).
	s.emitReadyToShipAlert(context.Background(), "o/r", 0)
	s.emitReadyToShipAlert(context.Background(), "o/r", 4067)
}

// TestEmitReadyToShipAlert_FailsClosedOnConfigError locks the #4076 review fix:
// a malformed config.yaml (config.Load errors) must SKIP the notification, not
// fall back to enabled-by-default and fire despite a user's opt-out intent.
func TestEmitReadyToShipAlert_FailsClosedOnConfigError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv(config.DefaultShipNotifyWebhookEnv, srv.URL)
	// Malformed YAML → config.Load returns an error.
	root := writeWorkspaceConfig(t, "owner: test-org\nproject: 1\nready_to_ship: [this is not a map\n")

	s := &Scheduler{workspaceRoot: root}
	s.emitReadyToShipAlert(context.Background(), "o/r", 4067)
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("a config load error must fail closed (no POST), got %d", hits)
	}
}

func TestEmitReadyToShipAlert_DisabledByConfig(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv(config.DefaultShipNotifyWebhookEnv, srv.URL)
	root := writeWorkspaceConfig(t, "owner: test-org\nproject: 1\nready_to_ship:\n  enabled: false\n")

	s := &Scheduler{workspaceRoot: root}
	s.emitReadyToShipAlert(context.Background(), "o/r", 4067)
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("disabled config must suppress the alert, got %d POSTs", hits)
	}
}

// The ready-to-ship alert must build a Slack sink when Slack is configured and
// no Discord webhook is set. Delivery is covered by SlackSink's own tests; this
// asserts the caller resolves credentials from the shared config block (#1089),
// which is the wiring that decides whether the alert can reach Slack at all.
func TestEmitReadyToShipAlert_BuildsSlackSinkFromSharedConfig(t *testing.T) {
	t.Setenv("TEST_SLACK_TOKEN", "xoxb-test")
	enabled := true
	cfg := &config.Config{Notifications: &config.NotificationsConfig{
		Slack: &config.SlackNotificationsConfig{
			Enabled: &enabled, BotTokenEnv: "TEST_SLACK_TOKEN", Channel: "C0123",
		},
	}}
	sink := notify.Sinks("", cfg, nil)
	if sink == nil || sink.Name() != "slack" {
		t.Fatalf("Sinks = %v, want a slack-only sink", sink)
	}
}
