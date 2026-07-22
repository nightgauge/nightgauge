package main

// `nightgauge attention list|show|resolve` — the local CLI surface over the
// Action Center DecisionRequest store (ADR 015). It reads and mutates the same
// `.nightgauge/attention/` store the daemon writes, through the one Store type
// (single-writer discipline: atomic temp+rename + terminal-state CAS make a
// standalone CLI resolve safe against a concurrent daemon writer).
//
// resolve validates the chosen option against the request's declared set AND
// the verb registry (ADR 015 §J) before executing via the verb registry. Verbs
// that need the live scheduler/GitHub are executed by the daemon; the CLI
// executes the deterministic file-based verbs (budget.raiseCeiling,
// run.retryWithEscalation) and the no-op choices directly, and records every
// resolution regardless.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/spf13/cobra"
)

func attentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attention",
		Short: "Inspect and resolve Action Center decision requests (ADR 015)",
		Long: `Action Center — the local-first inbox of pending human decisions.

Any pipeline component raises a DecisionRequest when it hits a dead-end that
needs a human: work exhaustion, an owner-action handoff, a cascade pause, a
budget ceiling, a branch-protection block, and more. List them, inspect one,
and resolve it — each option maps to a deterministic, audited verb.`,
	}
	cmd.AddCommand(attentionListCmd())
	cmd.AddCommand(attentionShowCmd())
	cmd.AddCommand(attentionResolveCmd())
	return cmd
}

// resolveAttentionWorkdir returns --workdir when set, else the cwd.
func resolveAttentionWorkdir(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getcwd: %w", err)
	}
	return cwd, nil
}

func attentionListCmd() *cobra.Command {
	var (
		jsonOutput bool
		all        bool
		repo       string
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List pending decision requests (most-severe-first)",
		SilenceUsage: true,
		Example: `  nightgauge attention list
  nightgauge attention list --all --json
  nightgauge attention list --repo octocat/acme-web`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			store := attention.New(root)
			reqs, err := store.List(attention.ListFilter{IncludeTerminal: all, Repo: repo})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(reqs)
			}
			printAttentionTable(cmd, reqs)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output requests as JSON")
	cmd.Flags().BoolVar(&all, "all", false, "Include resolved/expired requests")
	cmd.Flags().StringVar(&repo, "repo", "", "Filter to a single owner/name repo")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

func printAttentionTable(cmd *cobra.Command, reqs []attention.DecisionRequest) {
	if len(reqs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "✓ All clear — no decisions pending.")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSEVERITY\tKIND\tSTATE\tREPO\tTITLE")
	for _, r := range reqs {
		repo := r.Context.Repo
		if repo == "" {
			repo = "(fleet)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Severity, r.Kind, r.Lifecycle.State, repo, clip(r.Title, 48))
	}
	w.Flush()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func attentionShowCmd() *cobra.Command {
	var (
		jsonOutput bool
		workdir    string
	)
	cmd := &cobra.Command{
		Use:          "show <id>",
		Short:        "Show one decision request in detail",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			store := attention.New(root)
			req, found, err := store.Get(args[0])
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no decision request with id %q", args[0])
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(req)
			}
			printAttentionDetail(cmd, req)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the request as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

func printAttentionDetail(cmd *cobra.Command, r *attention.DecisionRequest) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s  [%s · %s · %s]\n", r.Title, r.Severity, r.Kind, r.Lifecycle.State)
	fmt.Fprintf(out, "  id:        %s\n", r.ID)
	fmt.Fprintf(out, "  producer:  %s\n", r.Producer)
	if r.Context.Repo != "" {
		fmt.Fprintf(out, "  repo:      %s", r.Context.Repo)
		if r.Context.Issue != 0 {
			fmt.Fprintf(out, " #%d", r.Context.Issue)
		}
		fmt.Fprintln(out)
	}
	if r.Context.RunID != "" {
		fmt.Fprintf(out, "  run:       %s\n", r.Context.RunID)
	}
	if r.Context.CostSoFarUSD > 0 {
		fmt.Fprintf(out, "  cost:      $%.2f\n", r.Context.CostSoFarUSD)
	}
	if r.Context.Blocker != "" {
		fmt.Fprintf(out, "  blocker:   %s\n", r.Context.Blocker)
	}
	fmt.Fprintf(out, "  expires:   %s (default: %s)\n", r.ExpiresAt, r.DefaultAction)
	if r.Body != "" {
		fmt.Fprintf(out, "\n  %s\n", r.Body)
	}
	fmt.Fprintln(out, "\n  Options:")
	for _, o := range r.Options {
		fmt.Fprintf(out, "    %-16s %-28s → %s\n", o.ID, o.Label, o.Verb)
	}
	if r.Lifecycle.Resolved != nil {
		fmt.Fprintf(out, "\n  Resolved by %s at %s → %s\n", r.Lifecycle.Resolved.Actor, r.Lifecycle.Resolved.At, r.Lifecycle.Resolved.OptionID)
	}
}

func attentionResolveCmd() *cobra.Command {
	var (
		option  string
		actor   string
		steer   string
		note    string
		workdir string
	)
	cmd := &cobra.Command{
		Use:          "resolve <id>",
		Short:        "Resolve a decision request by choosing one of its options",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge attention resolve dr_0189... --option rescan
  nightgauge attention resolve dr_0189... --option leave --actor octocat
  nightgauge attention resolve dr_0189... --option escalate --steer "focus the retry on the failing test"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if option == "" {
				return fmt.Errorf("--option is required (see `attention show %s`)", args[0])
			}
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			store := attention.New(root)
			store.SetSteerWriter(func(req *attention.DecisionRequest, steerText string) error {
				return orchestrator.WriteOperatorSteer(root, req.Context.Issue, steerText, req.Context.Stage)
			})
			res, err := store.Resolve(context.Background(), args[0], option, actor, steer, note, cliVerbExecutor{workspaceRoot: root})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.AlreadyResolved {
				fmt.Fprintf(out, "Request %s was already resolved — no-op.\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "Resolved %s → %s\n", args[0], option)
			if res.SteerErr != nil {
				fmt.Fprintf(out, "  note: steer write failed: %v\n", res.SteerErr)
			}
			if res.VerbErr != nil {
				fmt.Fprintf(out, "  note: %v\n", res.VerbErr)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&option, "option", "", "The option id to apply (required)")
	cmd.Flags().StringVar(&actor, "actor", "", "Who is resolving (recorded in the audit trail)")
	cmd.Flags().StringVar(&steer, "steer", "", "Free-text steer pinned as next-stage context")
	cmd.Flags().StringVar(&note, "note", "", "Optional resolution note")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

// cliVerbExecutor executes the deterministic, file-based verbs a standalone CLI
// can perform (ADR 015). Verbs that need the live scheduler or GitHub are
// executed by the daemon; the CLI records the resolution and reports that the
// action needs the daemon/extension.
type cliVerbExecutor struct {
	workspaceRoot string
}

func (e cliVerbExecutor) ExecuteVerb(_ context.Context, req *attention.DecisionRequest, opt attention.Option) error {
	actor := ""
	if req.Lifecycle.Resolved != nil {
		actor = req.Lifecycle.Resolved.Actor
	}
	switch opt.Verb {
	case attention.VerbNoop:
		return nil
	case attention.VerbBudgetRaiseCeiling:
		return orchestrator.WriteBudgetCeilingOverride(e.workspaceRoot, cliArgFloat(opt.Args, "ceilingUsd"), actor, "action-center (cli)")
	case attention.VerbRunRetryWithEscalation:
		tier := cliArgString(opt.Args, "tier")
		if tier == "" {
			tier = "opus"
		}
		return orchestrator.WriteEscalationOverride(e.workspaceRoot, req.Context.Issue, tier, actor)
	default:
		return fmt.Errorf("verb %q requires the running Nightgauge daemon or the VSCode extension — the resolution was recorded", opt.Verb)
	}
}

func cliArgString(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		switch s := v.(type) {
		case string:
			return s
		case fmt.Stringer:
			return s.String()
		}
	}
	return ""
}

func cliArgFloat(m map[string]any, k string) float64 {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case string:
			if f, err := strconv.ParseFloat(n, 64); err == nil {
				return f
			}
		}
	}
	return 0
}
