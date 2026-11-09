package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

// partialDeliveryReaderFactory builds the reader `issue partial-delivery` uses;
// tests replace it.
var partialDeliveryReaderFactory = func() (hooks.PartialDeliveryReader, error) {
	client, err := clientFromConfig()
	if err != nil {
		return nil, err
	}
	var bots []string
	if wd, err := os.Getwd(); err == nil {
		if cfg, err := config.Load(wd); err == nil && cfg != nil && cfg.Autonomous != nil {
			bots = cfg.Autonomous.TrustedBots
		}
	}
	return hooks.NewPartialDeliveryReader(client, bots), nil
}

// issuePartialDeliveryCmd reports whether an issue carries a recorded
// partial-delivery remainder that no later merged PR has closed (#3089). The
// headless orchestrator calls it before a verify-and-close early exit closes
// an issue. Exit 0 on a successful read (pending or not); exit 2 when the read
// failed, so callers fail closed.
func issuePartialDeliveryCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)
	cmd := &cobra.Command{
		Use:   "partial-delivery [number]",
		Short: "Report a pending partial-delivery remainder on an issue",
		Long: `Find the newest trusted <!-- nightgauge:partial-delivery pr=N --> comment on
the issue and report the criteria it left unmet. The remainder is pending unless
a PR that closes the issue merged after that comment.

Exit 0 when the read succeeded (pending or not), 2 when it failed.`,
		Args:    cobra.ExactArgs(1),
		Example: `  nightgauge issue partial-delivery 3084 --repo nightgauge/nightgauge --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}
			ownerPart, repoPart := splitRepo(owner, repo)
			code := runIssuePartialDelivery(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), ownerPart, repoPart, number, outputJSON)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
	ownerFlag(cmd, &owner, "GitHub organization")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository (name or owner/name)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// runIssuePartialDelivery returns the process exit code.
func runIssuePartialDelivery(ctx context.Context, stdout, stderr io.Writer, owner, repo string, number int, outputJSON bool) int {
	if owner == "" || repo == "" {
		fmt.Fprintln(stderr, "issue partial-delivery: owner and repo are required")
		return 2
	}
	reader, err := partialDeliveryReaderFactory()
	if err != nil {
		fmt.Fprintf(stderr, "issue partial-delivery: %v\n", err)
		return 2
	}
	status, err := hooks.PendingPartialDelivery(ctx, reader, owner, repo, number)
	if err != nil {
		fmt.Fprintf(stderr, "issue partial-delivery: %v\n", err)
		return 2
	}
	if outputJSON {
		data, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "issue partial-delivery: %v\n", err)
			return 2
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	switch {
	case status.Pending && status.Unlisted:
		fmt.Fprintf(stdout, "#%d: partial delivery by PR #%d is pending; its remaining criteria were not listed\n", number, status.PR)
	case status.Pending:
		fmt.Fprintf(stdout, "#%d: partial delivery by PR #%d is pending:\n- %s\n", number, status.PR, strings.Join(status.Criteria, "\n- "))
	case status.ClosedBy > 0:
		fmt.Fprintf(stdout, "#%d: partial delivery by PR #%d was closed by PR #%d\n", number, status.PR, status.ClosedBy)
	default:
		fmt.Fprintf(stdout, "#%d: no partial-delivery remainder\n", number)
	}
	return 0
}
