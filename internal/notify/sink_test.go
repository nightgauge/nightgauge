package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// stubSink records what it was asked to deliver and fails on demand.
type stubSink struct {
	name      string
	got       [][]Message
	delivered int
	err       error
	secret    string
}

func (s *stubSink) Post(_ context.Context, msgs []Message) (int, error) {
	s.got = append(s.got, msgs)
	if s.err != nil {
		return s.delivered, s.err
	}
	return len(msgs), nil
}
func (s *stubSink) Redact(str string) string { return RedactURL(str, s.secret) }
func (s *stubSink) Name() string             { return s.name }

func TestMultiSink_DeliversToEverySink(t *testing.T) {
	a := &stubSink{name: "discord"}
	b := &stubSink{name: "slack"}
	msgs := []Message{{Title: "one"}, {Title: "two"}}

	n, err := MultiSink{a, b}.Post(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if n != 2 {
		t.Errorf("delivered = %d, want 2", n)
	}
	for _, s := range []*stubSink{a, b} {
		if len(s.got) != 1 || len(s.got[0]) != 2 {
			t.Errorf("%s received %+v, want one batch of 2", s.name, s.got)
		}
	}
}

// One provider being down must never suppress the other — that is the entire
// reason an operator configures two.
func TestMultiSink_OneFailureDoesNotSuppressTheOther(t *testing.T) {
	down := &stubSink{name: "discord", err: errors.New("503"), secret: "tok"}
	up := &stubSink{name: "slack"}

	n, err := MultiSink{down, up}.Post(context.Background(), []Message{{Title: "x"}})
	if err == nil {
		t.Fatal("expected the failing sink's error to surface")
	}
	if !strings.Contains(err.Error(), "discord") {
		t.Errorf("error should name the failing sink, got %v", err)
	}
	if len(up.got) != 1 {
		t.Error("the healthy sink did not receive the message")
	}
	if n != 1 {
		t.Errorf("delivered = %d, want 1 (the healthy sink took it)", n)
	}
}

// A joined error can carry more than one provider's URL; Redact must scrub
// every one of them, not just the first.
func TestMultiSink_RedactsEverySinkCredential(t *testing.T) {
	m := MultiSink{
		&stubSink{name: "discord", secret: "https://discord/AAA"},
		&stubSink{name: "slack", secret: "https://slack/BBB"},
	}
	got := m.Redact("discord=https://discord/AAA slack=https://slack/BBB")
	if strings.Contains(got, "AAA") || strings.Contains(got, "BBB") {
		t.Fatalf("a credential survived redaction: %s", got)
	}
}

func TestMultiSink_Name(t *testing.T) {
	if got := (MultiSink{&stubSink{name: "discord"}, &stubSink{name: "slack"}}).Name(); got != "discord+slack" {
		t.Errorf("Name() = %q", got)
	}
	if got := (MultiSink{}).Name(); got != "none" {
		t.Errorf("empty Name() = %q, want none", got)
	}
}

// slackCfg builds a config with the shared outbound Slack block.
func slackCfg(enabled bool, tokenEnv, channel string) *config.Config {
	return &config.Config{Notifications: &config.NotificationsConfig{
		Slack: &config.SlackNotificationsConfig{
			Enabled: &enabled, BotTokenEnv: tokenEnv, Channel: channel,
		},
	}}
}

func TestSinks_NilWhenNothingConfigured(t *testing.T) {
	if s := Sinks("", nil, nil); s != nil {
		t.Fatalf("Sinks with no destination = %v, want nil", s)
	}
}

func TestSinks_DiscordOnly(t *testing.T) {
	s := Sinks("https://discord.com/api/webhooks/1/x", nil, nil)
	if s == nil || s.Name() != "discord" {
		t.Fatalf("Sinks = %v, want a discord-only sink", s)
	}
}

func TestSinks_SlackOnly(t *testing.T) {
	t.Setenv("MY_SLACK_TOKEN", fakeBotToken)
	s := Sinks("", slackCfg(true, "MY_SLACK_TOKEN", "C0123"), nil)
	if s == nil || s.Name() != "slack" {
		t.Fatalf("Sinks = %v, want a slack-only sink", s)
	}
}

func TestSinks_BothConfigured(t *testing.T) {
	t.Setenv("MY_SLACK_TOKEN", fakeBotToken)
	s := Sinks("https://discord.com/api/webhooks/1/x", slackCfg(true, "MY_SLACK_TOKEN", "C0123"), nil)
	if s == nil || s.Name() != "discord+slack" {
		t.Fatalf("Sinks = %v, want both sinks", s)
	}
}

// enabled:false must silence Slack even with a valid token and channel —
// adding a sink must not create a path around an explicit opt-out.
func TestSinks_DisabledSilencesSlack(t *testing.T) {
	t.Setenv("MY_SLACK_TOKEN", fakeBotToken)
	if s := Sinks("", slackCfg(false, "MY_SLACK_TOKEN", "C0123"), nil); s != nil {
		t.Fatalf("Sinks = %v, want nil when slack is disabled", s)
	}
}

// A channel is as required as a token: without one there is nowhere to post.
func TestSinks_NoChannelMeansNoSlack(t *testing.T) {
	t.Setenv("MY_SLACK_TOKEN", fakeBotToken)
	if s := Sinks("", slackCfg(true, "MY_SLACK_TOKEN", ""), nil); s != nil {
		t.Fatalf("Sinks = %v, want nil with no channel", s)
	}
}

// The env var name is configurable, and the resolver is the only thing that
// reads it — a call site must never grow its own os.Getenv.
func TestSlackCredentials_HonorsConfiguredEnvName(t *testing.T) {
	t.Setenv(config.DefaultSlackBotTokenEnv, "never-used")
	t.Setenv("MY_SLACK_TOKEN", fakeBotToken)
	tok, ch := SlackCredentials(slackCfg(true, "MY_SLACK_TOKEN", "C0123"))
	if tok != fakeBotToken || ch != "C0123" {
		t.Errorf("SlackCredentials = %q/%q", tok, ch)
	}
}

// Both sinks must render the same Message, so an operator with both configured
// sees the same content in both places.
func TestSinks_BothProvidersRenderTheSameMessage(t *testing.T) {
	var discordBody, slackBody []byte
	dsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discordBody, _ = readAll(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dsrv.Close()
	ssrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackBody, _ = readAll(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ssrv.Close()

	sink := MultiSink{
		&DiscordSink{WebhookURL: dsrv.URL, Client: dsrv.Client()},
		&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: ssrv.Client(), APIBase: ssrv.URL},
	}
	if _, err := sink.Post(context.Background(), []Message{{
		Title: "Ready to ship", Description: "epic closed", Color: ColorSuccess,
	}}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	for _, tc := range []struct{ name, body string }{
		{"discord", string(discordBody)},
		{"slack", string(slackBody)},
	} {
		if !strings.Contains(tc.body, "Ready to ship") || !strings.Contains(tc.body, "epic closed") {
			t.Errorf("%s payload lost content: %s", tc.name, tc.body)
		}
	}
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
