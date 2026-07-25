package main

// `nightgauge attention sweep|ack|mute|unmute` — the repo-scoped half of the
// Action Center (issues #89 / #92).
//
// `sweep` is the evaluation loop the store never had: every producer before it
// fired from the orchestrator's run loop, so a repo could only report a blocker
// while a run was already in flight. The sweep asks the registered repo-scoped
// producers "is this repo blocked?" with nothing running, and reconciles the
// answers against the store.
//
// `ack`, `mute` and `unmute` are the non-resolving affordances a standing card
// needs. They are deliberately NOT options bound to registry verbs: resolving a
// request is terminal, and neither acknowledging nor muting a condition that is
// still true should end its card.

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	forgecmd "github.com/nightgauge/nightgauge/cmd/nightgauge/forge"
	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/spf13/cobra"
)

// sweepForgeClient is swapped by tests to avoid constructing a live adapter.
var sweepForgeClient = func(repo string) (forge.ForgeClient, error) {
	router, err := forgecmd.BuildRouter("", 0, "")
	if err != nil {
		return nil, err
	}
	return router.For("", repo)
}

// cachedSweepForgeClient returns a resolver that builds the router at most
// once. The CLI path rebuilds per invocation because it runs one sweep and
// exits; the serve daemon sweeps every workspace repo on a timer, and
// BuildRouter re-reads config and re-runs the token chain (which can shell out
// to `gh`) each time. A failed build is not cached — a daemon that started
// before the operator authenticated must be able to recover without a restart.
func cachedSweepForgeClient() func(repo string) (forge.ForgeClient, error) {
	var (
		mu     sync.Mutex
		router *forge.Router
	)
	return func(repo string) (forge.ForgeClient, error) {
		mu.Lock()
		defer mu.Unlock()
		if router == nil {
			r, err := forgecmd.BuildRouter("", 0, "")
			if err != nil {
				return nil, err
			}
			router = r
		}
		return router.For("", repo)
	}
}

func attentionSweepCmd() *cobra.Command {
	var (
		repo       string
		workdir    string
		jsonOutput bool
		strict     bool
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Evaluate a repo's standing blockers with no run in flight",
		Long: `Evaluate the registered repo-scoped producers once and reconcile the result
against the attention store.

Reconcile, not append: the sweep raises what is newly true, leaves untouched
what is still true, and auto-resolves what is no longer true. Running it twice
over an unchanged repo is a no-op the second time.

The sweep is cheap and idempotent by design — safe to run on extension
activation, on a view refresh, on a timer, and after a run terminates. A
network, auth, or rate-limit failure degrades to a logged skip; pass --strict
to exit non-zero on a degraded sweep instead.`,
		SilenceUsage: true,
		Example: `  nightgauge attention sweep --repo octocat/acme-web
  nightgauge attention sweep --repo octocat/acme-web --json
  nightgauge attention sweep --repo octocat/acme-web --strict`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required (owner/name)")
			}
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			client, err := sweepForgeClient(repo)
			if err != nil {
				return fmt.Errorf("resolve forge client for %s: %w", repo, err)
			}
			s := &sweep.Sweeper{
				Store:         attention.New(root),
				Registry:      sweep.Default,
				Forge:         client,
				WorkspaceRoot: root,
				Timeout:       timeout,
			}
			res, err := s.Sweep(cmd.Context(), repo)
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(res); err != nil {
					return err
				}
			} else {
				printSweepResult(cmd, res)
			}
			if strict && !res.OK() {
				return fmt.Errorf("sweep degraded: %s", sweepDegradation(res))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Repo to sweep as owner/name (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the sweep result as JSON")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit non-zero when a producer failed or the sweep was skipped")
	cmd.Flags().DurationVar(&timeout, "timeout", sweep.DefaultTimeout, "Deadline bounding the sweep's forge traffic")
	return cmd
}

func sweepDegradation(res sweep.Result) string {
	if res.Skipped {
		return "skipped — " + res.SkipReason
	}
	return fmt.Sprintf("%d producer(s) could not observe the repo", len(res.Failed))
}

func printSweepResult(cmd *cobra.Command, res sweep.Result) {
	out := cmd.OutOrStdout()
	if res.Skipped {
		fmt.Fprintf(out, "Skipped sweep of %s — %s\n", res.Repo, res.SkipReason)
		return
	}
	if len(res.Evaluated) == 0 && len(res.Failed) == 0 {
		fmt.Fprintf(out, "No repo-scoped producers registered — nothing to evaluate for %s.\n", res.Repo)
		return
	}
	r := res.Reconciled
	fmt.Fprintf(out, "Swept %s — %d producer(s) evaluated\n", res.Repo, len(res.Evaluated))
	fmt.Fprintf(out, "  raised:       %d\n", r.Created)
	fmt.Fprintf(out, "  updated:      %d\n", r.Updated)
	fmt.Fprintf(out, "  unchanged:    %d\n", r.Refreshed)
	fmt.Fprintf(out, "  auto-resolved:%d\n", r.AutoResolved)
	if r.Suppressed > 0 {
		fmt.Fprintf(out, "  suppressed:   %d (already resolved, unchanged since)\n", r.Suppressed)
	}
	for name, msg := range res.Failed {
		fmt.Fprintf(cmd.ErrOrStderr(), "  ! producer %q could not observe %s (its cards left untouched): %s\n", name, res.Repo, msg)
	}
}

func attentionAckCmd() *cobra.Command {
	var (
		actor   string
		workdir string
	)
	cmd := &cobra.Command{
		Use:          "ack <id>",
		Short:        "Acknowledge a request — keeps the card, clears the badge",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			req, err := attention.New(root).Acknowledge(args[0], attentionActor(actor))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Acknowledged %s (%s)\n", req.ID, req.Lifecycle.State)
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "", "Who is acknowledging (recorded in the audit trail)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

func attentionMuteCmd() *cobra.Command {
	var (
		actor   string
		workdir string
	)
	cmd := &cobra.Command{
		Use:   "mute <id>",
		Short: "Silence a request until its underlying condition changes",
		Long: `Suppress alerting on a request until the CONDITION changes — not until a timer
expires. An operator who knows the default branch is red because they are
fixing it is not told again; if a second check starts failing, the fingerprint
moves, the mute drops, and the card alerts.

The card stays in the inbox at its severity. Muting is not resolving.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			req, err := attention.New(root).Mute(args[0], attentionActor(actor))
			if err != nil {
				return err
			}
			if !req.IsMuted() {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is already %s — nothing to mute.\n", req.ID, req.Lifecycle.State)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Muted %s until its condition changes.\n", req.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "", "Who is muting (recorded in the audit trail)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

func attentionUnmuteCmd() *cobra.Command {
	var (
		actor   string
		workdir string
	)
	cmd := &cobra.Command{
		Use:          "unmute <id>",
		Short:        "Restore alerting on a muted request",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			req, err := attention.New(root).Unmute(args[0], attentionActor(actor))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unmuted %s.\n", req.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "", "Who is unmuting (recorded in the audit trail)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	return cmd
}

// attentionActor falls back to the OS user so a local CLI action is still
// attributable in the audit trail.
func attentionActor(actor string) string {
	if actor != "" {
		return actor
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "cli"
}
