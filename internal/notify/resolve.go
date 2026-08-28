package notify

import (
	"net/http"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
)

// SlackCredentials is the SINGLE resolver for the Slack bot token and channel
// used by every Go-side alert. Call sites must not read an environment variable
// of their own — one destination, configured once, reached by ready-to-ship,
// stuck-epic and release-watch alike.
//
// This is deliberately the SAME token and channel the VSCode extension uses
// (#1089): before this, pipeline status authenticated with a bot token while
// the Go alerts used a separate incoming webhook, so one feature demanded two
// credentials and the setup document was twice as long for no capability gain.
//
// cfg may be nil (a failed config load still yields the default env var name).
// An unset or blank variable returns "", which Sinks reads as "Slack not
// configured".
func SlackCredentials(cfg *config.Config) (botToken, channel string) {
	if !cfg.SlackAlertsEnabled() {
		return "", "" // block absent or enabled:false — Slack is off
	}
	return strings.TrimSpace(os.Getenv(cfg.SlackBotTokenEnv())), cfg.SlackChannel()
}

// Sinks builds the delivery target for a caller that has already resolved its
// own Discord webhook (each Discord alert keeps its existing, independently
// configured source, so Discord behavior is unchanged) and pairs it with the
// shared Slack destination.
//
// Returns nil when neither provider is configured, so a caller can skip the
// whole alert path with a single nil check. A single configured provider still
// returns a MultiSink of one — callers never branch on provider count.
func Sinks(discordWebhookURL string, cfg *config.Config, client *http.Client) Sink {
	var sinks MultiSink
	if u := strings.TrimSpace(discordWebhookURL); u != "" {
		sinks = append(sinks, &DiscordSink{WebhookURL: u, Client: client})
	}
	if token, channel := SlackCredentials(cfg); token != "" && channel != "" {
		sinks = append(sinks, &SlackSink{BotToken: token, Channel: channel, Client: client})
	}
	if len(sinks) == 0 {
		return nil
	}
	return sinks
}
