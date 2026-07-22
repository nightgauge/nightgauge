package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// backfillPlatformCreds holds the platform base URL + license key resolved for
// the backfill command. The license key is NEVER logged or printed.
type backfillPlatformCreds struct {
	baseURL    string
	licenseKey string
}

// resolvePlatformCreds resolves the platform base URL and license key the same
// way the binary does for `serve`: env vars (NIGHTGAUGE_PLATFORM_URL /
// NIGHTGAUGE_LICENSE_KEY) win, otherwise fall back to the machine-tier
// config (~/.nightgauge/config.yaml) `platform.api_url` / `platform.license_key`.
// Falls back to the prod default base URL when neither is set. NEVER prints the key.
func resolvePlatformCreds() (backfillPlatformCreds, error) {
	creds := backfillPlatformCreds{
		baseURL:    os.Getenv("NIGHTGAUGE_PLATFORM_URL"),
		licenseKey: os.Getenv("NIGHTGAUGE_LICENSE_KEY"),
	}

	// Read the machine-tier config for any fields the env did not supply.
	if creds.baseURL == "" || creds.licenseKey == "" {
		path, err := config.MachineConfigPath()
		if err == nil {
			if data, readErr := os.ReadFile(path); readErr == nil {
				var parsed struct {
					Platform struct {
						APIURL     string `yaml:"api_url"`
						LicenseKey string `yaml:"license_key"`
					} `yaml:"platform"`
				}
				if yaml.Unmarshal(data, &parsed) == nil {
					if creds.baseURL == "" {
						creds.baseURL = parsed.Platform.APIURL
					}
					if creds.licenseKey == "" {
						creds.licenseKey = parsed.Platform.LicenseKey
					}
				}
			}
		}
	}

	// Prod default when no base URL is configured anywhere.
	if creds.baseURL == "" {
		creds.baseURL = platform.DefaultConfig().BaseURL
	}

	if creds.licenseKey == "" {
		return creds, fmt.Errorf("no license key configured (set platform.license_key in ~/.nightgauge/config.yaml or export NIGHTGAUGE_LICENSE_KEY)")
	}
	return creds, nil
}

// pipelineBackfillCmd backfills all local pipeline run history
// (.nightgauge/pipeline/history/*.jsonl) onto the production dashboard via
// POST /v1/telemetry/pipeline-run — the canonical pipeline-run telemetry sink
// Records are canonicalized
// (duplicates folded, noise dropped) before posting.
func pipelineBackfillCmd() *cobra.Command {
	var (
		days         int
		limit        int
		repo         string
		dryRun       bool
		workspaceDir string
	)
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Backfill local pipeline run history onto the production dashboard",
		Long: `Reads all local run history from .nightgauge/pipeline/history/*.jsonl,
canonicalizes it (folds the duplicate records written per logical run, drops
pure-synthetic noise), and POSTs each surviving run to the platform's
POST /v1/telemetry/pipeline-run endpoint.

Each record carries its run's own pipelineRunId when the local history has a
well-formed run UUID (#261) — the platform upserts on that id, converging with
any live event-stream row — and the endpoint falls back to deriving identity
from (repo, issueNumber, startedAt) for older records without one. Re-running
the backfill is safe to retry; a re-sync is reconciled server-side rather than
duplicated. The historical startedAt is preserved, so old runs appear on their
real dates.

The license key and platform base URL are resolved the same way as 'serve':
env (NIGHTGAUGE_PLATFORM_URL / NIGHTGAUGE_LICENSE_KEY) wins, else the
machine-tier ~/.nightgauge/config.yaml platform: block. The license key is
never printed.`,
		Example: `  nightgauge pipeline backfill --repo nightgauge/nightgauge --dry-run
  nightgauge pipeline backfill --repo nightgauge/nightgauge`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			// Resolve workspace root (default CWD).
			workspaceRoot := workspaceDir
			if workspaceRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve working directory: %w", err)
				}
				workspaceRoot = wd
			}

			// Resolve repo: explicit flag, else derive from origin remote.
			if repo == "" {
				if svc, err := gitpkg.NewService(workspaceRoot); err == nil {
					if slug, slugErr := svc.RemoteRepoSlug(); slugErr == nil {
						repo = slug
					}
				}
			}
			if repo == "" {
				return fmt.Errorf("--repo is required (could not derive owner/repo from origin remote)")
			}

			// Read all local history.
			hw := state.NewHistoryWriter(workspaceRoot)
			records, err := hw.ReadRecentV2(limit, days)
			if err != nil {
				return fmt.Errorf("read history: %w", err)
			}

			canonical, canon := platform.CanonicalizeRuns(records)

			fmt.Fprintf(out, "repo:              %s\n", repo)
			fmt.Fprintf(out, "records read:      %d\n", canon.Input)
			fmt.Fprintf(out, "parse-skipped:     %d\n", canon.ParseSkipped)
			fmt.Fprintf(out, "canonical runs:    %d\n", canon.Groups)
			fmt.Fprintf(out, "dropped-as-noise:  %d\n", canon.DroppedNoise)
			fmt.Fprintf(out, "to-backfill:       %d\n", canon.Merged)
			fmt.Fprintf(out, "date range:        %s\n", canon.DateRange())

			if dryRun {
				fmt.Fprintln(out, "\ndry-run: no records posted")
				return nil
			}

			if len(canonical) == 0 {
				fmt.Fprintln(out, "\nnothing to backfill")
				return nil
			}

			// Resolve platform credentials (license key never printed).
			creds, err := resolvePlatformCreds()
			if err != nil {
				return err
			}

			pcfg := platform.DefaultConfig()
			pcfg.BaseURL = creds.baseURL
			pcfg.LicenseKey = creds.licenseKey
			pcfg.AgentID = platform.ResolveMachineID()

			pc, err := platform.NewClient(pcfg)
			if err != nil {
				return fmt.Errorf("platform client: %w", err)
			}

			// Confirm the platform is reachable before pushing ~1,500 POSTs.
			// StartHealthPolling runs an initial health check synchronously, after
			// which IsOnline() reflects reachability.
			healthCtx, healthCancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer healthCancel()
			pc.StartHealthPolling(healthCtx)
			if !pc.IsOnline() {
				return fmt.Errorf("platform not reachable at %s — aborting backfill", creds.baseURL)
			}

			svc := platform.NewAnalyticsService(pc)

			fmt.Fprintf(out, "\nposting %d runs to %s ...\n", len(canonical), creds.baseURL)

			// Generous timeout — sequential POSTs over ~1,500 runs.
			syncCtx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()

			res := svc.SyncTelemetry(syncCtx, canonical, repo)

			fmt.Fprintf(out, "\nsynced: %d\nfailed: %d\n", res.Synced, res.Failed)
			if len(res.Errors) > 0 {
				fmt.Fprintln(out, "first errors:")
				for i, e := range res.Errors {
					if i >= 10 {
						fmt.Fprintf(out, "  ... and %d more\n", len(res.Errors)-10)
						break
					}
					fmt.Fprintf(out, "  - %s\n", e)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 3650, "How many recent daily history files to read (default: all history)")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max records to read (0 = no limit)")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository owner/repo (default: derive from origin remote)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print counts only — do not post to the platform")
	cmd.Flags().StringVar(&workspaceDir, "workspace", "", "Workspace root directory (default: CWD)")

	return cmd
}
