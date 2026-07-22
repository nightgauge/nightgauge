package forgecmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge/webhook"
	"github.com/nightgauge/nightgauge/internal/gitlab"
)

// webhookCmd returns the `forge webhook` cobra command group.
func webhookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Manage forge webhook receivers",
	}
	cmd.AddCommand(webhookServeCmd())
	return cmd
}

// webhookServeCmd returns the `forge webhook serve` subcommand.
func webhookServeCmd() *cobra.Command {
	var (
		host    string
		port    int
		path    string
		version string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the GitLab webhook receiver",
		Long: `Start the GitLab webhook receiver.

The server listens for POST requests from GitLab project hooks (Pipeline, MR,
Note, Push), verifies the shared secret, deduplicates deliveries, and publishes
verified events onto the IPC bus.

The shared secret is read from the environment variable named in
notifications.inbound.gitlab.secret_env_var (config), or from
GITLAB_WEBHOOK_SECRET when no config is present.

GitLab equivalent: this command is the GitLab-native receiver and has no
GitHub equivalent — GitHub webhook support is a separate future feature.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWebhookServe(cmd.Context(), host, port, path, version)
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "Bind host (default 127.0.0.1)")
	cmd.Flags().IntVar(&port, "port", 0, "Bind port (default 8766)")
	cmd.Flags().StringVar(&path, "path", "", "Webhook path (default /gitlab)")
	cmd.Flags().StringVar(&version, "version", "dev", "Build version reported by /-/health")

	return cmd
}

// runWebhookServe is the main entrypoint for `forge webhook serve`.
func runWebhookServe(ctx context.Context, hostFlag string, portFlag int, pathFlag, versionFlag string) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("forge webhook serve: getwd: %w", err)
	}

	cfg, err := config.Load(wd)
	if err != nil {
		log.Printf("forge webhook serve: config load warning: %v — using defaults", err)
		cfg = &config.Config{}
	}

	// Resolve GitLab inbound config with flag overrides.
	glCfg := &config.GitLabInboundConfig{}
	if cfg.Notifications != nil && cfg.Notifications.Inbound != nil && cfg.Notifications.Inbound.GitLab != nil {
		glCfg = cfg.Notifications.Inbound.GitLab
	}

	bindHost := glCfg.ResolvedHost()
	if hostFlag != "" {
		bindHost = hostFlag
	}
	bindPort := glCfg.ResolvedPort()
	if portFlag != 0 {
		bindPort = portFlag
	}
	bindPath := glCfg.ResolvedPath()
	if pathFlag != "" {
		bindPath = pathFlag
	}

	replayWindow := time.Duration(glCfg.ResolvedReplayWindowSec()) * time.Second
	dedupeWindow := time.Duration(glCfg.ResolvedDedupeWindowSec()) * time.Second

	// Load the GitLab shared secret.
	secret, err := glCfg.ResolveSecret()
	if err != nil {
		// Fall back to GITLAB_WEBHOOK_SECRET for local development.
		secret = os.Getenv("GITLAB_WEBHOOK_SECRET")
		if secret == "" {
			return fmt.Errorf("forge webhook serve: %w (set GITLAB_WEBHOOK_SECRET or configure secret_env_var)", err)
		}
		log.Printf("forge webhook serve: using GITLAB_WEBHOOK_SECRET env var (configure secret_env_var in config for production)")
	}

	// Open the dedup cache.
	dedup, err := gitlab.NewDedupeCache(glCfg.DedupeDBPath, dedupeWindow)
	if err != nil {
		return fmt.Errorf("forge webhook serve: dedup cache: %w", err)
	}
	defer dedup.Close()

	pruneCtx, pruneCancel := context.WithCancel(ctx)
	defer pruneCancel()
	dedup.StartPruner(pruneCtx)

	// Emit IPC events to stdout when running standalone (no live IPC server).
	emitFn := webhook.EmitFunc(func(event string, data interface{}) {
		log.Printf("forge webhook: IPC event %q emitted", event)
	})
	disp := webhook.NewIPCEventDispatcher(emitFn)

	handler := gitlab.NewGitLabHandler(secret, dedup, disp,
		gitlab.WithReplayWindow(replayWindow),
		gitlab.WithVersion(versionFlag),
	)

	mux := webhook.NewMux(handler, bindPath)
	srv := webhook.NewServer(bindHost, bindPort)

	// Honour SIGINT/SIGTERM for graceful shutdown.
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("forge webhook serve: GitLab receiver on http://%s:%d%s", bindHost, bindPort, bindPath)
	log.Printf("forge webhook serve: health  → http://%s:%d/-/health", bindHost, bindPort)
	log.Printf("forge webhook serve: metrics → http://%s:%d/-/metrics", bindHost, bindPort)

	if err := srv.Start(sigCtx, mux); err != nil {
		return fmt.Errorf("forge webhook serve: %w", err)
	}
	log.Printf("forge webhook serve: shutdown complete")
	return nil
}
