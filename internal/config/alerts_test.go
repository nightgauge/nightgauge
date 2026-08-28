package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlackBotTokenEnv_Default(t *testing.T) {
	if got := (&Config{}).SlackBotTokenEnv(); got != DefaultSlackBotTokenEnv {
		t.Errorf("empty config = %q, want %q", got, DefaultSlackBotTokenEnv)
	}
}

// A nil receiver must still resolve — callers reach this on the config-load
// error path, where failing would silently disable the Slack sink.
func TestSlackBotTokenEnv_NilReceiver(t *testing.T) {
	var c *Config
	if got := c.SlackBotTokenEnv(); got != DefaultSlackBotTokenEnv {
		t.Errorf("nil config = %q, want %q", got, DefaultSlackBotTokenEnv)
	}
}

func TestSlackBotTokenEnv_Configured(t *testing.T) {
	c := &Config{Notifications: &NotificationsConfig{
		Slack: &SlackNotificationsConfig{BotTokenEnv: "  MY_TOKEN  "},
	}}
	if got := c.SlackBotTokenEnv(); got != "MY_TOKEN" {
		t.Errorf("= %q, want the trimmed configured name", got)
	}
}

// A blank configured value must fall back rather than resolving os.Getenv("").
func TestSlackBotTokenEnv_BlankFallsBack(t *testing.T) {
	c := &Config{Notifications: &NotificationsConfig{
		Slack: &SlackNotificationsConfig{BotTokenEnv: "   "},
	}}
	if got := c.SlackBotTokenEnv(); got != DefaultSlackBotTokenEnv {
		t.Errorf("= %q, want the default", got)
	}
}

// Absent block or absent flag = disabled: adding this feature must not switch
// Slack on for an existing workspace that never asked for it.
func TestSlackAlertsEnabled_DefaultsOff(t *testing.T) {
	for name, c := range map[string]*Config{
		"nil config":        nil,
		"no notifications":  {},
		"no slack block":    {Notifications: &NotificationsConfig{}},
		"slack, no enabled": {Notifications: &NotificationsConfig{Slack: &SlackNotificationsConfig{Channel: "C1"}}},
		"explicitly false":  {Notifications: &NotificationsConfig{Slack: &SlackNotificationsConfig{Enabled: boolPtr(false)}}},
	} {
		if c.SlackAlertsEnabled() {
			t.Errorf("%s: expected disabled", name)
		}
	}
}

func TestSlackAlertsEnabled_True(t *testing.T) {
	c := &Config{Notifications: &NotificationsConfig{
		Slack: &SlackNotificationsConfig{Enabled: boolPtr(true)},
	}}
	if !c.SlackAlertsEnabled() {
		t.Error("expected enabled")
	}
}

func TestSlackChannel(t *testing.T) {
	c := &Config{Notifications: &NotificationsConfig{
		Slack: &SlackNotificationsConfig{Channel: "  C0123  "},
	}}
	if got := c.SlackChannel(); got != "C0123" {
		t.Errorf("= %q, want the trimmed channel", got)
	}
	if got := (&Config{}).SlackChannel(); got != "" {
		t.Errorf("absent block = %q, want empty", got)
	}
}

// The block must survive a real load, not just a hand-built struct — an
// unwired merge path is how a config field silently never takes effect. This
// is the SAME block the VSCode extension reads, which is the point: one
// credential and one channel for both halves of the integration (#1089).
func TestLoad_ParsesSharedSlackBlock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "owner: test-org\nrepo: test-repo\nproject: 1\n" +
		"notifications:\n  slack:\n    enabled: true\n    bot_token_env: TEAM_SLACK_TOKEN\n    channel: \"C0123456789\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SlackAlertsEnabled() {
		t.Fatal("slack block did not survive the load/merge path")
	}
	if got := cfg.SlackBotTokenEnv(); got != "TEAM_SLACK_TOKEN" {
		t.Errorf("SlackBotTokenEnv = %q", got)
	}
	if got := cfg.SlackChannel(); got != "C0123456789" {
		t.Errorf("SlackChannel = %q", got)
	}
}
