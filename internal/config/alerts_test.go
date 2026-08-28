package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlackAlertWebhookEnv_Default(t *testing.T) {
	if got := (&Config{}).SlackAlertWebhookEnv(); got != DefaultAlertsSlackWebhookEnv {
		t.Errorf("empty config = %q, want %q", got, DefaultAlertsSlackWebhookEnv)
	}
}

// A nil receiver must still resolve — callers reach this on the config-load
// error path, where failing would silently disable the Slack sink.
func TestSlackAlertWebhookEnv_NilReceiver(t *testing.T) {
	var c *Config
	if got := c.SlackAlertWebhookEnv(); got != DefaultAlertsSlackWebhookEnv {
		t.Errorf("nil config = %q, want %q", got, DefaultAlertsSlackWebhookEnv)
	}
}

func TestSlackAlertWebhookEnv_Configured(t *testing.T) {
	c := &Config{Alerts: &AlertsConfig{SlackWebhookEnv: "  MY_HOOK  "}}
	if got := c.SlackAlertWebhookEnv(); got != "MY_HOOK" {
		t.Errorf("= %q, want the trimmed configured name", got)
	}
}

// A blank configured value must fall back rather than resolving os.Getenv("").
func TestSlackAlertWebhookEnv_BlankFallsBack(t *testing.T) {
	c := &Config{Alerts: &AlertsConfig{SlackWebhookEnv: "   "}}
	if got := c.SlackAlertWebhookEnv(); got != DefaultAlertsSlackWebhookEnv {
		t.Errorf("= %q, want the default", got)
	}
}

// The block must survive a real load, not just a hand-built struct — an
// unwired merge path is how a config field silently never takes effect.
func TestLoad_ParsesAlertsBlock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "owner: test-org\nrepo: test-repo\nproject: 1\nalerts:\n  slack_webhook_env: TEAM_SLACK_HOOK\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Alerts == nil {
		t.Fatal("alerts block did not survive the load/merge path")
	}
	if got := cfg.SlackAlertWebhookEnv(); got != "TEAM_SLACK_HOOK" {
		t.Errorf("SlackAlertWebhookEnv = %q, want the configured name", got)
	}
}
