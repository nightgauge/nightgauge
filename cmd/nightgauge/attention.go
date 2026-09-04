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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/spf13/cobra"
)

// daemonDialTimeout bounds how long a CLI command waits to reach a co-located
// `nightgauge serve` daemon before falling back to local execution. Short by
// design (#263 plan): this is a same-host, same-filesystem Unix socket — no
// daemon means the dial fails near-instantly (ENOENT/ECONNREFUSED), so this
// timeout only bounds the genuinely-slow case and never makes an interactive
// command feel hung.
const daemonDialTimeout = 300 * time.Millisecond

// dialDaemonProbe reports whether a `nightgauge serve` daemon is reachable
// for workspace root. Used by both attentionResolveCmd (to route the call)
// and printAttentionDetail (to annotate option executability).
func dialDaemonProbe(ctx context.Context, root string) bool {
	c, err := ipc.DialClient(ctx, ipc.DaemonSocketPath(root), daemonDialTimeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

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
	cmd.AddCommand(attentionAckCmd())
	cmd.AddCommand(attentionMuteCmd())
	cmd.AddCommand(attentionUnmuteCmd())
	cmd.AddCommand(attentionSweepCmd())
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
			printAttentionTable(cmd, reqs, repo, store, all)
			printCoverageFooter(cmd, root)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output requests as JSON")
	cmd.Flags().BoolVar(&all, "all", false, "Include resolved/expired requests")
	cmd.Flags().StringVar(&repo, "repo", "", "Filter to a single owner/name repo")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

// printCoverageFooter states what the last sweep actually looked at (#260).
//
// "✓ All clear" is only reassuring if you know its scope. Before this, an
// empty Action Center read identically whether every repo was healthy or
// whether nothing had ever been swept — and the second is how a sibling repo
// accumulated six weeks of blocked PRs in silence.
//
// The count comes from the recorded sweep, never from config: the configured
// list says what WOULD be covered, and reporting that as coverage would repeat
// the same error one level up.
func printCoverageFooter(cmd *cobra.Command, root string) {
	cov, ok := sweep.ReadCoverage(root)
	if !ok || len(cov.Repos) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nCoverage: no sweep on record — nothing has been looked at yet. Run `nightgauge attention sweep --repo <owner/name>`.")
		return
	}
	when := ""
	if t, err := time.Parse(time.RFC3339, cov.SweptAt); err == nil {
		when = fmt.Sprintf(" (last swept %s)", t.Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nCoverage: %d repo(s) swept%s — %s\n",
		len(cov.Repos), when, strings.Join(cov.Repos, ", "))
}

// printAttentionTable renders the decision-request list, or an all-clear
// message when reqs is empty. repoFilter is the --repo value that produced
// reqs (empty when no filter was applied) — used to tell a genuinely empty
// store apart from a repo filter that matched nothing (#222): the latter
// prints the filter and the true fleet-wide total instead of the bare
// all-clear message, so an operator never mistakes "filtered to zero" for
// "nothing pending anywhere".
func printAttentionTable(cmd *cobra.Command, reqs []attention.DecisionRequest, repoFilter string, store *attention.Store, includeTerminal bool) {
	if len(reqs) == 0 {
		if repoFilter == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "✓ All clear — no decisions pending.")
			return
		}
		total := 0
		if all, err := store.List(attention.ListFilter{IncludeTerminal: includeTerminal}); err == nil {
			total = len(all)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "No pending decisions match --repo %q (%d total across all repos).\n", repoFilter, total)
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSEVERITY\tKIND\tSTATE\tREPO\tTITLE")
	for _, r := range reqs {
		repo := r.Context.Repo
		if repo == "" {
			repo = "(fleet)"
		}
		state := string(r.Lifecycle.State)
		if r.IsMuted() {
			// A muted card is silenced, not hidden — the operator still sees it,
			// annotated with why it is quiet.
			state += " (muted)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Severity, r.Kind, state, repo, clip(r.Title, 48))
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
			printAttentionDetail(cmd, req, root)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the request as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

func printAttentionDetail(cmd *cobra.Command, r *attention.DecisionRequest, root string) {
	out := cmd.OutOrStdout()
	daemonReachable := dialDaemonProbe(context.Background(), root)
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
	if r.Standing {
		fmt.Fprintf(out, "  standing:  yes (fingerprint %s)\n", r.Fingerprint)
	}
	if m := r.Lifecycle.Muted; m != nil {
		fmt.Fprintf(out, "  muted:     by %s at %s — until the condition changes\n", m.Actor, m.At)
	}
	fmt.Fprintf(out, "  expires:   %s (default: %s)\n", r.ExpiresAt, r.DefaultAction)
	if r.Body != "" {
		fmt.Fprintf(out, "\n  %s\n", r.Body)
	}
	fmt.Fprintln(out, "\n  Options:")
	for _, o := range r.Options {
		executable := daemonReachable || attention.IsCLIExecutableVerb(o.Verb)
		if executable {
			fmt.Fprintf(out, "    %-16s %-28s → %s\n", o.ID, o.Label, o.Verb)
		} else {
			fmt.Fprintf(out, "    %-16s %-28s → %s  — unavailable from this CLI: start the daemon with 'nightgauge serve' to enable\n", o.ID, o.Label, o.Verb)
		}
	}
	if r.Lifecycle.Resolved != nil {
		fmt.Fprintf(out, "\n  Resolved by %s at %s → %s\n", r.Lifecycle.Resolved.Actor, r.Lifecycle.Resolved.At, r.Lifecycle.Resolved.OptionID)
	}
	if a := r.Lifecycle.AutoResolved; a != nil {
		fmt.Fprintf(out, "\n  Auto-resolved at %s — %s (no human decided this)\n", a.At, a.Reason)
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
			out := cmd.OutOrStdout()

			// Prefer a co-located daemon when reachable (#263): the daemon's
			// handleAttentionResolve runs the FULL verb executor (all
			// registered verbs, not just the CLI's 3-verb local subset), the
			// same path the VSCode extension's click-to-resolve already uses.
			ctx := context.Background()
			if client, dialErr := ipc.DialClient(ctx, ipc.DaemonSocketPath(root), daemonDialTimeout); dialErr == nil {
				defer client.Close()
				var res ipc.AttentionResolveResult
				callErr := client.Call(ctx, "attention.resolve", ipc.AttentionResolveParams{
					ID:        args[0],
					OptionID:  option,
					Actor:     actor,
					SteerText: steer,
					Note:      note,
				}, &res)
				if callErr != nil {
					// Transport succeeded but the daemon rejected the call
					// (e.g. unknown option) — surface it unchanged, this is
					// not a "no daemon" condition.
					return callErr
				}
				if res.AlreadyResolved {
					fmt.Fprintf(out, "Request %s was already resolved — no-op.\n", args[0])
					return nil
				}
				fmt.Fprintf(out, "Resolved %s → %s\n", args[0], option)
				return nil
			}

			// No daemon reachable — fall back to the local, file-based path.
			store := attention.New(root)
			store.SetSteerWriter(func(req *attention.DecisionRequest, steerText string) error {
				// SCOPE NOTE (#1407). The daemon path resolves the per-repo run
				// root, because the reader (runPipeline) does. This fallback
				// cannot: resolveRunRoot is a Scheduler method and there is no
				// scheduler here — that is the definition of this branch.
				//
				// `root` is the workspace the CLI was pointed at, so on a
				// multi-repo workspace this can still write to a root the run
				// does not read. It is bounded: this path only runs when no
				// daemon is reachable, and the daemon is what the extension and
				// the autonomous scheduler both use. Named here rather than
				// silently left, because a steer that goes nowhere looks
				// identical to one that worked.
				return orchestrator.WriteOperatorSteer(root, req.Context.Issue, steerText, req.Context.Stage)
			})
			res, err := store.Resolve(ctx, args[0], option, actor, steer, note, cliVerbExecutor{workspaceRoot: root})
			if err != nil {
				return err
			}
			if res.AlreadyResolved {
				fmt.Fprintf(out, "Request %s was already resolved — no-op.\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "Resolved %s → %s\n", args[0], option)
			if res.SteerErr != nil {
				fmt.Fprintf(out, "  note: steer write failed: %v\n", res.SteerErr)
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
		if err := orchestrator.WriteBudgetCeilingOverride(e.workspaceRoot, cliArgFloat(opt.Args, "ceilingUsd"), actor, "action-center (cli)"); err != nil {
			return &attention.VerbExecutionError{Verb: opt.Verb, Retryable: false, Err: err}
		}
		return nil
	case attention.VerbRunRetryWithEscalation:
		tier := cliArgString(opt.Args, "tier")
		if tier == "" {
			tier = "opus"
		}
		if err := orchestrator.WriteEscalationOverride(e.workspaceRoot, req.Context.Issue, tier, actor); err != nil {
			return &attention.VerbExecutionError{Verb: opt.Verb, Retryable: false, Err: err}
		}
		return nil
	default:
		// Reached only after attentionResolveCmd's daemon dial already
		// failed (#263) — a genuinely different, retryable condition (e.g.
		// the daemon restarting mid-connect) cannot be distinguished from
		// "no daemon at all" here, so this defaults to the common,
		// non-retryable case: no daemon is running for this workspace at
		// all, and retrying the identical command without starting one
		// cannot succeed.
		return &attention.VerbExecutionError{
			Verb:      opt.Verb,
			Retryable: false,
			Err: fmt.Errorf(
				"verb %q requires the Nightgauge daemon, and none is running for this workspace — "+
					"start it with `nightgauge serve` (or open this workspace in the VSCode extension), then retry",
				opt.Verb,
			),
		}
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
