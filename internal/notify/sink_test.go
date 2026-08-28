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

func TestSinks_NilWhenNothingConfigured(t *testing.T) {
	t.Setenv(config.DefaultAlertsSlackWebhookEnv, "")
	if s := Sinks("", nil, nil); s != nil {
		t.Fatalf("Sinks with no destination = %v, want nil", s)
	}
}

func TestSinks_DiscordOnly(t *testing.T) {
	t.Setenv(config.DefaultAlertsSlackWebhookEnv, "")
	s := Sinks("https://discord.com/api/webhooks/1/x", nil, nil)
	if s == nil || s.Name() != "discord" {
		t.Fatalf("Sinks = %v, want a discord-only sink", s)
	}
}

func TestSinks_SlackOnly(t *testing.T) {
	t.Setenv(config.DefaultAlertsSlackWebhookEnv, fakeSlackURL)
	s := Sinks("", nil, nil)
	if s == nil || s.Name() != "slack" {
		t.Fatalf("Sinks = %v, want a slack-only sink", s)
	}
}

func TestSinks_BothConfigured(t *testing.T) {
	t.Setenv(config.DefaultAlertsSlackWebhookEnv, fakeSlackURL)
	s := Sinks("https://discord.com/api/webhooks/1/x", nil, nil)
	if s == nil || s.Name() != "discord+slack" {
		t.Fatalf("Sinks = %v, want both sinks", s)
	}
}

// The env var name is configurable, and the resolver is the only thing that
// reads it — a call site must never grow its own os.Getenv.
func TestSlackWebhookURL_HonorsConfiguredEnvName(t *testing.T) {
	t.Setenv(config.DefaultAlertsSlackWebhookEnv, "https://default/never-used")
	t.Setenv("MY_SLACK_HOOK", fakeSlackURL)
	cfg := &config.Config{Alerts: &config.AlertsConfig{SlackWebhookEnv: "MY_SLACK_HOOK"}}
	if got := SlackWebhookURL(cfg); got != fakeSlackURL {
		t.Errorf("SlackWebhookURL = %q, want the configured variable's value", got)
	}
}

func TestSlackWebhookURL_NilConfigUsesDefaultEnv(t *testing.T) {
	t.Setenv(config.DefaultAlertsSlackWebhookEnv, fakeSlackURL)
	if got := SlackWebhookURL(nil); got != fakeSlackURL {
		t.Errorf("SlackWebhookURL(nil) = %q, want the default variable's value", got)
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
	}))
	defer ssrv.Close()

	t.Setenv(config.DefaultAlertsSlackWebhookEnv, ssrv.URL)
	sink := Sinks(dsrv.URL, nil, dsrv.Client())
	if sink == nil {
		t.Fatal("expected both sinks")
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
