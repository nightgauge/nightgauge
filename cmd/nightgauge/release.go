package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/release"
	"github.com/spf13/cobra"
)

// releaseCmd is the top-level "release" command. It exposes deterministic
// operations for fetching and classifying GitHub releases. Implements audit
// appendix row B33 — absorbs the inline `gh api` + Python in
// skills/nightgauge-release-watch/SKILL.md Phases 2–4.
func releaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "GitHub release fetch and changelog classification",
		Long: `Deterministic verbs for monitoring upstream GitHub releases. Replaces the
inline gh api + Python embedded in skills/nightgauge-release-watch/SKILL.md
Phases 2–4 (audit row B33). Output schemas are stable v1 — additive evolution
only.`,
	}
	cmd.AddCommand(releaseFetchCmd())
	cmd.AddCommand(releaseClassifyChangesCmd())
	cmd.AddCommand(releaseNotifyFindingsCmd())
	return cmd
}

// releaseFetchCmd implements `nightgauge release fetch`.
//
// Exit codes:
//
//	0 — fetch completed (zero releases is not an error)
//	2 — hard error (bad flag, transport failure, non-2xx status, decode error)
func releaseFetchCmd() *cobra.Command {
	var (
		source     string
		since      string
		limit      int
		workdir    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch GitHub releases for a repo, optionally filtered by --since",
		Long: `GET https://api.github.com/repos/{source}/releases (or a custom base via tests),
optionally filter to releases strictly newer than --since (semver), and emit a
stable v1 JSON document.

The verb is the binary-side of skills/nightgauge-release-watch/SKILL.md
Phases 2–3 (audit row B33). It returns up to --limit releases (default 10).

Exit codes:
  0  fetch completed
  2  hard error (bad flag, transport, non-2xx, malformed response)`,
		Example: `  nightgauge release fetch --source anthropics/claude-code --json
  nightgauge release fetch --source anthropics/claude-code --since 2.1.74 --json
  nightgauge release fetch --source anthropics/claude-code --limit 5`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if source == "" {
				return fmt.Errorf("--source is required (owner/repo)")
			}
			_ = workdir // currently unused; reserved for parity with sibling verbs

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			result, err := release.Fetch(ctx, release.Options{
				Source: source,
				Since:  since,
				Limit:  limit,
				Token:  resolveReleaseToken(),
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, "release fetch:", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				return nil
			}
			printReleaseFetchHuman(&result)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "GitHub repo slug to query (owner/repo) — required")
	cmd.Flags().StringVar(&since, "since", "", "Lower bound semver version; only releases strictly newer are returned")
	cmd.Flags().IntVar(&limit, "limit", release.DefaultLimit, "Maximum releases to fetch (per_page)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (currently unused — parity with sibling verbs)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

// releaseClassifyChangesCmd implements `nightgauge release classify-changes`.
//
// Exit codes:
//
//	0 — classification completed
//	2 — input read or decode error
func releaseClassifyChangesCmd() *cobra.Command {
	var (
		input      string
		workdir    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "classify-changes",
		Short: "Classify release-body bullets into feature/fix/breaking/deprecation/improvement",
		Long: `Reads a JSON document (FetchResult or bare []Release) from --input or stdin,
walks each release body line-by-line, and emits one ClassifiedChange per
'-'-prefixed bullet. Mirrors the inline-Python parser in
skills/nightgauge-release-watch/SKILL.md Phase 4 byte-for-byte (audit row
B33).

Output is a top-level JSON array of ClassifiedRelease values — the field
names ('version', 'published_at', 'changes[].type', '.description', '.tags')
are pinned to the pre-migration /tmp/release-watch-classified.json shape so
the SKILL's Phase 5+ scoring code consumes the new output without changes.

Exit codes:
  0  classification completed
  2  input read or decode error`,
		Example: `  nightgauge release fetch --source anthropics/claude-code --json | \
    nightgauge release classify-changes --json
  nightgauge release classify-changes --input /tmp/release-watch-new.json --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = workdir // currently unused; reserved for parity with sibling verbs

			var src = os.Stdin
			if input != "" {
				f, err := os.Open(input)
				if err != nil {
					fmt.Fprintln(os.Stderr, "release classify-changes:", err)
					os.Exit(2)
				}
				defer f.Close()
				src = f
			}

			releases, err := release.ReadInput(src)
			if err != nil {
				fmt.Fprintln(os.Stderr, "release classify-changes:", err)
				os.Exit(2)
			}

			classified := release.Classify(releases)

			if jsonOutput {
				// Emit a bare top-level array — preserves consumer compatibility
				// with the existing /tmp/release-watch-classified.json shape.
				data, err := json.MarshalIndent(classified, "", "  ")
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
					return nil
				}
				fmt.Println(string(data))
				return nil
			}
			printClassifyHuman(classified)
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Path to JSON input (FetchResult or bare []Release); empty = stdin")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (currently unused — parity with sibling verbs)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printReleaseFetchHuman(r *release.FetchResult) {
	fmt.Printf("nightgauge release fetch — schema v%d\n", r.V)
	fmt.Printf("source=%s  since=%q  limit=%d  filtered=%d  fetched_at=%s\n",
		r.Source, r.Since, r.Limit, r.Filtered, r.FetchedAt)
	fmt.Printf("releases: %d\n", len(r.Releases))
	for _, rel := range r.Releases {
		flags := ""
		if rel.Prerelease {
			flags += " (prerelease)"
		}
		if rel.Draft {
			flags += " (draft)"
		}
		fmt.Printf("  • %s%s  %s\n", rel.TagName, flags, rel.PublishedAt)
		if rel.HTMLURL != "" {
			fmt.Printf("    %s\n", rel.HTMLURL)
		}
	}
}

func printClassifyHuman(rs []release.ClassifiedRelease) {
	fmt.Printf("nightgauge release classify-changes — %d release(s) with changes\n", len(rs))
	for _, r := range rs {
		fmt.Printf("\n  %s  published=%s  changes=%d\n", r.Version, r.PublishedAt, len(r.Changes))
		for _, c := range r.Changes {
			tags := ""
			if len(c.Tags) > 0 {
				tags = " [" + strings.Join(c.Tags, ", ") + "]"
			}
			fmt.Printf("    [%-11s] %s%s\n", c.Type, c.Description, tags)
		}
	}
}

// releaseNotifyFindingsCmd implements `nightgauge release notify-findings`.
//
// It reads a release-watch creation-log and routes the high-impact
// `issues_created` findings (score >= --min-score, capped at --max) to a Discord
// webhook — the alert sink of #4058. Delivery is BEST-EFFORT and OPT-IN: when
// the webhook env var is unset/empty the command is a clean no-op, and a webhook
// POST failure is reported but does NOT fail the command, so an alerting hiccup
// never breaks the release-watch workflow.
//
// Exit codes:
//
//	0 — finished (sent, skipped, or best-effort delivery failure)
//	2 — hard error (missing/unreadable/unparseable creation-log, bad flags)
func releaseNotifyFindingsCmd() *cobra.Command {
	var (
		logPath    string
		webhookEnv string
		minScore   int
		maxItems   int
		dryRun     bool
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "notify-findings",
		Short: "Route high-impact release-watch findings to a Discord alert sink",
		Long: `Read a release-watch creation-log and POST a consolidated Discord embed for the
high-impact issues_created findings (score >= --min-score, capped at --max). This
is the alert sink of #4058 — it surfaces new-model / breaking-change findings
beyond the VSCode Discovery tab while respecting the existing score threshold and
per-release cap.

OPT-IN + BEST-EFFORT: the webhook URL is read from the env var named by
--webhook-env. If that var is unset/empty the sink is disabled (clean no-op). A
webhook delivery failure is reported but does not fail the command.

Exit codes:
  0  finished (sent / skipped / best-effort delivery failure)
  2  hard error (missing/unreadable/unparseable creation-log, bad flags)`,
		Example: `  nightgauge release notify-findings \
    --creation-log .nightgauge/release-watch/creation-log-claude-code.json
  nightgauge release notify-findings --creation-log path.json --min-score 80 --max 5 --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if logPath == "" {
				return fmt.Errorf("--creation-log is required")
			}

			webhookURL := os.Getenv(webhookEnv)

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			result, err := release.NotifyFindings(ctx, release.NotifyOptions{
				LogPath:    logPath,
				WebhookURL: webhookURL,
				MinScore:   minScore,
				MaxItems:   maxItems,
				DryRun:     dryRun,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, "release notify-findings:", err)
				os.Exit(2)
			}

			if jsonOutput {
				data, encErr := json.MarshalIndent(result, "", "  ")
				if encErr != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", encErr)
					return nil
				}
				fmt.Println(string(data))
				return nil
			}
			printNotifyHuman(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&logPath, "creation-log", "", "Path to a release-watch creation-log JSON (required)")
	cmd.Flags().StringVar(&webhookEnv, "webhook-env", "RELEASE_WATCH_DISCORD_WEBHOOK", "Env var holding the Discord webhook URL; unset/empty disables the sink")
	cmd.Flags().IntVar(&minScore, "min-score", release.DefaultAlertMinScore, "Route only findings with score >= this value")
	cmd.Flags().IntVar(&maxItems, "max", release.DefaultAlertMaxItems, "Cap the number of findings routed (per-release cap)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Build the payload and report, but do not POST")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the result as JSON")
	return cmd
}

func printNotifyHuman(r release.NotifyResult) {
	status := "skipped"
	if r.Sent {
		status = "sent"
	}
	fmt.Printf("nightgauge release notify-findings — %s\n", status)
	fmt.Printf("provider=%s  version=%s  eligible=%d  routed=%d\n",
		r.Provider, r.Version, r.Eligible, r.Routed)
	if r.Reason != "" {
		fmt.Printf("reason: %s\n", r.Reason)
	}
}

// resolveReleaseToken returns the bearer token for release fetch operations.
// Order: --token CLI flag (globalToken) → GITHUB_TOKEN env var. An empty
// return value means "send no Authorization header" — public-repo fetches
// still work, just at the lower 60/hr unauthenticated rate limit.
func resolveReleaseToken() string {
	if globalToken != "" {
		return globalToken
	}
	return os.Getenv("GITHUB_TOKEN")
}
