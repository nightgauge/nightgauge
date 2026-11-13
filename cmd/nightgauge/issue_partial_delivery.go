package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// partialDeliveryDepsFactory builds the reader `issue partial-delivery` uses
// and the forge reads its remainder is reconciled through (#3242); tests
// replace it.
var partialDeliveryDepsFactory = func(owner, repo string, number int) (hooks.PartialDeliveryReader, orchestrator.PartialScopeReads, error) {
	client, err := clientFromConfig()
	if err != nil {
		return nil, orchestrator.PartialScopeReads{}, err
	}
	var cfg *config.Config
	if wd, err := os.Getwd(); err == nil {
		if c, err := config.Load(wd); err == nil {
			cfg = c
		}
	}
	var bots []string
	if cfg != nil && cfg.Autonomous != nil {
		bots = cfg.Autonomous.TrustedBots
	}
	gate := newConfiguredTrustGate(gh.NewTrustForge(client), cfg)
	reads := orchestrator.PartialScopeReads{
		// The body reaches a prompt, so it is vouched for after the read,
		// as the scheduler's readACReconcileBody does (#3231), by a gate
		// configured like the scheduler's.
		Body: func(ctx context.Context) (string, error) {
			issue, err := gh.NewIssueService(client).GetIssueWithRelations(ctx, owner, repo, number, gh.NoRelations)
			if err != nil {
				return "", err
			}
			if issue == nil {
				return "", errors.New("issue not found")
			}
			d := gate.Decide(ctx, trust.Subject{Owner: owner, Repo: repo, Number: number})
			if !d.Eligible {
				return "", fmt.Errorf("issue body not vouched for at read time: %s", d.Reason)
			}
			return issue.Body, nil
		},
		PR: func(ctx context.Context, pr int) (time.Time, string, error) {
			return client.PRCreatedAtAndBody(ctx, owner, repo, pr)
		},
		BodyEditedAt: func(ctx context.Context) (*time.Time, error) {
			return client.IssueLastEditedAt(ctx, owner, repo, number)
		},
	}
	return hooks.NewPartialDeliveryReader(client, bots), reads, nil
}

// partialDeliveryWorkspace is the tree the criteria report reads: the
// repository holding the working directory, else the directory itself.
func partialDeliveryWorkspace(ctx context.Context) string {
	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return top
		}
	}
	wd, _ := os.Getwd()
	return wd
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
	reader, reads, err := partialDeliveryDepsFactory(owner, repo, number)
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
		// prompt_blocks: the extension's stage loop appends these to the
		// feature-planning and feature-dev prompts, so a run it starts on an
		// issue with a pending remainder is scoped like the Go path's (#3095),
		// from the same reconciled brief (#3242). criteria stays the comment's
		// list; scope is the brief and dropped what left the body. The
		// reconcile logs to stderr, so stdout stays JSON.
		out := struct {
			hooks.PartialDeliveryStatus
			Scope        []string          `json:"scope,omitempty"`
			Dropped      []string          `json:"dropped,omitempty"`
			PromptBlocks map[string]string `json:"prompt_blocks,omitempty"`
		}{PartialDeliveryStatus: status}
		if status.Pending && !status.Unlisted && len(status.Criteria) > 0 {
			out.Scope, out.Dropped = orchestrator.ReconcilePartialScope(ctx, reads, partialDeliveryWorkspace(ctx),
				number, status.Criteria, status.PR)
			out.PromptBlocks = orchestrator.PartialRerunScopeBlocks(status.PR, out.Scope)
		}
		data, err := json.MarshalIndent(out, "", "  ")
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
