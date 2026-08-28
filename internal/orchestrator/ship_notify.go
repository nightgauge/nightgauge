package orchestrator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/notify"
)

// emitReadyToShipAlert posts a "ready to ship" notification when an epic fully
// closes (#4076), to every configured sink (Discord and/or Slack, #1072). The
// pipeline stops at "merged"; the owner expected "shipped". Store submission is a deliberate human action, so this only bridges
// "epic closed" → "ready to ship" by surfacing the exact deploy dispatch command
// — it NEVER auto-submits. Best-effort: a missing webhook or a failed POST is
// logged, never fatal to the pipeline.
func (s *Scheduler) emitReadyToShipAlert(ctx context.Context, repo string, epicNumber int) {
	if epicNumber == 0 {
		return
	}
	enabled, webhook, deployCommand, cfg := resolveShipNotify(s.workspaceRoot)
	if !enabled {
		return
	}
	sink := notify.Sinks(webhook, cfg, &http.Client{Timeout: 10 * time.Second})
	if sink == nil {
		// No destination configured — nothing to push. (The epic still closed;
		// this is purely the optional ship bridge.)
		return
	}
	msgs := []notify.Message{shipNotifyMessage(repo, epicNumber, deployCommand)}
	if _, err := sink.Post(ctx, msgs); err != nil {
		log.Printf("#%d: ready-to-ship notification failed (%s): %s",
			epicNumber, sink.Name(), sink.Redact(err.Error()))
		return
	}
	log.Printf("epic %s#%d closed — ready-to-ship notification sent", repo, epicNumber)
}

// resolveShipNotify reads the ready-to-ship settings from the workspace config
// (ready_to_ship:) plus the environment (the webhook secret). Defaults: enabled,
// the canonical deploy-stores dispatch command, and the NIGHTGAUGE_SHIP_-
// NOTIFY_WEBHOOK env var. Mirrors how other orchestrator config is loaded.
func resolveShipNotify(workspaceRoot string) (enabled bool, webhookURL, deployCommand string, cfg *config.Config) {
	enabled = true
	deployCommand = config.DefaultDeployCommand
	webhookEnv := config.DefaultShipNotifyWebhookEnv

	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		// Malformed/invalid config.yaml. For an optional best-effort
		// notification, fail CLOSED rather than fall back to enabled-by-default —
		// otherwise an explicit ready_to_ship.enabled:false would be silently
		// ignored on the error path (#4076 review). (A missing config.yaml is NOT
		// an error: config.Load returns defaults, so the happy path is unaffected.)
		log.Printf("ready-to-ship: skipping notification, config load failed: %v", err)
		return false, "", deployCommand, nil
	}
	if cfg != nil && cfg.ReadyToShip != nil {
		rts := cfg.ReadyToShip
		if rts.Enabled != nil {
			enabled = *rts.Enabled
		}
		if strings.TrimSpace(rts.DeployCommand) != "" {
			deployCommand = strings.TrimSpace(rts.DeployCommand)
		}
		if strings.TrimSpace(rts.DiscordWebhookEnv) != "" {
			webhookEnv = strings.TrimSpace(rts.DiscordWebhookEnv)
		}
	}
	webhookURL = strings.TrimSpace(os.Getenv(webhookEnv))
	return enabled, webhookURL, deployCommand, cfg
}

// shipNotifyMessage renders the ready-to-ship alert: a clear "epic closed, ready
// to ship" message with the deploy dispatch command in a fenced code block
// (copy-paste ready) and an explicit no-auto-submit note. Provider-neutral —
// each sink renders it in its own wire format.
func shipNotifyMessage(repo string, epicNumber int, deployCommand string) notify.Message {
	return notify.Message{
		Title:       notify.ClampField(fmt.Sprintf("🚀 Ready to ship: epic %s#%d closed", repo, epicNumber), 240),
		Description: "All sub-issues are merged and the epic is closed. Review, then dispatch the deploy below — nothing is submitted to stores automatically.",
		Color:       notify.ColorSuccess,
		Fields: []notify.Field{{
			Name:  "Deploy command",
			Value: notify.ClampField("```\n"+deployCommand+"\n```", 1024),
		}},
		Footer: "nightgauge ready-to-ship",
	}
}
