package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/integrationprobe"
	"github.com/spf13/cobra"
)

// integrationCmd is the top-level "integration" command. It exposes
// subcommands that audit cross-repo integration health. See Issue #3090
// and audit appendix row B32.
func integrationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "integration",
		Short: "Cross-repository integration audit operations",
	}
	cmd.AddCommand(integrationProbePlatformCmd())
	return cmd
}

// integrationProbePlatformCmd probes the platform API and emits a 6-category
// JSON report (WORKING, AUTH_REQUIRED, AUTH_MISMATCH, NOT_FOUND, BROKEN, STUB).
//
// Exit codes:
//
//	0 — all results are WORKING or STUB (no real findings)
//	1 — at least one non-WORKING/non-STUB category present
//	2 — config or IO error (manifest load failure, etc.)
//	3 — server unreachable (every probe transport-errored)
func integrationProbePlatformCmd() *cobra.Command {
	var (
		baseURL      string
		authMode     string
		token        string
		manifestPath string
		outputJSON   bool
		timeout      time.Duration
	)

	cmd := &cobra.Command{
		Use:   "probe-platform",
		Short: "Probe platform API endpoints and emit a categorized JSON report",
		Long: `Walk an embedded YAML endpoint manifest, issue HTTP probes against the
platform API, and categorize each response into one of six buckets:
WORKING, AUTH_REQUIRED, AUTH_MISMATCH, NOT_FOUND, BROKEN, STUB.

Categorization is purely status-code + body-shape based — no LLM calls,
no schema inference. The default manifest mirrors the endpoint list in
skills/nightgauge-integration-audit/SKILL.md Phase 2.

Exit codes:
  0 — all results are WORKING or STUB (no real findings)
  1 — at least one non-WORKING/non-STUB category present
  2 — config or IO error (manifest load failure, etc.)
  3 — server unreachable (every probe transport-errored)`,
		Example: `  nightgauge integration probe-platform --json
  nightgauge integration probe-platform --base-url http://localhost:3000 --auth-mode jwt --token "$TOKEN"
  nightgauge integration probe-platform --manifest configs/integration-platform-endpoints.yaml --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			manifest, err := integrationprobe.LoadManifest(manifestPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout*2)
			defer cancel()

			client := &http.Client{Timeout: timeout}
			report, err := integrationprobe.Probe(ctx, client, baseURL, authMode, token, manifest)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}

			if outputJSON {
				if err := printJSON(report); err != nil {
					return err
				}
			} else {
				renderProbeHuman(report)
			}

			os.Exit(probeExitCode(report))
			return nil
		},
	}

	cmd.Flags().StringVar(&baseURL, "base-url", "http://localhost:3000", "Platform API base URL")
	cmd.Flags().StringVar(&authMode, "auth-mode", integrationprobe.AuthModeNone, "Auth mode: jwt | license | none")
	cmd.Flags().StringVar(&token, "token", "", "Auth token (JWT or license key) sent per --auth-mode")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to endpoint manifest YAML (default: embedded)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output results as JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "HTTP timeout per request")
	return cmd
}

// probeExitCode maps a ProbeReport to the documented exit code contract.
func probeExitCode(r *integrationprobe.ProbeReport) int {
	if r.Unreachable {
		return 3
	}
	for cat, count := range r.Categories {
		if count == 0 {
			continue
		}
		if cat != integrationprobe.CategoryWorking && cat != integrationprobe.CategoryStub {
			return 1
		}
	}
	return 0
}

// renderProbeHuman prints a one-line summary plus a per-category count table.
func renderProbeHuman(r *integrationprobe.ProbeReport) {
	fmt.Printf("Integration Probe — %s (auth-mode=%s)\n", r.BaseURL, r.AuthMode)
	if r.Unreachable {
		fmt.Println("✗ server unreachable — every probe transport-errored")
	}
	fmt.Printf("  Endpoints probed: %d\n\n", len(r.Results))

	// Stable display order via integrationprobe.AllCategories.
	for _, cat := range integrationprobe.AllCategories {
		count := r.Categories[cat]
		marker := " "
		if count > 0 && cat != integrationprobe.CategoryWorking && cat != integrationprobe.CategoryStub {
			marker = "✗"
		}
		fmt.Printf("  %s %-15s %d\n", marker, cat, count)
	}

	// List the non-OK results so humans can see what to look at.
	var problems []integrationprobe.ProbeResult
	for _, res := range r.Results {
		if res.Category != integrationprobe.CategoryWorking && res.Category != integrationprobe.CategoryStub {
			problems = append(problems, res)
		}
	}
	if len(problems) == 0 {
		return
	}
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Category != problems[j].Category {
			return problems[i].Category < problems[j].Category
		}
		return problems[i].Path < problems[j].Path
	})
	fmt.Println("\nFindings:")
	for _, p := range problems {
		fmt.Printf("  [%s] %s %s — status=%d %s\n",
			p.Category, p.Method, p.Path, p.StatusCode, p.Error)
	}
}
