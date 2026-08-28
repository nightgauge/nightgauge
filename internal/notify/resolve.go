package notify

import (
	"net/http"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
)

// SlackWebhookURL is the SINGLE resolver for the Slack incoming-webhook URL used
// by every Go-side alert (#1072). Call sites must not read an environment
// variable of their own — one destination, configured once, reached by
// ready-to-ship, stuck-epic and release-watch alike.
//
// cfg may be nil (a failed config load still yields the default env var name).
// An unset or blank variable returns "", which Sinks reads as "Slack not
// configured".
func SlackWebhookURL(cfg *config.Config) string {
	return strings.TrimSpace(os.Getenv(cfg.SlackAlertWebhookEnv()))
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
	if u := SlackWebhookURL(cfg); u != "" {
		sinks = append(sinks, &SlackSink{WebhookURL: u, Client: client})
	}
	if len(sinks) == 0 {
		return nil
	}
	return sinks
}
